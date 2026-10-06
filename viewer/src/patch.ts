import type { IndexedHunk, IndexedHunkLine } from "./review-document";
import type { CodeDiffHunk, CodeDiffLine } from "./components/review-tiles/code-diff";

const hunkHeader = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/;

export function parseFilePatch(patch: string): CodeDiffHunk[] {
  const hunks: CodeDiffHunk[] = [];
  let current: { header: string; lines: CodeDiffLine[] } | null = null;
  let oldLine = 0;
  let newLine = 0;
  for (const raw of patch.split("\n")) {
    const match = hunkHeader.exec(raw);
    if (match) {
      current = { header: raw, lines: [] };
      hunks.push(current);
      oldLine = Number(match[1]);
      newLine = Number(match[3]);
      continue;
    }
    if (!current || raw.startsWith("\\ No newline at end of file")) continue;
    const prefix = raw[0];
    const text = raw.slice(1);
    if (prefix === "+") {
      current.lines.push({ kind: "add", newLine, text });
      newLine++;
    } else if (prefix === "-") {
      current.lines.push({ kind: "remove", oldLine, text });
      oldLine++;
    } else if (prefix === " ") {
      current.lines.push({ kind: "context", oldLine, newLine, text });
      oldLine++;
      newLine++;
    }
  }
  return hunks;
}

/** The indexed hunk's lines, found in the file's patch at the reviewed
 *  revision: the hunk with the same new-side range and the same counts of
 *  added and removed lines. Null when no hunk of the patch is that one, so a
 *  reader is never shown a different hunk as the one the review cites. */
export function indexedHunkFromFilePatch(
  patch: string,
  hunk: Pick<IndexedHunk, "new_range" | "additions" | "deletions">,
): NonNullable<IndexedHunk["diff"]> | null {
  const [start, count] = hunk.new_range;
  for (const candidate of parseFilePatch(patch)) {
    const match = hunkHeader.exec(candidate.header);
    if (Number(match?.[3]) !== start || Number(match?.[4] ?? 1) !== count) continue;
    const additions = candidate.lines.filter((line) => line.kind === "add").length;
    const deletions = candidate.lines.filter((line) => line.kind === "remove").length;
    if (additions !== hunk.additions || deletions !== hunk.deletions) return null;
    return {
      header: candidate.header,
      lines: candidate.lines.map((line): IndexedHunkLine => {
        if (line.kind === "add") return { kind: "add", new_line: line.newLine!, text: line.text };
        if (line.kind === "remove")
          return { kind: "remove", old_line: line.oldLine!, text: line.text };
        return {
          kind: "context",
          old_line: line.oldLine!,
          new_line: line.newLine!,
          text: line.text,
        };
      }),
    };
  }
  return null;
}
