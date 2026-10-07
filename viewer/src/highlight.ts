import { useEffect, useMemo, useState } from "react";

import type { CodeDiffHunk, CodeDiffLine } from "./components/review-tiles/code-diff";

/** Syntax highlighting for code the viewer shows: diff hunks and Markdown code
 *  blocks. Everything shiki is reached through dynamic `import()`, so a host's
 *  bundler splits it, and each grammar, out of the initial bundle. The
 *  synchronous exports here (language detection, token mapping) never load it.
 *  Colours are CSS variables (`--syntax-*`, defined in `viewer.css`), so the
 *  tokens follow light and dark mode without being tokenised again. */

/** The languages the viewer highlights. Anything else renders as plain text. */
export const LANGS = [
  "bash",
  "c",
  "cpp",
  "csharp",
  "css",
  "dockerfile",
  "go",
  "graphql",
  "html",
  "java",
  "javascript",
  "json",
  "jsx",
  "kotlin",
  "makefile",
  "markdown",
  "php",
  "proto",
  "python",
  "ruby",
  "rust",
  "scss",
  "sql",
  "svelte",
  "swift",
  "toml",
  "tsx",
  "typescript",
  "vue",
  "yaml",
] as const;

export type Lang = (typeof LANGS)[number];

/** One run of text with a colour (a CSS value, normally `var(--syntax-…)`) and
 *  shiki's font style bit flags: 1 italic, 2 bold, 4 underline, 8 strikethrough. */
export type HighlightToken = { content: string; color?: string; fontStyle?: number };

export type HighlightedLine = HighlightToken[];

/** Past either limit a diff or code block is left plain: tokenising it would
 *  hold the main thread for noticeably long and nobody reads that much inline. */
export const MAX_HIGHLIGHT_CHARS = 200_000;
export const MAX_HIGHLIGHT_LINES = 3_000;

const EXTENSIONS: Record<string, Lang> = {
  ts: "typescript",
  mts: "typescript",
  cts: "typescript",
  tsx: "tsx",
  js: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  jsx: "jsx",
  json: "json",
  jsonc: "json",
  go: "go",
  py: "python",
  rs: "rust",
  java: "java",
  kt: "kotlin",
  kts: "kotlin",
  rb: "ruby",
  php: "php",
  c: "c",
  h: "c",
  cc: "cpp",
  cpp: "cpp",
  cxx: "cpp",
  hpp: "cpp",
  hh: "cpp",
  cs: "csharp",
  swift: "swift",
  sql: "sql",
  sh: "bash",
  bash: "bash",
  zsh: "bash",
  yaml: "yaml",
  yml: "yaml",
  toml: "toml",
  md: "markdown",
  markdown: "markdown",
  html: "html",
  htm: "html",
  css: "css",
  scss: "scss",
  proto: "proto",
  graphql: "graphql",
  gql: "graphql",
  vue: "vue",
  svelte: "svelte",
  dockerfile: "dockerfile",
  mk: "makefile",
};

const BASENAMES: Record<string, Lang> = {
  dockerfile: "dockerfile",
  makefile: "makefile",
  gnumakefile: "makefile",
};

const FENCE_ALIASES: Record<string, Lang> = {
  ...(Object.fromEntries(LANGS.map((lang) => [lang, lang])) as Record<string, Lang>),
  ...EXTENSIONS,
  golang: "go",
  shell: "bash",
  shellscript: "bash",
  console: "bash",
  python3: "python",
  py3: "python",
  "c++": "cpp",
  "c#": "csharp",
  node: "javascript",
  protobuf: "proto",
  docker: "dockerfile",
  make: "makefile",
};

/** The language for a file path, from its basename or extension, or null. */
export function languageForPath(path: string): Lang | null {
  const name = (path.split("/").pop() ?? "").toLowerCase();
  const byName = BASENAMES[name];
  if (byName) return byName;
  // `Dockerfile.dev`, `app.Dockerfile`
  if (name.startsWith("dockerfile.")) return "dockerfile";
  const dot = name.lastIndexOf(".");
  if (dot <= 0) return null;
  return EXTENSIONS[name.slice(dot + 1)] ?? null;
}

/** The language for a Markdown fence's info string (` ```ts title="x" `): its
 *  first word, case-insensitively, through the usual aliases, or null. */
export function languageForFence(info: string | null | undefined): Lang | null {
  const word =
    (info ?? "")
      .trim()
      .split(/\s+/)[0]
      ?.toLowerCase()
      .replace(/^\{?\.?|\}$/g, "") ?? "";
  if (!word) return null;
  return Object.hasOwn(FENCE_ALIASES, word) ? (FENCE_ALIASES[word] ?? null) : null;
}

/** Whether the text is small enough to highlight. */
export function withinHighlightLimits(lines: number, chars: number): boolean {
  return lines <= MAX_HIGHLIGHT_LINES && chars <= MAX_HIGHLIGHT_CHARS;
}

/** Tokens per line of a hunk, from tokens of its two sides. A removed line
 *  takes its tokens from the old side (context and removed lines), an added
 *  line from the new side (context and added lines), and a context line from
 *  the new side. A construct that opens on an unchanged line and runs on
 *  through removed or added ones is therefore coloured the way each side's own
 *  file would be. A line whose tokens do not add up to its text, or that has
 *  none, falls back to one plain token, so the text is never altered. */
export function mapHunkTokens(
  lines: readonly CodeDiffLine[],
  oldTokens: readonly HighlightedLine[],
  newTokens: readonly HighlightedLine[],
): HighlightedLine[] {
  let oldIndex = 0;
  let newIndex = 0;
  return lines.map((line) => {
    const fromOld = line.kind !== "add";
    const fromNew = line.kind !== "remove";
    const tokens = line.kind === "remove" ? oldTokens[oldIndex] : newTokens[newIndex];
    if (fromOld) oldIndex++;
    if (fromNew) newIndex++;
    if (tokens && tokens.map((token) => token.content).join("") === line.text) return [...tokens];
    return [{ content: line.text }];
  });
}

/** The two sides of a hunk as the source text a highlighter reads. */
export function hunkSides(lines: readonly CodeDiffLine[]): { old: string; new: string } {
  return {
    old: lines
      .filter((line) => line.kind !== "add")
      .map((line) => line.text)
      .join("\n"),
    new: lines
      .filter((line) => line.kind !== "remove")
      .map((line) => line.text)
      .join("\n"),
  };
}

type Highlighter = {
  loadLanguage(...langs: unknown[]): Promise<void>;
  getLoadedLanguages(): string[];
  codeToTokensBase(
    code: string,
    options: { lang: string; theme: string; tokenizeMaxLineLength?: number },
  ): { content: string; color?: string; fontStyle?: number }[][];
};

const THEME = "baum-syntax";
/** Longer lines (minified code) are left as one plain token. */
const MAX_LINE_LENGTH = 5_000;

let highlighter: Promise<Highlighter> | undefined;

function getHighlighter(): Promise<Highlighter> {
  highlighter ??= (async () => {
    const [{ createHighlighterCore, createCssVariablesTheme }, { createJavaScriptRegexEngine }] =
      await Promise.all([import("shiki/core"), import("shiki/engine/javascript")]);
    const theme = createCssVariablesTheme({ name: THEME, variablePrefix: "--syntax-" });
    return createHighlighterCore({
      themes: [theme],
      langs: [],
      engine: createJavaScriptRegexEngine(),
    });
  })().catch((error: unknown) => {
    highlighter = undefined;
    throw error;
  });
  return highlighter;
}

/** Each grammar is its own chunk; only the requested one is fetched. */
const GRAMMARS: Record<Lang, () => Promise<{ default: unknown }>> = {
  bash: () => import("shiki/langs/bash.mjs"),
  c: () => import("shiki/langs/c.mjs"),
  cpp: () => import("shiki/langs/cpp.mjs"),
  csharp: () => import("shiki/langs/csharp.mjs"),
  css: () => import("shiki/langs/css.mjs"),
  dockerfile: () => import("shiki/langs/dockerfile.mjs"),
  go: () => import("shiki/langs/go.mjs"),
  graphql: () => import("shiki/langs/graphql.mjs"),
  html: () => import("shiki/langs/html.mjs"),
  java: () => import("shiki/langs/java.mjs"),
  javascript: () => import("shiki/langs/javascript.mjs"),
  json: () => import("shiki/langs/json.mjs"),
  jsx: () => import("shiki/langs/jsx.mjs"),
  kotlin: () => import("shiki/langs/kotlin.mjs"),
  makefile: () => import("shiki/langs/makefile.mjs"),
  markdown: () => import("shiki/langs/markdown.mjs"),
  php: () => import("shiki/langs/php.mjs"),
  proto: () => import("shiki/langs/proto.mjs"),
  python: () => import("shiki/langs/python.mjs"),
  ruby: () => import("shiki/langs/ruby.mjs"),
  rust: () => import("shiki/langs/rust.mjs"),
  scss: () => import("shiki/langs/scss.mjs"),
  sql: () => import("shiki/langs/sql.mjs"),
  svelte: () => import("shiki/langs/svelte.mjs"),
  swift: () => import("shiki/langs/swift.mjs"),
  toml: () => import("shiki/langs/toml.mjs"),
  tsx: () => import("shiki/langs/tsx.mjs"),
  typescript: () => import("shiki/langs/typescript.mjs"),
  vue: () => import("shiki/langs/vue.mjs"),
  yaml: () => import("shiki/langs/yaml.mjs"),
};

/** Tokenises each given source text in one language, loading shiki and the
 *  grammar on first use. Null for any failure, or when the text is over the
 *  size limits. The tokenizer is injectable so tests can fail it. */
export async function tokenize(
  codes: readonly string[],
  lang: Lang,
  tokenizer: (code: string, lang: Lang) => Promise<HighlightedLine[]> = shikiTokenize,
): Promise<HighlightedLine[][] | null> {
  const lines = codes.reduce((total, code) => total + code.split("\n").length, 0);
  const chars = codes.reduce((total, code) => total + code.length, 0);
  if (!withinHighlightLimits(lines, chars)) return null;
  try {
    return await Promise.all(codes.map((code) => tokenizer(code, lang)));
  } catch {
    return null;
  }
}

async function shikiTokenize(code: string, lang: Lang): Promise<HighlightedLine[]> {
  const shiki = await getHighlighter();
  if (!shiki.getLoadedLanguages().includes(lang)) {
    await shiki.loadLanguage((await GRAMMARS[lang]()).default);
  }
  return shiki.codeToTokensBase(code, {
    lang,
    theme: THEME,
    tokenizeMaxLineLength: MAX_LINE_LENGTH,
  });
}

/** Tokens per line for a block of code, or null when it cannot be highlighted
 *  (oversized, or shiki failed). Client-side only; the first call fetches
 *  shiki and the grammar. */
export async function highlightCode(code: string, lang: Lang): Promise<HighlightedLine[] | null> {
  const result = await tokenize([code], lang);
  return result?.[0] ?? null;
}

/** Tokens per line of every hunk, or null when the path's language is unknown,
 *  the hunks are oversized or highlighting fails. */
export async function highlightHunks(
  path: string,
  hunks: readonly CodeDiffHunk[],
  tokenizer?: (code: string, lang: Lang) => Promise<HighlightedLine[]>,
): Promise<HighlightedLine[][] | null> {
  const lang = languageForPath(path);
  if (!lang) return null;
  const sides = hunks.map((hunk) => hunkSides(hunk.lines));
  const tokens = await tokenize(
    sides.flatMap((side) => [side.old, side.new]),
    lang,
    tokenizer,
  );
  if (!tokens) return null;
  return hunks.map((hunk, index) =>
    mapHunkTokens(hunk.lines, tokens[index * 2] ?? [], tokens[index * 2 + 1] ?? []),
  );
}

/** A string that changes only when the highlighted content does, so a caller
 *  that rebuilds equal hunks on every render does not restart highlighting. */
function hunksKey(path: string, hunks: readonly CodeDiffHunk[]): string {
  const language = languageForPath(path);
  if (!language) return "";
  let chars = 0;
  let lines = 0;
  for (const hunk of hunks) {
    lines += hunk.lines.length;
    for (const line of hunk.lines) chars += line.text.length;
  }
  if (!withinHighlightLimits(lines, chars)) return "";
  return `${language}\0${hunks
    .map((hunk) => hunk.lines.map((line) => line.kind[0] + line.text).join("\n"))
    .join("\0")}`;
}

/** Highlighted tokens per line of each hunk, in the same order, for a file's
 *  diff. Null on the server and on the first client render, so both render
 *  the plain text and hydrate identically; null again while a change to the
 *  hunks is being highlighted, and for an unknown language, oversized hunks or
 *  a failure, so the caller renders the plain text. */
export function useHighlightedHunks(
  path: string,
  hunks: readonly CodeDiffHunk[],
): HighlightedLine[][] | null {
  const key = useMemo(() => hunksKey(path, hunks), [path, hunks]);
  const [result, setResult] = useState<{ key: string; tokens: HighlightedLine[][] } | null>(null);

  useEffect(() => {
    if (!key) return;
    let current = true;
    void highlightHunks(path, hunks).then((tokens) => {
      if (current && tokens) setResult({ key, tokens });
    });
    return () => {
      current = false;
    };
    // `key` stands for the content of `path` and `hunks`.
    // oxlint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  return key && result?.key === key ? result.tokens : null;
}
