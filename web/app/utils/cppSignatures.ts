// 函数签名提示(IDE 式 signature help): 光标落在已知函数的实参括号内时,
// 在函数名上方浮出签名, 逗号/嵌套深度感知地高亮当前参数。无 LSP, 纯静态表。
// 关闭时机: 光标越过配对右括号/函数不在表内/落在字符串或注释里/Esc。
import { Prec, StateEffect, StateField, type EditorState, type Extension } from "@codemirror/state";
import { EditorView, keymap, showTooltip, type Tooltip } from "@codemirror/view";
import { syntaxTree } from "@codemirror/language";

// ===== 签名表(与 cppCompletions 目录对齐, 常用 C 标准库 + C++ 模板) =====

const SIGNATURES: Record<string, string[]> = {
  printf: ["int printf(const char *format, ...)"],
  sprintf: ["int sprintf(char *str, const char *format, ...)"],
  snprintf: ["int snprintf(char *str, size_t size, const char *format, ...)"],
  fprintf: ["int fprintf(FILE *stream, const char *format, ...)"],
  fscanf: ["int fscanf(FILE *stream, const char *format, ...)"],
  scanf: ["int scanf(const char *format, ...)"],
  sscanf: ["int sscanf(const char *str, const char *format, ...)"],
  puts: ["int puts(const char *s)"],
  putchar: ["int putchar(int c)"],
  getchar: ["int getchar(void)"],
  fopen: ["FILE *fopen(const char *path, const char *mode)"],
  fclose: ["int fclose(FILE *fp)"],
  fread: ["size_t fread(void *ptr, size_t size, size_t nmemb, FILE *stream)"],
  fwrite: ["size_t fwrite(const void *ptr, size_t size, size_t nmemb, FILE *stream)"],
  malloc: ["void *malloc(size_t size)"],
  calloc: ["void *calloc(size_t nmemb, size_t size)"],
  realloc: ["void *realloc(void *ptr, size_t size)"],
  free: ["void free(void *ptr)"],
  memcpy: ["void *memcpy(void *dest, const void *src, size_t n)"],
  memmove: ["void *memmove(void *dest, const void *src, size_t n)"],
  memset: ["void *memset(void *s, int c, size_t n)"],
  memcmp: ["int memcmp(const void *s1, const void *s2, size_t n)"],
  strlen: ["size_t strlen(const char *s)"],
  strcpy: ["char *strcpy(char *dest, const char *src)"],
  strncpy: ["char *strncpy(char *dest, const char *src, size_t n)"],
  strcmp: ["int strcmp(const char *s1, const char *s2)"],
  strncmp: ["int strncmp(const char *s1, const char *s2, size_t n)"],
  strcat: ["char *strcat(char *dest, const char *src)"],
  qsort: ["void qsort(void *base, size_t nmemb, size_t size, int (*compar)(const void *, const void *))"],
  abs: ["int abs(int x)"],
  atoi: ["int atoi(const char *nptr)"],
  atof: ["double atof(const char *nptr)"],
  exit: ["void exit(int status)"],
  sort: ["void std::sort(RandomIt first, RandomIt last)", "void std::sort(RandomIt first, RandomIt last, Compare comp)"],
  swap: ["void std::swap(T &a, T &b)"],
  min: ["const T &std::min(const T &a, const T &b)"],
  max: ["const T &std::max(const T &a, const T &b)"],
};

// ===== 正向扫描定位光标所在的调用帧: 字符串/括号栈一次遍历无歧义 =====

interface CallSite {
  open: number;      // "(" 位置
  name: string;
  nameStart: number; // 名字首字符位置(作 tooltip 锚点)
  commas: number;    // 该层已敲过的顶层逗号数 = 当前参数序号
}

function isIdentCode(c: number): boolean {
  return (
    (c >= 65 && c <= 90) || // A-Z
    (c >= 97 && c <= 122) || // a-z
    (c >= 48 && c <= 57) || // 0-9
    c === 95 // _
  );
}

// 整段单次 sliceString 后在原生字符串上扫描, 避免每字符穿透 CodeMirror B-tree
function scanCall(doc: { length: number; sliceString: (a: number, b: number) => string }, pos: number, maxScan = 20000): CallSite | null {
  const start = Math.max(0, pos - maxScan);
  const text = doc.sliceString(start, pos);
  if (!text.includes("(")) return null;

  const stack: CallSite[] = [];
  let inStr: string | null = null;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (inStr) {
      if (ch === "\\") {
        i++;
        continue;
      }
      if (ch === inStr) inStr = null;
      continue;
    }
    if (ch === '"' || ch === "'") {
      inStr = ch;
      continue;
    }
    if (ch === "(") {
      let j = i;
      while (j > 0 && isIdentCode(text.charCodeAt(j - 1))) j--;
      const name = text.slice(j, i);
      // 未知函数也照常入栈: 深度守恒才能找对更外层的已知调用
      stack.push({ open: start + i, name, nameStart: start + j, commas: 0 });
    } else if (ch === ")") {
      stack.pop();
    } else if (ch === ",") {
      const top = stack[stack.length - 1];
      if (top) top.commas++;
    }
  }
  return stack[stack.length - 1] ?? null;
}

// 从 open 向前找配对右括号: 光标越过它就不再提示
function matchingClose(doc: { length: number; sliceString: (a: number, b: number) => string }, open: number, maxFwd = 800): number {
  const end = Math.min(doc.length, open + maxFwd);
  const text = doc.sliceString(open, end);
  let depth = 0;
  let inStr: string | null = null;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (inStr) {
      if (ch === "\\") {
        i++;
        continue;
      }
      if (ch === inStr) inStr = null;
      continue;
    }
    if (ch === '"' || ch === "'") {
      inStr = ch;
      continue;
    }
    if (ch === "(") depth++;
    else if (ch === ")") {
      depth--;
      if (depth === 0) return open + i;
    }
  }
  return -1;
}

// 顶层逗号切参(函数指针形参里的括号/逗号不切)
function splitParams(s: string): string[] {
  const out: string[] = [];
  let depth = 0;
  let cur = "";
  for (const ch of s) {
    if (ch === "(" || ch === "<") depth++;
    else if (ch === ")" || ch === ">") depth--;
    if (ch === "," && depth === 0) {
      out.push(cur);
      cur = "";
    } else {
      cur += ch;
    }
  }
  if (cur.trim()) out.push(cur);
  return out;
}

// ===== 渲染 =====

function buildSigDom(name: string, sigs: string[], active: number): HTMLElement {
  const dom = document.createElement("div");
  dom.className = "cm-sig-help";
  for (const sig of sigs) {
    const line = document.createElement("div");
    line.className = "cm-sig-line";
    const open = sig.indexOf("(");
    const close = sig.lastIndexOf(")");
    const head = sig.slice(0, open);
    const params = sig.slice(open + 1, close);
    const ret = document.createElement("span");
    ret.className = "sig-ret";
    ret.textContent = head.slice(0, Math.max(0, head.lastIndexOf(name)));
    const nm = document.createElement("span");
    nm.className = "sig-name";
    nm.textContent = name;
    line.append(ret, nm, document.createTextNode("("));
    const pList = params.trim() ? splitParams(params) : [];
    if (pList.length === 0 && params.trim()) {
      line.appendChild(document.createTextNode(params));
    }
    pList.forEach((p, i) => {
      const sp = document.createElement("span");
      sp.className = i === Math.min(active, pList.length - 1) ? "sig-p active" : "sig-p";
      sp.textContent = p.trim();
      line.appendChild(sp);
      if (i < pList.length - 1) line.appendChild(document.createTextNode(", "));
    });
    line.appendChild(document.createTextNode(")"));
    dom.appendChild(line);
  }
  return dom;
}

// ===== CodeMirror 接线 =====

const setSigHelp = StateEffect.define<Tooltip | null>();

interface SigTooltip extends Tooltip {
  sigKey: string;
}

export const sigHelpField = StateField.define<SigTooltip | null>({
  create: () => null,
  update(value, tr) {
    for (const e of tr.effects) if (e.is(setSigHelp)) return e.value as SigTooltip | null;
    return value;
  },
  provide: (f) => showTooltip.from(f, (v) => v),
});

function computeSig(state: EditorState): SigTooltip | null {
  const pos = state.selection.main.head;
  if (!state.selection.main.empty) return null;
  const node = syntaxTree(state as any).resolveInner(pos, -1);
  if (node && /String|Comment|Character|Literal/.test(node.type.name)) return null;
  const call = scanCall(state.doc, pos);
  if (!call) return null;
  const sigs = SIGNATURES[call.name];
  if (!sigs) return null;
  const close = matchingClose(state.doc, call.open);
  if (close !== -1 && pos > close) return null;
  const active = call.commas;
  const key = `${call.name}@${call.nameStart}#${active}`;
  const tooltip: SigTooltip = {
    pos: call.nameStart,
    above: true,
    sigKey: key,
    create: () => ({ dom: buildSigDom(call.name, sigs, active) }),
  };
  return tooltip;
}

export function signatureHelp(): Extension {
  return [
    sigHelpField,
    Prec.high(
      keymap.of([
        {
          key: "Escape",
          run: (v) => {
            if (!v.state.field(sigHelpField, false)) return false;
            v.dispatch({ effects: setSigHelp.of(null) });
            return true;
          },
        },
      ]),
    ),
    EditorView.updateListener.of((u) => {
      if (!u.docChanged && !u.selectionSet) return;
      const cur = u.state.field(sigHelpField, false);
      const res = computeSig(u.state);
      const curKey = cur ? cur.sigKey : null;
      const nextKey = res ? res.sigKey : null;
      if (curKey !== nextKey) {
        u.view.dispatch({ effects: setSigHelp.of(res) });
      }
    }),
  ];
}
