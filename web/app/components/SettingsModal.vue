<script setup lang="ts">
import { useSettings } from "~/composables/useSettings";
import { X } from "lucide-vue-next";
import { usePanels } from "~/composables/usePanels";
import { useToast } from "~/composables/useToast";

const props = defineProps<{
  show: boolean;
}>();

const emit = defineEmits<{
  (e: "close"): void;
}>();

const { settings, reset } = useSettings();
const toast = useToast();
const { resetLayout } = usePanels();

function handleResetAll() {
  reset();
  resetLayout();
  toast.success("已恢复默认");
}

function handleResetLayout() {
  resetLayout();
  toast.success("分栏已重置");
}

const fontSizes = [12, 13, 14, 15, 16, 18];
const statementSizes = [
  { label: "紧", size: 13 },
  { label: "中", size: 14.5 },
  { label: "大", size: 16 },
];

// 原生 <dialog>: showModal/close 由 show 驱动;
// Escape(cancel→close)、Tab 焦点圈禁、关闭后焦点归还会由浏览器提供
const dlg = ref<HTMLDialogElement | null>(null);
const doneBtn = ref<HTMLButtonElement | null>(null);
let prevFocused: HTMLElement | null = null;

function restoreFocus() {
  const target = prevFocused;
  prevFocused = null;
  if (target && document.contains(target) && (!document.activeElement || document.activeElement === document.body)) {
    target.focus();
  }
}

watch(
  () => props.show,
  (v) => {
    if (v) {
      prevFocused = document.activeElement as HTMLElement | null;
      if (dlg.value && !dlg.value.open) dlg.value.showModal();
      nextTick(() => doneBtn.value?.focus());
    } else {
      if (dlg.value?.open) dlg.value.close();
      restoreFocus();
    }
  },
);

function onClose() {
  restoreFocus();
  emit("close");
}

// 点击 ::backdrop(事件 target 为 dialog 本体)关闭; 窗口内的点击不动
function onBackdrop(e: MouseEvent) {
  if (e.target === dlg.value) emit("close");
}

// Escape 双保险: 显式 keydown 走 preventDefault(规范浏览器不会触发原生 cancel),
// 原生 cancel→close 事件作为兜底; 两条路都收敛到 emit('close') 同步父组件状态
function onKeydown(e: KeyboardEvent) {
  if (e.key === "Escape") {
    e.preventDefault();
    emit("close");
  }
}
</script>

<template>
  <dialog
    ref="dlg"
    class="modal"
    role="dialog"
    aria-modal="true"
    aria-labelledby="settings-modal-title"
    @click="onBackdrop"
    @close="onClose"
    @keydown="onKeydown"
  >
    <div class="modal-inner">
      <div class="modal-header">
        <h3 id="settings-modal-title" class="modal-title">偏好</h3>
        <button type="button" class="btn-icon close-btn" aria-label="关闭" @click="emit('close')">
          <X class="icon" :size="13" :stroke-width="1.5" />
        </button>
      </div>

      <div class="modal-body">
        <div class="setting-group">
          <div class="group-title">编辑器</div>

          <div class="setting-item">
            <div class="item-info">
              <span class="item-label">代码字号</span>
              <span class="item-sub font-mono">{{ settings.editorFontSize }}</span>
            </div>
            <div class="opt-pills" role="group" aria-label="代码字号">
              <button
                v-for="s in fontSizes"
                :key="s"
                type="button"
                class="opt-btn font-mono"
                :class="{ active: settings.editorFontSize === s }"
                :aria-pressed="settings.editorFontSize === s"
                @click="settings.editorFontSize = s"
              >
                {{ s }}
              </button>
            </div>
          </div>

          <div class="setting-item">
            <div class="item-info">
              <span class="item-label">缩进</span>
            </div>
            <div class="opt-pills" role="group" aria-label="缩进空格数">
              <button
                type="button"
                class="opt-btn"
                :class="{ active: settings.editorTabSize === 4 }"
                :aria-pressed="settings.editorTabSize === 4"
                @click="settings.editorTabSize = 4"
              >
                4
              </button>
              <button
                type="button"
                class="opt-btn"
                :class="{ active: settings.editorTabSize === 2 }"
                :aria-pressed="settings.editorTabSize === 2"
                @click="settings.editorTabSize = 2"
              >
                2
              </button>
            </div>
          </div>

          <div class="setting-item">
            <div class="item-info">
              <span class="item-label">折行</span>
            </div>
            <div class="opt-pills" role="group" aria-label="自动折行">
              <button
                type="button"
                class="opt-btn"
                :class="{ active: settings.editorLineWrapping }"
                :aria-pressed="settings.editorLineWrapping"
                @click="settings.editorLineWrapping = true"
              >
                开
              </button>
              <button
                type="button"
                class="opt-btn"
                :class="{ active: !settings.editorLineWrapping }"
                :aria-pressed="!settings.editorLineWrapping"
                @click="settings.editorLineWrapping = false"
              >
                关
              </button>
            </div>
          </div>
        </div>

        <div class="setting-group">
          <div class="group-title">题面</div>

          <div class="setting-item">
            <div class="item-info">
              <span class="item-label">字号</span>
            </div>
            <div class="opt-pills" role="group" aria-label="题面字号">
              <button
                v-for="opt in statementSizes"
                :key="opt.size"
                type="button"
                class="opt-btn"
                :class="{ active: settings.statementFontSize === opt.size }"
                :aria-pressed="settings.statementFontSize === opt.size"
                @click="settings.statementFontSize = opt.size"
              >
                {{ opt.label }}
              </button>
            </div>
          </div>
        </div>

        <div class="setting-group">
          <div class="group-title">分栏</div>

          <div class="setting-item">
            <div class="item-info">
              <span class="item-label">宽高与停靠</span>
            </div>
            <button type="button" class="opt-btn solo" @click="handleResetLayout">
              重置
            </button>
          </div>
        </div>
      </div>

      <div class="modal-footer">
        <button type="button" class="btn-text" @click="handleResetAll">全部默认</button>
        <div class="spacer" />
        <button ref="doneBtn" type="button" class="btn btn-primary" @click="emit('close')">完成</button>
      </div>
    </div>
  </dialog>
</template>

<style scoped>
.setting-group {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.group-title {
  font-size: 11px;
  font-weight: 600;
  color: var(--text-muted);
  letter-spacing: 0.04em;
}

.setting-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}

.item-info {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.item-label {
  font-size: 12.5px;
  color: var(--text-primary);
}

.item-sub {
  font-size: 11px;
  color: var(--text-muted);
}

.opt-pills {
  display: flex;
  align-items: center;
  background: var(--bg-canvas);
  border: 1px solid var(--border-subtle);
  padding: 2px;
  border-radius: var(--radius-control);
  gap: 2px;
}

.opt-btn {
  font-size: 11px;
  color: var(--text-muted);
  padding: 3px 8px;
  border-radius: 4px;
  transition: color 0.1s, background-color 0.1s;
}

.opt-btn.solo {
  background: var(--bg-canvas);
  border: 1px solid var(--border-subtle);
}

.opt-btn.solo:hover {
  background: var(--bg-elevated);
  color: var(--text-primary);
}

.opt-btn:hover {
  color: var(--text-secondary);
}

.opt-btn:active {
  opacity: 0.8;
}

.opt-btn.active {
  background: var(--bg-elevated);
  color: var(--text-primary);
  font-weight: 500;
}

.btn-text {
  font-size: 11.5px;
  color: var(--text-muted);
  transition: color 0.12s;
}

.btn-text:hover {
  color: var(--text-primary);
}
</style>
