// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToString } from "react-dom/server";
import { afterEach, beforeAll, describe, expect, it } from "vitest";

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
        { kind: "add", newLine: 2, text: "   new */" },
        { kind: "context", oldLine: 3, newLine: 3, text: 'const answer: number = 42; // "x"' },
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

  it("colours both sides of a multi-line comment", async () => {
    const tokens = await highlightHunks("a.ts", file("a.ts").hunks);
    const [start, removed, added] = tokens![0]!;
    for (const tokensOfLine of [start!, removed!, added!]) {
      expect(tokensOfLine.every((token) => token.color === "var(--syntax-token-comment)")).toBe(
        true,
      );
    }
  });

  it("renders plain text first and coloured spans after the effect", async () => {
    expect(renderToString(<CodeDiff files={[file("a.ts")]} />)).not.toContain("style=");

    const container = document.body.appendChild(document.createElement("div"));
    root = createRoot(container);
    act(() => root!.render(<CodeDiff files={[file("a.ts")]} />));
    expect(spans()).toHaveLength(0);
    expect(container.textContent).toContain("const answer: number = 42;");

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 300));
    });
    expect(spans().some((span) => span.style.color.startsWith("var(--syntax-"))).toBe(true);
    const cells = [...container.querySelectorAll("tr td:last-child")];
    expect(cells.map((cell) => cell.textContent).slice(1)).toEqual([
      "/* start",
      "   old */",
      "   new */",
      'const answer: number = 42; // "x"',
    ]);
  });

  it("leaves an unknown language plain", async () => {
    const container = document.body.appendChild(document.createElement("div"));
    root = createRoot(container);
    await act(async () => root!.render(<CodeDiff files={[file("notes.txt")]} />));
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 100));
    });
    expect(spans()).toHaveLength(0);
  });
});
