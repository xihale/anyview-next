<script setup lang="ts">
import { useClipboard } from "@vueuse/core";
import type { RunResult, Diag, DbgVarNode } from "~/composables/useApi";
import { useToast } from "~/composables/useToast";
import { Copy, PanelBottom, PanelRight, ChevronDown, Maximize2, Minimize2, Check, X, ArrowUpRight } from "lucide-vue-next";

const props = defineProps<{
  result: RunResult | null;
  running: boolean;
  error: string;
  diagnostics?: Diag[];
  dock?: "bottom" | "right";
  collapsed?: boolean;
  maximized?: boolean;
  debug?: {
    active: boolean;
    line: number;
    output: string;
    needInput: boolean;
    vars?: DbgVarNode[];
    stack?: string[];
    busy?: boolean;
  };
}>();

const emit = defineEmits<{
  (e: "submit-input", text: string): void;
  (e: "jump-to-diag", line: number, col: number): void;
  (e: "jump-to-line", line: number): void;
  (e: "clear"): void;
  (e: "toggle-dock"): void;
  (e: "toggle-collapse"): void;
  (e: "toggle-maximize"): void;
}>();

const MAX_DISPLAY_CHARS = 200_000;

const toast = useToast();
const { copy } = useClipboard({ legacy: true }); // legacy: 非安全上下文回落 execCommand
const stdinText = ref("");
const copied = ref(false);
let copiedTimer: ReturnType<typeof setTimeout> | null = null;

onBeforeUnmount(() => {
  if (copiedTimer) {
    clearTimeout(copiedTimer);
    copiedTimer = null;
  }
});

type PanelTab = "console" | "vars" | "stack" | "diag";
const activeTab = ref<PanelTab>("console");

// 空态(无结果/未运行/无错误/未调试): 面板默认收成标题条把高度还给编辑器,
// 但标题条可点开(pulledOpen), 没运行过也能拉起空控制台——按钮不能是死的
const empty = computed(
  () => !props.result && !props.running && !props.error && !props.debug?.active,
);
const pulledOpen = ref(false);
const stripMode = computed(() => empty.value && !pulledOpen.value && !props.maximized);

// 离开空态(跑了一次)后复位手动拉起标记, 下次空态仍从收条开始
watch(empty, (v) => {
  if (!v) pulledOpen.value = false;
});

function onToggleCollapse() {
  if (empty.value) {
    pulledOpen.value = !pulledOpen.value;
    // 持久化的 collapsed 条也一并展开, 否则拉起后父级 34px 高度把内容压没
    if (pulledOpen.value && props.collapsed) emit("toggle-collapse");
    return;
  }
  emit("toggle-collapse");
}

function onHeaderClick() {
  if (stripMode.value) onToggleCollapse();
}

const collapseBtnTitle = computed(() =>
  stripMode.value || props.collapsed ? "展开面板" : "折叠面板",
);

function selectTab(tab: PanelTab) {
  activeTab.value = tab;
  if (stripMode.value) {
    onToggleCollapse();
    return;
  }
  if (props.collapsed) {
    emit("toggle-collapse");
  }
}

// 进入调试模式自动切至「变量」tab
watch(
  () => props.debug?.active,
  (active) => {
    if (active) {
      activeTab.value = "vars";
    } else if (activeTab.value === "vars" || activeTab.value === "stack") {
      activeTab.value = "console";
    }
  },
);

// 编译错误自动切至「诊断」tab
watch(
  () => props.result?.compileOk,
  (ok) => {
    if (ok === false && props.diagnostics && props.diagnostics.length > 0) {
      activeTab.value = "diag";
    }
  },
);

// 诊断清空(如随后的成功运行)时回落控制台: 诊断 tab 会随 v-if 消失,
// activeTab 不能卡在已不存在的 tab 上, 否则面板整体空白
watch(
  () => props.diagnostics?.length ?? 0,
  (n) => {
    if (n === 0 && activeTab.value === "diag") {
      activeTab.value = "console";
    }
  },
);

const stdinInputRef = ref<HTMLInputElement | null>(null);

// 等待输入时自动切回控制台并聚焦输入框
watch(
  () => Boolean(props.debug?.needInput || props.result?.needInput),
  (needed) => {
    if (needed) {
      activeTab.value = "console";
      nextTick(() => {
        stdinInputRef.value?.focus();
      });
    }
  },
  { immediate: true },
);

function fmt(s: string | undefined): string {
  if (!s) return "";
  if (!/[<&]/.test(s)) return s;
  const withBreaks = s.replace(/<br\s*\/?>/gi, "\n");
  if (typeof DOMParser === "undefined") {
    return withBreaks.replace(/<[^>]+>/g, "");
  }
  return new DOMParser().parseFromString(withBreaks, "text/html").body.textContent ?? "";
}

function capDisplay(raw: string) {
  if (!raw) return { full: "", text: "", truncated: false, total: 0 };
  if (raw.length <= MAX_DISPLAY_CHARS) {
    return { full: raw, text: raw, truncated: false, total: raw.length };
  }
  return {
    full: raw,
    text: raw.slice(0, MAX_DISPLAY_CHARS),
    truncated: true,
    total: raw.length,
  };
}

const formattedDebugOutput = computed(() => capDisplay(fmt(props.debug?.output)));
const formattedResultOutput = computed(() => capDisplay(fmt(props.result?.output)));
const formattedCompileMsg = computed(() => capDisplay(fmt(props.result?.compileMsg)));
const formattedException = computed(() => capDisplay(fmt(props.result?.exception)));

const hasCopyableOutput = computed(() =>
  props.debug?.active
    ? Boolean(formattedDebugOutput.value.full)
    : Boolean(
        formattedResultOutput.value.full ||
          formattedCompileMsg.value.full ||
          formattedException.value.full,
      ),
);

function parseFrameLine(frame: string): number {
  const m = frame.match(/(?:Line|:)\s*(\d+)/i);
  return m ? Number(m[1]) : 0;
}

function cleanFrameName(frame: string): string {
  return frame
    .replace(/^#\d+\s+/, "")
    .replace(/\s*(?:Line|at)\s*.*$/i, "")
    .trim() || frame;
}

const parsedStack = computed(() =>
  (props.debug?.stack ?? []).map((raw, idx) => ({
    idx,
    raw,
    name: cleanFrameName(raw),
    line: parseFrameLine(raw),
  })),
);

function sendStdin() {
  if (!stdinText.value.trim()) return;
  emit("submit-input", stdinText.value);
  stdinText.value = "";
}

async function copyOutput() {
  const parts = props.debug?.active
    ? [formattedDebugOutput.value.full]
    : [
        formattedResultOutput.value.full,
        formattedException.value.full,
        !props.result?.compileOk ? formattedCompileMsg.value.full : "",
      ];
  const text = parts.filter(Boolean).join("\n");
  if (!text) return;
  try {
    await copy(text);
    copied.value = true;
    if (copiedTimer) clearTimeout(copiedTimer);
    copiedTimer = setTimeout(() => {
      copied.value = false;
      copiedTimer = null;
    }, 1500);
    toast.success("已复制输出内容");
  } catch {
    toast.error("复制失败");
  }
}
</script>

<template>
  <section class="output-panel" :class="{ 'is-pristine': stripMode }" aria-label="运行控制台与调试面板">
    <!-- 极简瑞士标签栏 -->
    <div class="panel-header" :class="{ clickable: stripMode }" @click="onHeaderClick">
      <nav class="panel-tabs" role="tablist" aria-label="面板标签">
        <button
          type="button"
          role="tab"
          :aria-selected="activeTab === 'console'"
          class="tab-item"
          :class="{ active: activeTab === 'console' }"
          @click.stop="selectTab('console')"
        >
          控制台
          <span v-if="debug?.needInput" class="tab-dot amber" title="等待输入" />
        </button>

        <button
          v-if="debug?.active || (debug?.vars && debug.vars.length > 0)"
          type="button"
          role="tab"
          :aria-selected="activeTab === 'vars'"
          class="tab-item"
          :class="{ active: activeTab === 'vars' }"
          @click.stop="selectTab('vars')"
        >
          变量
          <span v-if="debug?.vars?.length" class="tab-badge">
            {{ debug.vars.length }}
          </span>
        </button>

        <button
          v-if="debug?.active || (debug?.stack && debug.stack.length > 0)"
          type="button"
          role="tab"
          :aria-selected="activeTab === 'stack'"
          class="tab-item"
          :class="{ active: activeTab === 'stack' }"
          @click.stop="selectTab('stack')"
        >
          调用栈
          <span v-if="debug?.stack?.length" class="tab-badge">
            {{ debug.stack.length }}
          </span>
        </button>

        <button
          v-if="diagnostics && diagnostics.length > 0"
          type="button"
          role="tab"
          :aria-selected="activeTab === 'diag'"
          class="tab-item"
          :class="{ active: activeTab === 'diag', err: !result?.compileOk }"
          @click.stop="selectTab('diag')"
        >
          诊断
          <span class="tab-badge err">{{ diagnostics.length }}</span>
        </button>
      </nav>

      <div class="status-info font-mono">
        <span v-if="debug?.active" class="status-debug">
          <span class="dbg-dot" />
          调试中<span v-if="debug.line" class="detail">  行 {{ debug.line }}</span>
        </span>
        <span v-else-if="running" class="status-running">运行中…</span>
        <template v-else-if="result">
          <span v-if="!result.compileOk" class="status-fail">编译未通过</span>
          <template v-else>
            <span v-if="result.passed" class="status-pass">
              <Check class="icon" :size="12" :stroke-width="1.8" />
              通过
            </span>
            <span v-else-if="result.needInput" class="status-input">
              等待输入
            </span>
            <span v-else class="status-fail">
              <X class="icon" :size="12" :stroke-width="1.8" />
              评测未通过
              <span v-if="result.rightCount != null" class="detail">({{ result.rightCount }}/{{ (result.rightCount || 0) + (result.wrongCount || 0) }})</span>
            </span>

            <span v-if="!result.passed && result.elapsedMs != null" class="time-cost">
              {{ result.elapsedMs }}ms
            </span>
          </template>
        </template>
        <span v-else-if="stripMode" class="pristine-hint">
          Ctrl+Enter 编译并运行，F8 调试
        </span>
      </div>

      <div class="panel-actions" @click.stop>
        <button
          v-if="activeTab === 'console' && hasCopyableOutput"
          type="button"
          class="btn btn-ghost action-btn"
          :class="{ copied }"
          title="复制输出"
          aria-label="复制输出"
          @click="copyOutput"
        >
          <Check v-if="copied" class="icon" :size="12" :stroke-width="1.8" />
          <Copy v-else class="icon" :size="12" :stroke-width="1.5" />
        </button>

        <span v-if="activeTab === 'console' && hasCopyableOutput" class="action-sep" />

        <!-- 停靠方向切换: 底部 / 右侧 -->
        <button
          type="button"
          class="btn-icon"
          :title="dock === 'right' ? '停靠至底部 (上下分栏)' : '停靠至右侧 (左右分栏)'"
          :aria-label="dock === 'right' ? '停靠至底部' : '停靠至右侧'"
          @click="emit('toggle-dock')"
        >
          <PanelBottom v-if="dock === 'right'" class="icon" :size="14" :stroke-width="1.5" />
          <PanelRight v-else class="icon" :size="14" :stroke-width="1.5" />
        </button>

        <!-- 最小化 / 展开: 空态收条也由此拉起 -->
        <button
          type="button"
          class="btn-icon"
          :title="collapseBtnTitle"
          :aria-label="collapseBtnTitle"
          :aria-expanded="!collapsed && !stripMode"
          @click.stop="onToggleCollapse"
        >
          <ChevronDown
            class="icon"
            :size="14"
            :stroke-width="1.5"
            :style="{ transform: collapsed || stripMode ? 'none' : 'rotate(180deg)' }"
          />
        </button>

        <!-- 最大化 / 还原 -->
        <button
          type="button"
          class="btn-icon"
          :title="maximized ? '还原面板尺寸' : '最大化面板'"
          :aria-label="maximized ? '还原面板尺寸' : '最大化面板'"
          @click="emit('toggle-maximize')"
        >
          <Maximize2 v-if="!maximized" class="icon" :size="12" :stroke-width="1.5" />
          <Minimize2 v-else class="icon" :size="12" :stroke-width="1.5" />
        </button>
      </div>
    </div>

    <!-- 错误横幅 -->
    <div v-if="error" class="error-msg">
      {{ error }}
    </div>

    <!-- 面板内容区 -->
    <div v-show="!collapsed && !stripMode" class="panel-body">
      <!-- 1. 变量监控树 -->
      <div v-if="activeTab === 'vars'" class="vars-pane">
        <div v-if="!debug?.vars || debug.vars.length === 0" class="empty-hint">
          暂无局部变量（当前作用域未分配变量或位于系统调用中）
        </div>
        <ul v-else class="vars-tree">
          <li v-for="(v, idx) in debug.vars" :key="`${idx}:${v.name}`">
            <DebugVarNode :node="v" :depth="0" />
          </li>
        </ul>
      </div>

      <!-- 2. 调用栈 -->
      <div v-else-if="activeTab === 'stack'" class="stack-pane">
        <div v-if="!debug?.stack || debug.stack.length === 0" class="empty-hint">
          暂无调用栈信息
        </div>
        <div v-else class="stack-list">
          <div
            v-for="frame in parsedStack"
            :key="frame.idx"
            class="stack-row"
            :class="{ current: frame.idx === 0, clickable: !!frame.line }"
            :role="frame.line ? 'button' : undefined"
            :tabindex="frame.line ? 0 : undefined"
            :title="frame.raw"
            @click="frame.line && emit('jump-to-line', frame.line)"
            @keydown.enter.prevent="frame.line && emit('jump-to-line', frame.line)"
            @keydown.space.prevent="frame.line && emit('jump-to-line', frame.line)"
          >
            <span class="stack-badge font-mono">#{{ frame.idx }}</span>
            <span class="stack-fn">{{ frame.name }}</span>
            <span v-if="frame.line" class="stack-loc">
              行 {{ frame.line }}
              <ArrowUpRight class="icon jump-arrow" :size="10" :stroke-width="1.5" />
            </span>
          </div>
        </div>
      </div>

      <!-- 3. 诊断信息 -->
      <div v-else-if="activeTab === 'diag'" class="diag-container">
        <button
          v-for="(d, idx) in diagnostics"
          :key="`${d.line}:${d.col}:${idx}`"
          type="button"
          class="diag-line"
          :class="{ 'is-warning': d.severity === 'warning' }"
          @click="emit('jump-to-diag', d.line, d.col)"
        >
          <span class="diag-loc font-mono">行 {{ d.line }}<template v-if="d.col > 1">:{{ d.col }}</template></span>
          <span class="diag-text">{{ d.message }}</span>
          <span class="diag-arrow">跳转
            <ArrowUpRight class="icon" :size="10" :stroke-width="1.5" />
          </span>
        </button>
      </div>

      <!-- 4. 控制台标准输出 (默认) -->
      <div v-else class="console-pane">
        <!-- 调试模式累积输出 -->
        <template v-if="debug?.active">
          <template v-if="formattedDebugOutput.text">
            <pre class="terminal-text">{{ formattedDebugOutput.text }}</pre>
            <div v-if="formattedDebugOutput.truncated" class="truncate-note font-mono">
              输出过长（共 {{ formattedDebugOutput.total.toLocaleString() }} 字符），已截取前 {{ MAX_DISPLAY_CHARS.toLocaleString() }} 字符；可点击右上角复制完整内容
            </div>
          </template>
          <div v-else class="empty-hint">
            调试会话进行中；单步执行产生的控制台输出将在此实时累积。
          </div>
        </template>

        <!-- 普通运行模式输出 -->
        <template v-else>
          <template v-if="result && !result.compileOk && formattedCompileMsg.text">
            <div class="console-section-label font-mono">编译输出</div>
            <pre class="terminal-text err">{{ formattedCompileMsg.text }}</pre>
            <div v-if="formattedCompileMsg.truncated" class="truncate-note font-mono">
              编译日志过长，已截取前 {{ MAX_DISPLAY_CHARS.toLocaleString() }} 字符
            </div>
          </template>

          <template v-else-if="formattedResultOutput.text">
            <pre class="terminal-text">{{ formattedResultOutput.text }}</pre>
            <div v-if="formattedResultOutput.truncated" class="truncate-note font-mono">
              输出过长（共 {{ formattedResultOutput.total.toLocaleString() }} 字符），已截取前 {{ MAX_DISPLAY_CHARS.toLocaleString() }} 字符；可点击右上角复制完整内容
            </div>
          </template>

          <template v-if="formattedException.text">
            <div v-if="formattedResultOutput.text" class="console-section-label err font-mono">运行异常</div>
            <pre class="terminal-text err">{{ formattedException.text }}</pre>
          </template>

          <div
            v-if="result && result.compileOk && !formattedResultOutput.text && !formattedException.text && !result.needInput"
            class="empty-hint"
          >
            {{ result.passed ? "评测通过，程序无标准输出。" : "程序已结束，无标准输出。" }}
          </div>

          <div v-else-if="!result && !running" class="empty-hint empty-guide">
            <p><kbd class="kbd">Ctrl+Enter</kbd> 编译并运行，输出与判题结果在此显示</p>
            <p>点击编辑器行号左侧设置断点，<kbd class="kbd">F8</kbd> 开启单步调试</p>
          </div>
        </template>

        <!-- 显式等待输入提示 -->
        <div v-if="debug?.needInput || result?.needInput" class="stdin-waiting-alert">
          <span class="stdin-waiting-dot" />
          <span>程序暂停中：正在等待键盘标准输入 (stdin)，请在下方输入并按回车发送</span>
        </div>
      </div>
    </div>

    <!-- 标准输入交互行 -->
    <div
      v-if="(debug?.needInput || result?.needInput) && !collapsed"
      class="stdin-row"
    >
      <span class="prompt">›</span>
      <input
        ref="stdinInputRef"
        v-model="stdinText"
        type="text"
        class="stdin-input"
        placeholder="程序等待标准输入，输入后按 Enter 发送..."
        aria-label="程序标准输入"
        @keydown.enter.prevent="sendStdin"
      >
      <button type="button" class="stdin-send" :disabled="!stdinText.trim()" @click="sendStdin">发送</button>
    </div>
  </section>
</template>

<style scoped>
.output-panel.is-pristine {
  height: auto !important;
  width: auto !important;
  min-height: 32px;
  flex: none !important;
}

.pristine-hint {
  font-size: 11px;
  color: var(--text-muted);
  white-space: nowrap;
}

.output-panel {
  flex: none;
  display: flex;
  flex-direction: column;
  height: 100%;
  background: var(--bg-canvas);
  position: relative;
  overflow: hidden;
}

/* 顶栏与标签导航 */
.panel-header {
  height: 32px;
  flex: none;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 0 10px;
  border-bottom: 1px solid var(--border-subtle);
  background: var(--bg-surface);
  user-select: none;
  min-width: 0;
}

/* 空态收条: 整条可点拉起面板 */
.panel-header.clickable {
  cursor: pointer;
}

.panel-tabs {
  display: flex;
  align-items: center;
  gap: 2px;
  height: 100%;
  min-width: 0;
  overflow-x: auto;
  scrollbar-width: none;
}

.panel-tabs::-webkit-scrollbar {
  display: none;
}

.tab-item {
  height: 100%;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 0 8px;
  font-size: 11.5px;
  font-weight: 500;
  color: var(--text-muted);
  border-bottom: 2px solid transparent;
  transition: color 0.12s;
  cursor: pointer;
  background: none;
  white-space: nowrap;
  flex: none;
}

.tab-item:hover {
  color: var(--text-primary);
}

.tab-item.active {
  color: var(--text-primary);
  font-weight: 600;
  border-bottom-color: var(--text-primary);
}

.tab-item.err.active {
  border-bottom-color: var(--err);
}

.tab-badge {
  font-size: 10px;
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
  color: var(--text-muted);
  padding: 0 4px;
  line-height: 14px;
  display: inline-flex;
  align-items: center;
}

.tab-item.active .tab-badge {
  color: var(--text-primary);
}

.tab-badge.err {
  color: var(--err);
}

.tab-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
}

.tab-dot.amber {
  background: var(--warn);
}

/* 状态信息 */
.status-info {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.status-debug {
  color: var(--warn);
  font-weight: 600;
  display: inline-flex;
  align-items: center;
  gap: 5px;
}

.dbg-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--warn);
}

.status-pass {
  color: var(--ok);
  font-weight: 600;
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.status-fail {
  color: var(--err);
  font-weight: 600;
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.status-input {
  color: var(--warn);
  font-weight: 600;
}

.status-running {
  color: var(--text-secondary);
}

.detail {
  font-weight: 400;
  margin-left: 2px;
}

.time-cost {
  color: var(--text-muted);
  font-size: 11px;
}

.panel-actions {
  display: flex;
  align-items: center;
  gap: 2px;
  flex: none;
}

.action-btn {
  font-size: 11px;
  padding: 2px 7px;
}

.action-btn.copied {
  color: var(--ok);
}

.action-sep {
  width: 1px;
  height: 12px;
  background: var(--border);
  margin: 0 6px;
}

.error-msg {
  padding: 6px 14px;
  color: var(--err);
  font-size: 11.5px;
  background: var(--bg-surface);
  border-bottom: 1px solid var(--border-subtle);
}

/* 面板主体滚动 */
.panel-body {
  flex: 1;
  overflow-y: auto;
  min-height: 0;
  padding: 10px 14px;
  scrollbar-width: thin;
}

/* 变量树容器 */
.vars-pane {
  min-height: 0;
}

.vars-tree {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

/* 调用栈容器 */
.stack-pane {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.stack-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.stack-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 3px 8px;
  border-radius: 3px;
  font-family: var(--font-mono);
  font-size: 11.5px;
  color: var(--text-secondary);
  transition: background 0.1s;
}

.stack-row.clickable {
  cursor: pointer;
}

.stack-row.clickable:hover {
  background: var(--bg-elevated);
  color: var(--text-primary);
}

.stack-row.current {
  color: var(--text-primary);
  font-weight: 600;
}

.stack-badge {
  font-size: 10px;
  color: var(--text-muted);
  background: var(--bg-surface);
  border: 1px solid var(--border-subtle);
  padding: 0 4px;
  border-radius: 2px;
  flex: none;
}

.stack-fn {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.stack-loc {
  font-size: 11px;
  color: var(--text-muted);
  flex: none;
  display: inline-flex;
  align-items: center;
  gap: 2px;
}

.jump-arrow {
  font-size: 9px;
  opacity: 0.6;
}

/* 终端输出 */
.console-section-label {
  font-size: 10.5px;
  font-weight: 600;
  color: var(--text-muted);
  margin: 0 0 6px;
  letter-spacing: 0.02em;
}

.console-section-label.err {
  color: var(--err);
  margin-top: 10px;
}

.terminal-text {
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
  font-size: 12px;
  line-height: 1.6;
  color: var(--text-primary);
  white-space: pre-wrap;
  word-break: break-word;
  margin: 0;
}

.terminal-text.err {
  color: var(--err);
}

.truncate-note {
  margin-top: 8px;
  padding: 6px 10px;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-control);
  color: var(--text-muted);
  font-size: 11px;
}

.empty-hint {
  color: var(--text-muted);
  font-size: 12px;
  font-family: var(--font-mono);
  line-height: 1.6;
  padding: 10px 0;
}

.empty-guide {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.empty-guide .kbd {
  display: inline-block;
  padding: 0 5px;
  font-size: 11px;
  color: var(--text-secondary);
  background: var(--bg-surface);
  border: 1px solid var(--border);
  border-radius: 4px;
}

/* 诊断条目 */
.diag-container {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.diag-line {
  width: 100%;
  text-align: left;
  display: flex;
  align-items: baseline;
  gap: 8px;
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
  font-size: 11.5px;
  cursor: pointer;
  padding: 4px 6px;
  border-radius: 3px;
  transition: background-color 0.12s ease;
}

.diag-line:hover {
  background: var(--bg-elevated);
}

.diag-loc {
  color: var(--err);
  font-weight: 600;
  flex: none;
}

.diag-line.is-warning .diag-loc {
  color: var(--warn);
}

.diag-text {
  color: var(--text-primary);
  flex: 1;
  min-width: 0;
  white-space: pre-wrap;
  word-break: break-word;
  line-height: 1.5;
}

.diag-arrow {
  font-size: 10px;
  color: var(--text-muted);
  opacity: 0;
  flex: none;
  display: inline-flex;
  align-items: center;
  gap: 2px;
  transition: opacity 0.12s ease;
}

.diag-line:hover .diag-arrow,
.diag-line:focus-visible .diag-arrow {
  opacity: 1;
}

/* 交互式输入行: 仅在等待输入时渲染, 琥珀顶线即语义状态, 不铺彩底 */
.stdin-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 14px;
  border-top: 1px solid var(--warn);
  background: var(--bg-surface);
}

.prompt {
  color: var(--warn);
  font-family: var(--font-mono);
  font-size: 13px;
}

.stdin-input {
  flex: 1;
  background: transparent;
  border: none;
  outline: none;
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--text-primary);
}

.stdin-send {
  font-size: 11px;
  color: var(--warn);
  font-weight: 600;
  padding: 2px 6px;
  border-radius: 4px;
  transition: opacity 0.12s ease, background-color 0.12s ease;
}

.stdin-send:not(:disabled):hover {
  background: var(--bg-elevated);
}

.stdin-send:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.stdin-waiting-alert {
  margin-top: 10px;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  border-radius: 4px;
  border: 1px solid var(--border-subtle);
  color: var(--warn);
  font-size: 12px;
  font-family: var(--font-mono);
}

.stdin-waiting-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--warn);
  flex: none;
}

@media (prefers-reduced-motion: no-preference) {
  .stdin-waiting-dot {
    animation: stdin-blink 1.2s infinite ease-in-out;
  }
}

@keyframes stdin-blink {
  0%, 100% { opacity: 1; transform: scale(1); }
  50% { opacity: 0.3; transform: scale(0.85); }
}

@media (pointer: coarse) {
  .panel-header {
    height: 38px;
  }
  .tab-item {
    font-size: 12.5px;
    padding: 0 10px;
  }
  .stdin-row {
    padding: 8px 14px;
  }
  .stdin-input {
    font-size: 14px;
  }
  .stdin-send {
    padding: 6px 12px;
    font-size: 12px;
  }
}
</style>
