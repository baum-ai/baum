// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToString } from "react-dom/server";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import { CodeDiff, type CodeDiffFile } from "./components/review-tiles/code-diff";
import { highlightCode, highlightHunks } from "./highlight";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

let root: Root | null = null;

beforeAll(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.replaceChildren();
});

const file = (path: string): CodeDiffFile => ({
  sourceIndex: 0,
  path,
  href: "https://example.com",
  additions: 1,
  deletions: 1,
  hunks: [
    {
      header: "@@ -1,3 +1,3 @@",
      lines: [
        { kind: "context", oldLine: 1, newLine: 1, text: "/* start" },
        { kind: "remove", oldLine: 2, text: "   old */" },
        { kind: "remove", oldLine: 3, text: "const old = 1;" },
        { kind: "add", newLine: 2, text: "   new text" },
        { kind: "add", newLine: 3, text: "   more text" },
      ],
    },
  ],
});

const spans = () => [...document.querySelectorAll<HTMLElement>("td span[style]")];

describe("syntax highlighting with shiki", () => {
  it("tokenizes TypeScript with CSS-variable colours", async () => {
    const lines = await highlightCode(
      "const answer: number = 42;\n\nfunction f() {}",
      "typescript",
    );
    expect(lines).toHaveLength(3);
    expect(lines![0]!.map((token) => token.content).join("")).toBe("const answer: number = 42;");
    expect(lines![0]!.some((token) => token.color?.startsWith("var(--syntax-"))).toBe(true);
  });

  it("colours each side from its own stream", async () => {
    // The old side closes the comment and goes on with code; the new side
    // keeps it open through both added lines.
    const tokens = await highlightHunks("a.ts", file("a.ts").hunks);
    const [start, closed, code, added, more] = tokens![0]!;
    const comment = "var(--syntax-token-comment)";
    for (const tokensOfLine of [start!, closed!, added!, more!]) {
      expect(tokensOfLine.every((token) => token.color === comment)).toBe(true);
    }
    expect(code!.some((token) => token.color !== comment)).toBe(true);
  });

  it("renders plain text first and coloured spans after the effect", async () => {
    expect(renderToString(<CodeDiff files={[file("a.ts")]} />)).not.toContain("style=");

    const container = document.body.appendChild(document.createElement("div"));
    root = createRoot(container);
    act(() => root!.render(<CodeDiff files={[file("a.ts")]} />));
    expect(spans()).toHaveLength(0);
    expect(container.textContent).toContain("const old = 1;");

    await vi.waitFor(() => {
      act(() => {});
      expect(spans().some((span) => span.style.color.startsWith("var(--syntax-"))).toBe(true);
    });
    const cells = [...container.querySelectorAll("tr td:last-child")];
    expect(cells.map((cell) => cell.textContent).slice(1)).toEqual([
      "/* start",
      "   old */",
      "const old = 1;",
      "   new text",
      "   more text",
    ]);
  });

  it("leaves an unknown language plain", async () => {
    const container = document.body.appendChild(document.createElement("div"));
    root = createRoot(container);
    // Shiki is loaded by now (earlier tests), so a TypeScript file proves the
    // wait below is long enough to tell: it colours, this one does not.
    await act(async () => root!.render(<CodeDiff files={[file("notes.txt")]} />));
    await act(async () => {
      await highlightCode("let a", "typescript");
    });
    expect(spans()).toHaveLength(0);
  });
});
