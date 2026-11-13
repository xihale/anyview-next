import { describe, expect, it } from "vitest";
import { findMainLine } from "~/composables/useDebug";

describe("findMainLine", () => {
  it("普通 int main 定义", () => {
    expect(findMainLine("typedef struct {\n  int x;\n} S;\n\nint main() {\n  return 0;\n}")).toBe(5);
  });

  it("void main 也认", () => {
    expect(findMainLine("void main() {\n}")).toBe(1);
  });

  it("带 inline/static 修饰", () => {
    expect(findMainLine("static int main(void) {\n}")).toBe(1);
  });

  it("跳过注释行", () => {
    expect(findMainLine("// int main() {}\n/* 起始行\nint main() {\n}")).toBe(3);
  });

  it("声明(带分号)不算定义", () => {
    expect(findMainLine("int main(void);\n\nint main() { }")).toBe(3);
  });

  it("找不到返回 0", () => {
    expect(findMainLine("void foo() {}")).toBe(0);
    expect(findMainLine("")).toBe(0);
  });
});
