<script setup lang="ts">
import DOMPurify from "dompurify";
import { useClipboard } from "@vueuse/core";
import { useToast } from "~/composables/useToast";
import { highlightCpp } from "~/utils/shikiHighlight";
import { Check, Copy } from "lucide-vue-next";
import { enhanceRichHtml, escapeHtml, panguSpacing, renderProseHtml } from "~/utils/docFormat";

interface DocBlock {
  type: "desc" | "function" | "struct" | "note" | "example" | "program";
  title?: string;
  content: string;
  isCode?: boolean;
}

interface RenderedDocBlock extends DocBlock {
  html?: string;
}

function safeHighlightCpp(code: string): string {
  if (!code) return "";
  try {
    const html = highlightCpp(code);
    if (html) return html;
  } catch {}
  return `<pre><code>${escapeHtml(code)}</code></pre>`;
}

const props = withDefaults(
  defineProps<{
    doc: string;
    pname?: string;
    pmemo?: string;
    chapName?: string;
    passed?: boolean;
    fontSize?: number;
    loading?: boolean;
  }>(),
  {
    fontSize: 14.5,
    loading: false,
  },
);

const toast = useToast();
const { copy } = useClipboard({ legacy: true });
const copiedKey = ref<string | number | null>(null);
let copiedTimer: ReturnType<typeof setTimeout> | null = null;

function markCopied(key: string | number) {
  copiedKey.value = key;
  if (copiedTimer) clearTimeout(copiedTimer);
  copiedTimer = setTimeout(() => {
    copiedKey.value = null;
    copiedTimer = null;
  }, 1500);
}

onBeforeUnmount(() => {
  if (copiedTimer) {
    clearTimeout(copiedTimer);
    copiedTimer = null;
  }
});

function escapeRe(s: string) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function stripPnamePrefix(text: string, name: string) {
  if (!text || !name) return text.trim();
  const t = text.replace(/<[^>]+>/g, "").trim();
  if (t === name) return "";
  return t.replace(new RegExp(`^${escapeRe(name)}\\s*[·•\\-—:|/]?\\s*`), "").trim();
}

const displayTitle = computed(() => {
  const name = props.pname?.trim() || "";
  let t = stripPnamePrefix(props.pmemo || "", name);
  if (t) return panguSpacing(t);
  const m = props.doc?.match(/<h[1-3][^>]*>([\s\S]*?)<\/h[1-3]>/i);
  if (m && m[1]) {
    t = stripPnamePrefix(m[1], name);
    if (t) return panguSpacing(t);
  }
  return "";
});

// 判断是否为已有丰富 HTML 标签的内容（如表格、富文本排版）
const isRawHtml = computed(() => {
  if (!props.doc) return false;
  return /<(table|ul|ol|div|span|h[1-6])\b/i.test(props.doc);
});

// 清洗富文本 HTML
// DOMPurify 白名单净化替代旧正则: 正则可被 <img/onerror=>(/ 作属性分隔)、
// 未闭合标签、jav&#x61;script: 实体编码等绕过, 题面来自上游不可信
// 末尾 enhanceRichHtml: 文本节点级盘古 + inline 记号(跳过 pre/code)
const sanitizedRawDoc = computed(() => {
  if (!props.doc || !isRawHtml.value) return "";
  return DOMPurify.sanitize(props.doc);
});

const cleanedHtml = computed(() => {
  let s = sanitizedRawDoc.value;
  if (!s) return "";

  s = s.replace(/^[【\[]\d{8,14}[】\]]\s*/, "");
  const name = props.pname?.trim();
  if (name) {
    s = s.replace(
      new RegExp(`<h[1-3][^>]*>\\s*${escapeRe(name)}(?:\\s*[·•\\-—:]\\s*[^<]*)?\\s*</h[1-3]>`, "i"),
      "",
    );
  }
  s = s.replace(/【(.*?)】/g, '<div class="doc-section-title">$1</div>');
  return enhanceRichHtml(s);
});

// 纯文本语义流式状态机解析
const parsedBlocks = computed<DocBlock[]>(() => {
  if (!props.doc || isRawHtml.value) return [];

  let s = DOMPurify.sanitize(props.doc.trim(), { ALLOWED_TAGS: ["br"] })
    .replace(/<br\s*\/?>/gi, "\n");
  if (typeof DOMParser !== "undefined") {
    s = new DOMParser().parseFromString(s, "text/html").body.textContent ?? "";
  } else {
    s = s.replace(/<[^>]+>/g, "");
  }

  // 1. 去除首部版本时间戳噪声（如 [20220901]、[20231004]）
  s = s.replace(/^[【\[]\d{8,14}[】\]]\s*/, "");
  s = s.replace(/\r\n/g, "\n").replace(/\r/g, "\n");

  // 2. 保护例题中的【程序】完整代码
  let programCode: string | null = null;
  const programIdx = s.search(/【程序】\s*\n/);
  if (programIdx !== -1) {
    programCode = s.slice(programIdx).replace(/^【程序】\s*\n/, "").trim();
    s = s.slice(0, programIdx).trim();
  }

  const lines = s.split("\n");
  const blocks: DocBlock[] = [];

  let currentType: DocBlock["type"] = "desc";
  let currentTitle = "";
  let currentLines: string[] = [];

  function flush() {
    if (!currentLines.length) return;
    const text = currentLines.join("\n").trim();
    if (text) {
      const isCode = currentType === "function" || currentType === "struct" || currentType === "program" || isLikelyCode(text);
      blocks.push({
        type: currentType,
        title: currentTitle || undefined,
        content: text,
        isCode,
      });
    }
    currentLines = [];
    currentTitle = "";
  }

  const funcRegex = /^(?:要求实现|实现|请实现)(?:下列|以下)?函数[：:]?$/;
  const structRegex = /^(?:\/\/\s*)?(.{2,35}?(?:结构体|类型|数据结构|存储结构).*?(?:定义|可用)(?:如下|为)?)[：:]?$/;
  const noteRegex = /^(?:注意|注|建议)[：:]\s*(.*)$/;
  const exampleRegex = /^(?:例如|示例)[：:]\s*(.*)$/;

  for (let i = 0; i < lines.length; i++) {
    const rawLine = lines[i] ?? "";
    const line = rawLine.trim();

    // 匹配 "要求实现下列函数："
    if (funcRegex.test(line)) {
      flush();
      currentType = "function";
      currentTitle = "要求实现下列函数";
      continue;
    }

    // 匹配数据结构定义标题（如 "二叉链表类型定义："、"// 双向链表的类型定义为："）
    const structMatch = line.match(structRegex);
    if (structMatch && structMatch[1]) {
      flush();
      currentType = "struct";
      currentTitle = structMatch[1].replace(/^\/\/\s*/, "").replace(/^【题目】\s*/, "");
      continue;
    }

    // 匹配注意事项
    const noteMatch = line.match(noteRegex);
    if (noteMatch) {
      flush();
      currentType = "note";
      currentTitle = "注意事项";
      if (noteMatch[1]) currentLines.push(noteMatch[1]);
      continue;
    }

    // 匹配示例说明
    const egMatch = line.match(exampleRegex);
    if (egMatch) {
      flush();
      currentType = "example";
      currentTitle = "示例";
      if (egMatch[1]) currentLines.push(egMatch[1]);
      continue;
    }

    // 遇到独立的 typedef struct 自动切换为数据结构块
    if (currentType !== "struct" && /^(?:typedef\s+struct|struct\s+\w+)/.test(line)) {
      flush();
      currentType = "struct";
      currentTitle = "数据结构定义";
    }

    // 遇到独立的函数声明（如 void printName(...)）
    const isStandaloneFunc = /^(?:void|int|bool|Status|char|float|double|LinkList|BiTree|Node\*?)\s+[a-zA-Z_]\w*\s*\([^)]*\)\s*;?$/.test(line);
    if (isStandaloneFunc) {
      if (currentType === "desc" || (currentType === "struct" && currentLines.some((l) => l.trim().startsWith("}")))) {
        flush();
        currentType = "function";
        currentTitle = "要求实现下列函数";
      }
    }

    // struct 闭合后恢复正文描述
    if (currentType === "struct" && /^(?:试[写编]|编写|请[编写]|假定|已知|如果|若)/.test(line)) {
      flush();
      currentType = "desc";
      currentTitle = "";
    }

    // 清洗【题目】前缀
    if (/^【题目】/.test(line)) {
      const cleaned = line.replace(/^【题目】\s*/, "");
      if (cleaned) currentLines.push(cleaned);
      continue;
    }

    currentLines.push(rawLine);
  }

  flush();

  if (programCode) {
    blocks.push({
      type: "program",
      title: "完整参考代码",
      content: programCode,
      isCode: true,
    });
  }

  return blocks;
});

const renderedBlocks = computed<RenderedDocBlock[]>(() =>
  parsedBlocks.value.map((block) => {
    if (block.type === "function" || block.type === "struct" || block.type === "program") {
      return { ...block, html: safeHighlightCpp(block.content) };
    }
    if (block.type === "note" || block.type === "desc") {
      return { ...block, html: renderProseHtml(block.content) };
    }
    return block;
  }),
);

function isLikelyCode(text: string): boolean {
  if (/^(typedef|struct|void|int|bool|Status|char|double|float|#include|template)\b/.test(text)) return true;
  if (/^\/\*[\s\S]*\*\//.test(text)) return true;
  if (text.includes("{\n") || text.includes("{\r\n")) return true;
  if (/\b(return|printf|scanf|cout|cin)\b/.test(text) && text.includes(";")) return true;
  return false;
}

// 代码片段着色: highlightCpp 已迁至 utils/shikiHighlight(shiki TextMate 语法,
// github 双主题 CSS 变量), 此处仅引用并做缓存与降级兜底

// 复制单个代码片段或示例块
async function copySnippet(text: string, key: string | number, label = "代码已复制") {
  try {
    await copy(text);
    markCopied(key);
    toast.success(label);
  } catch {
    toast.error("复制失败");
  }
}

// 复制整篇题面纯文本
async function copyAllDoc() {
  if (!props.doc) return;
  try {
    const text = isRawHtml.value
      ? (() => {
          const t = document.createElement("div");
          t.innerHTML = cleanedHtml.value;
          return t.textContent || t.innerText || "";
        })()
      : parsedBlocks.value
          .map((b) => (b.title ? `【${b.title}】\n${b.content}` : b.content))
          .join("\n\n");

    await copy(text);
    markCopied("all");
    toast.success("题面全文已复制");
  } catch {
    toast.error("复制失败");
  }
}
</script>

<template>
  <div class="statement-container" :style="{ '--statement-size': `${fontSize}px` }">
    <!-- 顶栏：紧凑清晰的题目元信息与操作 -->
    <header class="statement-header">
      <div class="header-meta">
        <span class="pname-text font-mono">{{ pname || "PROB" }}</span>
        <span v-if="chapName" class="meta-tag">第 {{ chapName }} 章</span>
        <span v-if="passed" class="pass-tag">
          <span class="pass-dot" />
          <span>已通过</span>
        </span>

        <div class="spacer" />

        <button
          type="button"
          class="btn-icon action-btn"
          :class="{ copied: copiedKey === 'all' }"
          :disabled="!doc"
          title="复制题面"
          aria-label="复制题面"
          @click="copyAllDoc"
        >
          <Check v-if="copiedKey === 'all'" class="icon" :size="13" :stroke-width="1.8" />
          <Copy v-else class="icon" :size="13" :stroke-width="1.5" />
        </button>
      </div>

      <h1 v-if="displayTitle" class="statement-title">
        {{ displayTitle }}
      </h1>
    </header>

    <!-- 正文内容区 -->
    <main class="statement-body">
      <!-- 0. 加载占位骨架 -->
      <div v-if="loading && !doc" class="doc-skeleton" aria-busy="true" aria-label="载入题面">
        <div class="skeleton-line w-85" />
        <div class="skeleton-line w-100" />
        <div class="skeleton-line w-65" />
        <div class="skeleton-card" />
        <div class="skeleton-line w-90" />
        <div class="skeleton-line w-70" />
      </div>

      <!-- 1. 空描述提示 -->
      <div v-else-if="!doc" class="empty-doc">
        <p class="empty-doc-title">暂无题面描述</p>
        <p class="empty-doc-sub">可直接查看右侧代码模板并编写作答</p>
      </div>

      <!-- 2. 富文本原生 HTML 兼容 -->
      <article
        v-else-if="isRawHtml"
        class="doc-article"
        v-html="cleanedHtml"
      />

      <!-- 3. AnyView 纯文本语义渲染流 -->
      <div v-else class="doc-blocks">
        <template v-for="(block, idx) in renderedBlocks" :key="idx">
          <!-- 目标接口要求（最核心函数原型） -->
          <section v-if="block.type === 'function'" class="code-card target-func">
            <div class="code-card-header">
              <span class="code-card-title">{{ block.title || "要求实现下列函数" }}</span>
              <button
                type="button"
                class="copy-btn"
                :class="{ copied: copiedKey === idx }"
                title="复制函数原型"
                aria-label="复制函数原型"
                @click="copySnippet(block.content, idx, '函数原型已复制')"
              >
                <Check v-if="copiedKey === idx" class="icon" :size="12" :stroke-width="1.8" />
                <Copy v-else class="icon" :size="12" :stroke-width="1.5" />
              </button>
            </div>
            <div class="code-view" v-html="block.html" />
          </section>

          <!-- 数据结构与类型定义 -->
          <section v-else-if="block.type === 'struct'" class="code-card struct-def">
            <div class="code-card-header">
              <span class="code-card-title">{{ block.title || "数据结构定义" }}</span>
              <button
                type="button"
                class="copy-btn"
                :class="{ copied: copiedKey === idx }"
                title="复制代码"
                aria-label="复制代码"
                @click="copySnippet(block.content, idx)"
              >
                <Check v-if="copiedKey === idx" class="icon" :size="12" :stroke-width="1.8" />
                <Copy v-else class="icon" :size="12" :stroke-width="1.5" />
              </button>
            </div>
            <div class="code-view" v-html="block.html" />
          </section>

          <!-- 例题完整代码 -->
          <section v-else-if="block.type === 'program'" class="code-card full-program">
            <div class="code-card-header">
              <span class="code-card-title">{{ block.title || "完整参考代码" }}</span>
              <button
                type="button"
                class="copy-btn"
                :class="{ copied: copiedKey === idx }"
                title="复制代码"
                aria-label="复制代码"
                @click="copySnippet(block.content, idx)"
              >
                <Check v-if="copiedKey === idx" class="icon" :size="12" :stroke-width="1.8" />
                <Copy v-else class="icon" :size="12" :stroke-width="1.5" />
              </button>
            </div>
            <div class="code-view" v-html="block.html" />
          </section>

          <!-- 注意事项与提示 -->
          <aside v-else-if="block.type === 'note'" class="callout-note">
            <div class="callout-title">{{ block.title || "注意事项" }}</div>
            <div class="callout-content" v-html="block.html" />
          </aside>

          <!-- 示例说明与数据 -->
          <section v-else-if="block.type === 'example'" class="callout-example">
            <div class="example-header">
              <span class="example-title">{{ block.title || "示例" }}</span>
              <button
                type="button"
                class="copy-btn"
                :class="{ copied: copiedKey === idx }"
                title="复制示例"
                aria-label="复制示例"
                @click="copySnippet(block.content, idx, '示例已复制')"
              >
                <Check v-if="copiedKey === idx" class="icon" :size="12" :stroke-width="1.8" />
                <Copy v-else class="icon" :size="12" :stroke-width="1.5" />
              </button>
            </div>
            <pre class="example-body">{{ block.content }}</pre>
          </section>

          <!-- 普通题面叙述文本(渲染管线: 盘古 + 数学块 + inline 芯片) -->
          <div v-else class="doc-prose" v-html="block.html" />
        </template>
      </div>
    </main>
  </div>
</template>

<style scoped>
.statement-container {
  display: flex;
  flex-direction: column;
  height: 100%;
  overflow-y: auto;
  overflow-x: hidden;
  background: var(--bg-surface);
  border-right: 1px solid var(--border);
}

/* 顶部紧凑标题与元信息 */
.statement-header {
  padding: 14px 18px 12px;
  border-bottom: 1px solid var(--border-subtle);
  display: flex;
  flex-direction: column;
  gap: 8px;
  flex: none;
}

.header-meta {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 11.5px;
}

.pname-text {
  font-size: 11.5px;
  font-weight: 500;
  color: var(--text-secondary);
}

.meta-tag {
  color: var(--text-muted);
}

.pass-tag {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  color: var(--ok);
  font-weight: 500;
  font-size: 11.5px;
}

.pass-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--ok);
}

.action-btn {
  font-size: 11.5px;
  padding: 2px 7px;
}

.action-btn.copied,
.copy-btn.copied {
  color: var(--ok);
}

.statement-title {
  font-size: 19px;
  font-weight: 600;
  color: var(--text-primary);
  line-height: 1.4;
  margin: 2px 0 0;
  letter-spacing: -0.01em;
}

/* 正文区域 */
.statement-body {
  padding: 18px 20px 48px;
  flex: 1;
  min-width: 0;
}

.doc-blocks,
.doc-article {
  max-width: 72ch;
  min-width: 0;
  font-size: var(--statement-size, 14.5px);
  line-height: 1.75;
  color: var(--text-primary);
  overflow-wrap: break-word;
}

/* 正文叙述块: v-html 生成内容, :deep 穿透 */
.doc-prose :deep(.doc-para) {
  margin: 12px 0;
  white-space: pre-wrap;
  word-break: break-word;
  color: var(--text-primary);
}

/* inline 代码芯片 */
.doc-prose :deep(code.doc-code),
.callout-content :deep(code.doc-code) {
  font-family: var(--font-mono);
  font-size: 0.88em;
  background: var(--bg-canvas);
  border: 1px solid var(--border-subtle);
  border-radius: 5px;
  padding: 0 4px;
  word-break: break-all;
}

/* 数学块(等式行): 衬线斜体走真斜体面, font-synthesis:none 让
   CJK 尾部(如"当n=0")不合成假斜, 保持正体 */
.doc-prose :deep(.math-block) {
  font-family: "STIX Two Math", "Cambria Math", Georgia, "Times New Roman", serif;
  font-style: italic;
  font-synthesis: none;
  background: var(--bg-elevated);
  border-radius: var(--radius-control);
  padding: 10px 16px;
  margin: 14px 0;
  white-space: pre-wrap;
  line-height: 1.9;
  overflow-x: auto;
}

.doc-prose :deep(sup),
.doc-prose :deep(sub),
.callout-content :deep(sup),
.callout-content :deep(sub) {
  font-size: 0.72em;
  line-height: 0;
}

.doc-skeleton {
  max-width: 72ch;
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 6px 0;
}

.skeleton-line {
  height: 13px;
  border-radius: 4px;
  background: var(--bg-elevated);
}

.skeleton-line.w-100 { width: 100%; }
.skeleton-line.w-90 { width: 90%; }
.skeleton-line.w-85 { width: 85%; }
.skeleton-line.w-70 { width: 70%; }
.skeleton-line.w-65 { width: 65%; }

.skeleton-card {
  height: 88px;
  margin: 8px 0;
  border-radius: var(--radius-control);
  background: var(--bg-canvas);
  border: 1px solid var(--border-subtle);
}

@media (prefers-reduced-motion: no-preference) {
  .skeleton-line,
  .skeleton-card {
    animation: sk-pulse 1.5s ease-in-out infinite;
  }
}

@keyframes sk-pulse {
  0%, 100% { opacity: 0.7; }
  50% { opacity: 0.4; }
}

.empty-doc {
  color: var(--text-muted);
  font-size: 13px;
  line-height: 1.6;
  padding: 36px 12px;
  text-align: center;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.empty-doc-title {
  color: var(--text-secondary);
  font-weight: 500;
}

.empty-doc-sub {
  font-size: 12px;
  color: var(--text-muted);
}

/* 代码卡片通用风格（目标接口、数据结构、完整代码） */
.code-card {
  background: var(--bg-canvas);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-control);
  margin: 16px 0;
  overflow: hidden;
}

.code-card-header {
  padding: 6px 12px;
  border-bottom: 1px solid var(--border-subtle);
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.code-card-title {
  font-size: 11px;
  font-weight: 500;
  letter-spacing: 0.02em;
  color: var(--text-muted);
}

/* 目标接口卡是题面核心, 用提亮的中性色区分, 靛蓝留给交互(DESIGN.md 用色纪律) */
.target-func .code-card-title {
  color: var(--text-primary);
}

.copy-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  padding: 0;
  color: var(--text-muted);
  border-radius: 4px;
  transition: color 0.12s, background-color 0.12s;
}

.copy-btn:hover {
  color: var(--text-primary);
  background: var(--bg-elevated);
}

.code-view {
  margin: 0;
  padding: 14px 18px;
  /* 底色随主题: 配 vitesse-dark/light 的自家块底 */
  background: var(--code-block-bg);
  font-family: var(--font-mono);
  font-size: 13px;
  line-height: 1.65;
  overflow-x: auto;
  tab-size: 4;
  font-variant-numeric: tabular-nums;
  font-feature-settings: "calt" 1, "liga" 1, "tnum";
  font-synthesis: none;
}

/* shiki 内层 pre/code 复位: 背景与排版交给卡片 */
.code-view :deep(pre) {
  margin: 0;
  padding: 0;
  background: none;
}

.code-view :deep(pre:focus) {
  outline: none;
}

.code-view :deep(code) {
  display: block;
  font-family: inherit;
  font-size: inherit;
  background: none;
  padding: 0;
}

/* 注意事项 Callout —— 发丝线围合，无彩条无底色 */
.callout-note {
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-control);
  padding: 10px 14px;
  margin: 14px 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.callout-title {
  font-size: 12px;
  font-weight: 500;
  color: var(--text-secondary);
}

.callout-content {
  font-size: 13px;
  line-height: 1.65;
  color: var(--text-primary);
}

.callout-content :deep(.doc-para) {
  margin: 0;
  white-space: pre-wrap;
}

/* 示例说明 */
.callout-example {
  background: var(--bg-elevated);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-control);
  padding: 8px 12px 10px;
  margin: 14px 0;
}

.example-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 4px;
}

.example-title {
  font-size: 11.5px;
  font-weight: 500;
  color: var(--text-muted);
}

.example-body {
  margin: 0;
  font-family: var(--font-mono);
  font-size: 12.5px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
  overflow-x: auto;
  color: var(--text-primary);
}

/* 兼容原生 HTML 文章样式 */
.doc-article :deep(p) {
  margin: 12px 0;
}

.doc-article :deep(.doc-section-title),
.doc-article :deep(h1),
.doc-article :deep(h2),
.doc-article :deep(h3),
.doc-article :deep(h4) {
  margin: 20px 0 8px;
  font-weight: 600;
  line-height: 1.4;
  color: var(--text-primary);
}

.doc-article :deep(.doc-section-title) {
  font-size: 13px;
  color: var(--text-secondary);
}

.doc-article :deep(ul),
.doc-article :deep(ol) {
  padding-left: 1.5em;
  margin: 10px 0;
}

.doc-article :deep(li) {
  margin: 4px 0;
}

.doc-article :deep(a) {
  color: var(--accent);
  text-decoration: none;
}

.doc-article :deep(a:hover) {
  text-decoration: underline;
}

.doc-article :deep(blockquote) {
  border-left: 1px solid var(--border);
  padding-left: 12px;
  margin: 12px 0;
  color: var(--text-secondary);
}

.doc-article :deep(code) {
  font-family: var(--font-mono);
  font-size: 0.88em;
  background: var(--bg-canvas);
  border: 1px solid var(--border-subtle);
  border-radius: 5px;
  padding: 0 4px;
  word-break: break-all;
}

.doc-article :deep(pre) {
  background: var(--bg-canvas);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-control);
  padding: 12px 14px;
  margin: 12px 0;
  font-family: var(--font-mono);
  font-size: 13px;
  line-height: 1.65;
  max-width: 100%;
  overflow-x: auto;
}

.doc-article :deep(pre code) {
  background: none;
  border: none;
  border-radius: 0;
  padding: 0;
  font-size: inherit;
  word-break: normal;
}

.doc-article :deep(img) {
  max-width: 100%;
  height: auto;
  display: block;
  margin: 12px 0;
  border-radius: var(--radius-control);
}

.doc-article :deep(table) {
  border-collapse: collapse;
  display: block;
  width: max-content;
  min-width: 100%;
  max-width: 100%;
  overflow-x: auto;
  margin: 16px 0;
  font-size: 13px;
}

.doc-article :deep(th),
.doc-article :deep(td) {
  border-bottom: 1px solid var(--border-subtle);
  padding: 8px 12px;
  text-align: left;
}

.doc-article :deep(th) {
  font-weight: 600;
  color: var(--text-muted);
  font-size: 12px;
}

@media (max-width: 900px) {
  .statement-header {
    padding: 12px 14px;
  }
  .statement-body {
    padding: 14px 14px 40px;
  }
}

@media (pointer: coarse) {
  .copy-btn {
    width: 32px;
    height: 32px;
  }
  .action-btn {
    min-height: 32px;
    padding: 4px 10px;
  }
}
</style>
