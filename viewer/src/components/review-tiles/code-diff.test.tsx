// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";

import { CodeDiff, type CodeDiffFile } from "./code-diff";

let root: Root | null = null;

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.replaceChildren();
});

function file(sourceIndex: number, path: string, text: string): CodeDiffFile {
  return {
    sourceIndex,
    path,
    href: `https://github.com/acme/widgets/blob/main/${path}`,
    additions: 1,
    deletions: 0,
    hunks: [{ header: "@@ -1,0 +1,1 @@", lines: [{ kind: "add", newLine: 1, text }] }],
  };
}

describe("code diff", () => {
  it("folds and unfolds each file on its own", async () => {
    const container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    await act(async () =>
      root!.render(<CodeDiff bare files={[file(0, "a.go", "alpha"), file(1, "b.go", "beta")]} />),
    );
    const body = (path: string) =>
      container.querySelector<HTMLElement>(`[aria-label="${path} diff"] [id]`)!;
    expect(body("a.go").hidden).toBe(false);
    expect(body("b.go").hidden).toBe(false);

    const foldA = container.querySelector<HTMLButtonElement>('[aria-label="Fold a.go"]')!;
    expect(foldA.getAttribute("aria-controls")).toBe(body("a.go").id);
    await act(async () => foldA.click());
    expect(body("a.go").hidden).toBe(true);
    expect(body("b.go").hidden).toBe(false);

    const unfoldA = container.querySelector<HTMLButtonElement>('[aria-label="Unfold a.go"]')!;
    expect(unfoldA.getAttribute("aria-expanded")).toBe("false");
    await act(async () => unfoldA.click());
    expect(body("a.go").hidden).toBe(false);
    expect(body("a.go").textContent).toContain("alpha");
  });
});
