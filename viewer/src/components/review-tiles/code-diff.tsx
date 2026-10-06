import { cn } from "../classnames";
import { CodeFileLink } from "../code-file-link";

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

/** A compact GitHub-style unified diff for evidence that benefits from reading in place.
 *  `bare` leaves off the heading and totals, for a caller that already names them. */
export function CodeDiff({
  files,
  bare = false,
  className,
}: {
  files: readonly CodeDiffFile[];
  bare?: boolean;
  className?: string;
}) {
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
        {files.map((file) => (
          <article
            key={`${file.sourceIndex}:${file.path}`}
            aria-label={`${file.path} diff`}
            className="min-w-0 overflow-hidden rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card)"
          >
            <header className="flex min-w-0 flex-wrap items-center justify-between gap-2 border-b border-(--border-default) bg-(--surface-wash) px-3 py-2">
              <CodeFileLink
                path={file.path}
                line={file.hunks[0]?.lines.find((line) => line.newLine)?.newLine}
                href={file.href}
                className="inline-flex min-w-0 items-center gap-1 font-mono text-xs text-(--text-link) hover:underline"
              >
                <span className="truncate">{file.path}</span>
              </CodeFileLink>
              <span className="flex-none font-mono text-xs">
                <span className="text-(--status-positive)">+{file.additions}</span>{" "}
                <span className="text-(--status-critical)">−{file.deletions}</span>
              </span>
            </header>
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
                    <tr
                      key={`${file.path}:${hunk.header}:${hunkIndex}`}
                      className="bg-(--accent-50)"
                    >
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
                          {line.text}
                        </td>
                      </tr>
                    )),
                  ])}
                </tbody>
              </table>
            </div>
          </article>
        ))}
      </div>
    </section>
  );
}
