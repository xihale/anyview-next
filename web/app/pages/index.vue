<script setup lang="ts">
import { useEventListener } from "@vueuse/core";
import { PanelLeft, BookOpen, ChevronLeft, ChevronRight, Play, StepForward, ArrowDownToLine, ArrowUpFromLine, Square, Undo2, History, RotateCcw, Save, Bug } from "lucide-vue-next";
import type { AuthInfo, CatalogItem, Diag, RunResult, SchemeData } from "~/composables/useApi";
import { decodeStuCode, useApi } from "~/composables/useApi";
import { useToast } from "~/composables/useToast";
import { useSettings } from "~/composables/useSettings";
import { useDebug } from "~/composables/useDebug";
import { useQuestionCache } from "~/composables/useQuestionCache";
import { useTheme } from "~/composables/useTheme";
import { useExamTick } from "~/composables/useExamTick";

const api = useApi();
const route = useRoute();
const router = useRouter();
const toast = useToast();
const { settings } = useSettings();
const dbg = useDebug();
const {
  getCachedQuestion,
  setCachedQuestion,
  getCachedResult,
  setCachedResult,
  getDraftEntry,
  resolveDraft,
  markCloudSynced,
  saveDraftSync,
  backupDraft,
  hasBackup,
  restoreBackup,
} = useQuestionCache();

const canUndoReset = ref(false);
// 云端更新挤下来的本地草稿: bak 槽里有货时可一键取回
const canRestoreDraft = ref(false);

// 全局状态
const auth = ref<AuthInfo | null>(null);
const mock = ref(false);
function lsPositive(key: string, fallback: number): number {
  try {
    const n = Number(localStorage.getItem(key) ?? fallback);
    return Number.isFinite(n) && n > 0 ? n : fallback;
  } catch {
    return fallback;
  }
}
const schemeId = ref(lsPositive("an.schemeId", 465));
const classId = ref(lsPositive("an.classId", 381));
const catalog = ref<CatalogItem[]>([]);
const loadingCatalog = ref(false);

const activeEid = computed(() => Number(route.query.eid ?? 0));
const active = computed(() => catalog.value.find((i) => i.eid === activeEid.value) ?? null);

const q = ref<SchemeData | null>(null);
const template = ref("");
const code = ref("");
const diags = ref<Diag[]>([]);
const result = ref<RunResult | null>(null);
const running = ref(false);
const pageError = ref("");
const saving = ref(false);
const loadingQuestion = ref(false);

const showCourseModal = ref(false);
const showSettingsModal = ref(false);
const mobileSidebarOpen = ref(false);
const { isDark, toggleTheme } = useTheme();
const activeMobileTab = ref<"statement" | "code" | "output">("statement");

import { usePanels } from "~/composables/usePanels";

// 多模块尺寸、折叠与停靠控制
const {
  sidebarWidth,
  sidebarCollapsed,
  statementWidth,
  statementCollapsed,
  outputDock,
  outputHeight,
  outputWidth,
  outputCollapsed,
  outputMaximized,
  isDragging: isResizing,
  activeResizer,
  outputPanelStyle,
  startResize,
  toggleSidebar,
  toggleStatement,
  toggleOutputCollapse,
  toggleOutputMaximize,
  toggleOutputDock,
  checkDockSafety,
  resetLayout,
} = usePanels();

function handleToggleDock() {
  const ok = toggleOutputDock();
  if (!ok) {
    toast.info("平板横屏及窄屏模式已保持底部停靠，以保证代码宽度");
  }
}

const editorPaneRef = ref<any>(null);
const sidebarRef = ref<any>(null);

// 题目分组: 沿用上游原始数组序——那是平台官方题序(章内按题号 PE03e→PE65);
// eid 是创建序, 与题号序不一致(章1里 PE03e 的 eid 反而最大), 按 eid 重排会打乱题序
const groups = computed(() => {
  const byChap = new Map<string, CatalogItem[]>();
  for (const it of catalog.value) {
    if (!byChap.has(it.chapName)) byChap.set(it.chapName, []);
    byChap.get(it.chapName)!.push(it);
  }
  return [...byChap.entries()].map(([chap, items]) => ({ chap, items }));
});

// 扁平顺序题目列表与前后题导航: 同样保持上游原序
const flatCatalog = computed(() => [...catalog.value]);

const currentIdx = computed(() =>
  flatCatalog.value.findIndex((i) => i.eid === activeEid.value)
);
const activeSeq = computed(() => (currentIdx.value >= 0 ? currentIdx.value + 1 : 0));
const prevEid = computed(() =>
  currentIdx.value > 0 ? (flatCatalog.value[currentIdx.value - 1]?.eid ?? 0) : 0
);
const nextEid = computed(() =>
  currentIdx.value >= 0 && currentIdx.value < flatCatalog.value.length - 1
    ? (flatCatalog.value[currentIdx.value + 1]?.eid ?? 0)
    : 0
);

const passed = computed(() => catalog.value.filter((i) => i.pass).length);
const isDraftModified = computed(() => template.value && code.value !== template.value);

const isOffline = ref(typeof navigator !== "undefined" ? !navigator.onLine : false);
const wafCooldown = ref("");

function trackUpstreamError(e: any) {
  const msg = String(e?.message ?? "");
  if (e?.status === 429 || /WAF|冷却/i.test(msg)) {
    wafCooldown.value = msg;
  }
}

let disposed = false;
let workspaceReady = false;

if (import.meta.client) {
  // 生命周期类监听必须在 setup 同步阶段注册(await 后 EffectScope 已失活会导致组件卸载不清理)
  useEventListener(window, "beforeunload", stopTickBeacon);
  useEventListener(window, "keydown", handleGlobalShortcuts);
  useEventListener(window, "resize", checkDockSafety);
  useEventListener(window, "online", () => { isOffline.value = false; });
  useEventListener(window, "offline", () => { isOffline.value = true; });
}

async function bootstrapWorkspace() {
  pageError.value = "";
  try {
    const h = await api.health();
    if (disposed) return;
    mock.value = h.mock;
  } catch {
    if (disposed) return;
    pageError.value = "relay 不可达: 请先启动 relay (:8787)";
    toast.error(pageError.value);
    return;
  }

  try {
    auth.value = await api.authStatus();
    if (disposed) return;
    if (!auth.value.loggedIn || auth.value.expired) {
      navigateTo("/login");
      return;
    }
  } catch {
    if (disposed) return;
    navigateTo("/login");
    return;
  }

  await loadCatalog();
  if (disposed) return;

  if (!workspaceReady) {
    workspaceReady = true;
    startExamCoordination();
  }

  if (activeEid.value) {
    await openQuestion(activeEid.value);
  } else {
    const first = catalog.value[0];
    if (first) await openQuestion(first.eid);
  }
}

function retryLoad() {
  if (!auth.value) {
    bootstrapWorkspace();
  } else if (catalog.value.length === 0) {
    loadCatalog().then(() => {
      const target = activeEid.value || catalog.value[0]?.eid;
      if (target && !q.value) {
        openQuestion(target);
      }
    });
  } else if (activeEid.value) {
    loadedEid = 0;
    openQuestion(activeEid.value);
  }
}

onMounted(() => {
  if (import.meta.dev) {
    (window as any).__andbg = dbg; // 调试句柄(dev only)
  }
  bootstrapWorkspace();
});

function handleGlobalShortcuts(e: KeyboardEvent) {
  if (!workspaceReady) return;
  if (e.key === "Escape" && mobileSidebarOpen.value) {
    e.preventDefault();
    mobileSidebarOpen.value = false;
    return;
  }
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "b") {
    e.preventDefault();
    toggleSidebar();
    return;
  }
  if (e.key === "/" && !e.ctrlKey && !e.metaKey && !e.altKey) {
    // 编辑器(contenteditable)、输入框里不劫持, 否则代码里打不出除号/注释
    const t = e.target as HTMLElement | null;
    const typing = !!t && (["INPUT", "TEXTAREA"].includes(t.tagName) || t.isContentEditable);
    if (!typing) {
      e.preventDefault();
      sidebarRef.value?.focusSearch?.();
      return;
    }
  }
  // 上一题 / 下一题快捷导航 (Alt+[ / Alt+])
  if (e.altKey && (e.key === "[" || e.key === "【")) {
    e.preventDefault();
    if (prevEid.value) openQuestion(prevEid.value);
    return;
  }
  if (e.altKey && (e.key === "]" || e.key === "】")) {
    e.preventDefault();
    if (nextEid.value) openQuestion(nextEid.value);
    return;
  }
  // 调试快捷键(同原版: F8 开/停, F10 步过, F11 步入, Shift+F11 跳出, F9 继续)
  if (!q.value || !loadedEid) return;
  const eid = loadedEid;
  if (e.key === "F8") {
    e.preventDefault();
    dbg.active.value ? dbg.quit(eid) : startDebug();
  } else if (dbg.active.value) {
    if (e.key === "F10") {
      e.preventDefault();
      dbg.stepOver(eid);
    } else if (e.key === "F11" && e.shiftKey) {
      e.preventDefault();
      dbg.skipOut(eid);
    } else if (e.key === "F11") {
      e.preventDefault();
      dbg.stepInto(eid);
    } else if (e.key === "F9") {
      e.preventDefault();
      dbg.resume(eid);
    }
  }
}

function startDebug() {
  if (!q.value || dbg.busy.value || !loadedEid) return;
  dbg.start({ eid: loadedEid, qfn: q.value.questionFullName, code: code.value });
}

const CATALOG_KEY = (sid: number, cid: number) => `an.catalog.${sid}.${cid}`;

async function loadCatalog() {
  // 捕获调用时刻的方案/班级: 响应回来时若已切方案, 丢弃并不得写入新方案的缓存键
  const sid = schemeId.value;
  const cid = classId.value;

  // 1. 优先秒级恢复本地题库缓存
  if (catalog.value.length === 0 && import.meta.client) {
    try {
      const cached = sessionStorage.getItem(CATALOG_KEY(sid, cid));
      if (cached) {
        catalog.value = JSON.parse(cached);
      }
    } catch {}
  }

  loadingCatalog.value = catalog.value.length === 0;
  pageError.value = "";
  try {
    const r = await api.catalog(sid, cid);
    if (sid !== schemeId.value || cid !== classId.value) return;
    wafCooldown.value = "";
    catalog.value = r.data ?? [];
    if (import.meta.client) {
      try {
        sessionStorage.setItem(CATALOG_KEY(sid, cid), JSON.stringify(catalog.value));
      } catch {}
    }
  } catch (e: any) {
    trackUpstreamError(e);
    if (catalog.value.length === 0 && sid === schemeId.value && cid === classId.value) {
      pageError.value = e?.message ?? "题库拉取失败";
      toast.error(pageError.value);
    }
  } finally {
    loadingCatalog.value = false;
  }
}

async function applyCourse(sid: number, cid: number) {
  if (draftTimer) {
    clearTimeout(draftTimer);
    draftTimer = null;
  }
  if (loadedEid && q.value) {
    saveDraftSync(loadedScheme || schemeId.value, loadedEid, code.value, template.value);
  }
  schemeId.value = Number.isFinite(sid) && sid > 0 ? sid : 465;
  classId.value = Number.isFinite(cid) && cid > 0 ? cid : 381;
  try {
    localStorage.setItem("an.schemeId", String(schemeId.value));
    localStorage.setItem("an.classId", String(classId.value));
  } catch {}
  catalog.value = [];
  q.value = null;
  result.value = null;
  diags.value = [];
  loadedEid = 0;
  toast.success(`切换至方案 ${sid} / 班级 ${cid}`);
  await loadCatalog();
  const first = catalog.value[0];
  if (first) {
    await openQuestion(first.eid);
  }
}

// 打开题目: 全程带序号守卫。快速连点/切题时, 迟到的旧题响应在 await 后被丢弃,
// 否则会覆盖新题的 q/template/code, 并经草稿防抖把旧题代码写进新题的草稿键。
let openSeq = 0;
// 已加载完成的题(与 URL 上的 activeEid 区分: 拉取在途时 URL 已变而题未就位)
let loadedEid = 0;
let loadedScheme = 0;

// 多 tab 计时协调(exam 轮询/leader 选举/tick 串行链)已抽至 useExamTick
const {
  exam,
  requestExamTick,
  publishIntent,
  waitTickChain,
  start: startExamCoordination,
  shouldStopTickOnUnload,
} = useExamTick({ api, loadedEid: () => loadedEid });

async function openQuestion(eid: number) {
  if (eid === loadedEid && loadedScheme === schemeId.value && q.value) return;
  const seq = ++openSeq;

  // 1. 立即同步刷新上一题草稿, 并取消在途防抖(防定时器把旧内容写到新键)
  if (loadedEid && q.value) {
    saveDraftSync(loadedScheme || schemeId.value, loadedEid, code.value, template.value);
  }
  if (draftTimer) {
    clearTimeout(draftTimer);
    draftTimer = null;
  }
  running.value = false;
  saving.value = false;

  if (dbg.active.value) {
    await dbg.quit(loadedEid || activeEid.value); // 切题前释放服务端 gdb 会话
    if (seq !== openSeq) return;
  }

  router.replace({ query: { ...route.query, eid: String(eid) } });
  pageError.value = "";
  activeMobileTab.value = "statement";
  canUndoReset.value = false;
  canRestoreDraft.value = false;
  dbg.resetForQuestion(schemeId.value, eid);
  loadedEid = eid;
  loadedScheme = schemeId.value;

  const sid = schemeId.value;
  const cachedQ = getCachedQuestion(sid, eid);
  if (cachedQ) {
    loadingQuestion.value = false;
    q.value = cachedQ;
    template.value = decodeStuCode(cachedQ.stuCode ?? "");
    const rd = resolveDraft(sid, eid, template.value);
    code.value = rd.code;
    const resolvedCode = rd.code;
    if (rd.cloudUpdated) {
      canRestoreDraft.value = true;
      toast.info("云端代码已更新，已以云端为准（本地草稿已备份）");
    }

    // sessionStorage/relay 缓存可能滞后于云端(它设备/外部写入): 后台拉新校正。
    // relay 缓存命中会贴 X-Relay-Stale(后台校验在途): 1.5s 后补拉追新, 最多 3 次停
    let refetchTries = 0;
    const refetchFresh = (): void => {
      api.question(eid).then((r) => {
        if (seq !== openSeq || !r.data) return;
        setCachedQuestion(sid, eid, r.data);
        const fresh = decodeStuCode(r.data.stuCode ?? "");
        if (fresh !== template.value) {
          template.value = fresh;
          const rd2 = resolveDraft(sid, eid, fresh);
          if (rd2.code !== code.value) {
            if (code.value === resolvedCode) {
              // 用户还没敲过字 → 直接换到最新裁决
              code.value = rd2.code;
              if (rd2.cloudUpdated) {
                canRestoreDraft.value = true;
                toast.info("云端代码已更新，已以云端为准（本地草稿已备份）");
              }
            } else {
              toast.info("云端代码已有外部更新，保存前请留意");
            }
          }
        }
        if (r.stale && refetchTries < 3) {
          refetchTries++;
          setTimeout(refetchFresh, 1500);
        }
      }).catch(() => {});
    };
    refetchFresh();

    const cachedRes = getCachedResult(sid, eid, code.value);
    if (cachedRes) {
      result.value = cachedRes.result;
      diags.value = cachedRes.diags;
    } else {
      result.value = null;
      diags.value = [];
    }
  } else {
    q.value = null;
    result.value = null;
    diags.value = [];
    loadingQuestion.value = true;
    code.value = getDraftEntry(sid, eid)?.code ?? "";

    try {
      const r = await api.question(eid);
      if (seq !== openSeq) return;
      if (!r.data) throw new Error("题目响应缺少 data");
      wafCooldown.value = "";
      q.value = r.data;
      setCachedQuestion(sid, eid, r.data);
      template.value = decodeStuCode(r.data?.stuCode ?? "");
      const rd = resolveDraft(sid, eid, template.value);
      code.value = rd.code;
      if (rd.cloudUpdated) {
        canRestoreDraft.value = true;
        toast.info("云端代码已更新，已以云端为准（本地草稿已备份）");
      }
    } catch (e: any) {
      if (seq !== openSeq) return;
      trackUpstreamError(e);
      pageError.value = e?.message ?? "题目拉取失败";
      toast.error(pageError.value);
      return;
    } finally {
      if (seq === openSeq) loadingQuestion.value = false;
    }
  }

  if (seq === openSeq) {
    publishIntent(eid);
    requestExamTick(eid);
  }
  prefetchAdjacent();
}

// 浏览器前进/后退变更 query.eid 时同步加载对应题
// (否则 URL 已换而题面未换, 下一次落草稿会把 A 题代码写进 B 题草稿键)
watch(() => route.query.eid, (neu) => {
  const n = Number(neu ?? 0);
  if (n && n !== loadedEid) openQuestion(n);
});

function prefetchAdjacent() {
  if (typeof window === "undefined") return;
  const next = nextEid.value;
  const sid = schemeId.value;
  if (next && !getCachedQuestion(sid, next)) {
    const idleRunner = (window as any).requestIdleCallback || ((cb: () => void) => setTimeout(cb, 300));
    idleRunner(() => {
      api.question(next).then((r) => {
        if (r?.data) setCachedQuestion(sid, next, r.data);
      }).catch(() => {});
    });
  }
}

let draftTimer: ReturnType<typeof setTimeout> | null = null;
watch(code, () => {
  // 拉题在途(q 为空)时 code 还是旧题占位内容, 不落盘
  if (!loadedEid || !q.value) return;
  // 归属必须用 loadedEid/loadedScheme: route.query 派生的 activeEid 在
  // router.replace 生效前仍是旧题, 按它落盘会把新题代码写进旧题草稿键(实测串题)
  const eid = loadedEid;
  const sid = loadedScheme || schemeId.value;
  const snapshot = code.value;
  const baseline = template.value;
  if (draftTimer) clearTimeout(draftTimer);
  draftTimer = setTimeout(() => {
    draftTimer = null;
    saveDraftSync(sid, eid, snapshot, baseline);
  }, 350);
});

// 重置模板走 ConfirmDialog(原生 dialog), 替代与设计体系脱节的 window.confirm
const showResetConfirm = ref(false);

function askResetTemplate() {
  if (!loadedEid) return;
  showResetConfirm.value = true;
}

async function doResetTemplate() {
  showResetConfirm.value = false;
  if (!loadedEid) return;
  const sid = loadedScheme || schemeId.value;
  backupDraft(sid, loadedEid, code.value);
  canRestoreDraft.value = false; // bak 槽被本次备份占用
  // scheme/list/student 的 stuCode 是"上次保存的代码"而非初始模板(fuckView analysis/04),
  // 真模板从 getQuestionTemplate 拉; 拉不到才退回上次保存的代码
  let tpl = template.value;
  try {
    const r = await api.questionTemplate(loadedEid);
    const t = decodeStuCode(r.data ?? "");
    if (t) tpl = t;
  } catch {}
  code.value = tpl;
  saveDraftSync(sid, loadedEid, tpl, template.value);
  canUndoReset.value = true;
  toast.info("已恢复模板（误操作可点上方撤销）");
}

function undoReset() {
  if (!loadedEid) return;
  const sid = loadedScheme || schemeId.value;
  const restored = restoreBackup(sid, loadedEid);
  if (restored != null) {
    code.value = restored;
    saveDraftSync(sid, loadedEid, restored, template.value);
    canUndoReset.value = false;
    toast.success("已撤销并恢复之前的代码");
  }
}

// 取回被云端更新挤下来的本地草稿(bak 槽)
function restoreDisplacedDraft() {
  if (!loadedEid) return;
  const restored = restoreBackup(loadedScheme || schemeId.value, loadedEid);
  canRestoreDraft.value = false;
  if (restored != null) {
    code.value = restored;
    toast.success("已恢复本地草稿（云端版本仍在云端存档）");
  } else {
    toast.warning("没有可恢复的草稿备份");
  }
}

async function run(input?: string) {
  if (!q.value || running.value || !loadedEid) return;
  if (dbg.active.value) {
    toast.warning("调试进行中，请先停止调试");
    return;
  }
  // 全量快照(归属 loadedEid: 它与 q/code 同源, 不受 router.replace 生效滞后影响):
  // 响应回来时若已切题, 结果不得写进新题的状态
  const runSid = loadedScheme || schemeId.value;
  const runEid = loadedEid;
  const runQfn = q.value.questionFullName;
  const codeSnap = code.value;
  const runLang = q.value.language || 1;
  const runKind = catalog.value.find((i) => i.eid === runEid)?.kind || 4;
  // 运行前立即持久化本地草稿
  saveDraftSync(runSid, runEid, codeSnap, template.value);

  running.value = true;
  pageError.value = "";
  activeMobileTab.value = "output";

  try {
    const res = await api.run({
      eid: runEid,
      qfn: runQfn,
      code: codeSnap,
      language: runLang,
      kind: runKind,
      input,
    });
    if (runEid !== loadedEid) return; // 已切题: 丢弃迟到结果
    wafCooldown.value = "";
    result.value = res;
    diags.value = res?.diagnostics ?? [];
    // compile 已把 codeSnap 落到云端存档 → 草稿基线对齐(此后外部改动才能被识别)
    markCloudSynced(runSid, runEid, codeSnap);

    setCachedResult(runSid, runEid, res, diags.value, codeSnap);

    if (res?.passed) {
      const item = catalog.value.find((i) => i.eid === runEid);
      if (item) item.pass = true;
      toast.success("评测通过");
      // 自动静默存档云端，防止学生忘记保存; 失败必须留个提示(本地草稿仍兜底)
      api.saveCode(runEid, codeSnap).catch(() => {
        toast.warning("云端存档同步失败，代码仅在本地草稿");
      });
    } else if (res?.needInput) {
      toast.warning("程序等待输入");
    } else if (!res?.compileOk) {
      toast.error("编译错误");
    }
  } catch (e: any) {
    if (runEid !== loadedEid) return;
    trackUpstreamError(e);
    pageError.value = e?.message ?? "运行失败";
    toast.error(pageError.value);
  } finally {
    if (runEid === loadedEid) running.value = false;
  }
}

async function saveCode() {
  if (!q.value || saving.value || dbg.active.value || !loadedEid) return;
  const saveSid = loadedScheme || schemeId.value;
  const saveEid = loadedEid;
  const codeSnap = code.value;
  // 保存前同步本地草稿
  saveDraftSync(saveSid, saveEid, codeSnap, template.value);
  saving.value = true;
  try {
    const res = await api.saveCode(saveEid, codeSnap);
    if (saveEid !== loadedEid) return;
    wafCooldown.value = "";
    // 兜底旧版 relay 的 200+ok:false 形状: 掉登录时曾假弹"已保存至云端"
    if (res && res.ok === false) {
      toast.error("保存失败：登录状态失效，请重新登录");
      return;
    }
    markCloudSynced(saveSid, saveEid, codeSnap);
    toast.success("已保存至云端");
  } catch (e: any) {
    trackUpstreamError(e);
    toast.error(e?.message ?? "保存失败");
  } finally {
    saving.value = false;
  }
}

function onJumpToDiag(line: number, col: number) {
  editorPaneRef.value?.jumpToLine(line, col);
  activeMobileTab.value = "code";
}

function stopTickBeacon() {
  if (!workspaceReady) return;
  if (loadedEid && q.value) {
    saveDraftSync(loadedScheme || schemeId.value, loadedEid, code.value, template.value);
  }
  // 只有 leader 且 relay 正记着自己这道题才停表: 别的 tab 可能正在用计时器
  if (shouldStopTickOnUnload()) {
    navigator.sendBeacon("/api/exam/tick", JSON.stringify({ eid: null }));
  }
  if (dbg.active.value) {
    // 调试中断中直接关页: 尽力通知 relay 释放服务端 gdb 会话
    navigator.sendBeacon("/api/gdb/oj/debug/quit", new Blob(
      [JSON.stringify({ eID: loadedEid, isTeacher: false })],
      { type: "application/json" },
    ));
  }
}

async function logout() {
  try {
    await dbg.abandon(loadedEid); // 即使 compile 在途也作废代际并 quit
    requestExamTick(null);
    await waitTickChain();
    await api.logout();
  } catch {}
  navigateTo("/login");
}

// 窄屏输出 tab 用 display:none 藏编辑器, 切回时 CodeMirror 需要重新量测
watch(activeMobileTab, (tab) => {
  if (tab === "code") editorPaneRef.value?.refreshMeasure?.();
});

function onResizerKeydown(type: "sidebar" | "statement" | "output", e: KeyboardEvent) {
  const step = e.shiftKey ? 32 : 16;
  if (e.key === "Enter" || e.key === " ") {
    e.preventDefault();
    if (type === "sidebar") toggleSidebar();
    else if (type === "statement") toggleStatement();
    else toggleOutputCollapse();
    return;
  }
  if (type === "sidebar") {
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
    e.preventDefault();
    const delta = e.key === "ArrowRight" ? step : -step;
    sidebarWidth.value = Math.max(160, Math.min(520, sidebarWidth.value + delta));
    try { localStorage.setItem("an.sidebarW", String(sidebarWidth.value)); } catch {}
  } else if (type === "statement") {
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
    e.preventDefault();
    const delta = e.key === "ArrowRight" ? step : -step;
    statementWidth.value = Math.max(200, Math.min(900, statementWidth.value + delta));
    try { localStorage.setItem("an.statementW", String(statementWidth.value)); } catch {}
  } else if (outputDock.value === "bottom") {
    if (e.key !== "ArrowUp" && e.key !== "ArrowDown") return;
    e.preventDefault();
    const delta = e.key === "ArrowUp" ? step : -step;
    outputHeight.value = Math.max(34, Math.min(Math.floor(window.innerHeight * 0.8), outputHeight.value + delta));
    try { localStorage.setItem("an.outH", String(outputHeight.value)); } catch {}
  } else {
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
    e.preventDefault();
    const delta = e.key === "ArrowLeft" ? step : -step;
    outputWidth.value = Math.max(220, Math.min(Math.floor(window.innerWidth * 0.7), outputWidth.value + delta));
    try { localStorage.setItem("an.outW", String(outputWidth.value)); } catch {}
  }
}

// 监听/定时器的清理由 VueUse 与 useExamTick 的作用域处置自动完成
onBeforeUnmount(() => {
  disposed = true;
  dbg.abandon(loadedEid);
  requestExamTick(null);
});
</script>

<template>
  <div class="app-shell">
    <!-- 移动端遮罩 -->
    <div
      v-if="mobileSidebarOpen"
      class="mobile-scrim"
      @click="mobileSidebarOpen = false"
    />

    <!-- 工作台主体分栏 -->
    <div
      class="app-body"
      :class="[
        { 'is-resizing': isResizing },
        `mobile-tab-${activeMobileTab}`
      ]"
    >
      <!-- 1. 题库 (可自由调整宽度，支持双击/快捷键折叠) -->
      <Sidebar
        class="sidebar-host"
        :class="{ 'desktop-hidden': sidebarCollapsed }"
        ref="sidebarRef"
        v-model:sid="schemeId"
        v-model:cid="classId"
        :style="{ width: `${sidebarWidth}px`, flex: 'none' }"
        :groups="groups"
        :active-eid="activeEid"
        :passed="passed"
        :total="catalog.length"
        :open="mobileSidebarOpen"
        :student-name="auth?.studentName"
        :mock="mock"
        :kicked="exam.kicked"
        :offline="isOffline"
        :waf-cooldown="wafCooldown"
        :loading="loadingCatalog"
        :error="catalog.length === 0 ? pageError : ''"
        :is-dark="isDark"
        @select="(eid) => { mobileSidebarOpen = false; activeMobileTab = 'statement'; openQuestion(eid); }"
        @open-course-modal="showCourseModal = true"
        @open-settings="showSettingsModal = true"
        @toggle-theme="toggleTheme"
        @logout="logout"
        @close="mobileSidebarOpen = false"
        @retry="retryLoad"
      />

      <!-- 题库与题面垂直分割线 -->
      <div
        v-show="!sidebarCollapsed"
        class="app-resizer v-resizer sidebar-resizer"
        :class="{ dragging: activeResizer === 'sidebar' }"
        role="separator"
        aria-orientation="vertical"
        aria-label="调节题库宽度"
        tabindex="0"
        title="拖拽调节题库宽度，双击折叠/展开"
        style="touch-action: none"
        @pointerdown="startResize('sidebar', $event)"
        @dblclick="toggleSidebar"
        @keydown="onResizerKeydown('sidebar', $event)"
      />

      <!-- 2. 题面规范 (可自由调整宽度，支持双击折叠) -->
      <section
        v-show="!statementCollapsed"
        class="statement-panel"
        aria-label="题目描述"
        :style="{ width: `${statementWidth}px`, flex: 'none' }"
      >
        <Statement
          v-if="q"
          :doc="q.doc"
          :pname="active?.pname"
          :pmemo="active?.pmemo"
          :chap-name="active?.chapName"
          :passed="active?.pass"
          :font-size="settings.statementFontSize"
        />

        <div v-else class="empty-statement">
          <template v-if="loadingQuestion || loadingCatalog">
            <p class="empty-title">{{ loadingCatalog ? "正在载入题库…" : "正在载入题目…" }}</p>
            <p class="empty-sub font-mono">{{ schemeId }}/{{ classId }}</p>
          </template>
          <template v-else-if="pageError">
            <p class="empty-title err">{{ pageError }}</p>
            <button type="button" class="btn btn-ghost empty-action" @click="retryLoad">
              重试
            </button>
          </template>
          <template v-else-if="catalog.length === 0">
            <p class="empty-title">当前方案暂无题目</p>
            <p class="empty-sub font-mono">方案 {{ schemeId }} / 班级 {{ classId }}</p>
            <button type="button" class="btn btn-ghost empty-action" @click="showCourseModal = true">
              切换方案 / 班级
            </button>
          </template>
          <template v-else>
            <p class="empty-title">在题库中选择一道题</p>
            <p class="empty-sub font-mono">{{ passed }}/{{ catalog.length }}</p>
          </template>
        </div>
      </section>

      <!-- 题面与编辑器垂直分割线 -->
      <div
        v-show="!statementCollapsed"
        class="app-resizer v-resizer statement-resizer"
        :class="{ dragging: activeResizer === 'statement' }"
        role="separator"
        aria-orientation="vertical"
        aria-label="调节题面宽度"
        tabindex="0"
        title="拖拽调节题面宽度，双击折叠/展开"
        style="touch-action: none"
        @pointerdown="startResize('statement', $event)"
        @dblclick="toggleStatement"
        @keydown="onResizerKeydown('statement', $event)"
      />

      <!-- 3. 代码编辑器与输出 (全弹性工作台) -->
      <section class="editor-workbench">
        <!-- 编辑器工具栏 -->
        <div class="workbench-bar">
          <!-- 题库显示/隐藏快捷按钮: 桌面折叠后的重入点, ≤800 由底部 tabbar 的「题库」接管 -->
          <button
            type="button"
            class="btn btn-ghost toggle-panel-btn library-toggle-btn"
            :class="{ active: !sidebarCollapsed }"
            :aria-expanded="!sidebarCollapsed"
            :title="sidebarCollapsed ? '展开题库 (Ctrl+B)' : '收起题库 (Ctrl+B)'"
            @click="toggleSidebar"
          >
            <PanelLeft class="icon" :size="13" :stroke-width="1.5" />
            <span class="btn-label">题库</span>
          </button>

          <!-- 题面显示/隐藏快捷按钮 -->
          <button
            type="button"
            class="btn btn-ghost toggle-panel-btn"
            :class="{ active: !statementCollapsed }"
            :aria-expanded="!statementCollapsed"
            :title="statementCollapsed ? '展开题面' : '收起题面'"
            @click="toggleStatement"
          >
            <BookOpen class="icon" :size="13" :stroke-width="1.5" />
            <span class="btn-label">题面</span>
          </button>

          <div class="bar-divider" />

          <div class="file-name font-mono">
            <span class="file-name-text">{{ active?.pname ? `${active.pname}.cpp` : "solution.cpp" }}</span>
            <span v-if="isDraftModified && !dbg.active.value" class="draft-indicator" title="未保存的本地草稿" />
            <span v-if="dbg.active.value" class="dbg-session-tag" :title="dbg.line.value ? `调试，行 ${dbg.line.value}` : '调试中'">
              <span class="dbg-live-dot" />
              <span class="btn-label">{{ dbg.line.value ? `行 ${dbg.line.value}` : "调试中" }}</span>
            </span>
          </div>

          <!-- 上一题 / 下一题极简切题导航 (支持快捷键 Alt+[ / Alt+]) -->
          <div v-if="flatCatalog.length > 0" class="nav-question-group">
            <button
              type="button"
              class="btn-icon nav-q-btn"
              :disabled="!prevEid"
              aria-label="上一题"
              title="上一题 (Alt+[)"
              @click="prevEid && openQuestion(prevEid)"
            >
              <ChevronLeft class="icon" :size="12" :stroke-width="1.8" />
            </button>
            <span class="q-seq font-mono" :title="`第 ${activeSeq} 题，共 ${flatCatalog.length} 题`">
              {{ activeSeq }}<span class="seq-total">/{{ flatCatalog.length }}</span>
            </span>
            <button
              type="button"
              class="btn-icon nav-q-btn"
              :disabled="!nextEid"
              aria-label="下一题"
              title="下一题 (Alt+])"
              @click="nextEid && openQuestion(nextEid)"
            >
              <ChevronRight class="icon" :size="12" :stroke-width="1.8" />
            </button>
          </div>

          <!-- 调试控制器组 -->
          <div v-if="dbg.active.value" class="debug-pill-bar">
            <button
              type="button"
              class="btn btn-ghost dbg-pill-btn"
              :disabled="dbg.busy.value"
              title="继续执行 (F9)"
              @click="dbg.resume(loadedEid)"
            >
              <Play class="icon" :size="13" :stroke-width="1.8" />
              <span class="btn-label">继续</span>
            </button>

            <button
              type="button"
              class="btn btn-ghost dbg-pill-btn"
              :disabled="dbg.busy.value"
              title="单步步过 (F10)"
              @click="dbg.stepOver(loadedEid)"
            >
              <StepForward class="icon" :size="13" :stroke-width="1.5" />
              <span class="btn-label">步过</span>
            </button>

            <button
              type="button"
              class="btn btn-ghost dbg-pill-btn"
              :disabled="dbg.busy.value"
              title="单步步入 (F11)"
              @click="dbg.stepInto(loadedEid)"
            >
              <ArrowDownToLine class="icon" :size="13" :stroke-width="1.5" />
              <span class="btn-label">步入</span>
            </button>

            <button
              type="button"
              class="btn btn-ghost dbg-pill-btn"
              :disabled="dbg.busy.value"
              title="单步跳出 (Shift+F11)"
              @click="dbg.skipOut(loadedEid)"
            >
              <ArrowUpFromLine class="icon" :size="13" :stroke-width="1.5" />
              <span class="btn-label">跳出</span>
            </button>

            <div class="dbg-pill-sep" />

            <button
              type="button"
              class="btn btn-ghost dbg-pill-btn stop"
              :disabled="dbg.busy.value"
              title="停止调试 (F8)"
              @click="dbg.quit(loadedEid)"
            >
              <Square class="icon" :size="12" :stroke-width="1.8" />
              <span class="btn-label">停止</span>
            </button>
          </div>

          <div class="spacer" />

          <div class="workbench-actions">
            <button
              v-if="canUndoReset"
              type="button"
              class="btn btn-ghost undo-reset-btn"
              title="撤销模板重置，恢复我的代码"
              @click="undoReset"
            >
              <Undo2 class="icon" :size="12" :stroke-width="1.6" />
              <span class="btn-label">撤销重置</span>
            </button>

            <button
              v-if="canRestoreDraft"
              type="button"
              class="btn btn-ghost undo-reset-btn"
              title="取回被云端更新替换下来的本地草稿"
              @click="restoreDisplacedDraft"
            >
              <History class="icon" :size="12" :stroke-width="1.6" />
              <span class="btn-label">恢复草稿</span>
            </button>

            <button
              type="button"
              class="btn btn-ghost"
              :disabled="!q || dbg.active.value"
              title="重置为平台初始模板"
              @click="askResetTemplate"
            >
              <RotateCcw class="icon" :size="13" :stroke-width="1.5" />
              <span class="btn-label">重置</span>
            </button>

            <button
              type="button"
              class="btn btn-ghost"
              :disabled="!q || saving || dbg.active.value"
              :aria-busy="saving || undefined"
              title="保存到云端 (Ctrl+S)"
              @click="saveCode"
            >
              <Save class="icon" :size="13" :stroke-width="1.5" />
              <span class="btn-label">{{ saving ? "保存中" : "保存" }}</span>
            </button>

            <button
              type="button"
              class="btn btn-ghost"
              :class="{ 'debug-active-btn': dbg.active.value }"
              :disabled="!q || dbg.busy.value || dbg.active.value"
              title="开启调试 (F8)，无断点则从 main 进入"
              @click="startDebug"
            >
              <Bug class="icon" :size="13" :stroke-width="1.5" />
              <span class="btn-label">调试</span>
            </button>

            <button
              type="button"
              class="btn btn-ghost run-btn"
              :disabled="!q || running || dbg.active.value"
              :aria-busy="running || undefined"
              title="编译并运行 (Ctrl+Enter)"
              @click="run()"
            >
              <Play v-if="!running" class="icon" :size="12" :stroke-width="1.8" />
              <span>{{ running ? "运行中" : "运行" }}</span>
            </button>
          </div>
        </div>

        <div class="workbench-main" :class="[outputDock]">
          <!-- 代码编辑器容器 -->
          <EditorPane
            :key="`${schemeId}:${activeEid}`"
            ref="editorPaneRef"
            v-model="code"
            :diagnostics="diags"
            :is-dark="isDark"
            :font-size="settings.editorFontSize"
            :tab-size="settings.editorTabSize"
            :line-wrapping="settings.editorLineWrapping"
            :breakpoints="dbg.bps.value"
            :debug-line="dbg.line.value"
            :readonly="dbg.active.value || !q"
            :run-shortcut="() => run()"
            @save="saveCode"
            @toggle-breakpoint="dbg.toggleBp"
          />

          <!-- 分割线调节手柄 (底部停靠为行高度调节，右侧停靠为列宽度调节) -->
          <div
            class="app-resizer"
            :class="[
              outputDock === 'bottom' ? 'h-resizer' : 'v-resizer',
              { dragging: activeResizer === 'output' },
            ]"
            role="separator"
            :aria-orientation="outputDock === 'bottom' ? 'horizontal' : 'vertical'"
            aria-label="调节控制台大小"
            tabindex="0"
            :title="outputDock === 'bottom' ? '拖拽调节控制台高度，双击折叠/展开' : '拖拽调节控制台宽度，双击折叠/展开'"
            style="touch-action: none"
            @pointerdown="startResize('output', $event)"
            @dblclick="toggleOutputCollapse"
            @keydown="onResizerKeydown('output', $event)"
          />

          <!-- 综合运行与调试控制台（变量 · 调用栈 · 终端） -->
          <OutputPanel
            :style="outputPanelStyle"
            :dock="outputDock"
            :collapsed="outputCollapsed"
            :maximized="outputMaximized"
            :result="result"
            :running="running"
            :error="pageError"
            :diagnostics="diags"
            :debug="{
              active: dbg.active.value,
              line: dbg.line.value,
              output: dbg.output.value,
              needInput: dbg.needInput.value,
              vars: dbg.vars.value,
              stack: dbg.stack.value,
              busy: dbg.busy.value,
            }"
            @toggle-dock="handleToggleDock"
            @toggle-collapse="toggleOutputCollapse"
            @toggle-maximize="toggleOutputMaximize"
            @submit-input="(t) => dbg.active.value ? dbg.sendInput(t, loadedEid) : run(t)"
            @jump-to-diag="onJumpToDiag"
            @jump-to-line="(ln) => { editorPaneRef?.jumpToLine(ln); activeMobileTab = 'code'; }"
          />
        </div>
      </section>
    </div>

    <!-- 窄屏导航条: 首项「题库」是动作而非页签, 弹出题库抽屉 -->
    <nav class="mobile-tabbar" aria-label="移动端面板切换">
      <button
        type="button"
        class="tab-btn"
        :aria-expanded="mobileSidebarOpen"
        @click="mobileSidebarOpen = true"
      >
        题库
      </button>
      <button
        type="button"
        class="tab-btn"
        :class="{ active: activeMobileTab === 'statement' }"
        :aria-current="activeMobileTab === 'statement' ? 'page' : undefined"
        @click="activeMobileTab = 'statement'"
      >
        题面
      </button>
      <button
        type="button"
        class="tab-btn"
        :class="{ active: activeMobileTab === 'code' }"
        :aria-current="activeMobileTab === 'code' ? 'page' : undefined"
        @click="activeMobileTab = 'code'"
      >
        代码
      </button>
      <button
        type="button"
        class="tab-btn"
        :class="{ active: activeMobileTab === 'output' }"
        :aria-current="activeMobileTab === 'output' ? 'page' : undefined"
        @click="activeMobileTab = 'output'"
      >
        输出
      </button>
    </nav>

    <CourseModal
      :show="showCourseModal"
      :scheme-id="schemeId"
      :class-id="classId"
      @close="showCourseModal = false"
      @apply="applyCourse"
    />

    <SettingsModal
      :show="showSettingsModal"
      @close="showSettingsModal = false"
    />

    <ConfirmDialog
      :show="showResetConfirm"
      title="重置代码"
      message="确认重置当前题目代码为初始模板？当前代码会先备份，可通过「撤销重置」恢复。"
      confirm-label="重置"
      @confirm="doResetTemplate"
      @close="showResetConfirm = false"
    />

    <Toast />
  </div>
</template>

<style scoped>
.sidebar-host.desktop-hidden {
  display: none;
}

@media (max-width: 1200px) {
  .sidebar-host.desktop-hidden {
    display: flex;
  }
}

/* 工作条上的常显文字标签: 替代旧「按钮文字」设置, 图标不表意的按钮一律带字 */
.btn-label {
  font-size: 11.5px;
  line-height: 1;
  white-space: nowrap;
}

/* 题面面板 */
.statement-panel {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  overflow: hidden;
  background: var(--bg-surface);
}

.empty-statement {
  height: 100%;
  padding: 24px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  text-align: center;
  color: var(--text-muted);
}

.empty-title {
  font-size: 13px;
  color: var(--text-secondary);
  margin: 0;
  line-height: 1.5;
  word-break: break-word;
}

.empty-title.err {
  color: var(--err);
}

.empty-sub {
  font-size: 12px;
  font-family: var(--font-mono);
  margin: 0;
}

.empty-action {
  margin-top: 4px;
}

/* 编辑器工作区 */
.editor-workbench {
  flex: 1 1 0;
  min-width: 0;
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  background: var(--bg-canvas);
  border-left: 1px solid var(--border-subtle);
  position: relative;
}

.workbench-main {
  flex: 1;
  display: flex;
  min-height: 0;
  min-width: 0;
  position: relative;
  overflow: hidden;
}

.workbench-main.bottom {
  flex-direction: column;
}

.workbench-main.right {
  flex-direction: row;
}

.toggle-panel-btn {
  font-size: 11.5px;
  color: var(--text-muted);
  padding: 3px 7px;
}

.toggle-panel-btn.active {
  color: var(--text-secondary);
}

.bar-divider {
  width: 1px;
  height: 12px;
  background: var(--border);
  margin: 0 2px;
}

.workbench-bar {
  height: var(--workbench-h);
  flex: none;
  display: flex;
  align-items: center;
  padding: 0 12px;
  background: var(--bg-surface);
  border-bottom: 1px solid var(--border-subtle);
  gap: 10px;
  overflow-x: auto;
  scrollbar-width: none;
}

.workbench-bar::-webkit-scrollbar {
  display: none;
}

.file-name {
  font-size: 12px;
  font-weight: 500;
  color: var(--text-primary);
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  flex-shrink: 1;
}

.file-name-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.draft-indicator {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--warn);
}

.dbg-session-tag {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
  font-weight: 500;
  color: var(--warn);
  letter-spacing: 0.01em;
}

/* 题目切题导航 */
.nav-question-group {
  display: inline-flex;
  align-items: center;
  gap: 1px;
  background: var(--bg-canvas);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-control);
  padding: 1px 2px;
}

.nav-q-btn {
  width: 20px;
  height: 20px;
  color: var(--text-muted);
}

.nav-q-btn:disabled {
  opacity: 0.25;
}

.q-seq {
  font-size: 10.5px;
  color: var(--text-secondary);
  padding: 0 3px;
  user-select: none;
}

.seq-total {
  color: var(--text-muted);
}

.undo-reset-btn {
  color: var(--warn) !important;
  font-size: 11.5px;
}

.dbg-live-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--warn);
}

@media (prefers-reduced-motion: no-preference) {
  .dbg-live-dot {
    animation: dbg-pulse 1.8s infinite ease-in-out;
  }
}

@keyframes dbg-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.35; }
}

/* 调试控制器组 */
.debug-pill-bar {
  display: flex;
  align-items: center;
  gap: 2px;
  padding: 0 2px;
}

.dbg-pill-btn {
  font-size: 11px;
  padding: 3px 7px;
  gap: 5px;
}

.dbg-pill-btn:disabled {
  opacity: 0.38;
}

.dbg-pill-btn.stop {
  color: var(--err);
}

.dbg-pill-btn.stop:hover:not(:disabled) {
  color: var(--err);
  background: var(--bg-elevated);
}

.dbg-pill-sep {
  width: 1px;
  height: 12px;
  background: var(--border);
  margin: 0 4px;
}

.workbench-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.debug-active-btn {
  color: var(--warn) !important;
  font-weight: 600;
}

.run-btn {
  padding: 4px 12px;
}

/* ==========================================================================
   RESIZERS — 1px 可见线 + 7px 命中区，hover/拖拽变靛蓝
   ========================================================================== */
.app-resizer {
  flex: none;
  background: transparent;
  position: relative;
  z-index: 10;
  transition: background 0.15s ease;
  user-select: none;
}

.app-resizer.v-resizer {
  width: 7px;
  cursor: col-resize;
  margin: 0 -3px;
}

.app-resizer.v-resizer::after {
  content: "";
  position: absolute;
  top: 0;
  bottom: 0;
  left: 3px;
  width: 1px;
  background: var(--border-subtle);
  transition: background 0.15s ease;
}

.app-resizer.h-resizer {
  height: 7px;
  cursor: row-resize;
  margin: -3px 0;
}

.app-resizer.h-resizer::after {
  content: "";
  position: absolute;
  left: 0;
  right: 0;
  top: 3px;
  height: 1px;
  background: var(--border-subtle);
  transition: background 0.15s ease;
}

.app-resizer:hover::after,
.app-resizer.dragging::after,
.app-resizer:focus-visible::after {
  background: var(--accent);
}

.app-resizer:focus-visible {
  outline: none;
}

.app-resizer.v-resizer:focus-visible::after {
  width: 2px;
  left: 2.5px;
}

.app-resizer.h-resizer:focus-visible::after {
  height: 2px;
  top: 2.5px;
}

/* 拖拽期间全局禁用选区与指针拦截 */
.app-body.is-resizing {
  user-select: none !important;
}

.app-body.is-resizing * {
  pointer-events: none !important;
}

.app-body.is-resizing .app-resizer {
  pointer-events: auto !important;
}

.mobile-tabbar {
  display: none;
}

/* 响应式断点：1200px 以下（涵盖主流平板横屏如 iPad 1024/1180/1194）侧栏自动收为抽屉，保持题面+代码宽阔工作台 */
@media (max-width: 1200px) {
  /* 抽屉模式下题库按钮由工作条让位给底部 tabbar/抽屉自身 */
  .library-toggle-btn {
    display: none !important;
  }
  .sidebar-resizer {
    display: none !important;
  }
  .mobile-scrim {
    position: fixed;
    inset: 0;
    z-index: 40;
    background: var(--overlay);
  }
}

@media (max-width: 800px) {
  .app-resizer {
    display: none !important;
  }

  .statement-panel,
  .editor-workbench {
    display: none;
    width: 100% !important;
  }

  .app-body.mobile-tab-statement .statement-panel {
    display: flex !important;
  }

  .app-body.mobile-tab-code .editor-workbench {
    display: flex !important;
  }

  .app-body.mobile-tab-code .output-panel {
    display: none !important;
  }

  .app-body.mobile-tab-output .editor-workbench {
    display: flex !important;
  }

  .app-body.mobile-tab-output .editor-pane {
    display: none;
  }

  .app-body.mobile-tab-output .output-panel {
    height: 100% !important;
    width: 100% !important;
  }

  .mobile-tabbar {
    display: flex;
    height: calc(44px + env(safe-area-inset-bottom, 0px));
    padding-bottom: env(safe-area-inset-bottom, 0px);
    background: var(--bg-surface);
    border-top: 1px solid var(--border-subtle);
    flex: none;
  }

  .tab-btn {
    flex: 1;
    font-size: 12px;
    color: var(--text-muted);
    transition: color 0.12s;
  }

  .tab-btn.active {
    color: var(--text-primary);
    font-weight: 600;
  }
}
</style>
