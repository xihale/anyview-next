// 题面代码卡高亮: shiki/core + JS 正则引擎 + cpp/vitesse 双主题按需懒加载(免阻塞首屏与登录页),
// 双主题走 CSS 变量(defaultColor:false), 由 main.css 的 html[data-theme] 切换;
// 未就绪时同步返回转义 <pre class="shiki"><code> 兜底, 就绪后经 shikiReady 响应式自动升色。
import { shallowRef } from "vue";
import type { HighlighterCore } from "shiki/core";
import { escapeHtml } from "./docFormat";

const MAX_CACHE = 200;
const highlightCache = new Map<string, string>();

let highlighter: HighlighterCore | null = null;
let loader: Promise<HighlighterCore> | null = null;

export const shikiReady = shallowRef(false);

function cacheGet(code: string): string | undefined {
  const hit = highlightCache.get(code);
  if (hit !== undefined) {
    highlightCache.delete(code);
    highlightCache.set(code, hit);
  }
  return hit;
}

function cacheSet(code: string, html: string) {
  if (highlightCache.has(code)) {
    highlightCache.delete(code);
  } else if (highlightCache.size >= MAX_CACHE) {
    const oldest = highlightCache.keys().next().value;
    if (oldest !== undefined) highlightCache.delete(oldest);
  }
  highlightCache.set(code, html);
}

function renderTokens(h: HighlighterCore, code: string): string {
  const cached = cacheGet(code);
  if (cached !== undefined) return cached;
  const html = h.codeToHtml(code, {
    lang: "cpp",
    themes: { light: "vitesse-light", dark: "vitesse-dark" },
    defaultColor: false,
  });
  cacheSet(code, html);
  return html;
}

/** 预加载 shiki 核心、JS 正则引擎及 C++ 语法/双主题(幂等, 并发共享同一 Promise) */
export function preloadHighlight(): Promise<HighlighterCore> {
  if (highlighter) return Promise.resolve(highlighter);
  if (loader) return loader;
  loader = Promise.all([
    import("shiki/core"),
    import("shiki/engine/javascript"),
    import("shiki/langs/cpp.mjs"),
    import("shiki/themes/vitesse-dark.mjs"),
    import("shiki/themes/vitesse-light.mjs"),
  ])
    .then(async ([{ createHighlighterCore }, { createJavaScriptRegexEngine }, langCpp, themeDark, themeLight]) => {
      const h = await createHighlighterCore({
        langs: [langCpp.default],
        themes: [themeDark.default, themeLight.default],
        // forgiving: 个别语法模式 JS 引擎不支持时跳过而非抛错
        engine: createJavaScriptRegexEngine({ forgiving: true }),
      });
      highlighter = h;
      shikiReady.value = true;
      return h;
    })
    .catch((err) => {
      loader = null;
      throw err;
    });
  return loader;
}

export function highlightCpp(code: string): string {
  if (!code) return "";
  // 读取响应式标记: 懒加载完成后自动驱动调用方模板/computed 升色
  const ready = shikiReady.value;
  if (ready && highlighter) {
    return renderTokens(highlighter, code);
  }
  void preloadHighlight().catch(() => {});
  return `<pre class="shiki"><code>${escapeHtml(code)}</code></pre>`;
}

export async function highlightCppAsync(code: string): Promise<string> {
  if (!code) return "";
  const cached = cacheGet(code);
  if (cached !== undefined) return cached;
  const h = await preloadHighlight();
  return renderTokens(h, code);
}
