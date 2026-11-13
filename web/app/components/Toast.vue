<script setup lang="ts">
import { useToast } from "~/composables/useToast";
import { X } from "lucide-vue-next";

const { toasts, dismiss } = useToast();
</script>

<template>
  <div class="toast-container" aria-live="polite">
    <TransitionGroup name="toast">
      <div
        v-for="item in toasts"
        :key="item.id"
        class="toast-item"
        :class="item.type"
        :role="item.type === 'error' ? 'alert' : 'status'"
        @click="dismiss(item.id)"
      >
        <span class="toast-dot" />
        <span class="toast-msg">{{ item.message }}</span>
        <button type="button" class="toast-close" aria-label="关闭" @click.stop="dismiss(item.id)">
          <X class="icon" :size="12" :stroke-width="1.5" />
        </button>
      </div>
    </TransitionGroup>
  </div>
</template>

<style scoped>
.toast-container {
  position: fixed;
  bottom: 24px;
  right: 24px;
  z-index: 9999;
  display: flex;
  flex-direction: column;
  gap: 8px;
  pointer-events: none;
  max-width: 380px;
  width: calc(100vw - 48px);
}

@media (max-width: 800px) {
  .toast-container {
    bottom: calc(44px + 12px + env(safe-area-inset-bottom, 0px));
    right: 12px;
    left: 12px;
    width: auto;
    max-width: none;
  }
}

.toast-item {
  pointer-events: auto;
  display: flex;
  align-items: center;
  gap: 10px;
  background: var(--bg-surface);
  border: 1px solid var(--border);
  box-shadow: var(--shadow-overlay);
  padding: 9px 12px;
  border-radius: var(--radius-panel);
  font-size: 12.5px;
  cursor: pointer;
  user-select: none;
}

.toast-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--text-faint);
  flex: none;
}

.toast-item.success .toast-dot {
  background: var(--ok);
}

.toast-item.error .toast-dot {
  background: var(--err);
}

.toast-item.warning .toast-dot {
  background: var(--warn);
}

.toast-item.info .toast-dot {
  background: var(--text-muted);
}

.toast-msg {
  color: var(--text-primary);
  line-height: 1.45;
  word-break: break-word;
  flex: 1;
}

.toast-close {
  color: var(--text-muted);
  display: inline-flex;
  padding: 3px;
  border: none;
  background: none;
  cursor: pointer;
  border-radius: 4px;
  transition: color 0.12s;
}

.toast-close:hover {
  color: var(--text-primary);
}

/* Animations */
.toast-enter-active,
.toast-leave-active {
  transition: all 0.2s cubic-bezier(0.16, 1, 0.3, 1);
}

.toast-enter-from {
  opacity: 0;
  transform: translateY(8px);
}

.toast-leave-to {
  opacity: 0;
  transform: translateY(4px);
}

@media (prefers-reduced-motion: reduce) {
  .toast-enter-active,
  .toast-leave-active {
    transition: opacity 0.12s ease;
  }

  .toast-enter-from,
  .toast-leave-to {
    transform: none;
  }
}
</style>
