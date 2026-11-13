import {
  completeAnyWord,
  ifNotIn,
  snippetCompletion,
  type Completion,
  type CompletionContext,
  type CompletionSource,
} from "@codemirror/autocomplete";

function kw(label: string, boost = 0): Completion {
  return { label, type: "keyword", boost };
}

function ty(label: string): Completion {
  return { label, type: "type" };
}

function fn(label: string, detail?: string): Completion {
  return { label, type: "function", detail };
}

const catalog: Completion[] = [
  snippetCompletion("for (int ${i} = 0; ${i} < ${n}; ${i}++) {\n\t${}\n}", {
    label: "for",
    type: "keyword",
    detail: "for (int i = 0; …)",
    boost: 5,
  }),
  snippetCompletion("if (${}) {\n\t${}\n}", {
    label: "if",
    type: "keyword",
    detail: "if (…) { }",
    boost: 4,
  }),
  snippetCompletion("while (${}) {\n\t${}\n}", {
    label: "while",
    type: "keyword",
    detail: "while (…) { }",
    boost: 3,
  }),
  snippetCompletion("else {\n\t${}\n}", { label: "else", type: "keyword", detail: "else { }" }),
  snippetCompletion("switch (${}) {\n\tcase ${}:\n\t\t${}\n\t\tbreak;\n\tdefault:\n\t\t${}\n}", {
    label: "switch",
    type: "keyword",
    detail: "switch",
  }),
  snippetCompletion("struct ${Name} {\n\t${}\n};", {
    label: "struct",
    type: "keyword",
    detail: "struct { }",
  }),
  snippetCompletion("#include <${stdio.h}>", {
    label: "#include",
    type: "keyword",
    detail: "include header",
    boost: 6,
  }),
  snippetCompletion("printf(\"${%d}\\n\", ${});", {
    label: "printf",
    type: "function",
    detail: "printf",
    boost: 4,
  }),
  snippetCompletion("scanf(\"${%d}\", &${});", {
    label: "scanf",
    type: "function",
    detail: "scanf",
    boost: 4,
  }),
  snippetCompletion("int main() {\n\t${}\n\treturn 0;\n}", {
    label: "main",
    type: "function",
    detail: "int main()",
  }),

  ...[
    "auto", "break", "case", "const", "continue", "default", "do",
    "enum", "extern", "goto", "inline", "register", "restrict", "return",
    "sizeof", "static", "typedef", "union", "volatile", "namespace",
    "using", "class", "public", "private", "protected", "template",
    "typename", "this", "new", "delete", "try", "catch", "throw",
  ].map((l) => kw(l)),

  ...[
    "void", "int", "char", "float", "double", "short", "long",
    "unsigned", "signed", "bool", "size_t", "FILE",
    "ElemType", "Status", "LNode", "LinkList", "BiTNode", "BiTree",
    "SqStack", "Stack", "Queue", "Position",
  ].map(ty),

  ...[
    "true", "false", "NULL", "nullptr", "OK", "ERROR", "OVERFLOW", "TRUE", "FALSE",
  ].map((l) => ({ label: l, type: "constant" as const })),

  fn("sprintf"), fn("sscanf"), fn("snprintf"),
  fn("puts"), fn("putchar"), fn("getchar"), fn("gets"),
  fn("malloc"), fn("calloc"), fn("realloc"), fn("free"),
  fn("memcpy"), fn("memmove"), fn("memset"), fn("memcmp"),
  fn("strlen"), fn("strcpy"), fn("strncpy"), fn("strcmp"), fn("strncmp"), fn("strcat"),
  fn("fopen"), fn("fclose"), fn("fread"), fn("fwrite"), fn("fprintf"), fn("fscanf"),
  fn("qsort"), fn("abs"), fn("atoi"), fn("atof"), fn("exit"),
  fn("cin"), fn("cout"), fn("cerr"), fn("endl"),
  fn("sort"), fn("swap"), fn("min"), fn("max"),
  ty("vector"), ty("string"), ty("pair"), ty("map"), ty("set"),
  ty("queue"), ty("stack"), ty("priority_queue"),
  { label: "std", type: "namespace" },
  { label: "include", type: "keyword" },
];

function fromCatalog(ctx: CompletionContext) {
  const word = ctx.matchBefore(/[#A-Za-z_]\w*/);
  if (!word && !ctx.explicit) return null;
  if (word && word.from === word.to && !ctx.explicit) return null;
  return {
    from: word ? word.from : ctx.pos,
    options: catalog,
    validFor: /^[#A-Za-z_]\w*$/,
  };
}

const skipStrings = ifNotIn(
  ["LineComment", "BlockComment", "String", "Character", "CharLiteral"],
  fromCatalog,
);

export const cppCompletionSources: CompletionSource[] = [skipStrings, completeAnyWord];
