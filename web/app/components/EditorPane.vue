<script setup lang="ts">
import { Annotation, Compartment, EditorState, Prec, StateEffect, StateField, Transaction } from "@codemirror/state";
import {
  Decoration,
  EditorView,
  gutter,
  GutterMarker,
  keymap,
  lineNumbers,
  highlightActiveLineGutter,
  highlightSpecialChars,
  drawSelection,
  dropCursor,
  rectangularSelection,
  crosshairCursor,
  highlightActiveLine,
  type DecorationSet,
} from "@codemirror/view";
import { cpp } from "@codemirror/lang-cpp";
import {
  HighlightStyle,
  syntaxHighlighting,
  defaultHighlightStyle,
  codeFolding,
  foldGutter,
  indentOnInput,
  bracketMatching,
  foldKeymap,
  indentUnit,
} from "@codemirror/language";
import { tags as t } from "@lezer/highlight";
import { setDiagnostics, lintKeymap, type Diagnostic } from "@codemirror/lint";
import {
  autocompletion,
  closeBrackets,
  closeBracketsKeymap,
  completionKeymap,
  startCompletion,
} from "@codemirror/autocomplete";
import { history, defaultKeymap, historyKeymap, indentWithTab } from "@codemirror/commands";
import { highlightSelectionMatches, searchKeymap } from "@codemirror/search";
import type { Diag } from "~/composables/useApi";
import { cppCompletionSources } from "~/utils/cppCompletions";
import { signatureHelp } from "~/utils/cppSignatures";

const props = withDefaults(
  defineProps<{
    modelValue: string;
    diagnostics: Diag[];
    isDark?: boolean;
    fontSize?: number;
    tabSize?: number;
    lineWrapping?: boolean;
    breakpoints?: number[];
    debugLine?: number;
    readonly?: boolean;
    runShortcut?: () => void;
  }>(),
  {
    isDark: true,
    fontSize: 15,
    tabSize: 4,
    lineWrapping: false,
    breakpoints: () => [],
    debugLine: 0,
    readonly: false,
  },
);

const emit = defineEmits<{
  (e: "update:modelValue", v: string): void;
  (e: "save"): void;
  (e: "toggle-breakpoint", line: number): void;
}>();

const host = ref<HTMLElement | null>(null);
let view: EditorView | null = null;

// Compartments: 支持主题、高亮、Tab缩进、自动折行的动态热插拔
const themeCompartment = new Compartment();
const highlightCompartment = new Compartment();
const indentCompartment = new Compartment();
const lineWrappingCompartment = new Compartment();
const readonlyCompartment = new Compartment();
// history 用 compartment 承载: 切题时 reconfigure 新实例即重置撤销栈,
// Ctrl+Z 不会回滚出上一题的全文(否则经 updateListener 还会把旧内容写回草稿)
const historyCompartment = new Compartment();
// 父组件注入的内容替换(切题/重置模板): 不进撤销栈、不回声 update:modelValue
const externalChange = Annotation.define<boolean>();

function indentExt(size: number) {
  return [EditorState.tabSize.of(size), indentUnit.of(" ".repeat(Math.max(1, size)))];
}

// ===== 断点与调试行高亮(状态在 Vue 侧, 编辑器只渲染) =====

const setBpEffect = StateEffect.define<number[]>();
const bpField = StateField.define<number[]>({
  create: () => [],
  update(v, tr) {
    for (const e of tr.effects) if (e.is(setBpEffect)) return e.value;
    return v;
  },
});

class BpMarker extends GutterMarker {
  override toDOM() {
    const el = document.createElement("div");
    el.className = "bp-dot";
    el.title = "断点 (点击移除)";
    return el;
  }
}
const bpMarker = new BpMarker();

class CurrentDbgMarker extends GutterMarker {
  override toDOM() {
    const el = document.createElement("div");
    el.className = "dbg-arrow-marker";
    el.title = "当前停行位置";
    el.innerHTML = '<svg width="10" height="10" viewBox="0 0 16 16" fill="currentColor"><path d="M4 2.8c0-.8.87-1.29 1.55-.87l8.16 5.2a1.03 1.03 0 0 1 0 1.74l-8.16 5.2A1.03 1.03 0 0 1 4 13.2V2.8Z"/></svg>';
    return el;
  }
}
const currentDbgMarker = new CurrentDbgMarker();

class BpAndDbgMarker extends GutterMarker {
  override toDOM() {
    const el = document.createElement("div");
    el.className = "bp-dot current-dbg";
    el.title = "断点，当前停行";
    el.innerHTML = '<svg width="10" height="10" viewBox="0 0 16 16" fill="currentColor"><path d="M4 2.8c0-.8.87-1.29 1.55-.87l8.16 5.2a1.03 1.03 0 0 1 0 1.74l-8.16 5.2A1.03 1.03 0 0 1 4 13.2V2.8Z"/></svg>';
    return el;
  }
}
const bpAndDbgMarker = new BpAndDbgMarker();

const setDebugLineEffect = StateEffect.define<number>();
const currentDebugLineField = StateField.define<number>({
  create: () => 0,
  update(v, tr) {
    for (const e of tr.effects) if (e.is(setDebugLineEffect)) return e.value;
    return v;
  },
});

const debugLineField = StateField.define<DecorationSet>({
  create: () => Decoration.none,
  update(deco, tr) {
    deco = deco.map(tr.changes);
    for (const e of tr.effects) {
      if (e.is(setDebugLineEffect)) {
        if (e.value >= 1 && e.value <= tr.state.doc.lines) {
          const info = tr.state.doc.line(e.value);
          deco = Decoration.set([Decoration.line({ class: "cm-debug-line" }).range(info.from)]);
        } else {
          deco = Decoration.none;
        }
      }
    }
    return deco;
  },
  provide: (f) => EditorView.decorations.from(f),
});

// 断点 gutter 的点击回调在 onMounted 里闭包注入(编辑器实例唯一)
let onToggleBp: ((line: number) => void) | null = null;
const bpLineNo = (v: EditorView, line: { from: number }) => v.state.doc.lineAt(line.from).number;
const bpGutter = gutter({
  class: "cm-bp-gutter",
  lineMarker: (v, line) => {
    const ln = bpLineNo(v, line);
    const hasBp = v.state.field(bpField).includes(ln);
    const isDbg = v.state.field(currentDebugLineField) === ln;
    if (hasBp && isDbg) return bpAndDbgMarker;
    if (hasBp) return bpMarker;
    if (isDbg) return currentDbgMarker;
    return null;
  },
  lineMarkerChange: (u) =>
    u.startState.field(bpField) !== u.state.field(bpField) ||
    u.startState.field(currentDebugLineField) !== u.state.field(currentDebugLineField),
  domEventHandlers: {
    mousedown(view, line, e) {
      if ((e as MouseEvent).button !== 0) return false;
      e.preventDefault();
      onToggleBp?.(bpLineNo(view, line));
      return true;
    },
    touchstart(view, line, e) {
      if (e.cancelable) e.preventDefault();
      onToggleBp?.(bpLineNo(view, line));
      return true;
    },
  },
});

// 暗色主题高亮
const darkHighlightStyle = HighlightStyle.define([
  { tag: [t.keyword, t.controlKeyword, t.definitionKeyword], color: "#ff7b72" },
  { tag: [t.typeName, t.standard(t.typeName), t.modifier], color: "#79c0ff" },
  { tag: [t.function(t.variableName), t.function(t.definition(t.variableName))], color: "#d2a8ff" },
  { tag: [t.string, t.character], color: "#7ee787" },
  { tag: [t.number, t.bool, t.null], color: "#ffa657" },
  { tag: [t.comment, t.lineComment, t.blockComment], color: "#768390", fontStyle: "italic" },
  { tag: [t.macroName, t.processingInstruction], color: "#ff7b72" },
  { tag: t.escape, color: "#79c0ff" },
  { tag: t.operator, color: "#e6edf3" },
  { tag: [t.punctuation, t.bracket, t.paren, t.brace], color: "#8b949e" },
  { tag: t.variableName, color: "#e6edf3" },
]);

// 浅色主题高亮
const lightHighlightStyle = HighlightStyle.define([
  { tag: [t.keyword, t.controlKeyword, t.definitionKeyword], color: "#cf222e" },
  { tag: [t.typeName, t.standard(t.typeName), t.modifier], color: "#0550ae" },
  { tag: [t.function(t.variableName), t.function(t.definition(t.variableName))], color: "#8250df" },
  { tag: [t.string, t.character], color: "#116329" },
  { tag: [t.number, t.bool, t.null], color: "#953800" },
  { tag: [t.comment, t.lineComment, t.blockComment], color: "#6e7781", fontStyle: "italic" },
  { tag: [t.macroName, t.processingInstruction], color: "#cf222e" },
  { tag: t.escape, color: "#0550ae" },
  { tag: t.operator, color: "#24292f" },
  { tag: [t.punctuation, t.bracket, t.paren, t.brace], color: "#57606a" },
  { tag: t.variableName, color: "#24292f" },
]);

function buildEditorTheme(dark: boolean) {
  const fg = dark ? "#e6edf3" : "#24292f";
  const gutterFg = dark ? "#484f58" : "#afb8c1";
  const activeGutterFg = dark ? "#8b949e" : "#57606a";
  const activeLineBg = dark ? "rgba(255, 255, 255, 0.025)" : "rgba(0, 0, 0, 0.025)";
  const matchBg = dark ? "rgba(255, 255, 255, 0.12)" : "rgba(9, 105, 218, 0.12)";
  const matchFg = dark ? "#ffffff !important" : "#24292f !important";
  const foldGutterFg = dark ? "var(--text-faint)" : "var(--text-muted)";

  return EditorView.theme({
    "&": {
      height: "100%",
      backgroundColor: "var(--bg-canvas)",
      color: fg,
      fontFamily: "var(--font-mono)",
      fontSize: "var(--editor-font-size, 15px)",
    },
    ".cm-content": {
      padding: "12px 0",
      caretColor: fg,
      lineHeight: "1.65",
      fontFamily: "var(--font-mono)",
      fontFeatureSettings: '"calt" 1, "liga" 1',
      fontVariantLigatures: "contextual",
    },
    ".cm-gutters": {
      backgroundColor: "var(--bg-canvas)",
      color: gutterFg,
      borderRight: "1px solid var(--border-subtle)",
      paddingRight: "8px",
      paddingLeft: "4px",
      userSelect: "none",
      fontVariantNumeric: "tabular-nums",
      fontFeatureSettings: '"tnum"',
      fontWeight: "400",
      fontSynthesis: "none",
    },
    ".cm-lineNumbers .cm-gutterElement": {
      fontWeight: "400",
      fontVariantNumeric: "tabular-nums",
    },
    ".cm-activeLineGutter": {
      backgroundColor: "transparent",
      color: activeGutterFg,
      fontWeight: "400",
    },
    ".cm-activeLine": {
      backgroundColor: activeLineBg,
    },
    ".cm-selectionBackground, &.cm-focused .cm-selectionBackground": {
      backgroundColor: "var(--accent-dim) !important",
    },
    ".cm-cursor": {
      borderLeftColor: fg,
      borderLeftWidth: "2px",
    },
    ".cm-matchingBracket": {
      backgroundColor: matchBg,
      color: matchFg,
      outline: "none",
    },
    ".cm-tooltip": {
      backgroundColor: "var(--bg-surface)",
      border: "1px solid var(--border)",
      color: "var(--text-primary)",
      fontSize: "var(--editor-font-size, 15px)",
      lineHeight: "1.65",
      fontFamily: "var(--font-mono)",
      boxShadow: "var(--shadow-overlay)",
    },
    ".cm-tooltip-autocomplete ul": {
      backgroundColor: "var(--bg-surface)",
      fontSize: "inherit",
      lineHeight: "inherit",
      maxHeight: "min(22em, 60vh)",
    },
    ".cm-tooltip-autocomplete ul li": {
      color: "var(--text-secondary)",
      lineHeight: "1.65",
      padding: "0.2em 0.7em",
    },
    ".cm-tooltip-autocomplete ul li[aria-selected]": {
      backgroundColor: "var(--bg-elevated) !important",
      color: "var(--text-primary) !important",
    },
    ".cm-completionMatchedText": {
      color: "var(--accent)",
      textDecoration: "none",
      fontWeight: "500",
    },
    ".cm-completionDetail": {
      color: "var(--text-muted)",
      fontStyle: "normal",
    },
    ".cm-sig-help": {
      fontFamily: "var(--font-mono)",
      fontSize: "12px",
      lineHeight: 1.7,
      padding: "6px 10px",
      whiteSpace: "pre",
    },
    ".cm-sig-help .sig-ret": {
      color: "var(--text-muted)",
    },
    ".cm-sig-help .sig-name": {
      color: "var(--text-primary)",
      fontWeight: 600,
    },
    ".cm-sig-help .sig-p": {
      color: "var(--text-secondary)",
    },
    ".cm-sig-help .sig-p.active": {
      color: "var(--accent)",
      fontWeight: 600,
    },
    ".cm-foldPlaceholder": {
      backgroundColor: "var(--bg-elevated)",
      border: "1px solid var(--border)",
      color: "var(--text-muted)",
      borderRadius: "4px",
      margin: "0 4px",
      padding: "0 6px",
      fontFamily: "var(--font-mono)",
      fontSize: "0.85em",
    },
    ".cm-foldGutter": {
      width: "14px",
    },
    ".cm-foldGutter .cm-gutterElement": {
      color: foldGutterFg,
      cursor: "pointer",
    },
    ".cm-panels": {
      backgroundColor: "var(--bg-surface)",
      color: "var(--text-primary)",
      borderColor: "var(--border-subtle)",
    },
    ".cm-panels .cm-button, .cm-panels input, .cm-textfield": {
      backgroundColor: "var(--bg-canvas)",
      color: "var(--text-primary)",
      border: "1px solid var(--border)",
      borderRadius: "4px",
    },
    ".cm-panel.cm-search label": {
      color: "var(--text-secondary)",
    },
    ".cm-lintRange-error": {
      backgroundImage: "none",
      borderBottom: "1.5px solid var(--err)",
    },
    ".cm-lintRange-warning": {
      backgroundImage: "none",
      borderBottom: "1.5px solid var(--warn)",
    },
  }, { dark });
}

const darkEditorTheme = buildEditorTheme(true);
const lightEditorTheme = buildEditorTheme(false);

function applyDiags(diags: Diag[]) {
  if (!view) return;
  const doc = view.state.doc;
  const list: Diagnostic[] = diags.map((d) => {
    const ln = Math.min(Math.max(1, d.line), doc.lines);
    const info = doc.line(ln);
    const from = Math.min(info.from + Math.max(0, (d.col || 1) - 1), info.to);
    return {
      from,
      to: info.to,
      message: d.message,
      severity: d.severity,
    };
  });
  view.dispatch(setDiagnostics(view.state, list));
}

function jumpToLine(lineNum: number, colNum = 1) {
  if (!view) return;
  const doc = view.state.doc;
  const targetLine = Math.min(Math.max(1, lineNum), doc.lines);
  const lineInfo = doc.line(targetLine);
  const pos = Math.min(lineInfo.from + Math.max(0, colNum - 1), lineInfo.to);

  view.dispatch({
    selection: { anchor: pos, head: pos },
    scrollIntoView: true,
  });
  view.focus();
}

function scrollToLine(lineNum: number) {
  if (!view) return;
  const doc = view.state.doc;
  const targetLine = Math.min(Math.max(1, lineNum), doc.lines);
  const lineInfo = doc.line(targetLine);
  view.dispatch({
    effects: EditorView.scrollIntoView(lineInfo.from, { y: "center" }),
  });
}

function refreshMeasure() {
  view?.requestMeasure();
}

defineExpose({
  jumpToLine,
  refreshMeasure,
});

let imeEl: HTMLInputElement | null = null;
let imeTimer: ReturnType<typeof setTimeout> | null = null;

function readonlyExtensions(ro: boolean) {
  return ro
    ? [
        EditorState.readOnly.of(true),
        EditorView.editable.of(false),
        EditorView.contentAttributes.of({ "aria-readonly": "true" }),
      ]
    : [];
}

onMounted(() => {
  if (!host.value) return;

  const currentTheme = props.isDark ? darkEditorTheme : lightEditorTheme;
  const currentHighlight = props.isDark ? darkHighlightStyle : lightHighlightStyle;
  onToggleBp = (ln) => emit("toggle-breakpoint", ln);

  view = new EditorView({
    state: EditorState.create({
      doc: props.modelValue,
      extensions: [
        lineNumbers(),
        highlightActiveLineGutter(),
        highlightSpecialChars(),
        historyCompartment.of(history()),
        codeFolding({ placeholderText: "…" }),
        foldGutter(),
        drawSelection(),
        dropCursor(),
        EditorState.allowMultipleSelections.of(true),
        indentOnInput(),
        syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
        bracketMatching(),
        closeBrackets(),
        rectangularSelection(),
        crosshairCursor(),
        highlightActiveLine(),
        highlightSelectionMatches(),
        keymap.of([
          ...closeBracketsKeymap,
          ...defaultKeymap,
          ...searchKeymap,
          ...historyKeymap,
          ...foldKeymap,
          ...lintKeymap,
          indentWithTab,
        ]),
        Prec.highest(keymap.of([
          { key: "Ctrl-Shift-Space", run: startCompletion },
          ...completionKeymap.filter((b) => b.key !== "Ctrl-Space"),
        ])),
        cpp(),
        themeCompartment.of(currentTheme),
        highlightCompartment.of(syntaxHighlighting(currentHighlight)),
        indentCompartment.of(indentExt(props.tabSize)),
        lineWrappingCompartment.of(props.lineWrapping ? EditorView.lineWrapping : []),
        readonlyCompartment.of(readonlyExtensions(props.readonly)),
        bpField,
        bpGutter,
        debugLineField,
        currentDebugLineField,
        autocompletion({
          override: cppCompletionSources,
          activateOnTyping: true,
          activateOnTypingDelay: 200,
          icons: true,
          defaultKeymap: false,
        }),
        signatureHelp(),
        EditorView.updateListener.of((u) => {
          if (u.docChanged && !u.transactions.some((tr) => tr.annotation(externalChange))) {
            emit("update:modelValue", u.state.doc.toString());
          }
        }),
        EditorView.contentAttributes.of({
          lang: "en",
          inputmode: "text",
          autocorrect: "off",
          autocapitalize: "none",
          spellcheck: "false",
          translate: "no",
          "data-gramm": "false",
          "aria-label": "C/C++ 代码编辑器",
        }),
      ],
    }),
    parent: host.value,
  });

  if (props.diagnostics.length > 0) {
    applyDiags(props.diagnostics);
  }

  // IME 引导: 浏览器没有直接切系统输入法的 API, 但主流输入法(微软拼音/搜狗/
  // macOS 拼音等)在密码框会自动切英文。聚焦编辑器时先闪一下隐藏密码框再回焦,
  // 借这一行为实现"进编辑器自动切一次英文"; 不支持该行为的输入法下是无感空操作。
  // 1.5s 防抖: 回焦自身会再触发 focus, 防止循环。触屏虚拟键盘无 IME 可切, 跳过。
  let imeLastNudge = 0;
  view.contentDOM.addEventListener("focus", () => {
    if (props.readonly || matchMedia("(pointer: coarse)").matches) return;
    const now = Date.now();
    if (now - imeLastNudge < 1500) return;
    imeLastNudge = now;
    try {
      if (!imeEl) {
        imeEl = document.createElement("input");
        imeEl.type = "password";
        imeEl.tabIndex = -1;
        imeEl.setAttribute("aria-hidden", "true");
        imeEl.style.cssText =
          "position:fixed;top:0;left:0;width:1px;height:1px;opacity:0;pointer-events:none;border:0;padding:0;";
        document.body.appendChild(imeEl);
      }
      imeEl.value = "";
      imeEl.focus({ preventScroll: true });
      if (imeTimer) clearTimeout(imeTimer);
      imeTimer = setTimeout(() => {
        imeTimer = null;
        if (!view || !imeEl) return;
        imeEl.blur();
        view.focus();
      }, 0);
    } catch {}
  });

  if (import.meta.dev) {
    (window as any).__anview = view; // 调试句柄(dev only)
  }
  if (props.breakpoints.length) {
    view.dispatch({ effects: setBpEffect.of([...props.breakpoints]) });
  }
  if (props.debugLine) {
    view.dispatch({ effects: setDebugLineEffect.of(props.debugLine) });
    scrollToLine(props.debugLine);
  }
});

// 监听暗色/浅色模式切换
watch(
  () => props.isDark,
  (dark) => {
    if (!view) return;
    const newTheme = dark ? darkEditorTheme : lightEditorTheme;
    const newHighlight = dark ? darkHighlightStyle : lightHighlightStyle;

    view.dispatch({
      effects: [
        themeCompartment.reconfigure(newTheme),
        highlightCompartment.reconfigure(syntaxHighlighting(newHighlight)),
      ],
    });
  },
);

// 监听 Tab 缩进宽度变化
watch(
  () => props.tabSize,
  (size) => {
    view?.dispatch({
      effects: indentCompartment.reconfigure(indentExt(size)),
    });
  },
);

// 监听自动折行变化
watch(
  () => props.lineWrapping,
  (wrap) => {
    view?.dispatch({
      effects: lineWrappingCompartment.reconfigure(wrap ? EditorView.lineWrapping : []),
    });
  },
);

watch(
  () => props.modelValue,
  (newVal) => {
    if (!view) return;
    const currentVal = view.state.doc.toString();
    if (newVal !== currentVal) {
      view.dispatch({
        changes: { from: 0, to: currentVal.length, insert: newVal },
        annotations: [externalChange.of(true), Transaction.addToHistory.of(false)],
        effects: historyCompartment.reconfigure(history()),
      });
    }
  },
);

watch(() => props.diagnostics, (d) => applyDiags(d));

// 断点集合/调试行/只读锁 → 编辑器同步
watch(
  () => (props.breakpoints ?? []).join(","),
  () => {
    if (!view) return;
    const next = props.breakpoints ?? [];
    const cur = view.state.field(bpField);
    if (next.length === cur.length && next.every((v, i) => v === cur[i])) return;
    view.dispatch({ effects: setBpEffect.of([...next]) });
  },
);

watch(
  () => props.debugLine,
  (ln) => {
    if (!view) return;
    const lineNum = Number(ln) || 0;
    if (lineNum !== view.state.field(currentDebugLineField)) {
      view.dispatch({ effects: setDebugLineEffect.of(lineNum) });
    }
    if (lineNum > 0) {
      scrollToLine(lineNum);
    }
  },
);

watch(
  () => props.readonly,
  (ro) => {
    view?.dispatch({
      effects: readonlyCompartment.reconfigure(readonlyExtensions(ro)),
    });
  },
);

function handleKeyDown(e: KeyboardEvent) {
  // 只读(调试中)时运行/保存快捷键全部禁用, 与工具栏按钮的禁用态保持一致
  if (props.readonly) return;
  if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
    e.preventDefault();
    props.runShortcut?.();
  } else if ((e.ctrlKey || e.metaKey) && e.key === "s") {
    e.preventDefault();
    emit("save");
  }
}

onBeforeUnmount(() => {
  if (imeTimer) {
    clearTimeout(imeTimer);
    imeTimer = null;
  }
  if (imeEl) {
    imeEl.remove();
    imeEl = null;
  }
  onToggleBp = null;
  view?.destroy();
  view = null;
});
</script>

<template>
  <div
    class="editor-pane"
    :class="{ 'is-readonly': readonly, 'is-debugging': debugLine > 0 }"
    :style="{ '--editor-font-size': `${fontSize}px` }"
    @keydown="handleKeyDown"
  >
    <div ref="host" class="cm-host" />
  </div>
</template>

<style scoped>
.editor-pane {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  background: var(--bg-canvas);
  position: relative;
}

.cm-host {
  flex: 1;
  min-height: 0;
  height: 100%;
}

.cm-host :deep(.cm-editor) {
  height: 100%;
  font-family: var(--font-mono);
  font-size: var(--editor-font-size, 15px) !important;
}

.cm-host :deep(.cm-foldPlaceholder) {
  background: var(--bg-elevated) !important;
  border: 1px solid var(--border) !important;
  color: var(--text-muted) !important;
  border-radius: 4px;
  margin: 0 4px;
  padding: 0 6px;
}

.cm-host :deep(.cm-foldGutter .cm-gutterElement) {
  color: var(--text-faint);
}

.cm-host :deep(.cm-tooltip-autocomplete ul li[aria-selected]) {
  background: var(--bg-elevated) !important;
  color: var(--text-primary) !important;
}

.cm-host :deep(.cm-editor.cm-focused) {
  outline: none;
}

.cm-host :deep(.cm-gutters),
.cm-host :deep(.cm-lineNumbers),
.cm-host :deep(.cm-lineNumbers .cm-gutterElement),
.cm-host :deep(.cm-activeLineGutter) {
  font-weight: 400;
  font-variant-numeric: tabular-nums;
  font-feature-settings: "tnum";
  font-synthesis: none;
}

/* 断点与调试行 Gutter */
.cm-host :deep(.cm-bp-gutter) {
  width: 20px;
  cursor: pointer;
  background: var(--bg-canvas);
  user-select: none;
}

.editor-pane.is-readonly .cm-host :deep(.cm-bp-gutter) {
  cursor: default;
}

.editor-pane.is-debugging .cm-host :deep(.cm-activeLine) {
  background-color: transparent;
}

.cm-host :deep(.cm-bp-gutter .cm-gutterElement) {
  display: flex;
  align-items: center;
  justify-content: center;
  position: relative;
}

/* 现有断点圆点 */
.cm-host :deep(.bp-dot) {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--err);
  transition: opacity 0.12s ease;
  flex: none;
}

.editor-pane:not(.is-readonly) .cm-host :deep(.bp-dot:hover) {
  opacity: 0.78;
}

/* 悬停未设断点的行时，半透明预览圆点 (仅可编辑态) */
.editor-pane:not(.is-readonly) .cm-host :deep(.cm-bp-gutter .cm-gutterElement:hover:not(:has(.bp-dot)):not(:has(.dbg-arrow-marker))::before) {
  content: "";
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: color-mix(in srgb, var(--err) 45%, transparent);
}

/* 调试当前暂停行指示箭头 */
.cm-host :deep(.dbg-arrow-marker) {
  font-size: 9px;
  color: var(--warn);
  line-height: 1;
}

.cm-host :deep(.bp-dot.current-dbg) {
  width: 10px;
  height: 10px;
  background: var(--warn);
  color: var(--bg-canvas);
  display: flex;
  align-items: center;
  justify-content: center;
}

/* 当前调试执行行背景 (2px 琥珀内嵌线) */
.cm-host :deep(.cm-debug-line) {
  background: rgba(232, 163, 61, 0.12) !important;
  box-shadow: inset 2px 0 0 var(--warn);
}

:global(html[data-theme="light"] .cm-host .cm-debug-line) {
  background: rgba(232, 163, 61, 0.16) !important;
  box-shadow: inset 2px 0 0 var(--warn);
}

@media (pointer: coarse) {
  .cm-host :deep(.cm-bp-gutter) {
    width: 28px;
  }
  .cm-host :deep(.bp-dot) {
    width: 10px;
    height: 10px;
  }
}
</style>
