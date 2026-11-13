import { describe, expect, it } from "vitest";
import {
  enhanceRichHtml,
  isEquationLine,
  panguSpacing,
  renderProseHtml,
} from "~/utils/docFormat";

// 样本取自上游真实题面(2025-09 抓取的 DC 全部 131 题, 纯文本语义流格式)

describe("panguSpacing 中英文/中数字盘古之白", () => {
  it("汉字与拉丁/数字邻界插空格", () => {
    expect(panguSpacing("计算i!×2^i的值并存入数组a[0..n-1]的第i-1个分量中")).toBe(
      "计算 i!×2^i 的值并存入数组 a[0..n-1] 的第 i-1 个分量中",
    );
    expect(panguSpacing("求指向T中第k小的结点的位置")).toBe(
      "求指向 T 中第 k 小的结点的位置",
    );
    expect(panguSpacing("第2章-DC02PE73")).toBe("第 2 章-DC02PE73");
  });

  it("全角标点与汉字不插空格, 已有空格不重复插", () => {
    expect(panguSpacing("试写一算法，实现带头结点单链表的销毁操作。")).toBe(
      "试写一算法，实现带头结点单链表的销毁操作。",
    );
    expect(panguSpacing("计算 i!×2^i 的值")).toBe("计算 i!×2^i 的值");
  });

  it("幂等", () => {
    const s = "计算i!×2^i的值, 时间复杂度为O(logn)";
    expect(panguSpacing(panguSpacing(s))).toBe(panguSpacing(s));
  });
});

describe("isEquationLine 数学块(等式行)识别", () => {
  it("识别教材等式行(含分段函数/缩进)", () => {
    expect(isEquationLine("    P(x) = a0 + a1x + a2x^2 + ... + anx^n")).toBe(true);
    expect(isEquationLine("F(n) = n+1      当n=0")).toBe(true);
    expect(isEquationLine("akm(m,n) = akm(m-1,akm(m,n-1)) 当m!=0,n!=0")).toBe(true);
    expect(isEquationLine("sqrt(A,p,e) = p                   当|p*p-A|<e")).toBe(true);
  });

  it("不误伤 C 语句/注释/正文", () => {
    expect(isEquationLine("Status Series(int a[], int n);")).toBe(false);
    expect(isEquationLine("int LocateVex(MGraph G, VexType v); // 查找顶点v在图G中的位序")).toBe(false);
    expect(isEquationLine("* @param  rd: 取值为[2~9]，表示具体的进制")).toBe(false);
    expect(isEquationLine("试写出求递归函数F(n)的非递归算法：")).toBe(false);
    expect(isEquationLine("要求实现下列函数：")).toBe(false);
    expect(isEquationLine("")).toBe(false);
  });
});

describe("renderProseHtml 正文渲染管线", () => {
  it("一元多项式题: 等式行聚合为数学块, 下标/上标就位", () => {
    const html = renderProseHtml(
      "试编写算法，求一元多项式\n    P(x) = a0 + a1x + a2x^2 + ... + anx^n\n的值P(x0)，并确定算法中每一语句的执行次数和整个算法的时间复杂度。",
    );
    expect(html).toContain(
      '<div class="math-block">P(x) = a<sub>0</sub> + a<sub>1</sub>x + a<sub>2</sub>x<sup>2</sup> + ... + anx<sup>n</sup></div>',
    );
    // 数学块外的 P(x0) 是 inline 芯片而非下标变换
    expect(html).toContain('<code class="doc-code">P(x0)</code>');
  });

  it("分段函数连续等式聚进同一个数学块, CJK 尾部盘古且不被芯片吞", () => {
    const html = renderProseHtml(
      "F(n) = n+1      当n=0\nF(n) = nF(n/2)  当n>0",
    );
    expect(html.match(/<div class="math-block">/g)).toHaveLength(1);
    expect(html).toContain("当 n=0");
    expect(html).toContain("&gt;0");
    expect(html).not.toContain("<code");
  });

  it("inline 记号包芯片: 常量/函数调用/数组记号/上标", () => {
    const html = renderProseHtml(
      "假设计算机中允许的整数最大值为MAXINT，使k!×2^k>MAXINT时按出错处理。哈希函数H(key)为关键字的序号。假设以二维数组g[1..m][1..n]表示图像区域。时间复杂度为O(logn)。",
    );
    expect(html).toContain('<code class="doc-code">MAXINT</code>');
    expect(html).toContain("×2<sup>k</sup>&gt;<code class=\"doc-code\">MAXINT</code>");
    expect(html).toContain('<code class="doc-code">H(key)</code>');
    expect(html).toContain('<code class="doc-code">g[1..m][1..n]</code>');
    expect(html).toContain('<code class="doc-code">O(logn)</code>');
  });

  it("小写普通标识符不包芯片(与英文单词无法区分)", () => {
    expect(renderProseHtml("在含lsize域的二叉排序树T中")).not.toContain("<code");
  });

  it("正文里的 k(1≤k≤n) 不包芯片(内容含非 ASCII), 芯片后缘与汉字间盘古", () => {
    expect(renderProseHtml("当对某个k(1≤k≤n)，使k!×2^k>MAXINT时")).toBe(
      '<div class="doc-para">当对某个 k(1≤k≤n)，使 k!×2<sup>k</sup>&gt;<code class="doc-code">MAXINT</code> 时</div>',
    );
  });

  it("HTML 先转义, 上游恶意内容无法逃逸", () => {
    const html = renderProseHtml('值<img src=x onerror=alert(1)>和<script>alert(2)</script>OK');
    expect(html).not.toMatch(/<(?!\/?(div|code|sup|sub)[ >])/);
    expect(html).toContain("&lt;script&gt;");
    expect(html).toContain('<code class="doc-code">OK</code>');
  });

  it("空内容返回空串", () => {
    expect(renderProseHtml("")).toBe("");
  });
});

describe("enhanceRichHtml 富文本路径(文本节点级处理)", () => {
  it("正文文本节点盘古+上标, pre 内保持原样", () => {
    const html = enhanceRichHtml(
      "<p>计算i!×2^i的值, MAXINT为上限</p><pre>计算i!×2^i的值</pre>",
    );
    expect(html).toContain("<p>计算 i!×2<sup>i</sup> 的值, <code class=\"doc-code\">MAXINT</code> 为上限</p>");
    expect(html).toContain("<pre>计算i!×2^i的值</pre>");
  });

  it("空串原样返回", () => {
    expect(enhanceRichHtml("")).toBe("");
  });
});
