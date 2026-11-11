import { describe, expect, it } from "vitest";
import { firstByPath, hunkIndex } from "./hunk-index";
import { parseFilePatch } from "./patch";
import {
  anchorFile,
  hunkUrl,
  isContextDirectory,
  type IndexedHunk,
  type ReviewDocument,
} from "./review-document";
import { reviewDocumentFor } from "../test/review-document";

function fixture(): ReviewDocument {
  const doc = structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;
  const base = doc.index.hunks[0]!;
  const make = (id: string, path: string, old_path?: string): IndexedHunk => ({
    ...structuredClone(base),
    id,
    path,
    ...(old_path ? { old_path } : {}),
  });
  doc.index.hunks.push(
    make("dup", "src/first.ts"),
    make("dup", "src/second.ts"),
    make("moved", "lib/new.ts", "lib/old.ts"),
    make("noext", "Makefile"),
  );
  return doc;
}

// The linear scans the index replaces.
const scanById = (doc: ReviewDocument, id: string) =>
  doc.index.hunks.find((candidate) => candidate.id === id);
const scanDirectory = (doc: ReviewDocument, path: string) => {
  if (path.endsWith("/")) return true;
  const cited = new Set<string>();
  for (const hunk of doc.index.hunks) {
    cited.add(hunk.path);
    if (hunk.old_path) cited.add(hunk.old_path);
  }
  for (const part of Object.values(doc.summary.context ?? {})) {
    for (const ref of part?.refs ?? []) cited.add(ref.path);
  }
  for (const candidate of cited) if (candidate.startsWith(`${path}/`)) return true;
  const name = path.slice(path.lastIndexOf("/") + 1);
  return !name.includes(".") && !doc.index.hunks.some((hunk) => hunk.path === path);
};

describe("hunkIndex", () => {
  const doc = fixture();

  it("resolves ids as a linear find does, first duplicate winning, misses undefined", () => {
    const ids = [...doc.index.hunks.map((hunk) => hunk.id), "missing", ""];
    for (const id of ids) expect(hunkIndex(doc).byId.get(id)).toBe(scanById(doc, id));
    expect(hunkIndex(doc).byId.get("dup")?.path).toBe("src/first.ts");
  });

  it("answers anchorFile like the scan", () => {
    expect(anchorFile(doc, { type: "hunk", id: "dup" })?.path).toBe("src/first.ts");
    expect(anchorFile(doc, { type: "hunk", id: "nope" })).toBeNull();
  });

  it("answers isContextDirectory like the scan", () => {
    const paths = [
      "src",
      "src/",
      "src/first.ts",
      "lib",
      "lib/old.ts",
      "Makefile",
      "NoSuchDir",
      "pkg",
      "pkg/a.go",
      "pkg/a",
      "sr",
      "",
    ];
    for (const path of paths) {
      expect(isContextDirectory(doc, path), path).toBe(scanDirectory(doc, path));
    }
    expect(isContextDirectory(doc, "src")).toBe(true);
    expect(isContextDirectory(doc, "Makefile")).toBe(false);
  });

  it("is built once per document object and rebuilt for another", () => {
    expect(hunkIndex(doc)).toBe(hunkIndex(doc));
    expect(hunkIndex(structuredClone(doc))).not.toBe(hunkIndex(doc));
  });

  it("memoises hunk urls without changing them", () => {
    const hunk = doc.index.hunks[0]!;
    const first = hunkUrl(doc, hunk);
    expect(hunkUrl(doc, hunk)).toBe(first);
    expect(hunkUrl(structuredClone(doc), hunk)).toBe(first);
  });
});

describe("firstByPath", () => {
  const files = [
    { id: "1", path: "a.ts" },
    { id: "2", path: "b.ts", old_path: "a.ts" },
    { id: "3", path: "a.ts", old_path: "c.ts" },
    { id: "4", path: "c.ts" },
  ];
  it("matches find on path or old path, first item winning", () => {
    const lookup = firstByPath(files);
    for (const path of ["a.ts", "b.ts", "c.ts", "d.ts"]) {
      expect(lookup(path)).toBe(
        files.find(
          (file) => file.path === path || (file as { old_path?: string }).old_path === path,
        ),
      );
    }
  });
});

describe("parseFilePatch cache", () => {
  it("returns the same parse for the same patch text", () => {
    const patch = "@@ -1,1 +1,2 @@\n a\n+b\n";
    expect(parseFilePatch(patch)).toBe(parseFilePatch(patch));
    expect(parseFilePatch(patch)[0]?.lines).toHaveLength(2);
  });
});
