// 题面正文渲染管线(纯字符串, 无 DOM 依赖, bare vitest 可测):
//   1. panguSpacing  —— 中英文/中数字之间补空格(盘古之白)
//   2. renderProseHtml —— 转义 → 盘古 → 数学块(等式行) + inline code 芯片 + 上标
// 题面来自上游不可信: HTML 先转义, 之后只产出本模块自有的受控标签, 天然免 XSS。

const HAN = "\\u3400-\\u4DBF\\u4E00-\\u9FFF\\uF900-\\uFAFF";

// 盘古之白: 仅在汉字 ↔ [A-Za-z0-9] 的直接邻界插一个空格(右侧含闭括号,
// 否则 "a[0..n-1]的第" 这类记号后缘漏空)。不动全角标点(，。：)与【】等。
const PANGU_L = new RegExp(`([${HAN}])([A-Za-z0-9])`, "g");
const PANGU_R = new RegExp(`([A-Za-z0-9\\]\\)])([${HAN}])`, "g");

export function panguSpacing(text: string): string {
  if (!text) return text;
  return text.replace(PANGU_L, "$1 $2").replace(PANGU_R, "$1 $2");
}

export function escapeHtml(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

// 等式行(数学块): LHS 必须是 f(...) 形式且紧跟 =。语料中的真实样本:
//   P(x) = a0 + a1x + a2x^2 + ... + anx^n      (一元多项式)
//   F(n) = n+1      当n=0                      (分段函数)
//   akm(m,n) = akm(m-1,akm(m,n-1)) 当m!=0,n!=0
// 排除 C 语句(尾随 ;)、注释行、#define——它们归代码卡/正文。
const EQ_LINE = /^[A-Za-z][A-Za-z0-9_]*\([^()]*\)\s*=/;

export function isEquationLine(line: string): boolean {
  const t = line.trim();
  if (!EQ_LINE.test(t)) return false;
  return !/[;#]|\/[/*]|^\s*\*/.test(t);
}

// 数学块内部: 字母后紧跟的数字串转下标(a0→a₀, a1x→a₁x), x^2→上标。
// 仅等式行内启用字母+数字下标——正文里 T1/RcdType2 是标识符, 不能动。
function mathInner(line: string): string {
  let s = escapeHtml(line.trim());
  s = panguSpacing(s);
  s = s.replace(/([A-Za-z])(\d+)(?!\d)/g, "$1<sub>$2</sub>");
  s = s.replace(/([A-Za-z0-9\])])\^([A-Za-z0-9]+)/g, "$1<sup>$2</sup>");
  return s;
}

// 正文 inline 记号(单趟合并, 各候选在同行内互不重扫, 已消费的整段被吞掉):
//   函数调用  H(x) / P(x0) / visit(T->data)
//   数组记号  a[0..n-1] / g[1..m][1..n] / L.rcd[1..L.length] / vexs[MAX_VEX_NUM]
//   上标      2^i / x^n
//   全大写常量 MAXINT / OK / EOVERFLOW / TRUE
// 小写普通单词(lsize 等)不包芯片——无法与英文单词区分, 误伤面太大。
// 芯片内容限 ASCII(\x20-\x7e): 防止 "f(与中文)" 之类被整段吞进芯片。
const ASCII = "\\x20-\\x7e";
const INLINE_TOKEN = new RegExp(
  [
    `[A-Za-z_]\\w*\\([${ASCII}]{0,100}\\)`, // 函数调用
    `[A-Za-z_][\\w]*(?:\\.\\w+)*\\[[${ASCII}]{1,24}\\](?:\\[[${ASCII}]{1,24}\\])*`, // 数组记号(允许连续维)
    "[A-Za-z0-9\\])]\\^[A-Za-z0-9]+", // 上标
    "\\b[A-Z][A-Z0-9_]{1,}\\b", // 全大写常量(≥2 字符)
  ].join("|"),
  "g",
);

function inlineTokens(escaped: string): string {
  return escaped.replace(INLINE_TOKEN, (m) => {
    if (m.includes("^")) {
      const k = m.indexOf("^");
      return `${m.slice(0, k)}<sup>${m.slice(k + 1)}</sup>`;
    }
    return `<code class="doc-code">${m}</code>`;
  });
}

// 正文块 → HTML: 逐行扫描, 连续等式行聚合成一个数学块, 其余为普通段落。
// 输出为兄弟 <div> 序列(等式块不能嵌进 <p>), 由调用方容器 v-html 承载。
export function renderProseHtml(text: string): string {
  if (!text) return "";
  const lines = text.split("\n");
  const out: string[] = [];
  let prose: string[] = [];
  let eqs: string[] = [];

  function flushProse() {
    if (!prose.length) return;
    const body = inlineTokens(escapeHtml(panguSpacing(prose.join("\n"))));
    out.push(`<div class="doc-para">${body}</div>`);
    prose = [];
  }

  function flushEqs() {
    if (!eqs.length) return;
    out.push(`<div class="math-block">${eqs.join("\n")}</div>`);
    eqs = [];
  }

  for (const line of lines) {
    if (isEquationLine(line)) {
      flushProse();
      eqs.push(mathInner(line));
    } else {
      flushEqs();
      prose.push(line);
    }
  }
  flushProse();
  flushEqs();
  return out.join("");
}

const RICH_SKIP_TAGS = new Set(["PRE", "CODE", "SCRIPT", "STYLE", "TEXTAREA"]);

// 富文本 HTML 路径(上游含 <div>/<table> 的题面): 遍历文本节点, 逐节点做
// 盘古 + inline 记号, 跳过 pre/code 内部。仅客户端调用(SPA, 无 SSR)。
export function enhanceRichHtml(html: string): string {
  if (!html || typeof DOMParser === "undefined") return html;
  const doc = new DOMParser().parseFromString(html, "text/html");
  const walker = doc.createTreeWalker(doc.body, NodeFilter.SHOW_TEXT);
  const nodes: Text[] = [];
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const el = n.parentElement;
    if (el && !RICH_SKIP_TAGS.has(el.tagName)) nodes.push(n as Text);
  }
  const tpl = doc.createElement("template");
  for (const node of nodes) {
    const raw = node.nodeValue ?? "";
    if (!raw.trim()) continue;
    const spaced = panguSpacing(raw);
    const html2 = inlineTokens(escapeHtml(spaced));
    if (html2 === raw) continue;
    tpl.innerHTML = html2;
    node.replaceWith(tpl.content);
  }
  return doc.body.innerHTML;
}
