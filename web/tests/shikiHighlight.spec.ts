import { describe, expect, it } from "vitest";
import { highlightCpp, highlightCppAsync, preloadHighlight, shikiReady } from "~/utils/shikiHighlight";

describe("shikiHighlight", () => {
  it("空串返回空", () => {
    expect(highlightCpp("")).toBe("");
  });

  it("未就绪时同步返回转义兜底 <pre><code>，不阻塞首屏", () => {
    expect(shikiReady.value).toBe(false);
    const html = highlightCpp('const char* s = "<img src=x onerror=alert(1)>";');
    expect(html).toContain('<pre class="shiki"><code>');
    expect(html).toContain("&lt;img");
    expect(html).not.toContain("<img src");
  });

  it("并发 preloadHighlight 共享同一加载 Promise", async () => {
    const p1 = preloadHighlight();
    const p2 = preloadHighlight();
    expect(p1).toBe(p2);
    await p1;
    expect(shikiReady.value).toBe(true);
  });

  it("就绪后输出 shiki 双主题结构(CSS 变量随 data-theme 切换)", async () => {
    await preloadHighlight();
    const html = highlightCpp("int main() {\n  return 0;\n}");
    expect(html).toContain('class="shiki');
    expect(html).toContain("--shiki-dark:");
    expect(html).toContain("--shiki-light:");
  });

  it("着色命中关键字与注释(vitesse 色板)", async () => {
    const html = await highlightCppAsync("void f() {\n  // 注释 comment\n}");
    expect(html).toContain("CB7676"); // vitesse-dark 关键字玫瑰红
    expect(html).toContain("758575"); // vitesse-dark 注释灰
  });

  it("HTML 特殊字符被转义(无注入面)", async () => {
    const html = await highlightCppAsync('const char* s = "<img src=x onerror=alert(1)>";');
    // shiki 把 < 转成 &#x3C; 或 &lt;, 断言两种实体形态都算转义
    expect(html.includes("&lt;img") || html.includes("&#x3C;img")).toBe(true);
    expect(html).not.toContain("<img src");
  });
});
