<script setup lang="ts">
import { X } from "lucide-vue-next";
const props = defineProps<{
  show: boolean;
  schemeId: number;
  classId: number;
}>();

const emit = defineEmits<{
  (e: "close"): void;
  (e: "apply", schemeId: number, classId: number): void;
}>();

const inputSid = ref(props.schemeId);
const inputCid = ref(props.classId);

const isValid = computed(() => {
  const sid = Number(inputSid.value);
  const cid = Number(inputCid.value);
  return Number.isInteger(sid) && sid > 0 && Number.isInteger(cid) && cid > 0;
});

function apply() {
  if (!isValid.value) return;
  emit("apply", Number(inputSid.value), Number(inputCid.value));
  emit("close");
}

// 原生 <dialog>: Escape→cancel→close、焦点圈禁/归还由浏览器提供
const dlg = ref<HTMLDialogElement | null>(null);
const sidInput = ref<HTMLInputElement | null>(null);
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
      inputSid.value = props.schemeId;
      inputCid.value = props.classId;
      prevFocused = document.activeElement as HTMLElement | null;
      if (dlg.value && !dlg.value.open) dlg.value.showModal();
      nextTick(() => {
        sidInput.value?.focus();
        sidInput.value?.select();
      });
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

function onBackdrop(e: MouseEvent) {
  if (e.target === dlg.value) emit("close");
}

// Escape 双保险(同 SettingsModal): keydown preventDefault + 原生 close 事件兜底
function onKeydown(e: KeyboardEvent) {
  if (e.key === "Escape") {
    e.preventDefault();
    emit("close");
    return;
  }
  if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) apply();
}
</script>

<template>
  <dialog
    ref="dlg"
    class="modal narrow"
    role="dialog"
    aria-modal="true"
    aria-labelledby="course-modal-title"
    @click="onBackdrop"
    @close="onClose"
    @keydown="onKeydown"
  >
    <form class="modal-inner" @submit.prevent="apply">
      <div class="modal-header">
        <h3 id="course-modal-title" class="modal-title">切换方案 / 班级</h3>
        <button type="button" class="btn-icon close-btn" aria-label="关闭" @click="emit('close')">
          <X class="icon" :size="13" :stroke-width="1.5" />
        </button>
      </div>

      <div class="modal-body">
        <div class="input-row">
          <div class="field">
            <label for="course-scheme-id">方案号 (schemeId)</label>
            <input
              id="course-scheme-id"
              ref="sidInput"
              v-model.number="inputSid"
              type="number"
              min="1"
              step="1"
              placeholder="465"
              autofocus
              required
            >
          </div>
          <div class="field">
            <label for="course-class-id">班级号 (classId)</label>
            <input
              id="course-class-id"
              v-model.number="inputCid"
              type="number"
              min="1"
              step="1"
              placeholder="381"
              required
            >
          </div>
        </div>
      </div>

      <div class="modal-footer">
        <button type="button" class="btn btn-ghost" @click="emit('close')">取消</button>
        <button
          type="submit"
          class="btn btn-primary"
          :disabled="!isValid"
        >
          应用
        </button>
      </div>
    </form>
  </dialog>
</template>

<style scoped>
.input-row {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.field label {
  font-size: 11px;
  color: var(--text-muted);
}

.field input {
  background: var(--bg-canvas);
  border: 1px solid var(--border);
  border-radius: var(--radius-control);
  padding: 6px 10px;
  font-family: var(--font-mono);
  font-size: 13px;
  color: var(--text-primary);
  outline: none;
}

.field input:focus {
  border-color: var(--accent);
}
</style>
