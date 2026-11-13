// gdb 调试会话(单例): OJ 变体服务端调试, 协议 = 原版 store/actions.js 流程。
// start 前必 compile(isDebug:true); 无用户断点时在 main 上临时下断再 start;
// 步过/步入/继续全打 debug/nextStep, 差异仅 cmd 裸 gdb 命令; 响应 RunData。
import type { DbgVarNode, RunData } from "~/composables/useApi";
import { b64encodeUtf8, useApi } from "~/composables/useApi";
import { useToast } from "~/composables/useToast";
// 显式导入: 供裸 vitest 直接加载(不走 Nuxt 自动导入)
import { ref, shallowRef } from "vue";

const api = useApi();
const toast = useToast();

const active = ref(false); // 会话进行中(编辑器锁定)
const busy = ref(false); // 请求在途
const line = ref(0); // 当前停行(1-based, 0=无)
// 调试变量树与调用栈每步整批替换, 用 shallowRef 避免深层递归 Proxy 开销
const vars = shallowRef<DbgVarNode[]>([]);
const stack = shallowRef<string[]>([]);
const output = ref(""); // 跨步累积的程序输出(原始 HTML, 展示层净化)
const needInput = ref(false);
const startedAt = ref(0);

// 断点按题持久化(纯客户端状态, start 时随 bp 字段上送)
const bps = ref<number[]>([]);
let bpEid = 0;
let bpScheme = 0;

function bpKey(eid: number) {
  return `an.bp.${bpScheme || 0}.${eid}`;
}

function loadBps(schemeId: number, eid: number) {
  bpScheme = schemeId;
  bpEid = eid;
  try {
    const raw = JSON.parse(localStorage.getItem(bpKey(eid)) ?? "[]");
    bps.value = Array.isArray(raw) ? raw.filter((n) => Number.isInteger(n) && n > 0) : [];
  } catch {
    bps.value = [];
  }
}

function saveBps() {
  if (!bpEid) return;
  try {
    localStorage.setItem(bpKey(bpEid), JSON.stringify(bps.value));
  } catch {}
}

/** 找 main 定义行(1-based)。优先函数声明行; 找不到返回 0。不写入用户断点集。 */
export function findMainLine(code: string): number {
  const lines = code.split(/\r?\n/);
  const def = /^\s*(?:(?:inline|static|extern)\s+)*(?:int|void)\s+main\s*\(/;
  for (let i = 0; i < lines.length; i++) {
    const s = lines[i] ?? "";
    if (/^\s*\/\//.test(s) || /^\s*\*/.test(s)) continue;
    if (!def.test(s) || s.includes(";")) continue;
    return i + 1;
  }
  return 0;
}

function toggleBp(ln: number) {
  if (active.value) return; // 调试中断点集冻结
  const i = bps.value.indexOf(ln);
  if (i >= 0) bps.value.splice(i, 1);
  else bps.value.push(ln);
  bps.value.sort((a, b) => a - b);
  saveBps();
}

function absorb(d: RunData | null | undefined) {
  if (!d) return false;
  line.value = Number(d.lineNum ?? 0) || 0;
  vars.value = (d.variables ?? []).map((v) => v.variate).filter(Boolean);
  stack.value = d.backtrace ?? [];
  needInput.value = !!d.needInput;
  if (d.output) output.value += d.output;
  return !!d.end;
}

// 切题代际: resetForQuestion 递增后, 旧题在途请求的响应一律作废,
// 避免 start/step 的迟到结果把旧会话的变量/输出写进新题的展示状态
let generation = 0;
function stale(gen: number) {
  return gen !== generation;
}

function clearSessionState() {
  active.value = false;
  needInput.value = false;
  line.value = 0;
  vars.value = [];
  stack.value = [];
  output.value = "";
  startedAt.value = 0;
}

let busyDepth = 0;
function enterBusy() {
  busyDepth++;
  busy.value = true;
}
function leaveBusy() {
  busyDepth = Math.max(0, busyDepth - 1);
  busy.value = busyDepth > 0;
}

async function call(path: string, content: Record<string, unknown>): Promise<RunData | null> {
  enterBusy();
  try {
    const env = await api.gdb(path, content);
    // 成功码 200/0 + 上游"废弃但成功"兼容码 31/41/70, 照常读 data(fuckView analysis/08 响应封装)
    if (env.code !== 200 && env.code !== 0 && env.code !== 41 && env.code !== 31 && env.code !== 70) {
      throw new Error(env.message || `上游返回 ${env.code}`);
    }
    return (env.data ?? null) as RunData | null;
  } finally {
    leaveBusy();
  }
}

// 开调试: compile(isDebug) → start(bp)。code 传当前编辑器内容(会写入平台存档,
// 与运行一致); 若不想动存档, 调用方须先还原为平台已存代码。
async function start(opts: { eid: number; qfn: string; code: string }) {
  if (active.value || busy.value) return;
  const gen = generation;
  const bpSend = [...bps.value]; // 快照: compile 在途期间断点集不得漂移
  let bpList = bpSend;
  if (!bpList.length) {
    const main = findMainLine(opts.code);
    bpList = main > 0 ? [main] : [];
  }
  if (bpList.length === 0) {
    toast.warning("找不到 main，请先在行号左侧设一个断点");
    return;
  }
  enterBusy(); // 跨 compile→start 保持 busy, 避免 call() 间隙里二次开调试
  try {
    const c = await call("code/compile", {
      questionRes: { questionFullName: opts.qfn },
      eID: opts.eid,
      stuCode: b64encodeUtf8(opts.code),
      isTeacher: false,
      isDebug: true,
    });
    if (stale(gen)) return;
    const result = String((c as any)?.result ?? "");
    if (!result.includes("编译成功")) {
      toast.error("编译未通过，无法开启调试");
      return;
    }
    const d = await call("debug/start", {
      bp: bpList.join(","),
      questionFullName: opts.qfn,
      eID: opts.eid,
      isTeacher: false,
    });
    if (stale(gen)) {
      // 服务端会话已建立, 必须释放, 且不得动新题的 UI 状态
      try {
        await api.gdb("debug/quit", { eID: opts.eid, isTeacher: false });
      } catch {}
      return;
    }
    output.value = "";
    startedAt.value = Date.now();
    const end = absorb(d);
    if ((d as any)?.exception) {
      toast.error(`调试异常: ${(d as any).exception}`);
      await forceQuit(opts.eid);
      return;
    }
    active.value = true;
    if (end) {
      toast.info("程序未经过任何断点，调试已结束");
      await forceQuit(opts.eid);
    } else {
      toast.success(line.value ? `调试中  停在第 ${line.value} 行` : "调试中");
    }
  } catch (e: any) {
    toast.error(`开启调试失败: ${e?.message ?? e}`);
  } finally {
    leaveBusy();
  }
}

async function nextStep(cmd: "next\n" | "step\n" | "continue\n", eid: number) {
  if (!active.value || busy.value) return;
  const gen = generation;
  needInput.value = false;
  try {
    const d = await call("debug/nextStep", { cmd, eID: eid, isTeacher: false });
    if (stale(gen)) return;
    if (absorb(d)) await endSession(eid);
  } catch (e: any) {
    toast.error(`步进失败: ${e?.message ?? e}`);
  }
}

async function skipOut(eid: number) {
  if (!active.value || busy.value) return;
  const gen = generation;
  needInput.value = false;
  try {
    const d = await call("debug/skipOut", { eID: eid, isTeacher: false });
    if (stale(gen)) return;
    if (absorb(d)) await endSession(eid);
  } catch (e: any) {
    toast.error(`跳出失败: ${e?.message ?? e}`);
  }
}

async function sendInput(text: string, eid: number) {
  if (!active.value || busy.value) return;
  const gen = generation;
  try {
    const d = await call("code/exeInput", {
      inputStr: text,
      isTeacher: false,
      language: 1,
      kind: 4,
      eID: eid,
    });
    if (stale(gen)) return;
    if (absorb(d)) await endSession(eid);
  } catch (e: any) {
    toast.error(`发送输入失败: ${e?.message ?? e}`);
  }
}

async function quit(eid: number) {
  if (!active.value) return;
  await forceQuit(eid);
}

async function forceQuit(eid: number) {
  generation++; // 作废在途 step, 避免 quit 后迟到 absorb 把幽灵变量写回来
  clearSessionState();
  try {
    await call("debug/quit", { eID: eid, isTeacher: false });
  } catch {}
}

// 登出/卸载: 即使还没 active(compile 在途)也要作废代际并尽力 quit
async function abandon(eid: number) {
  generation++;
  clearSessionState();
  if (!eid) return;
  try {
    await call("debug/quit", { eID: eid, isTeacher: false });
  } catch {}
}

async function endSession(eid: number) {
  toast.info("调试已结束");
  await forceQuit(eid);
}

function resetForQuestion(schemeId: number, eid: number) {
  generation++; // 作废旧题在途的调试请求
  clearSessionState();
  loadBps(schemeId, eid);
  // 断点行超出当前文档长度由编辑器侧自行钳制, 这里只管集合
}

export function useDebug() {
  return {
    active,
    busy,
    line,
    vars,
    stack,
    output,
    needInput,
    bps,
    startedAt,
    toggleBp,
    loadBps,
    start,
    stepOver: (eid: number) => nextStep("next\n", eid),
    stepInto: (eid: number) => nextStep("step\n", eid),
    resume: (eid: number) => nextStep("continue\n", eid),
    skipOut,
    sendInput,
    quit,
    abandon,
    resetForQuestion,
  };
}
