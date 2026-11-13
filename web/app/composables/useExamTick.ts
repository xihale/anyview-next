// 多 tab 计时协调(自 index.vue 原样抽出): relay 只有一个单活计时器,
// 多 tab 同开时若各自纠偏, 401 计时对会每 10s 在两题间翻转,
// 任一 tab 关页的停表 beacon 还会误停别人的计时。对策:
//   - Web Locks 选 leader: 只有 leader 在轮询里纠偏计时目标/关页停表, 其余 tab 只看状态;
//   - 计时目标跟随 intent(最近被使用的 tab 在切题/获得焦点时发布), leader 服务最新意图;
//   - 切题本身仍由当前 tab 立即 tick(与 intent 一致, 不产生争用)。
// 无 Web Locks 的浏览器退化为旧行为(每 tab 自管)。
import { shallowRef } from "vue";
import { tryOnScopeDispose, useEventListener, useIntervalFn } from "@vueuse/core";
import type { ExamStatus, useApi } from "~/composables/useApi";

const EXAM_LOCK = "an.exam-leader";
const INTENT_KEY = "an.exam.intent";

export function useExamTick(opts: {
  api: ReturnType<typeof useApi>;
  loadedEid: () => number;
}) {
  const { api, loadedEid } = opts;

  const exam = shallowRef<ExamStatus>({
    active: 0,
    running: false,
    tickedAt: 0,
    connected: false,
    kicked: false,
    lastError: "",
  });
  const tabId = Math.random().toString(36).slice(2);
  let isLeader = false;
  let stopped = false;
  let polling = false;
  let resignLeader: (() => void) | null = null;

  // 切题 tick 串行且只发最新 eid: 避免慢请求 Tick(A) 迟到覆盖已切到的 B
  let tickChain: Promise<void> = Promise.resolve();
  let tickWanted: number | null | undefined;

  function requestExamTick(eid: number | null) {
    tickWanted = eid;
    tickChain = tickChain
      .then(async () => {
        const want = tickWanted;
        if (want === undefined) return;
        tickWanted = undefined;
        await api.examTick(want);
      })
      .catch(() => {});
  }

  function waitTickChain(): Promise<void> {
    return tickChain;
  }

  function publishIntent(eid: number) {
    if (!eid) return;
    try {
      localStorage.setItem(INTENT_KEY, JSON.stringify({ eid, tab: tabId, ts: Date.now() }));
    } catch {}
  }

  function readIntent(): number {
    try {
      const eid = Number(JSON.parse(localStorage.getItem(INTENT_KEY) ?? "null")?.eid);
      return Number.isFinite(eid) && eid > 0 ? eid : 0;
    } catch {
      return 0;
    }
  }

  function publishOwnIntent() {
    if (document.visibilityState === "visible" && loadedEid()) publishIntent(loadedEid());
  }

  async function tryBecomeLeader() {
    if (isLeader || stopped) return;
    const locks = (navigator as any).locks;
    if (!locks?.request) {
      isLeader = true; // 退化: 无锁 API, 每 tab 自管(多 tab 计时会翻转, 同旧行为)
      return;
    }
    try {
      await locks.request(EXAM_LOCK, { ifAvailable: true }, async (lock: unknown) => {
        if (!lock) return;
        isLeader = true;
        pollExam(); // 接管后立刻纠偏一次
        await new Promise<void>((r) => { resignLeader = r; });
        isLeader = false;
        resignLeader = null;
      });
    } catch {}
  }

  async function pollExam() {
    if (polling || stopped) return;
    polling = true;
    try {
      const next = await api.examStatus();
      const cur = exam.value;
      if (
        cur.active !== next.active ||
        cur.running !== next.running ||
        cur.tickedAt !== next.tickedAt ||
        cur.kicked !== next.kicked ||
        cur.connected !== next.connected ||
        cur.lastError !== next.lastError
      ) {
        exam.value = { ...cur, ...next };
      }
      if (!isLeader) return; // 非 leader 只看状态不动计时
      const target = readIntent() || loadedEid();
      if (
        target &&
        !exam.value.kicked &&
        (!exam.value.connected || (exam.value.active && exam.value.active !== target))
      ) {
        requestExamTick(target);
      }
    } catch {
    } finally {
      polling = false;
    }
  }

  // 关页 beacon 的唯一放行条件: 只有 leader 且 relay 正记着自己这道题才停表
  function shouldStopTickOnUnload(): boolean {
    const eid = loadedEid();
    return isLeader && !!eid && exam.value.active === eid;
  }

  // 认证就绪后由页面调用: 立即尝试选主并启动两个轮询
  function start() {
    void tryBecomeLeader().then(() => {
      if (!isLeader && !stopped) void pollExam();
    });
    pollTimer.resume();
    leaderTimer.resume();
  }

  if (typeof window !== "undefined") {
    useEventListener(window, "focus", publishOwnIntent);
  }
  if (typeof document !== "undefined") {
    useEventListener(document, "visibilitychange", publishOwnIntent);
  }
  const pollTimer = useIntervalFn(pollExam, 10_000, { immediate: false });
  const leaderTimer = useIntervalFn(() => { if (!isLeader) void tryBecomeLeader(); }, 15_000, { immediate: false });

  tryOnScopeDispose(() => {
    stopped = true;
    pollTimer.pause();
    leaderTimer.pause();
    resignLeader?.();
  });

  return {
    exam,
    requestExamTick,
    publishIntent,
    waitTickChain,
    start,
    shouldStopTickOnUnload,
  };
}
