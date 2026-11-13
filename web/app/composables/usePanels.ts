// 多面板布局状态(模块级单例): index.vue 工作台与 SettingsModal 共享同一份状态,
// 否则设置弹窗里的"重置分栏"只改自己的孤儿副本, 当前布局刷新前不生效。
import { computed, ref } from "vue";

export type OutputDock = "bottom" | "right";
export type ResizerType = "sidebar" | "statement" | "output";

const hasLS = typeof localStorage !== "undefined";

function lsStr(key: string, fallback: string): string {
  if (!hasLS) return fallback;
  try {
    return localStorage.getItem(key) ?? fallback;
  } catch {
    return fallback;
  }
}

function lsSet(key: string, value: string) {
  if (!hasLS) return;
  try {
    localStorage.setItem(key, value);
  } catch {}
}

// NaN/0/负数防护: 键损坏时回退默认值, 否则 "NaNpx" 会传染所有拖拽钳制逻辑
function lsNum(key: string, fallback: number): number {
  const n = Number(lsStr(key, String(fallback)));
  return Number.isFinite(n) && n > 0 ? n : fallback;
}

// 1. 题库侧栏 (Sidebar)
const sidebarWidth = ref(lsNum("an.sidebarW", 250));
const sidebarCollapsed = ref(lsStr("an.sidebarCollapsed", "false") === "true");

// 2. 题面面板 (Statement)
const statementWidth = ref(lsNum("an.statementW", 420));
const statementCollapsed = ref(lsStr("an.statementCollapsed", "false") === "true");

// 3. 运行与调试控制台 (OutputPanel)
const outputDock = ref<OutputDock>(lsStr("an.outDock", "bottom") === "right" ? "right" : "bottom");
const outputHeight = ref(lsNum("an.outH", 200));
const outputWidth = ref(lsNum("an.outW", 380));
const outputCollapsed = ref(lsStr("an.outCollapsed", "false") === "true");
const outputMaximized = ref(false);

// 拖拽状态
const isDragging = ref(false);
const activeResizer = ref<ResizerType | null>(null);

let startX = 0;
let startY = 0;
let startDim = 0;
let latestX = 0;
let latestY = 0;
let dragWinW = 0;
let dragWinH = 0;
let rafId: number | null = null;
let activePointerId = -1;

// 检查小屏/平板横屏保护：屏幕宽度较小时禁用右侧停靠，避免三栏挤压撕裂
function checkDockSafety() {
  if (typeof window === "undefined") return;
  if (window.innerWidth <= 1200 && outputDock.value === "right") {
    outputDock.value = "bottom";
    outputMaximized.value = false;
    lsSet("an.outDock", "bottom");
  }
}

function persistResizer(type: ResizerType | null) {
  if (type === "sidebar") {
    lsSet("an.sidebarW", String(sidebarWidth.value));
    lsSet("an.sidebarCollapsed", String(sidebarCollapsed.value));
  } else if (type === "statement") {
    lsSet("an.statementW", String(statementWidth.value));
    lsSet("an.statementCollapsed", String(statementCollapsed.value));
  } else if (type === "output") {
    lsSet(outputDock.value === "bottom" ? "an.outH" : "an.outW", String(outputDock.value === "bottom" ? outputHeight.value : outputWidth.value));
    lsSet("an.outCollapsed", String(outputCollapsed.value));
  }
}

function finishResize() {
  if (!isDragging.value) return;
  const finishedType = activeResizer.value;
  if (rafId !== null) {
    cancelAnimationFrame(rafId);
    rafId = null;
    applyResize();
  }

  isDragging.value = false;
  activeResizer.value = null;
  activePointerId = -1;

  window.removeEventListener("pointermove", onPointerMove);
  window.removeEventListener("pointerup", onPointerUp);
  window.removeEventListener("pointercancel", onPointerUp);
  window.removeEventListener("blur", finishResize);

  document.body.style.userSelect = "";
  document.body.style.cursor = "";

  persistResizer(finishedType);
}

// Pointer Events + setPointerCapture: 窗口外移动/松开也持续收到事件,
// 不会粘拖; 分割线元素已带 touch-action:none, 触屏滚动不会抢事件
function startResize(type: ResizerType, e: PointerEvent) {
  if (!e.isPrimary) return; // 忽略多指
  if (isDragging.value) finishResize();

  isDragging.value = true;
  activeResizer.value = type;
  activePointerId = e.pointerId;
  try {
    (e.currentTarget as HTMLElement | null)?.setPointerCapture(e.pointerId);
  } catch {}

  startX = e.clientX;
  startY = e.clientY;
  latestX = e.clientX;
  latestY = e.clientY;
  dragWinW = typeof window !== "undefined" ? window.innerWidth : 1280;
  dragWinH = typeof window !== "undefined" ? window.innerHeight : 800;

  if (type === "sidebar") {
    startDim = sidebarWidth.value;
    if (sidebarCollapsed.value) sidebarCollapsed.value = false;
  } else if (type === "statement") {
    startDim = statementWidth.value;
    if (statementCollapsed.value) statementCollapsed.value = false;
  } else if (type === "output") {
    startDim = outputDock.value === "bottom" ? outputHeight.value : outputWidth.value;
    if (outputCollapsed.value) outputCollapsed.value = false;
    if (outputMaximized.value) outputMaximized.value = false;
  }

  window.addEventListener("pointermove", onPointerMove);
  window.addEventListener("pointerup", onPointerUp);
  window.addEventListener("pointercancel", onPointerUp);
  window.addEventListener("blur", finishResize);

  document.body.style.userSelect = "none";
  const isCol = type === "sidebar" || type === "statement" || (type === "output" && outputDock.value === "right");
  document.body.style.cursor = isCol ? "col-resize" : "row-resize";
}

function onPointerMove(e: PointerEvent) {
  if (!isDragging.value || !activeResizer.value || e.pointerId !== activePointerId) return;

  latestX = e.clientX;
  latestY = e.clientY;

  if (rafId === null) {
    rafId = requestAnimationFrame(applyResize);
  }
}

function applyResize() {
  rafId = null;
  if (!isDragging.value || !activeResizer.value) return;

  if (activeResizer.value === "sidebar") {
    const delta = latestX - startX;
    const maxW = Math.min(520, dragWinW - 450);
    const newW = Math.max(160, Math.min(maxW, startDim + delta));
    if (sidebarWidth.value !== newW) sidebarWidth.value = newW;
  } else if (activeResizer.value === "statement") {
    const delta = latestX - startX;
    const currentSidebarW = sidebarCollapsed.value ? 0 : sidebarWidth.value;
    // 平板横屏保护：给代码编辑器至少预留 320px
    const maxW = Math.min(900, dragWinW - currentSidebarW - 320);
    const newW = Math.max(200, Math.min(maxW, startDim + delta));
    if (statementWidth.value !== newW) statementWidth.value = newW;
  } else if (activeResizer.value === "output") {
    if (outputDock.value === "bottom") {
      const delta = startY - latestY;
      const maxH = Math.min(dragWinH * 0.8, dragWinH - 120);
      const newH = Math.max(34, Math.min(maxH, startDim + delta));
      if (outputHeight.value !== newH) outputHeight.value = newH;
      if (outputCollapsed.value && newH > 50) {
        outputCollapsed.value = false;
      }
    } else {
      const delta = startX - latestX;
      const currentSidebarW = sidebarCollapsed.value ? 0 : sidebarWidth.value;
      const currentStmtW = statementCollapsed.value ? 0 : statementWidth.value;
      const maxW = Math.min(dragWinW - currentSidebarW - currentStmtW - 240, dragWinW * 0.7);
      const newW = Math.max(220, Math.min(maxW, startDim + delta));
      if (outputWidth.value !== newW) outputWidth.value = newW;
      if (outputCollapsed.value && newW > 80) {
        outputCollapsed.value = false;
      }
    }
  }
}

function onPointerUp(e: PointerEvent) {
  if (!isDragging.value || e.pointerId !== activePointerId) return;
  finishResize();
}

function toggleSidebar() {
  sidebarCollapsed.value = !sidebarCollapsed.value;
  lsSet("an.sidebarCollapsed", String(sidebarCollapsed.value));
}

function toggleStatement() {
  statementCollapsed.value = !statementCollapsed.value;
  lsSet("an.statementCollapsed", String(statementCollapsed.value));
}

function toggleOutputCollapse() {
  outputCollapsed.value = !outputCollapsed.value;
  outputMaximized.value = false;
  lsSet("an.outCollapsed", String(outputCollapsed.value));
}

function toggleOutputMaximize() {
  outputMaximized.value = !outputMaximized.value;
  if (outputMaximized.value) {
    outputCollapsed.value = false;
  }
}

function toggleOutputDock() {
  if (typeof window !== "undefined" && window.innerWidth <= 1200 && outputDock.value === "bottom") {
    // 平板横屏/小屏保护：禁止切换到右侧停靠
    return false;
  }
  outputDock.value = outputDock.value === "bottom" ? "right" : "bottom";
  outputMaximized.value = false;
  lsSet("an.outDock", outputDock.value);
  return true;
}

function resetLayout() {
  sidebarWidth.value = 250;
  sidebarCollapsed.value = false;
  statementWidth.value = 420;
  statementCollapsed.value = false;
  outputDock.value = "bottom";
  outputHeight.value = 200;
  outputWidth.value = 380;
  outputCollapsed.value = false;
  outputMaximized.value = false;

  try {
    localStorage.removeItem("an.sidebarW");
    localStorage.removeItem("an.sidebarCollapsed");
    localStorage.removeItem("an.statementW");
    localStorage.removeItem("an.statementCollapsed");
    localStorage.removeItem("an.outDock");
    localStorage.removeItem("an.outH");
    localStorage.removeItem("an.outW");
    localStorage.removeItem("an.outCollapsed");
  } catch {}
}

const outputPanelStyle = computed(() => {
  if (outputDock.value === "bottom") {
    if (outputMaximized.value) {
      return { height: "calc(100% - 36px)", flex: "none" };
    }
    if (outputCollapsed.value) {
      return { height: "34px", flex: "none" };
    }
    return { height: `${outputHeight.value}px`, flex: "none" };
  } else {
    if (outputMaximized.value) {
      return { width: "calc(100% - 160px)", height: "100%", flex: "none" };
    }
    if (outputCollapsed.value) {
      return { width: "34px", height: "100%", flex: "none" };
    }
    return { width: `${outputWidth.value}px`, height: "100%", flex: "none" };
  }
});

// 初始化检查停靠安全性
checkDockSafety();

export function usePanels() {
  return {
    sidebarWidth,
    sidebarCollapsed,
    statementWidth,
    statementCollapsed,
    outputDock,
    outputHeight,
    outputWidth,
    outputCollapsed,
    outputMaximized,
    isDragging,
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
  };
}
