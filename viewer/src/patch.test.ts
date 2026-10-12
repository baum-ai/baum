import { describe, expect, it } from "vitest";

import { indexedHunkFromFilePatch, parseFilePatch } from "./patch";

describe("parseFilePatch", () => {
  it("builds GitHub-style old and new line coordinates across hunks", () => {
    expect(
      parseFilePatch(
        "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -2,2 +2,3 @@\n same\n-old\n+new\n+extra\n@@ -10 +11 @@\n-tail\n+last\n",
      ),
    ).toEqual([
      {
        header: "@@ -2,2 +2,3 @@",
        lines: [
          { kind: "context", oldLine: 2, newLine: 2, text: "same" },
          { kind: "remove", oldLine: 3, text: "old" },
          { kind: "add", newLine: 3, text: "new" },
          { kind: "add", newLine: 4, text: "extra" },
        ],
      },
      {
        header: "@@ -10 +11 @@",
        lines: [
          { kind: "remove", oldLine: 10, text: "tail" },
          { kind: "add", newLine: 11, text: "last" },
        ],
      },
    ]);
  });
});

describe("indexedHunkFromFilePatch", () => {
  const patch =
    "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -2,2 +2,3 @@\n same\n-old\n+new\n+extra\n@@ -10 +11 @@\n-tail\n+last\n";

  it("returns the hunk with the indexed new-side range in stored excerpt lines", () => {
    expect(
      indexedHunkFromFilePatch(patch, { new_range: [11, 1], additions: 1, deletions: 1 }),
    ).toEqual({
      header: "@@ -10 +11 @@",
      lines: [
        { kind: "remove", old_line: 10, text: "tail" },
        { kind: "add", new_line: 11, text: "last" },
      ],
    });
    expect(
      indexedHunkFromFilePatch(patch, { new_range: [2, 3], additions: 2, deletions: 1 })?.lines,
    ).toContainEqual({ kind: "context", old_line: 2, new_line: 2, text: "same" });
  });

  it("reads a whole added file as its one hunk", () => {
    const added = `@@ -0,0 +1,300 @@\n${Array.from({ length: 300 }, (_, i) => `+line ${i + 1}`).join("\n")}\n`;
    const hunk = indexedHunkFromFilePatch(added, {
      new_range: [1, 300],
      additions: 300,
      deletions: 0,
    });
    expect(hunk?.lines).toHaveLength(300);
    expect(hunk?.lines.at(-1)).toEqual({ kind: "add", new_line: 300, text: "line 300" });
  });

  it("finds nothing when no hunk has the same range and counts", () => {
    expect(
      indexedHunkFromFilePatch(patch, { new_range: [12, 1], additions: 1, deletions: 1 }),
    ).toBeNull();
    expect(
      indexedHunkFromFilePatch(patch, { new_range: [11, 1], additions: 2, deletions: 1 }),
    ).toBeNull();
  });
});

describe("indexedHunkFromFilePatch on a local patch", () => {
  // What `baum view` serves for a renamed and edited file: the diff from its
  // first hunk, with the function-context text after the header.
  const local =
    "@@ -18,6 +18,7 @@ line 17\n line 18\n line 19\n line 20\n+line 20b\n line 21\n line 22\n line 23\n";

  it("finds the hunk the review cites in a renamed file", () => {
    const hunk = indexedHunkFromFilePatch(local, {
      new_range: [18, 7],
      additions: 1,
      deletions: 0,
    });
    expect(hunk?.header).toBe("@@ -18,6 +18,7 @@ line 17");
    expect(hunk?.lines).toHaveLength(7);
    expect(hunk?.lines[3]).toEqual({ kind: "add", new_line: 21, text: "line 20b" });
  });
});
