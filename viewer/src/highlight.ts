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
    const text = withoutCarriageReturn(line.text);
    if (!tokens || tokens.map((token) => token.content).join("") !== text) {
      return [{ content: line.text }];
    }
    if (text === line.text) return [...tokens];
    // Shiki reads a CRLF as a line break, so the `\r` went in as none.
    const last = tokens.at(-1);
    return last
      ? [...tokens.slice(0, -1), { ...last, content: `${last.content}\r` }]
      : [{ content: "\r" }];
  });
}

/** A line's text as the highlighter reads it: without one trailing `\r`. */
function withoutCarriageReturn(text: string): string {
  return text.endsWith("\r") ? text.slice(0, -1) : text;
}

/** The two sides of a hunk as the source text a highlighter reads. */
export function hunkSides(lines: readonly CodeDiffLine[]): { old: string; new: string } {
  return {
    old: lines
      .filter((line) => line.kind !== "add")
      .map((line) => withoutCarriageReturn(line.text))
      .join("\n"),
    new: lines
      .filter((line) => line.kind !== "remove")
      .map((line) => withoutCarriageReturn(line.text))
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

/** Work done before tokenising yields to the event loop, so one large file
 *  cannot hold the main thread for its whole run. */
const YIELD_AFTER_MS = 8;

/** Hands the main thread back: `scheduler.yield()` where the browser has it,
 *  else a macrotask. */
export function yieldToEventLoop(): Promise<void> {
  const scheduler = (globalThis as { scheduler?: { yield?: () => Promise<void> } }).scheduler;
  if (typeof scheduler?.yield === "function") return scheduler.yield();
  return new Promise((resolve) => setTimeout(resolve, 0));
}

export type TokenizeOptions = {
  /** Stops the run at its next yield; the result is then null. */
  signal?: AbortSignal;
  /** Replaceable so tests can count yields and control the clock. */
  yieldFn?: () => Promise<void>;
  now?: () => number;
};

/** Token results by language and code, least recently used first. Bounded by
 *  entry count and by the total length of the cached code, so a long session
 *  cannot grow it without limit. */
export class TokenCache {
  private readonly entries = new Map<string, { chars: number; lines: HighlightedLine[] }>();
  private chars = 0;

  private readonly maxEntries: number;
  private readonly maxChars: number;

  constructor(maxEntries: number, maxChars: number) {
    this.maxEntries = maxEntries;
    this.maxChars = maxChars;
  }

  get(lang: Lang, code: string): HighlightedLine[] | undefined {
    const key = `${lang}\0${code}`;
    const entry = this.entries.get(key);
    if (!entry) return undefined;
    this.entries.delete(key);
    this.entries.set(key, entry);
    return entry.lines;
  }

  set(lang: Lang, code: string, lines: HighlightedLine[]): void {
    if (code.length > this.maxChars) return;
    const key = `${lang}\0${code}`;
    const existing = this.entries.get(key);
    if (existing) {
      this.chars -= existing.chars;
      this.entries.delete(key);
    }
    this.entries.set(key, { chars: code.length, lines });
    this.chars += code.length;
    for (const [oldest, entry] of this.entries) {
      if (this.entries.size <= this.maxEntries && this.chars <= this.maxChars) break;
      this.entries.delete(oldest);
      this.chars -= entry.chars;
    }
  }

  get size(): number {
    return this.entries.size;
  }

  clear(): void {
    this.entries.clear();
    this.chars = 0;
  }
}

/** Tokens are CSS-variable coloured, so a theme change needs no invalidation. */
const tokenCache = new TokenCache(500, 4_000_000);

/** Tokenises each given source text in one language, loading shiki and the
 *  grammar on first use. Null for any failure, when the text is over the
 *  size limits, or when `options.signal` aborts. The texts are tokenised one
 *  after another with a yield to the event loop whenever a run has held the
 *  thread for a few milliseconds. The tokenizer is injectable so tests can
 *  fail it. */
export async function tokenize(
  codes: readonly string[],
  lang: Lang,
  tokenizer: (code: string, lang: Lang) => Promise<HighlightedLine[]> = shikiTokenize,
  options: TokenizeOptions = {},
): Promise<HighlightedLine[][] | null> {
  const lines = codes.reduce((total, code) => total + code.split("\n").length, 0);
  const chars = codes.reduce((total, code) => total + code.length, 0);
  if (!withinHighlightLimits(lines, chars)) return null;
  const { signal, yieldFn = yieldToEventLoop, now = () => performance.now() } = options;
  try {
    const result: HighlightedLine[][] = [];
    let sliceStart = now();
    for (const code of codes) {
      if (signal?.aborted) return null;
      if (now() - sliceStart >= YIELD_AFTER_MS) {
        await yieldFn();
        if (signal?.aborted) return null;
        sliceStart = now();
      }
      result.push(await tokenizer(code, lang));
    }
    return result;
  } catch {
    return null;
  }
}

/** One load per language, shared by concurrent calls; a failure is dropped so
 *  the next call retries. */
const grammarLoads = new Map<Lang, Promise<void>>();

async function shikiTokenize(code: string, lang: Lang): Promise<HighlightedLine[]> {
  const cached = tokenCache.get(lang, code);
  if (cached) return cached;
  const shiki = await getHighlighter();
  let loading = grammarLoads.get(lang);
  if (!loading) {
    loading = (async () => {
      if (!shiki.getLoadedLanguages().includes(lang)) {
        await shiki.loadLanguage((await GRAMMARS[lang]()).default);
      }
    })();
    grammarLoads.set(lang, loading);
    loading.catch(() => grammarLoads.delete(lang));
  }
  await loading;
  const tokens = shiki.codeToTokensBase(code, {
    lang,
    theme: THEME,
    tokenizeMaxLineLength: MAX_LINE_LENGTH,
  });
  tokenCache.set(lang, code, tokens);
  return tokens;
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
  options?: TokenizeOptions,
): Promise<HighlightedLine[][] | null> {
  const lang = languageForPath(path);
  if (!lang) return null;
  const sides = hunks.map((hunk) => hunkSides(hunk.lines));
  const tokens = await tokenize(
    sides.flatMap((side) => [side.old, side.new]),
    lang,
    tokenizer,
    options,
  );
  if (!tokens) return null;
  return hunks.map((hunk, index) =>
    mapHunkTokens(hunk.lines, tokens[index * 2] ?? [], tokens[index * 2 + 1] ?? []),
  );
}

/** The tokens of every hunk when each side is already cached, else null.
 *  Synchronous, so a file seen before shows highlighted straight away. */
export function cachedHighlightedHunks(
  path: string,
  hunks: readonly CodeDiffHunk[],
): HighlightedLine[][] | null {
  const lang = languageForPath(path);
  if (!lang) return null;
  const result: HighlightedLine[][] = [];
  for (const hunk of hunks) {
    const side = hunkSides(hunk.lines);
    const old = tokenCache.get(lang, side.old);
    const next = tokenCache.get(lang, side.new);
    if (!old || !next) return null;
    result.push(mapHunkTokens(hunk.lines, old, next));
  }
  return result;
}

/** Empties the token cache. For tests. */
export function clearTokenCache(): void {
  tokenCache.clear();
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
    // Seen before: set at once, without waiting for the highlighter to load.
    const cached = cachedHighlightedHunks(path, hunks);
    if (cached) {
      setResult({ key, tokens: cached });
      return;
    }
    const controller = new AbortController();
    void highlightHunks(path, hunks, undefined, { signal: controller.signal }).then((tokens) => {
      if (!controller.signal.aborted && tokens) setResult({ key, tokens });
    });
    return () => controller.abort();
    // `key` stands for the content of `path` and `hunks`.
    // oxlint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  return key && result?.key === key ? result.tokens : null;
}
