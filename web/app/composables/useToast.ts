// 显式导入: 供裸 vitest 直接加载(不走 Nuxt 自动导入)
import { ref, readonly } from "vue";

export interface ToastItem {
  id: number;
  message: string;
  type: "info" | "success" | "error" | "warning";
}

const MAX_TOASTS = 6;
const toasts = ref<ToastItem[]>([]);
const timers = new Map<number, ReturnType<typeof setTimeout>>();
let nextId = 1;

export function useToast() {
  function dismiss(id: number) {
    const t = timers.get(id);
    if (t !== undefined) {
      clearTimeout(t);
      timers.delete(id);
    }
    const index = toasts.value.findIndex((item) => item.id === id);
    if (index !== -1) {
      toasts.value.splice(index, 1);
    }
  }

  function show(message: string, type: ToastItem["type"] = "info", duration = 3000) {
    while (toasts.value.length >= MAX_TOASTS) {
      const oldest = toasts.value[0];
      if (!oldest) break;
      dismiss(oldest.id);
    }
    const id = nextId++;
    toasts.value.push({ id, message, type });
    if (duration > 0) {
      timers.set(
        id,
        setTimeout(() => {
          timers.delete(id);
          dismiss(id);
        }, duration),
      );
    }
    return id;
  }

  return {
    toasts: readonly(toasts),
    show,
    success: (msg: string, dur?: number) => show(msg, "success", dur),
    error: (msg: string, dur?: number) => show(msg, "error", dur ?? 4500),
    warning: (msg: string, dur?: number) => show(msg, "warning", dur),
    info: (msg: string, dur?: number) => show(msg, "info", dur),
    dismiss,
  };
}
