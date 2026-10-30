import { useEffect, useMemo, useRef, useState } from "react";
import { CodeDiff, type CodeDiffFile } from "@baum/viewer/components/review-tiles/code-diff";
import type {
  FileOpenRequest,
  ReviewFile,
  ReviewFilesSlot,
} from "@baum/viewer/components/review-viewer";
import { SideNavButton } from "@baum/viewer/components/side-nav";
import { parseFilePatch } from "@baum/viewer/patch";
import { parseReviewFacts, type ReviewFacts } from "@baum/viewer/review-document";
import { LoadingStatus } from "./nine-dots";
import { LocalRequestError, request, selector } from "./api";

/** A pull request's facts while it has no saved review, as far as the page
 *  has them. A daemon that serves none (an older one, say) is unavailable,
 *  which the page does not call a failure. */
export type FactsLoad =
  | { state: "loading" }
  | { state: "ready"; facts: ReviewFacts }
  | { state: "unavailable" }
  | { state: "failed"; rateLimited: boolean };

/** Where the facts' own routes are, at the head of the facts the page
 *  shows: the daemon reads their sessions, commits and patches against the
 *  facts it last built, never a version, and answers 404 once those describe
 *  another head. */
export const factsQuery = (clone: string, pr: number, head: string) =>
  `${selector(clone, pr)}&head=${encodeURIComponent(head)}`;

/** Asks the daemon for the pull request's facts at the head the page shows,
 *  again each time `revision` changes; the daemon reads GitHub at most once
 *  per refresh window however often it is asked. Nothing is asked while
 *  `enabled` is false, and the facts asked for before are dropped. */
export function useFacts(
  clone: string,
  pr: number,
  head: string,
  revision: number,
  enabled: boolean,
): FactsLoad {
  const [load, setLoad] = useState<FactsLoad>({ state: "loading" });
  // A later answer about the same head refreshes the facts in place; a new
  // head or pull request starts from loading.
  const shown = useRef("");
  useEffect(() => {
    if (!enabled) {
      shown.current = "";
      setLoad({ state: "loading" });
      return;
    }
    const target = `${clone}\n${pr}\n${head}`;
    if (shown.current !== target) setLoad({ state: "loading" });
    const abort = new AbortController();
    request<unknown>(`api/facts?${factsQuery(clone, pr, head)}`, undefined, "GET", abort.signal)
      .then((body) => {
        const facts = parseReviewFacts(body);
        if (!facts) throw Error("Invalid facts");
        if (abort.signal.aborted) return;
        shown.current = target;
        setLoad({ state: "ready", facts });
      })
      .catch((error: unknown) => {
        if (abort.signal.aborted) return;
        // Facts shown already stay while a refresh of them fails.
        if (shown.current === target) return;
        const local = error instanceof LocalRequestError ? error : null;
        setLoad(
          local?.code === "facts_unavailable" || local?.status === 404
            ? { state: "unavailable" }
            : { state: "failed", rateLimited: local?.code === "rate_limited" },
        );
      });
    return () => abort.abort();
  }, [clone, pr, head, revision, enabled]);
  return enabled ? load : { state: "loading" };
}

/** A changed file the facts list, with its line counts. */
export type FactsFile = ReviewFile & { additions: number; deletions: number };

/** The facts' changed files, in the order their hunks are indexed. */
export function factsFiles(facts: ReviewFacts): FactsFile[] {
  const files = new Map<string, FactsFile>();
  for (const hunk of facts.index.hunks) {
    const file = files.get(hunk.path) ?? {
      id: hunk.path,
      path: hunk.path,
      old_path: hunk.old_path,
      viewed: false,
      additions: 0,
      deletions: 0,
    };
    file.additions += hunk.additions;
    file.deletions += hunk.deletions;
    files.set(hunk.path, file);
  }
  return [...files.values()];
}

/** Reads one file's patch; null when there is no text diff to show. */
export type FilePatchSource = (path: string) => Promise<string | null>;

/** The viewer's Files slot for the facts: the rail lists the changed files
 *  and the pane every file folded, each read from the daemon when it is
 *  opened. Files keep no viewed marks. */
export function factsFilesSlot(
  load: FactsLoad,
  retry: () => void,
  patches: FilePatchSource,
  pullRequestUrl: string,
): ReviewFilesSlot<FactsFile> {
  const files = load.state === "ready" ? factsFiles(load.facts) : null;
  return {
    files,
    loading: load.state === "loading",
    failed: load.state === "failed",
    retry,
    tracksViewed: false,
    renderTree: ({ files, selected, onSelect }) => (
      <>
        {files.map((file) => (
          <SideNavButton
            key={file.id}
            depth={1}
            label={file.path}
            active={selected === file.id}
            controls="review-section"
            aside={
              <span className="font-mono text-xs text-(--text-tertiary)">
                +{file.additions} −{file.deletions}
              </span>
            }
            description={`${file.additions} added, ${file.deletions} removed`}
            onSelect={() => onSelect(file)}
          />
        ))}
      </>
    ),
    renderFiles: ({ files, open }) => (
      <FactsFilesPane
        files={files}
        open={open}
        patches={patches}
        href={`${pullRequestUrl}/files`}
      />
    ),
  };
}

/** Every changed file, folded until it is opened here or from the rail. */
function FactsFilesPane({
  files,
  open,
  patches,
  href,
}: {
  files: FactsFile[];
  open: FileOpenRequest;
  patches: FilePatchSource;
  href: string;
}) {
  const [opened, setOpened] = useState<ReadonlySet<string>>(() =>
    open ? new Set([open.id]) : new Set(),
  );
  const elements = useRef(new Map<string, HTMLElement>());
  useEffect(() => {
    if (!open) return;
    setOpened((current) => new Set(current).add(open.id));
    elements.current.get(open.id)?.scrollIntoView?.({ block: "start" });
  }, [open]);
  return (
    <section aria-label="Files changed" className="space-y-3">
      {files.map((file) => (
        <div
          key={file.id}
          ref={(element) => {
            if (element) elements.current.set(file.id, element);
            else elements.current.delete(file.id);
          }}
        >
          {opened.has(file.id) ? (
            <FilePatch file={file} patches={patches} href={href} />
          ) : (
            <button
              type="button"
              aria-expanded={false}
              aria-label={`${file.path} code`}
              onClick={() => setOpened((current) => new Set(current).add(file.id))}
              className="flex w-full min-w-0 cursor-pointer items-center justify-between gap-2 rounded-(--radius-sm) border border-(--border-default) bg-(--surface-wash) px-3 py-2 text-left hover:bg-(--surface-hover)"
            >
              <span className="truncate font-mono text-xs text-(--text-link)">{file.path}</span>
              <span className="flex-none font-mono text-xs">
                <span className="text-(--status-positive)">+{file.additions}</span>{" "}
                <span className="text-(--status-critical)">−{file.deletions}</span>
              </span>
            </button>
          )}
        </div>
      ))}
    </section>
  );
}

/** One opened file: its patch from the clone, drawn as the review's code is. */
function FilePatch({
  file,
  patches,
  href,
}: {
  file: FactsFile;
  patches: FilePatchSource;
  href: string;
}) {
  const [patch, setPatch] = useState<string | null | undefined>(undefined);
  useEffect(() => {
    let current = true;
    setPatch(undefined);
    void patches(file.path).then((text) => {
      if (current) setPatch(text);
    });
    return () => {
      current = false;
    };
  }, [file.path, patches]);
  const diff = useMemo<CodeDiffFile | null>(
    () =>
      patch
        ? {
            sourceIndex: 0,
            path: file.path,
            href,
            additions: file.additions,
            deletions: file.deletions,
            hunks: parseFilePatch(patch),
          }
        : null,
    [patch, file, href],
  );
  if (patch === undefined)
    return <LoadingStatus block>Reading {file.path} from the local clone…</LoadingStatus>;
  if (!diff || diff.hunks.length === 0)
    return (
      <p className="rounded-(--radius-sm) border border-(--border-default) px-3 py-2 text-base text-(--text-secondary)">
        <span className="font-mono text-xs">{file.path}</span>: no text diff to show here.{" "}
        <a href={href} target="_blank" rel="noreferrer">
          View on GitHub
        </a>
      </p>
    );
  return <CodeDiff bare files={[diff]} className="mt-0" />;
}
