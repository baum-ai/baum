import { type ReactNode, useId, useState } from "react";

import { useHighlightedHunks } from "../highlight";
import { cn, FOCUS_RING } from "./classnames";
import { CodeFileLink } from "./code-file-link";
import { HighlightedText } from "./highlighted-text";
import { ChevronDownIcon } from "./icons";

export type CodeDiffLine = {
  kind: "context" | "add" | "remove";
  oldLine?: number;
  newLine?: number;
  text: string;
  /** A line that carries the point: marked, with the hunk's other lines
   *  dimmed. */
  focus?: boolean;
};

export type CodeDiffHunk = {
  header: string;
  lines: readonly CodeDiffLine[];
};

export type CodeDiffFile = {
  sourceIndex: number;
  path: string;
  href: string;
  additions: number;
  deletions: number;
  hunks: readonly CodeDiffHunk[];
};

const lineStyle: Record<CodeDiffLine["kind"], string> = {
  context: "bg-surface-card",
  add: "bg-status-positive-bg",
  remove: "bg-status-critical-bg",
};

const mark: Record<CodeDiffLine["kind"], string> = {
  context: " ",
  add: "+",
  remove: "−",
};

/** One file's hunks. Highlighting is client-only: the first render is the
 *  plain text, and the tokens replace it once they are ready. */
function CodeDiffTable({
  file,
  highlight,
  afterHunk,
}: {
  file: CodeDiffFile;
  highlight: boolean;
  afterHunk?: (file: CodeDiffFile, hunk: number) => ReactNode;
}) {
  // An empty path has no language, which reads as "do not highlight".
  const highlighted = useHighlightedHunks(highlight ? file.path : "", file.hunks);
  return (
    <div className="relative min-w-0 overflow-x-auto">
      <table className="w-full table-fixed border-collapse font-mono text-xs leading-[1.55] sm:text-base">
        <colgroup>
          <col className="w-14" />
          <col className="w-14" />
          <col className="w-5 sm:w-6" />
          <col />
        </colgroup>
        <tbody>
          {file.hunks.flatMap((hunk, hunkIndex) => {
            const focused = hunk.lines.some((line) => line.focus);
            return [
              <tr key={`${file.path}:${hunk.header}:${hunkIndex}`} className="bg-accent-50">
                <td colSpan={4} className="px-3 py-1 text-accent-700">
                  {hunk.header}
                </td>
              </tr>,
              ...hunk.lines.map((line, index) => (
                <tr
                  key={`${file.path}:${hunk.header}:${hunkIndex}:${index}`}
                  data-new-line={line.newLine}
                  data-old-line={line.oldLine}
                  data-focus={line.focus ? "" : undefined}
                  className={cn(
                    "align-top",
                    lineStyle[line.kind],
                    focused && !line.focus && "opacity-35",
                  )}
                >
                  <td
                    className={cn(
                      "relative w-14 border-r border-subtle px-1.5 text-right sm:px-2 text-tertiary select-none",
                      line.focus && "font-semibold text-primary",
                    )}
                  >
                    {line.focus ? (
                      <span
                        aria-hidden
                        className="absolute inset-y-0.5 left-0 w-1 rounded-full bg-status-info"
                      />
                    ) : null}
                    {line.oldLine ?? ""}
                  </td>
                  <td
                    className={cn(
                      "w-14 border-r border-subtle px-1.5 text-right sm:px-2 text-tertiary select-none",
                      line.focus && "font-semibold text-primary",
                    )}
                  >
                    {line.newLine ?? ""}
                  </td>
                  <td
                    className={cn(
                      "w-5 px-1 text-center select-none sm:w-6",
                      line.kind === "add" && "text-status-positive",
                      line.kind === "remove" && "text-status-critical",
                    )}
                  >
                    {mark[line.kind]}
                    {line.focus ? <span className="sr-only">Highlighted line</span> : null}
                  </td>
                  <td className="pr-3 whitespace-pre-wrap text-primary [overflow-wrap:anywhere]">
                    <HighlightedText text={line.text} tokens={highlighted?.[hunkIndex]?.[index]} />
                  </td>
                </tr>
              )),
              ...hunkNotes(file, hunkIndex, afterHunk),
            ];
          })}
        </tbody>
      </table>
    </div>
  );
}

/** What a host puts under a hunk, such as the notes that explain it, as a row
 *  of its own across the table; nothing when it has none. */
function hunkNotes(
  file: CodeDiffFile,
  hunkIndex: number,
  afterHunk?: (file: CodeDiffFile, hunk: number) => ReactNode,
) {
  const notes = afterHunk?.(file, hunkIndex);
  if (notes === null || notes === undefined || notes === false) return [];
  return [
    <tr key={`${file.path}:notes:${hunkIndex}`} className="border-y border-subtle bg-surface-wash">
      <td colSpan={4} className="px-3 py-2 font-sans">
        {notes}
      </td>
    </tr>,
  ];
}

/** A compact GitHub-style unified diff for evidence that benefits from reading in place.
 *  `bare` leaves off the heading and totals, for a caller that already names them.
 *  Every file opens unfolded, and each folds on its own from its header. */
export function CodeDiff({
  files,
  bare = false,
  foldable = true,
  highlight = true,
  className,
  afterHunk,
  headerNote,
  intro,
}: {
  files: readonly CodeDiffFile[];
  bare?: boolean;
  /** False for a diff with no fold of its own, such as an excerpt that
   *  folds its lines itself: its header has no chevron. */
  foldable?: boolean;
  /** Drawn under a hunk, across the table: a host's notes on it. */
  afterHunk?: (file: CodeDiffFile, hunk: number) => ReactNode;
  /** Drawn in a file's header, on a line under its path: a host's word on
   *  the file, such as where it is explained. */
  headerNote?: (file: CodeDiffFile) => ReactNode;
  /** Drawn between a file's header and its code, and kept when the code is
   *  folded: a host's summary of the file, such as the tests it changes. */
  intro?: (file: CodeDiffFile) => ReactNode;
  /** False for a diff that is on the page but not yet shown, so nothing is
   *  tokenised until the reader can see it. */
  highlight?: boolean;
  className?: string;
}) {
  const id = useId();
  const [folded, setFolded] = useState<ReadonlySet<string>>(() => new Set());
  if (files.length === 0) return null;
  const additions = files.reduce((total, file) => total + file.additions, 0);
  const deletions = files.reduce((total, file) => total + file.deletions, 0);

  return (
    <section aria-label="Code changes" className={cn("mt-3", className)}>
      <div
        className={cn("mb-2 flex flex-wrap items-center justify-between gap-2", bare && "hidden")}
      >
        <h5 className="text-xs font-semibold tracking-[0.12em] uppercase text-tertiary">
          Code excerpts
        </h5>
        <span className="font-mono text-xs">
          <span className="text-status-positive">+{additions}</span>{" "}
          <span className="text-status-critical">−{deletions}</span>
        </span>
      </div>
      <div className="space-y-3">
        {files.map((file, index) => {
          // By path, not position: evidence draws each file once, and its
          // excerpts arrive one by one, so a file's position can shift under
          // a reader who folded it.
          const key = file.path;
          const expanded = !folded.has(key);
          const body = `${id}-${index}`;
          const summary = intro?.(file);
          const toggle = () =>
            setFolded((current) => {
              const next = new Set(current);
              if (expanded) next.add(key);
              else next.delete(key);
              return next;
            });
          return (
            <article
              key={key}
              aria-label={`${file.path} diff`}
              className="min-w-0 overflow-hidden rounded-sm border border-default bg-surface-card"
            >
              <header
                className={cn(
                  "flex min-w-0 flex-wrap items-center justify-between gap-2 bg-surface-wash px-3 py-2",
                  (expanded || Boolean(summary)) && "border-b border-default",
                )}
              >
                {/* The path stays a link to the file, so folding is its own
                    control beside it. */}
                <span className="flex min-w-0 items-center gap-1.5">
                  {foldable ? (
                    <button
                      type="button"
                      aria-label={`${file.path} code`}
                      aria-expanded={expanded}
                      aria-controls={body}
                      onClick={toggle}
                      className={cn(
                        "flex-none cursor-pointer rounded-sm -m-1 p-1.5 text-tertiary hover:text-secondary",
                        FOCUS_RING,
                      )}
                    >
                      <ChevronDownIcon
                        size={12}
                        className={cn(
                          "transition-transform motion-reduce:transition-none",
                          !expanded && "-rotate-90",
                        )}
                      />
                    </button>
                  ) : null}
                  <CodeFileLink
                    path={file.path}
                    line={file.hunks[0]?.lines.find((line) => line.newLine)?.newLine}
                    href={file.href}
                    className="inline-flex min-w-0 items-center gap-1 font-mono text-xs text-link hover:underline"
                  >
                    <span className="truncate">{file.path}</span>
                  </CodeFileLink>
                </span>
                <span className="flex-none font-mono text-xs">
                  <span className="text-status-positive">+{file.additions}</span>{" "}
                  <span className="text-status-critical">−{file.deletions}</span>
                </span>
                {headerNote ? (
                  <div className="min-w-0 basis-full pl-6">{headerNote(file)}</div>
                ) : null}
              </header>
              {summary ? (
                <div className={cn("min-w-0", expanded && "border-b border-default")}>
                  {summary}
                </div>
              ) : null}
              {/* Stays on the page for aria-controls; a folded file draws and
                  highlights nothing. */}
              <div id={body} hidden={!expanded}>
                {expanded ? (
                  <CodeDiffTable file={file} highlight={highlight} afterHunk={afterHunk} />
                ) : null}
              </div>
            </article>
          );
        })}
      </div>
    </section>
  );
}
