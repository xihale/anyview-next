import type { SchemeData, RunResult, Diag } from "~/composables/useApi";

interface CachedResult {
  result: RunResult;
  diags: Diag[];
  codeSnapshot: string;
}

// 草稿条目: code=本地最新编辑; seen=最后一次确认过的云端原文(裁决基线)。
// 加载裁决(resolveDraft):
//   code == 云端              → 一致, 直接用云端
//   云端 == seen(云端没动过)  → 本地编辑优先(刷新不丢未保存的改动)
//   云端 != seen(被外部改过)  → 云端优先(它设备/外部写入可见), 旧草稿挪备份兜底
// 旧格式(纯字符串草稿)按 seen='' 处理 → 云端优先。
interface DraftEntry {
  code: string;
  seen: string;
}

// 内存单例缓存，跨组件与路由持久留存
const SS_KEY = "an.qc.v2";
const MAX_MEM_QUESTIONS = 120;
const MAX_PERSIST_QUESTIONS = 30;
const MAX_MEM_RESULTS = 60;

const memorySchemeCache = new Map<string, SchemeData>();
const memoryResultCache = new Map<string, CachedResult>();

// typeof 守卫而非 import.meta.client: SSR/裸 vitest 两边语义一致(裸 vitest 无该标记)
const hasLS = typeof localStorage !== "undefined";
const hasSS = typeof sessionStorage !== "undefined";

let sessionHydrated = false;
let persistTimer: ReturnType<typeof setTimeout> | null = null;
let persistDirty = false;

function hydrateSessionCache() {
  if (sessionHydrated || !hasSS) return;
  sessionHydrated = true;
  if (memorySchemeCache.size > 0) return;
  try {
    const raw = sessionStorage.getItem(SS_KEY);
    if (raw) {
      const obj = JSON.parse(raw);
      for (const [k, v] of Object.entries(obj)) {
        if (k.includes(":")) memorySchemeCache.set(k, v as SchemeData);
      }
    }
  } catch {}
}

function persistSessionCache() {
  if (!hasSS || !persistDirty) return;
  persistDirty = false;
  try {
    const entries = [...memorySchemeCache.entries()].slice(-MAX_PERSIST_QUESTIONS);
    const obj = Object.fromEntries(entries);
    sessionStorage.setItem(SS_KEY, JSON.stringify(obj));
  } catch {}
}

// 落盘防抖 + 闲时调度: persist 要 stringify 全部缓存题(含整篇题面 HTML),
// 推迟到闲时一次写齐，避免阻塞切题/打字关键路径。
function schedulePersist() {
  persistDirty = true;
  if (!hasSS || persistTimer) return;
  persistTimer = setTimeout(() => {
    persistTimer = null;
    const ric = typeof window !== "undefined" ? (window as any).requestIdleCallback : undefined;
    if (typeof ric === "function") {
      ric(() => persistSessionCache(), { timeout: 1500 });
    } else {
      persistSessionCache();
    }
  }, 600);
}

function qKey(schemeId: number, eid: number) {
  return `${schemeId}:${eid}`;
}

export function useQuestionCache() {
  hydrateSessionCache();

  function getCachedQuestion(schemeId: number, eid: number): SchemeData | null {
    const key = qKey(schemeId, eid);
    const hit = memorySchemeCache.get(key);
    if (!hit) return null;
    // LRU 刷新: 保证最近访问的题目留在 sessionStorage 热集里
    memorySchemeCache.delete(key);
    memorySchemeCache.set(key, hit);
    return hit;
  }

  function setCachedQuestion(schemeId: number, eid: number, data: SchemeData) {
    const key = qKey(schemeId, eid);
    const prev = memorySchemeCache.get(key);
    const changed =
      !prev ||
      prev.stuCode !== data.stuCode ||
      prev.doc !== data.doc ||
      prev.questionFullName !== data.questionFullName;

    if (prev) {
      memorySchemeCache.delete(key);
    } else if (memorySchemeCache.size >= MAX_MEM_QUESTIONS) {
      const oldest = memorySchemeCache.keys().next().value;
      if (oldest !== undefined) memorySchemeCache.delete(oldest);
    }
    memorySchemeCache.set(key, data);
    if (changed) schedulePersist();
  }

  function getCachedResult(schemeId: number, eid: number, currentCode: string): CachedResult | null {
    const cached = memoryResultCache.get(qKey(schemeId, eid));
    if (!cached) return null;
    if (cached.codeSnapshot !== currentCode) return null;
    return cached;
  }

  function setCachedResult(schemeId: number, eid: number, result: RunResult, diags: Diag[], codeSnapshot: string) {
    const key = qKey(schemeId, eid);
    if (memoryResultCache.has(key)) {
      memoryResultCache.delete(key);
    } else if (memoryResultCache.size >= MAX_MEM_RESULTS) {
      const oldest = memoryResultCache.keys().next().value;
      if (oldest !== undefined) memoryResultCache.delete(oldest);
    }
    memoryResultCache.set(key, { result, diags, codeSnapshot });
  }

  // 安全草稿体系
  const draftKey = (schemeId: number, eid: number) => `an.draft.${schemeId}.${eid}`;
  const backupKey = (schemeId: number, eid: number) => `an.draft.bak.${schemeId}.${eid}`;

  function getDraftEntry(schemeId: number, eid: number): DraftEntry | null {
    if (!hasLS) return null;
    try {
      const raw = localStorage.getItem(draftKey(schemeId, eid));
      if (!raw) return null;
      if (raw.charCodeAt(0) === 123 /* '{' */) {
        try {
          const e = JSON.parse(raw);
          if (e && typeof e.code === "string") {
            return { code: e.code, seen: typeof e.seen === "string" ? e.seen : "" };
          }
        } catch {}
      }
      return { code: raw, seen: "" };
    } catch {
      return null;
    }
  }

  function writeDraftEntry(schemeId: number, eid: number, entry: DraftEntry) {
    if (!hasLS || !eid) return;
    // quota 满等异常必须吞掉: 该函数在切题/运行/关页的关键路径上, 炸掉会卡死切题
    try {
      localStorage.setItem(draftKey(schemeId, eid), JSON.stringify(entry));
    } catch {}
  }

  // 加载裁决: 给定云端原文, 决定编辑器初始代码与是否提示"云端已更新"
  function resolveDraft(schemeId: number, eid: number, cloudCode: string): { code: string; cloudUpdated: boolean } {
    const d = getDraftEntry(schemeId, eid);
    if (!d) return { code: cloudCode, cloudUpdated: false };
    if (d.code === cloudCode) return { code: cloudCode, cloudUpdated: false };
    if (d.seen && d.seen === cloudCode) {
      // 云端没动过 → 未保存的本地编辑仍然最新
      return { code: d.code, cloudUpdated: false };
    }
    // 云端被外部更新(或旧草稿无基线): 云端优先, 旧草稿挪备份
    if (d.code) backupDraft(schemeId, eid, d.code);
    writeDraftEntry(schemeId, eid, { code: cloudCode, seen: cloudCode });
    return { code: cloudCode, cloudUpdated: true };
  }

  // 打标: 云端已与 code 同步(save 成功/compile 落盘)。本地若有更新的未保存编辑则保留
  function markCloudSynced(schemeId: number, eid: number, code: string) {
    if (!hasLS || !eid) return;
    const d = getDraftEntry(schemeId, eid);
    const nextCode = d && d.code !== code ? d.code : code;
    if (d && d.code === nextCode && d.seen === code) return;
    writeDraftEntry(schemeId, eid, { code: nextCode, seen: code });
  }

  function saveDraftSync(schemeId: number, eid: number, code: string, cloudBaseline?: string) {
    if (!hasLS || !eid) return;
    const d = getDraftEntry(schemeId, eid);
    // 首次落草稿(无线索条目)时以调用方给的云端原文为基线, 否则刷新会被误判"云端已变"
    const nextSeen = d?.seen || cloudBaseline || "";
    if (d && d.code === code && d.seen === nextSeen) return;
    writeDraftEntry(schemeId, eid, { code, seen: nextSeen });
  }

  function removeDraft(schemeId: number, eid: number) {
    if (!hasLS || !eid) return;
    try {
      localStorage.removeItem(draftKey(schemeId, eid));
    } catch {}
  }

  function backupDraft(schemeId: number, eid: number, code: string) {
    if (!hasLS || !eid) return;
    try {
      localStorage.setItem(backupKey(schemeId, eid), code);
    } catch {}
  }

  function hasBackup(schemeId: number, eid: number): boolean {
    if (!hasLS || !eid) return false;
    return Boolean(localStorage.getItem(backupKey(schemeId, eid)));
  }

  function restoreBackup(schemeId: number, eid: number): string | null {
    if (!hasLS || !eid) return null;
    const bak = localStorage.getItem(backupKey(schemeId, eid));
    if (bak != null) {
      saveDraftSync(schemeId, eid, bak);
      return bak;
    }
    return null;
  }

  // 提示: 所有 saveDraftSync 调用方应把当前已加载的云端原文(template.value)作为
  // cloudBaseline 传入, 保证首写草稿就带基线

  return {
    getCachedQuestion,
    setCachedQuestion,
    getCachedResult,
    setCachedResult,
    getDraftEntry,
    resolveDraft,
    markCloudSynced,
    saveDraftSync,
    removeDraft,
    backupDraft,
    hasBackup,
    restoreBackup,
  };
}
