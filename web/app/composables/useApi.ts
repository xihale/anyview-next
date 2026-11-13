// relay API 客户端: 全部走同源 /api(dev 由 devProxy 转发, 生产同源托管)
// $fetch 显式导入: 供裸 vitest 直接加载(不走 Nuxt 自动导入)
import { $fetch } from "ofetch";
export interface AuthInfo {
  loggedIn: boolean;
  expired: boolean;
  username?: string;
  studentId?: string;
  studentName?: string;
  exp?: number;
  expInSec?: number;
}

export interface CatalogItem {
  difficulty: number;
  eid: number;
  pass: boolean;
  pname: string;
  kind: number;
  pmemo: string;
  accumTime: number; // 秒
  chapName: string;
  questionType: number;
}

export interface SchemeData {
  questionFullName: string;
  questionId: number;
  kind: number;
  doc: string;
  language: number;
  completes: unknown[];
  stuCode: string; // 上次保存的代码(base64), 非初始模板; 初始模板走 questionTemplate()
}

export interface Diag {
  line: number;
  col: number;
  severity: "error" | "warning";
  message: string;
}

export interface RunResult {
  compileOk: boolean;
  compileMsg: string;
  diagnostics?: Diag[];
  passed?: boolean;
  needInput?: boolean;
  output?: string;
  exception?: string;
  rightCount?: number;
  wrongCount?: number;
  elapsedMs: number;
  upstream?: string;
}

export interface ExamStatus {
  active: number;
  running: boolean;
  tickedAt: number;
  kicked: boolean;
  connected: boolean;
  lastError?: string;
}

// question 的响应: relay 缓存命中时秒回, stale=true 表示后台校验在途,
// 约 1.5s 后补拉一次可能拿到更新的作答代码(它设备/外部写入)
export interface QuestionFetch {
  data?: SchemeData;
  stale: boolean;
}

// ===== gdb 调试(上游 RunData, 2026-09-09 真机实测字段) =====

export interface DbgVarNode {
  name: string;
  type?: string;
  value?: string;
  innerObj?: DbgVarNode[];
}

export interface RunData {
  output?: string; // 上游是 HTML(<br>&nbsp;), 展示前须净化
  lineNum?: string;
  end?: boolean;
  needInput?: boolean;
  backtrace?: string[];
  variables?: { variate: DbgVarNode }[]; // variate 递归 innerObj
  watchPoint?: Record<string, unknown>;
  dataStruct?: unknown;
  exception?: string;
  errorOrder?: Record<string, unknown>;
  order?: number;
  path?: string;
  runRightCount?: number;
  runErrCount?: number;
  passed?: boolean;
}

export interface GdbEnvelope<T = unknown> {
  code: number;
  message?: string | null;
  data: T;
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

export interface RequestOpts {
  signal?: AbortSignal;
}

const base = $fetch.create({ baseURL: "/api", retry: 0 });

function toApiError(e: any): ApiError {
  if (e instanceof ApiError) return e;
  return new ApiError(e?.status ?? 0, e?.data?.message ?? e?.message ?? String(e));
}

interface InflightEntry<T> {
  promise: Promise<T>;
  controller: AbortController;
  refs: number;
}

export function createApiClient(fetcher = base) {
  const inflight = new Map<string, InflightEntry<any>>();
  // 命中 relay 429(限速/WAF 冷却)后短时抑制后台预取，避免继续排队雪崩
  let backgroundCooldownUntil = 0;

  async function unwrap<T>(p: Promise<T>): Promise<T> {
    try {
      return await p;
    } catch (e: any) {
      const err = toApiError(e);
      if (err.status === 429) {
        backgroundCooldownUntil = Date.now() + 15_000;
      }
      throw err;
    }
  }

  function dedupGet<T>(key: string, fn: (signal: AbortSignal) => Promise<T>, signal?: AbortSignal): Promise<T> {
    if (signal?.aborted) {
      return Promise.reject(new ApiError(0, "Aborted"));
    }

    let entry = inflight.get(key) as InflightEntry<T> | undefined;
    if (!entry) {
      const controller = new AbortController();
      const promise = unwrap(fn(controller.signal)).finally(() => {
        if (inflight.get(key) === entry) {
          inflight.delete(key);
        }
      });
      entry = { promise, controller, refs: 0 };
      inflight.set(key, entry);
    }

    entry.refs++;
    const current = entry;

    if (!signal) {
      return current.promise.finally(() => {
        current.refs = Math.max(0, current.refs - 1);
      });
    }

    return new Promise<T>((resolve, reject) => {
      let settled = false;
      const onAbort = () => {
        if (settled) return;
        settled = true;
        current.refs = Math.max(0, current.refs - 1);
        if (current.refs === 0) {
          current.controller.abort();
          if (inflight.get(key) === current) {
            inflight.delete(key);
          }
        }
        reject(new ApiError(0, "Aborted"));
      };
      signal.addEventListener("abort", onAbort, { once: true });

      current.promise.then(
        (val) => {
          if (settled) return;
          settled = true;
          signal.removeEventListener("abort", onAbort);
          current.refs = Math.max(0, current.refs - 1);
          resolve(val);
        },
        (err) => {
          if (settled) return;
          settled = true;
          signal.removeEventListener("abort", onAbort);
          current.refs = Math.max(0, current.refs - 1);
          reject(err);
        },
      );
    });
  }

  return {
    health: (opts?: RequestOpts) =>
      dedupGet("health", (signal) => fetcher<{ ok: boolean; mock: boolean }>("/health", { signal }), opts?.signal),
    authStatus: (opts?: RequestOpts) =>
      dedupGet("authStatus", (signal) => fetcher<AuthInfo>("/auth/status", { signal }), opts?.signal),
    login: (username: string, password: string) =>
      unwrap(fetcher<{ ok: boolean; student: { id: string; name: string } }>("/auth/login", {
        method: "POST",
        body: { username, password },
      })),
    logout: () => unwrap(fetcher("/auth/logout", { method: "POST" })),
    catalog: (schemeId: number, classId: number, opts?: RequestOpts) =>
      dedupGet(
        `catalog:${schemeId}:${classId}`,
        (signal) =>
          fetcher<{ code: number; data: CatalogItem[] }>(`/up/scheme-service/catalog/list`, {
            query: { schemeId, classId },
            signal,
          }),
        opts?.signal,
      ),
    // 走 raw 暴露响应头: relay 缓存的 X-Relay-Stale 要透给切题逻辑决定补拉;
    // 并发相同 GET 自动去重共享同一在途请求, 并支持 AbortSignal 取消无人等待的请求
    question: (eid: number, opts?: RequestOpts): Promise<QuestionFetch> =>
      dedupGet(
        `question:${eid}`,
        (signal) =>
          fetcher
            .raw<{ code: number; data: SchemeData }>(`/up/scheme-service/scheme/list/student/${eid}`, { signal })
            .then((r) => ({ data: r._data?.data, stale: r.headers.get("x-relay-stale") === "1" })),
        opts?.signal,
      ),
    // 初始模板(scheme/list/student 的 stuCode 是上次保存的代码, 不是模板)
    questionTemplate: (eid: number, opts?: RequestOpts) =>
      dedupGet(
        `template:${eid}`,
        (signal) =>
          fetcher<{ code: number; data: string }>(`/up/scheme-service/scheme/getQuestionTemplate/student/${eid}`, {
            signal,
          }),
        opts?.signal,
      ),
    run: (body: { eid: number; qfn: string; code: string; language?: number; kind?: number; input?: string }) =>
      unwrap(fetcher<RunResult>("/work/run", { method: "POST", body })),
    saveCode: (eid: number, code: string) =>
      unwrap(fetcher<{ ok: boolean }>("/work/save", { method: "POST", body: { eid, code } })),
    examTick: (eid: number | null) =>
      unwrap(fetcher<{ ok: boolean }>("/exam/tick", { method: "POST", body: { eid } })),
    examStatus: () => unwrap(fetcher<ExamStatus>("/exam/status")),
    // gdb-service 镜像(OJ 变体): relay 负责按原版形状编码(multipart fn 拍平)。
    // content 就原版 store/actions.js 组装的 content 原样传。
    gdb: (path: string, content: Record<string, unknown>) =>
      unwrap(fetcher<GdbEnvelope>(`/gdb/oj/${path}`, { method: "POST", body: content })),
    isBackgroundCooling: () => Date.now() < backgroundCooldownUntil,
    _inflightCount: () => inflight.size,
  };
}

const sharedApi = createApiClient();

export function useApi() {
  return sharedApi;
}

// stuCode 上传编码: base64(UTF-8); TextEncoder 出字节, 分块 String.fromCharCode 防栈溢出
export function b64encodeUtf8(s: string): string {
  const bytes = new TextEncoder().encode(s);
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) {
    bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  }
  return btoa(bin);
}

// stuCode 存档是 base64(UTF-8); atob 只给 Latin-1, 须再过 TextDecoder 还原多字节;
// 解不出(本来就是明文/非 UTF-8)则按原文用
export function decodeStuCode(s: string): string {
  if (!s) return "";
  try {
    const bin = atob(s);
    const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0));
    return new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    return s;
  }
}

export function fmtClock(sec: number): string {
  sec = Math.max(0, Math.floor(sec));
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = sec % 60;
  return h > 0
    ? `${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`
    : `${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}
