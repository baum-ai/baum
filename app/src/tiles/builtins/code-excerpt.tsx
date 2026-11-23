import { useState } from "react";
import { CodeDiff, type CodeDiffFile, type CodeDiffLine } from "@baum/viewer/components/code-diff";
import { codeDiffHunk } from "@baum/viewer/patch";
import { hunkUrl, type IndexedHunk, type ReviewDocument } from "@baum/viewer/review-document";

/** How many lines a long excerpt shows until it is opened. */
export const EXCERPT_SHOWN = 12;

/** The lines of a code tile's excerpt, as the Changed files tab draws its
 *  hunk: the hunk's lines from `lines[0]` to `lines[1]` on the new side,
 *  with the lines removed among and just before them, each line of `focus`
 *  marked. Without the hunk's diff, the host's text as unchanged lines. */
export function excerptLines(
  hunk: IndexedHunk,
  text: string,
  lines: [number, number] | undefined,
  focus: number[],
): CodeDiffLine[] {
  const marked = (line: CodeDiffLine): CodeDiffLine =>
    line.newLine !== undefined && focus.includes(line.newLine) ? { ...line, focus: true } : line;
  const plain = () => {
    const first = lines?.[0] ?? hunk.new_range[0];
    return text
      .split("\n")
      .map((each, at) => marked({ kind: "context", newLine: first + at, text: each }));
  };
  if (!hunk.diff) return plain();
  const all = codeDiffHunk(hunk.diff).lines;
  if (!lines) return all.map(marked);
  const [first, last] = lines;
  const lastNew = all.reduce((most, line) => Math.max(most, line.newLine ?? 0), 0);
  // A removed line stands after the new line before it: kept from just
  // before the first line, and after the last only at the hunk's end.
  let before = (all.find((line) => line.newLine !== undefined)?.newLine ?? 1) - 1;
  const out: CodeDiffLine[] = [];
  for (const line of all) {
    if (line.newLine !== undefined) {
      before = line.newLine;
      if (line.newLine >= first && line.newLine <= last) out.push(marked(line));
    } else if (before >= first - 1 && (before < last || last >= lastNew)) out.push(line);
  }
  // Lines the index's diff doesn't hold: the host's text, as it read them.
  return out.some((line) => line.newLine !== undefined) ? out : plain();
}

/** The lines a folded excerpt shows: `EXCERPT_SHOWN` of them from the start,
 *  or from just above its first focused line when that lies further down.
 *  Folding one or two lines saves nothing, so a short excerpt never folds. */
export function foldedLines(lines: CodeDiffLine[]): CodeDiffLine[] | null {
  if (lines.length <= EXCERPT_SHOWN + 2) return null;
  const focus = lines.findIndex((line) => line.focus);
  const start = Math.max(0, Math.min(focus - 2, lines.length - EXCERPT_SHOWN));
  return lines.slice(start, start + EXCERPT_SHOWN);
}

/** A code tile's excerpt drawn as a diff: the file's path (opening it in
 *  Changed files), line numbers, added and removed lines marked, the lines
 *  that matter highlighted and the rest dimmed, folded when long. */
export function CodeExcerpt({
  review,
  hunk,
  text,
  lines,
  focus,
}: {
  review: ReviewDocument;
  hunk: IndexedHunk;
  text: string;
  lines?: [number, number];
  focus: number[];
}) {
  const [open, setOpen] = useState(false);
  const all = excerptLines(hunk, text, lines, focus);
  const folded = foldedLines(all);
  const shown = all.filter((line) => line.newLine !== undefined);
  const header =
    hunk.diff?.header ?? `@@ +${shown[0]?.newLine ?? hunk.new_range[0]},${shown.length} @@`;
  const file: CodeDiffFile = {
    sourceIndex: 0,
    path: hunk.path,
    href: hunkUrl(review, hunk),
    additions: all.filter((line) => line.kind === "add").length,
    deletions: all.filter((line) => line.kind === "remove").length,
    hunks: [{ header, lines: folded && !open ? folded : all }],
  };
  return (
    <div className="space-y-2">
      <CodeDiff bare files={[file]} className="mt-0" />
      {folded ? (
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen((shown) => !shown)}
          className="text-base text-link hover:underline"
        >
          {open ? "Show fewer lines" : `Show all ${all.length} lines`}
        </button>
      ) : null}
    </div>
  );
}
