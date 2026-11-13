import { describe, it, expect, beforeEach, vi } from "vitest";
import { useQuestionCache } from "~/composables/useQuestionCache";

// 草稿裁决模型: {code, seen} 三方裁决 + 旧格式迁移
describe("useQuestionCache resolveDraft", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("无草稿 → 直接用云端", () => {
    const { resolveDraft } = useQuestionCache();
    expect(resolveDraft(465, 1001, "CLOUD")).toEqual({ code: "CLOUD", cloudUpdated: false });
  });

  it("旧格式纯字符串草稿(无基线) → 云端优先, 旧草稿挪备份", () => {
    localStorage.setItem("an.draft.465.1002", "OLDEDIT");
    const { resolveDraft, hasBackup } = useQuestionCache();
    expect(resolveDraft(465, 1002, "CLOUD")).toEqual({ code: "CLOUD", cloudUpdated: true });
    expect(hasBackup(465, 1002)).toBe(true);
    expect(localStorage.getItem("an.draft.bak.465.1002")).toBe("OLDEDIT");
  });

  it("云端没动(seen 命中) → 未保存的本地编辑优先, 刷新不丢", () => {
    const { resolveDraft, saveDraftSync } = useQuestionCache();
    // 首开题目直接打字: 无既有条目, 调用方(编辑器)传入当前云端原文作基线
    saveDraftSync(465, 1003, "EDIT", "C0");
    expect(resolveDraft(465, 1003, "C0")).toEqual({ code: "EDIT", cloudUpdated: false });
  });

  it("云端被外部更新 → 云端优先, 本地旧编辑挪备份", () => {
    const { resolveDraft, saveDraftSync, getDraftEntry } = useQuestionCache();
    resolveDraft(465, 1004, "C0");
    saveDraftSync(465, 1004, "EDIT");
    expect(resolveDraft(465, 1004, "C1")).toEqual({ code: "C1", cloudUpdated: true });
    expect(localStorage.getItem("an.draft.bak.465.1004")).toBe("EDIT");
    // 基线推进: 下次仍一致
    expect(getDraftEntry(465, 1004)).toEqual({ code: "C1", seen: "C1" });
  });

  it("markCloudSynced 打标后, 本地无新编辑 → 与云端一致", () => {
    const { resolveDraft, markCloudSynced, getDraftEntry } = useQuestionCache();
    markCloudSynced(465, 1005, "SAVED");
    expect(getDraftEntry(465, 1005)).toEqual({ code: "SAVED", seen: "SAVED" });
    expect(resolveDraft(465, 1005, "SAVED")).toEqual({ code: "SAVED", cloudUpdated: false });
  });

  it("markCloudSynced 不吞掉打字快照之后的新编辑", () => {
    const { resolveDraft, markCloudSynced, saveDraftSync, getDraftEntry } = useQuestionCache();
    markCloudSynced(465, 1006, "RUNSNAP");
    saveDraftSync(465, 1006, "RUNSNAP+typing"); // 运行期间又打了字
    markCloudSynced(465, 1006, "RUNSNAP"); // compile 落盘的是快照
    expect(getDraftEntry(465, 1006)).toEqual({ code: "RUNSNAP+typing", seen: "RUNSNAP" });
    // 云端还是快照没动 → 编辑保留
    expect(resolveDraft(465, 1006, "RUNSNAP").code).toBe("RUNSNAP+typing");
  });

  it("saveDraftSync 与 markCloudSynced 内容未变时跳过冗余 localStorage 写入", () => {
    const { saveDraftSync, markCloudSynced } = useQuestionCache();
    const setSpy = vi.spyOn(localStorage, "setItem");

    saveDraftSync(465, 1007, "CODE", "BASE");
    expect(setSpy).toHaveBeenCalledTimes(1);

    saveDraftSync(465, 1007, "CODE", "BASE");
    expect(setSpy).toHaveBeenCalledTimes(1);

    markCloudSynced(465, 1007, "CODE");
    expect(setSpy).toHaveBeenCalledTimes(2);

    markCloudSynced(465, 1007, "CODE");
    expect(setSpy).toHaveBeenCalledTimes(2);
    setSpy.mockRestore();
  });

  it("setCachedQuestion 防抖写 sessionStorage 且命中同内容不重复调度", () => {
    vi.useFakeTimers();
    sessionStorage.clear();
    const ssSpy = vi.spyOn(sessionStorage, "setItem");
    const { setCachedQuestion, getCachedQuestion } = useQuestionCache();

    const q = {
      questionFullName: "q1",
      questionId: 2001,
      kind: 4,
      doc: "hello",
      language: 1,
      completes: [],
      stuCode: "abc",
    };
    setCachedQuestion(465, 2001, q);
    expect(getCachedQuestion(465, 2001)?.questionFullName).toBe("q1");
    expect(ssSpy).not.toHaveBeenCalled();

    vi.advanceTimersByTime(650);
    expect(ssSpy).toHaveBeenCalledTimes(1);

    // 同内容再写不触发 stringify/setItem
    setCachedQuestion(465, 2001, { ...q });
    vi.advanceTimersByTime(650);
    expect(ssSpy).toHaveBeenCalledTimes(1);

    ssSpy.mockRestore();
    vi.useRealTimers();
  });
});
