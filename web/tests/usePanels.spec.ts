import { beforeEach, describe, expect, it, vi } from "vitest";
import { usePanels } from "~/composables/usePanels";

describe("usePanels 拖拽节流与持久化", () => {
  beforeEach(() => {
    localStorage.clear();
    usePanels().resetLayout();
  });

  it("拖拽期间走 rAF 批量更新，仅在 pointerup 落盘 localStorage", () => {
    let rafCb: FrameRequestCallback | null = null;
    const rafSpy = vi.spyOn(window, "requestAnimationFrame").mockImplementation((cb) => {
      rafCb = cb;
      return 1;
    });
    const cancelSpy = vi.spyOn(window, "cancelAnimationFrame").mockImplementation(() => {
      rafCb = null;
    });
    const setSpy = vi.spyOn(localStorage, "setItem");

    const { sidebarWidth, isDragging, startResize } = usePanels();
    startResize("sidebar", { isPrimary: true, pointerId: 1, clientX: 250, clientY: 100 } as unknown as PointerEvent);
    expect(isDragging.value).toBe(true);
    expect(setSpy).not.toHaveBeenCalled();

    // 连续多次 pointermove 只排队一次 rAF
    window.dispatchEvent(Object.assign(new Event("pointermove"), { pointerId: 1, clientX: 280, clientY: 100 }));
    window.dispatchEvent(Object.assign(new Event("pointermove"), { pointerId: 1, clientX: 310, clientY: 100 }));
    expect(rafSpy).toHaveBeenCalledTimes(1);
    expect(setSpy).not.toHaveBeenCalled();

    // 执行 rAF 生效最新坐标
    rafCb?.(16);
    expect(sidebarWidth.value).toBe(310);
    expect(setSpy).not.toHaveBeenCalled();

    // pointerup 才落盘且只写对应分栏键
    window.dispatchEvent(Object.assign(new Event("pointerup"), { pointerId: 1 }));
    expect(isDragging.value).toBe(false);
    expect(setSpy).toHaveBeenCalledWith("an.sidebarW", "310");
    expect(setSpy).not.toHaveBeenCalledWith("an.outH", expect.anything());

    rafSpy.mockRestore();
    cancelSpy.mockRestore();
    setSpy.mockRestore();
  });

  it("窗口失焦(blur)自动结束拖拽避免粘拖", () => {
    const { isDragging, startResize } = usePanels();
    startResize("statement", { isPrimary: true, pointerId: 2, clientX: 400, clientY: 100 } as unknown as PointerEvent);
    expect(isDragging.value).toBe(true);

    window.dispatchEvent(new Event("blur"));
    expect(isDragging.value).toBe(false);
    expect(document.body.style.userSelect).toBe("");
  });
});
