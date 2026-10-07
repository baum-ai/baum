import { describe, expect, it, vi } from "vitest";

import type { CodeDiffLine } from "./components/review-tiles/code-diff";
import {
  type HighlightedLine,
  highlightCode,
  highlightHunks,
  languageForFence,
  languageForPath,
  mapHunkTokens,
  MAX_HIGHLIGHT_CHARS,
  MAX_HIGHLIGHT_LINES,
  tokenize,
} from "./highlight";

describe("languageForPath", () => {
  it.each([
    ["src/a.ts", "typescript"],
    ["src/a.tsx", "tsx"],
    ["a.js", "javascript"],
    ["a.mjs", "javascript"],
    ["a.cjs", "javascript"],
    ["a.jsx", "jsx"],
    ["a.json", "json"],
    ["cmd/main.go", "go"],
    ["a.py", "python"],
    ["a.rs", "rust"],
    ["A.java", "java"],
    ["a.kt", "kotlin"],
    ["a.rb", "ruby"],
    ["a.php", "php"],
    ["a.c", "c"],
    ["a.h", "c"],
    ["a.cpp", "cpp"],
    ["a.cs", "csharp"],
    ["a.swift", "swift"],
    ["q.sql", "sql"],
    ["a.sh", "bash"],
    ["a.bash", "bash"],
    ["a.yaml", "yaml"],
    ["a.yml", "yaml"],
    ["a.toml", "toml"],
    ["README.md", "markdown"],
    ["a.html", "html"],
    ["a.css", "css"],
    ["a.scss", "scss"],
    ["svc/Dockerfile", "dockerfile"],
    ["Dockerfile.dev", "dockerfile"],
    ["Makefile", "makefile"],
    ["a.proto", "proto"],
    ["a.graphql", "graphql"],
    ["a.vue", "vue"],
    ["a.svelte", "svelte"],
    ["SRC/A.TS", "typescript"],
  ])("%s is %s", (path, lang) => {
    expect(languageForPath(path)).toBe(lang);
  });

  it.each(["a.xyz", "LICENSE", ".gitignore", "dir.ts/file", ""])("%j has no language", (path) => {
    expect(languageForPath(path)).toBeNull();
  });
});

describe("languageForFence", () => {
  it.each([
    ["ts", "typescript"],
    ["typescript", "typescript"],
    ["TypeScript", "typescript"],
    ["golang", "go"],
    ["shell", "bash"],
    ["sh", "bash"],
    ["yml", "yaml"],
    ['tsx title="x.tsx"', "tsx"],
    ["  py  ", "python"],
    ["{.rust}", "rust"],
  ])("%j is %s", (info, lang) => {
    expect(languageForFence(info)).toBe(lang);
  });

  it.each(["", "   ", "text", "nope", "constructor", "__proto__", null, undefined])(
    "%j has no language",
    (info) => {
      expect(languageForFence(info)).toBeNull();
    },
  );
});

/** Tokens that colour a line by `tag`, split in two so a join is exercised. */
function tagged(text: string, tag: string): HighlightedLine {
  if (!text) return [];
  const middle = Math.ceil(text.length / 2);
  return [
    { content: text.slice(0, middle), color: tag },
    ...(text.length > middle ? [{ content: text.slice(middle), color: tag }] : []),
  ];
}

const line = (kind: CodeDiffLine["kind"], text: string): CodeDiffLine => ({ kind, text });
const comment = (text: string): HighlightedLine => [{ content: text, color: "comment" }];
const joined = (tokens: HighlightedLine) => tokens.map((token) => token.content).join("");

describe("mapHunkTokens", () => {
  it("takes removed lines from the old side and added and context lines from the new", () => {
    const lines = [
      line("context", "/* open"),
      line("remove", "old a"),
      line("remove", "old b */"),
      line("add", "new a"),
      line("add", "new b"),
      line("add", "new c */"),
      line("context", "tail"),
    ];
    // Each side's tokenizer saw its own stream.
    const oldTokens = ["/* open", "old a", "old b */", "tail"].map((text) => tagged(text, "old"));
    const newTokens = ["/* open", "new a", "new b", "new c */", "tail"].map((text) =>
      tagged(text, "new"),
    );
    const mapped = mapHunkTokens(lines, oldTokens, newTokens);
    expect(mapped.map((tokens) => tokens[0]?.color)).toEqual([
      "new",
      "old",
      "old",
      "new",
      "new",
      "new",
      "new",
    ]);
    mapped.forEach((tokens, index) => {
      expect(joined(tokens)).toBe(lines[index]!.text);
    });
  });

  it("keeps a comment that opens on a context line coloured through both sides", () => {
    const lines = [
      line("context", "/*"),
      line("remove", " * removed"),
      line("add", " * added"),
      line("context", " */"),
    ];
    const mapped = mapHunkTokens(
      lines,
      ["/*", " * removed", " */"].map(comment),
      ["/*", " * added", " */"].map(comment),
    );
    expect(mapped.every((tokens) => tokens.every((token) => token.color === "comment"))).toBe(true);
  });

  it("returns the exact text for empty and masked lines", () => {
    const lines = [
      line("context", ""),
      line("add", "token = [REDACTED]"),
      line("remove", "secret = ••••"),
      line("add", ""),
    ];
    const mapped = mapHunkTokens(
      lines,
      [[], tagged("secret = ••••", "x")],
      [[], tagged("token = [REDACTED]", "x"), []],
    );
    mapped.forEach((tokens, index) => {
      expect(joined(tokens)).toBe(lines[index]!.text);
    });
  });

  it("falls back to plain text for a line whose tokens do not match or are missing", () => {
    const lines = [line("context", "abc"), line("add", "def")];
    expect(mapHunkTokens(lines, [], [[{ content: "xyz", color: "c" }]])).toEqual([
      [{ content: "abc" }],
      [{ content: "def" }],
    ]);
  });
});

describe("fallbacks", () => {
  const hunk = { header: "@@", lines: [line("add", "const a = 1;")] };
  const fake = vi.fn(async (code: string) => code.split("\n").map((text) => tagged(text, "c")));

  it("is null for an unknown language without tokenizing", async () => {
    fake.mockClear();
    expect(await highlightHunks("notes.txt", [hunk], fake)).toBeNull();
    expect(fake).not.toHaveBeenCalled();
  });

  it("is null past the character limit", async () => {
    const big = { header: "@@", lines: [line("add", "x".repeat(MAX_HIGHLIGHT_CHARS + 1))] };
    fake.mockClear();
    expect(await highlightHunks("a.ts", [big], fake)).toBeNull();
    expect(fake).not.toHaveBeenCalled();
    expect(await highlightCode("x".repeat(MAX_HIGHLIGHT_CHARS + 1), "typescript")).toBeNull();
  });

  it("is null past the line limit", async () => {
    const many = {
      header: "@@",
      lines: Array.from({ length: MAX_HIGHLIGHT_LINES + 1 }, () => line("context", "")),
    };
    expect(await highlightHunks("a.ts", [many], fake)).toBeNull();
  });

  it("is null when the tokenizer throws", async () => {
    const failing = vi.fn(async (): Promise<HighlightedLine[]> => {
      throw new Error("no grammar");
    });
    expect(await tokenize(["a"], "typescript", failing)).toBeNull();
    expect(await highlightHunks("a.ts", [hunk], failing)).toBeNull();
  });

  it("maps tokens from the tokenizer for a known language", async () => {
    const tokens = await highlightHunks("a.ts", [hunk], fake);
    expect(tokens?.[0]?.[0]?.[0]?.color).toBe("c");
  });
});
