import { describe, it, expect, beforeAll } from "vitest";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { signatureHelp, sigHelpField } from "~/utils/cppSignatures";

beforeAll(() => {
  const g = globalThis as any;
  if (typeof g.ResizeObserver === "undefined") {
    g.ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    };
  }
});

function makeView(doc: string) {
  return new EditorView({
    state: EditorState.create({ doc, extensions: [signatureHelp()] }),
  });
}

const type = (view: EditorView, text: string) => {
  const end = view.state.doc.length;
  view.dispatch({ changes: { from: end, insert: text }, selection: { anchor: end + text.length } });
};

describe("cppSignatures 签名提示", () => {
  it("memcpy( 触发提示, 第 0 参高亮", () => {
    const v = makeView("");
    type(v, "memcpy(");
    const t = v.state.field(sigHelpField);
    expect(t).not.toBeNull();
    expect(t!.sigKey).toMatch(/^memcpy@\d+#0$/);
    expect(t!.pos).toBe(0);
  });

  it("敲逗号推进当前参数高亮", () => {
    const v = makeView("");
    type(v, "memcpy(a");
    expect(v.state.field(sigHelpField)!.sigKey).toMatch(/#0$/);
    type(v, ",");
    expect(v.state.field(sigHelpField)!.sigKey).toMatch(/#1$/);
  });

  it("越过配对右括号 → 关闭", () => {
    const v = makeView("");
    type(v, "memcpy(a, b)");
    expect(v.state.field(sigHelpField)).toBeNull();
  });

  it("括号内回点光标 → 重新出现(IDE 行为)", () => {
    const v = makeView("memcpy(a, b)");
    v.dispatch({ selection: { anchor: 11 } }); // b 与 ) 之间
    expect(v.state.field(sigHelpField)).not.toBeNull();
    v.dispatch({ selection: { anchor: 12 } }); // 越过右括号
    expect(v.state.field(sigHelpField)).toBeNull();
  });

  it("未知函数不提示", () => {
    const v = makeView("");
    type(v, "myfunc(");
    expect(v.state.field(sigHelpField)).toBeNull();
  });

  it("字符串里的逗号不推进参数", () => {
    const v = makeView("");
    type(v, 'printf("a,b');
    expect(v.state.field(sigHelpField)!.sigKey).toMatch(/#0$/);
    type(v, '"');
    expect(v.state.field(sigHelpField)!.sigKey).toMatch(/#0$/);
  });

  it("字符串里的转义反斜杠不吞掉闭合引号", () => {
    const v = makeView("");
    type(v, 'printf("a\\\\",b');
    expect(v.state.field(sigHelpField)!.sigKey).toMatch(/#1$/);
  });
});
