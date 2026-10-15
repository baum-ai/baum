import { useId, useState } from "react";

import { useHighlightedHunks } from "../../highlight";
import { cn, FOCUS_RING } from "../classnames";
import { CodeFileLink } from "../code-file-link";
import { HighlightedText } from "../highlighted-text";
import { ChevronDownIcon } from "../icons";

export type CodeDiffLine = {
  kind: "context" | "add" | "remove";
  oldLine?: number;
  newLine?: number;
  text: string;
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
  context: "bg-(--surface-card)",
  add: "bg-(--status-positive-bg)",
  remove: "bg-(--status-critical-bg)",
};

const mark: Record<CodeDiffLine["kind"], string> = {
  context: " ",
  add: "+",
  remove: "−",
};

/** One file's hunks. Highlighting is client-only: the first render is the
 *  plain text, and the tokens replace it once they are ready. */
function CodeDiffTable({ file, highlight }: { file: CodeDiffFile; highlight: boolean }) {
  // An empty path has no language, which reads as "do not highlight".
  const highlighted = useHighlightedHunks(highlight ? file.path : "", file.hunks);
  return (
    <div className="relative min-w-0 overflow-x-auto">
      <table className="w-full table-fixed border-collapse font-mono text-xs leading-[1.55] sm:text-base">
        <colgroup>
          <col className="w-10" />
          <col className="w-10" />
          <col className="w-5 sm:w-6" />
          <col />
        </colgroup>
        <tbody>
          {file.hunks.flatMap((hunk, hunkIndex) => [
            <tr key={`${file.path}:${hunk.header}:${hunkIndex}`} className="bg-(--accent-50)">
              <td colSpan={4} className="px-3 py-1 text-(--accent-700)">
                {hunk.header}
              </td>
            </tr>,
            ...hunk.lines.map((line, index) => (
              <tr
                key={`${file.path}:${hunk.header}:${hunkIndex}:${index}`}
                className={cn("align-top", lineStyle[line.kind])}
              >
                <td className="w-10 border-r border-(--border-subtle) px-1.5 text-right sm:px-2 text-(--text-tertiary) select-none">
                  {line.oldLine ?? ""}
                </td>
                <td className="w-10 border-r border-(--border-subtle) px-1.5 text-right sm:px-2 text-(--text-tertiary) select-none">
                  {line.newLine ?? ""}
                </td>
                <td
                  className={cn(
                    "w-5 px-1 text-center select-none sm:w-6",
                    line.kind === "add" && "text-(--status-positive)",
                    line.kind === "remove" && "text-(--status-critical)",
                  )}
                >
                  {mark[line.kind]}
                </td>
                <td className="pr-3 whitespace-pre-wrap text-(--text-primary) [overflow-wrap:anywhere]">
                  <HighlightedText text={line.text} tokens={highlighted?.[hunkIndex]?.[index]} />
                </td>
              </tr>
            )),
          ])}
        </tbody>
      </table>
    </div>
  );
}

/** A compact GitHub-style unified diff for evidence that benefits from reading in place.
 *  `bare` leaves off the heading and totals, for a caller that already names them.
 *  Every file opens unfolded, and each folds on its own from its header. */
export function CodeDiff({
  files,
  bare = false,
  highlight = true,
  className,
}: {
  files: readonly CodeDiffFile[];
  bare?: boolean;
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
        <h5 className="text-xs font-semibold tracking-[0.12em] uppercase text-(--text-tertiary)">
          Code excerpts
        </h5>
        <span className="font-mono text-xs">
          <span className="text-(--status-positive)">+{additions}</span>{" "}
          <span className="text-(--status-critical)">−{deletions}</span>
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
              className="min-w-0 overflow-hidden rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card)"
            >
              <header
                className={cn(
                  "flex min-w-0 flex-wrap items-center justify-between gap-2 bg-(--surface-wash) px-3 py-2",
                  expanded && "border-b border-(--border-default)",
                )}
              >
                {/* The path stays a link to the file, so folding is its own
                    control beside it. */}
                <span className="flex min-w-0 items-center gap-1.5">
                  <button
                    type="button"
                    aria-label={`${file.path} code`}
                    aria-expanded={expanded}
                    aria-controls={body}
                    onClick={toggle}
                    className={cn(
                      "flex-none cursor-pointer rounded-(--radius-sm) -m-1 p-1.5 text-(--text-tertiary) hover:text-(--text-secondary)",
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
                  <CodeFileLink
                    path={file.path}
                    line={file.hunks[0]?.lines.find((line) => line.newLine)?.newLine}
                    href={file.href}
                    className="inline-flex min-w-0 items-center gap-1 font-mono text-xs text-(--text-link) hover:underline"
                  >
                    <span className="truncate">{file.path}</span>
                  </CodeFileLink>
                </span>
                <span className="flex-none font-mono text-xs">
                  <span className="text-(--status-positive)">+{file.additions}</span>{" "}
                  <span className="text-(--status-critical)">−{file.deletions}</span>
                </span>
              </header>
              {/* Stays on the page for aria-controls; a folded file draws and
                  highlights nothing. */}
              <div id={body} hidden={!expanded}>
                {expanded ? <CodeDiffTable file={file} highlight={highlight} /> : null}
              </div>
            </article>
          );
        })}
      </div>
    </section>
  );
}
