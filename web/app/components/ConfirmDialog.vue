<script setup lang="ts">
// 通用确认弹窗(原生 <dialog>): 替代 window.confirm, 与应用 modal 体系同一套观感
const props = withDefaults(
  defineProps<{
    show: boolean;
    message: string;
    title?: string;
    confirmLabel?: string;
  }>(),
  {
    title: "确认操作",
    confirmLabel: "确认",
  },
);

const emit = defineEmits<{
  (e: "confirm"): void;
  (e: "close"): void;
}>();

const dlg = ref<HTMLDialogElement | null>(null);
const cancelBtn = ref<HTMLButtonElement | null>(null);
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
      nextTick(() => cancelBtn.value?.focus());
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
  }
}
</script>

<template>
  <dialog
    ref="dlg"
    class="modal confirm-modal"
    role="dialog"
    aria-modal="true"
    aria-labelledby="confirm-dialog-title"
    aria-describedby="confirm-dialog-msg"
    @click="onBackdrop"
    @close="onClose"
    @keydown="onKeydown"
  >
    <div class="modal-inner">
      <div class="modal-header">
        <h3 id="confirm-dialog-title" class="modal-title">{{ title }}</h3>
      </div>

      <div class="modal-body">
        <p id="confirm-dialog-msg" class="confirm-msg">{{ message }}</p>
      </div>

      <div class="modal-footer">
        <!-- 焦点默认落在取消: 确认键多为破坏性操作, 回车即取消更安全 -->
        <button ref="cancelBtn" type="button" class="btn btn-ghost" autofocus @click="emit('close')">取消</button>
        <button type="button" class="btn btn-primary" @click="emit('confirm')">
          {{ confirmLabel }}
        </button>
      </div>
    </div>
  </dialog>
</template>

<style scoped>
.confirm-modal {
  max-width: 320px;
}

.confirm-msg {
  margin: 0;
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--text-primary);
}
</style>
