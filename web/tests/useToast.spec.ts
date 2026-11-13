import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useToast } from "~/composables/useToast";

// useToast 是模块级单例, 每用例先清场
function drain() {
  const { toasts, dismiss } = useToast();
  for (const t of [...toasts.value]) dismiss(t.id);
}

beforeEach(() => {
  vi.useFakeTimers();
  drain();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("useToast", () => {
  it("show 入队且带类型", () => {
    const { toasts, show } = useToast();
    show("hello", "warning");
    expect(toasts.value).toHaveLength(1);
    expect(toasts.value[0].message).toBe("hello");
    expect(toasts.value[0].type).toBe("warning");
  });

  it("到时自动消失(默认 3000ms)", () => {
    const { toasts, show } = useToast();
    show("auto");
    vi.advanceTimersByTime(2999);
    expect(toasts.value).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(toasts.value).toHaveLength(0);
  });

  it("error 默认 4500ms", () => {
    const { toasts, error } = useToast();
    error("boom");
    vi.advanceTimersByTime(4499);
    expect(toasts.value).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(toasts.value).toHaveLength(0);
  });

  it("手动 dismiss 后定时器触发不复活不报错", () => {
    const { toasts, show, dismiss } = useToast();
    const id = show("x");
    dismiss(id);
    expect(toasts.value).toHaveLength(0);
    vi.advanceTimersByTime(10_000);
    expect(toasts.value).toHaveLength(0);
  });

  it("超过上限时自动淘汰最早的 toast 防止堆积", () => {
    const { toasts, show } = useToast();
    for (let i = 1; i <= 8; i++) show(`msg-${i}`);
    expect(toasts.value).toHaveLength(6);
    expect(toasts.value[0].message).toBe("msg-3");
    expect(toasts.value[5].message).toBe("msg-8");
  });
});
