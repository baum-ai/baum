import pluralize from "pluralize";
import { useEffect, useMemo, useState } from "react";

import { parseFilePatch } from "../patch";
import { humanize } from "../review-document";
import { EM_DASH, formatDate, formatSHA, formatTime } from "./format";
import { Item, JumpLink } from "./review-parts";
import { CodeDiff, type CodeDiffFile } from "./review-tiles/code-diff";
import type { ReviewViewerActions } from "./review-viewer";
import { Empty, Failed, Skeleton } from "./states";
import { Badge, type BadgeTone, Button, CardHeader } from "./ui";

/** A session a commit was matched to, with how it was matched. */
export type CommitLinkView = {
  /** The session's id, which opens it in the Sessions pane. */
  session: string;
  /** What to call the session. */
  label: string;
  /** How the match was made: `commit_ref`, `start_commit` or `files_overlap`. */
  method: string;
  /** From 0 to 1. */
  confidence: number;
};

/** One commit as the Commits pane draws it. Plain data. */
export type CommitView = {
  /** The commit's full id. */
  id: string;
  subject: string;
  author?: string | null;
  /** An ISO instant. */
  committedAt?: string | null;
  links: CommitLinkView[];
};

/** One file a commit changed. */
export type CommitFileView = {
  /** The path after the commit (before it, for a deletion). */
  path: string;
  /** The path before a rename. */
  oldPath?: string | null;
  /** `added`, `modified`, `deleted` or `renamed`. */
  status: string;
  /** Lines added and removed; null for a binary file. */
  additions: number | null;
  deletions: number | null;
};

/** What a commit holds beyond its list entry: the message after its subject
 *  line, untrusted plain text, and the files it changed. */
export type CommitDetailView = {
  body: string;
  files: CommitFileView[];
};

/** How a host reads a commit's detail and its files' diffs. The viewer never
 *  fetches; without these the pane shows the commit's list entry alone. */
export type CommitLoaders = {
  /** The commit's detail; null when this machine cannot read it. A rejection
   *  is a failure worth a retry. */
  loadCommit?: (oid: string) => Promise<CommitDetailView | null>;
  /** One file's patch in the commit, from its first "@@" line; null when there
   *  is no text diff. A rejection is a failure worth a retry. */
  loadPatch?: (oid: string, path: string) => Promise<string | null>;
};

// The details each host's loader has answered, so a commit read once shows
// its totals in the list too without being asked for again.
type LoadCommit = NonNullable<CommitLoaders["loadCommit"]>;
const answered = new WeakMap<LoadCommit, Map<string, CommitDetailView>>();

function answeredDetail(loaders: CommitLoaders | undefined, oid: string) {
  return loaders?.loadCommit ? answered.get(loaders.loadCommit)?.get(oid) : undefined;
}

function remember(load: LoadCommit, oid: string, detail: CommitDetailView) {
  let known = answered.get(load);
  if (!known) {
    known = new Map();
    answered.set(load, known);
  }
  known.set(oid, detail);
}

function totals(files: readonly CommitFileView[]) {
  let additions = 0;
  let deletions = 0;
  for (const file of files) {
    additions += file.additions ?? 0;
    deletions += file.deletions ?? 0;
  }
  return { additions, deletions };
}

/** "+N −M across K files", or with `compact` "+N −M · K files". */
function CommitStats({ files, compact }: { files: readonly CommitFileView[]; compact: boolean }) {
  const { additions, deletions } = totals(files);
  const count = pluralize("file", files.length, true);
  return (
    <p className="text-xs text-(--text-tertiary)">
      <span className="font-mono text-(--status-positive)">+{additions}</span>{" "}
      <span className="font-mono text-(--status-critical)">−{deletions}</span>
      {compact ? ` · ${count}` : ` across ${count}`}
    </p>
  );
}

const STATUS_TONES: Record<string, BadgeTone> = {
  added: "positive",
  deleted: "critical",
  renamed: "info",
};

function FileStatus({ status }: { status: string }) {
  const known = Object.hasOwn(STATUS_TONES, status) ? STATUS_TONES[status] : undefined;
  const tone = known ?? "neutral";
  return <Badge size="compact" tone={tone}>{humanize(status)}</Badge>;
}

function FileCounts({ file }: { file: CommitFileView }) {
  if (file.additions === null || file.deletions === null)
    return <span className="flex-none text-xs text-(--text-tertiary)">binary</span>;
  return (
    <span className="flex-none font-mono text-xs">
      <span className="text-(--status-positive)">+{file.additions}</span>{" "}
      <span className="text-(--status-critical)">−{file.deletions}</span>
    </span>
  );
}

/** A file's names: its path, and the one it had before a rename. */
function FileName({ file }: { file: CommitFileView }) {
  return (
    <span className="flex min-w-0 flex-col">
      <span className="truncate font-mono text-xs text-(--text-link)">{file.path}</span>
      {file.oldPath && file.oldPath !== file.path ? (
        <span className="truncate font-mono text-xs text-(--text-tertiary)">
          from {file.oldPath}
        </span>
      ) : null}
    </span>
  );
}

type Loaded<T> = { status: "loading" } | { status: "failed" } | { status: "ready"; value: T };

/** One opened file: its patch, read when it is opened, drawn as the Files
 *  pane draws a file. */
function CommitFilePatch({
  oid,
  file,
  loadPatch,
}: {
  oid: string;
  file: CommitFileView;
  loadPatch: NonNullable<CommitLoaders["loadPatch"]>;
}) {
  const [retry, setRetry] = useState(0);
  const [patch, setPatch] = useState<Loaded<string | null>>({ status: "loading" });
  useEffect(() => {
    let current = true;
    setPatch({ status: "loading" });
    loadPatch(oid, file.path).then(
      (value) => {
        if (current) setPatch({ status: "ready", value });
      },
      () => {
        if (current) setPatch({ status: "failed" });
      },
    );
    return () => {
      current = false;
    };
  }, [oid, file.path, loadPatch, retry]);
  const diff = useMemo<CodeDiffFile | null>(
    () =>
      patch.status === "ready" && patch.value
        ? {
            sourceIndex: 0,
            path: file.path,
            href: "",
            additions: file.additions ?? 0,
            deletions: file.deletions ?? 0,
            hunks: parseFilePatch(patch.value),
          }
        : null,
    [patch, file],
  );
  if (patch.status === "loading")
    return (
      <div role="status" aria-label={`Loading ${file.path}`}>
        <Skeleton height={96} />
      </div>
    );
  if (patch.status === "failed")
    return (
      <div
        role="alert"
        className="flex flex-wrap items-center justify-between gap-2 rounded-(--radius-sm) border border-(--border-default) px-3 py-2 text-base text-(--status-critical)"
      >
        <span className="[overflow-wrap:anywhere]">
          <span className="font-mono text-xs">{file.path}</span> could not be read.
        </span>
        <Button variant="secondary" size="tiny" onClick={() => setRetry((n) => n + 1)}>
          Retry
        </Button>
      </div>
    );
  if (!diff || diff.hunks.length === 0)
    return (
      <p className="rounded-(--radius-sm) border border-(--border-default) px-3 py-2 text-base text-(--text-secondary) [overflow-wrap:anywhere]">
        <span className="font-mono text-xs">{file.path}</span>: No diff to show.
      </p>
    );
  return <CodeDiff bare files={[diff]} className="mt-0" />;
}

/** The files a commit changed, each folded until it is opened. */
function CommitFiles({
  oid,
  files,
  loadPatch,
}: {
  oid: string;
  files: CommitFileView[];
  loadPatch?: CommitLoaders["loadPatch"];
}) {
  const [opened, setOpened] = useState<ReadonlySet<string>>(() => new Set());
  const row =
    "flex w-full min-w-0 items-center justify-between gap-2 rounded-(--radius-sm) border border-(--border-default) bg-(--surface-wash) px-3 py-2 text-left";
  return (
    <section aria-label="Files changed" className="space-y-2">
      <h3 className="text-xs font-medium tracking-[0.08em] uppercase text-(--text-tertiary)">
        Files changed
      </h3>
      {files.length === 0 ? (
        <p className="text-base text-(--text-tertiary)">This commit changes no file.</p>
      ) : (
        <ul className="space-y-2">
          {files.map((file) => (
            // A block the review's keys move through (j and k), whose diff
            // Space opens; outlined while it is the one.
            <li
              key={file.path}
              data-review-block=""
              // Lightly filled while it is the one, as the review's other blocks.
              className="rounded-(--radius-md) outline-none data-[review-block-active]:[&>*]:bg-(--surface-wash)"
            >
              {loadPatch && opened.has(file.path) ? (
                <CommitFilePatch oid={oid} file={file} loadPatch={loadPatch} />
              ) : loadPatch ? (
                <button
                  type="button"
                  aria-expanded={false}
                  aria-label={`${file.path} code`}
                  onClick={() => setOpened((current) => new Set(current).add(file.path))}
                  className={`${row} cursor-pointer hover:bg-(--surface-hover)`}
                >
                  <FileName file={file} />
                  <span className="flex flex-none items-center gap-2">
                    <FileStatus status={file.status} />
                    <FileCounts file={file} />
                  </span>
                </button>
              ) : (
                <div className={row}>
                  <FileName file={file} />
                  <span className="flex flex-none items-center gap-2">
                    <FileStatus status={file.status} />
                    <FileCounts file={file} />
                  </span>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

/** One commit opened alone: its entry, then, from the host, its message
 *  body, totals and changed files. */
function CommitDetailPane({
  commit,
  actions,
  loaders,
}: {
  commit: CommitView;
  actions: ReviewViewerActions;
  loaders?: CommitLoaders;
}) {
  const loadCommit = loaders?.loadCommit;
  const [retry, setRetry] = useState(0);
  const [detail, setDetail] = useState<Loaded<CommitDetailView | null>>({ status: "loading" });
  useEffect(() => {
    if (!loadCommit) return;
    let current = true;
    setDetail({ status: "loading" });
    loadCommit(commit.id).then(
      (value) => {
        if (value) remember(loadCommit, commit.id, value);
        if (current) setDetail({ status: "ready", value });
      },
      () => {
        if (current) setDetail({ status: "failed" });
      },
    );
    return () => {
      current = false;
    };
  }, [commit.id, loadCommit, retry]);
  const ready = loadCommit && detail.status === "ready" ? detail.value : null;
  return (
    <div className="flex flex-col gap-3">
      <ol>
        <CommitCard commit={commit} actions={actions} openable={false} detail={ready} />
      </ol>
      {!loadCommit ? null : detail.status === "loading" ? (
        <div role="status" aria-label="Loading the commit" className="space-y-2">
          <Skeleton height={40} />
          <Skeleton height={40} />
        </div>
      ) : detail.status === "failed" ? (
        <Failed>
          This commit could not be read.{" "}
          <Button variant="secondary" size="tiny" onClick={() => setRetry((n) => n + 1)}>
            Retry
          </Button>
        </Failed>
      ) : detail.value === null ? (
        <p className="text-base text-(--text-tertiary)">
          This commit's message and files could not be read from the clone on this machine.
        </p>
      ) : (
        <CommitFiles oid={commit.id} files={detail.value.files} loadPatch={loaders?.loadPatch} />
      )}
    </div>
  );
}

export type CommitListState =
  | { status: "loading" }
  | { status: "failed" }
  | { status: "ready"; commits: CommitView[] };

const METHODS: Record<string, string> = {
  entire_checkpoint: "Recorded by Entire",
  commit_ref: "Named in the session",
  start_commit: "Started from this commit",
  files_overlap: "Edited the same files",
};

function methodLabel(method: string): string {
  return (Object.hasOwn(METHODS, method) ? METHODS[method] : undefined) ?? humanize(method);
}

function CommitCard({
  commit,
  actions,
  openable,
  detail,
}: {
  commit: CommitView;
  actions: ReviewViewerActions;
  /** Whether the subject opens the commit alone (in the list) or not (in its
   *  own pane). */
  openable: boolean;
  /** The commit's detail, when the host has read it: its totals, and in its
   *  own pane its message body. */
  detail?: CommitDetailView | null;
}) {
  const title = commit.subject || "(no subject)";
  return (
    <Item id={`commit-${commit.id}`} className="space-y-2">
      {openable ? (
        <JumpLink
          tone="primary"
          className="block font-medium [overflow-wrap:anywhere]"
          onClick={() => actions.select({ kind: "commit", id: commit.id })}
        >
          {title}
        </JumpLink>
      ) : (
        <p className="font-medium text-(--text-primary) [overflow-wrap:anywhere]">{title}</p>
      )}
      <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-(--text-tertiary)">
        <span className="font-mono text-(--text-secondary)" title={commit.id}>
          {formatSHA(commit.id)}
        </span>
        <span>{commit.author || EM_DASH}</span>
        {commit.committedAt ? (
          <span>{`${formatDate(commit.committedAt)} ${formatTime(commit.committedAt)}`}</span>
        ) : null}
      </p>
      {detail ? <CommitStats files={detail.files} compact={openable} /> : null}
      {detail?.body && !openable ? (
        <p className="text-base whitespace-pre-wrap text-(--text-secondary) [overflow-wrap:anywhere]">
          {detail.body}
        </p>
      ) : null}
      {commit.links.length === 0 ? (
        <p className="text-base text-(--text-tertiary)">No session is linked to this commit.</p>
      ) : (
        <ul className="flex flex-col gap-1">
          {commit.links.map((link) => (
            <li
              key={link.session}
              className="flex flex-wrap items-baseline gap-x-2 gap-y-1 text-base"
            >
              <JumpLink
                className="[overflow-wrap:anywhere]"
                onClick={() => actions.select({ kind: "session", id: link.session })}
              >
                {link.label}
              </JumpLink>
              <Badge size="compact">{methodLabel(link.method)}</Badge>
              <span className="text-xs text-(--text-tertiary)">
                {Math.round(Math.min(1, Math.max(0, link.confidence)) * 100)}% confidence
              </span>
            </li>
          ))}
        </ul>
      )}
    </Item>
  );
}

/** The Commits pane: every commit of the list, or the one `selected`. It takes
 *  the commits as data and never fetches them. */
export function CommitList({
  state,
  selected,
  actions,
  loaders,
}: {
  state: CommitListState;
  selected: string | null;
  actions: ReviewViewerActions;
  /** The host's reads of a commit's detail and diffs, for a commit opened
   *  alone. */
  loaders?: CommitLoaders;
}) {
  if (state.status !== "ready") {
    return (
      <>
        <CardHeader size="pane" title="Commits" />
        {state.status === "loading" ? (
          <div role="status" aria-label="Loading commits" className="space-y-3">
            <Skeleton height={80} />
            <Skeleton height={80} />
          </div>
        ) : (
          <Failed>The commits could not be loaded.</Failed>
        )}
      </>
    );
  }
  const { commits } = state;
  const shown = selected ? commits.filter((commit) => commit.id === selected) : commits;
  const single = Boolean(selected) && shown.length > 0;
  const count = pluralize("commit", commits.length, true);
  // A commit opened alone leads with its own subject and message: the rail
  // says it is one of the pull request's commits, and leads back to them.
  if (single)
    return (
      <div className="min-h-0 flex-1 xl:overflow-y-auto">
        <CommitDetailPane
          key={shown[0]!.id}
          commit={shown[0]!}
          actions={actions}
          loaders={loaders}
        />
      </div>
    );
  return (
    <>
      <CardHeader
        size="pane"
        title="Commits"
        detail={
          commits.length === 0
            ? "No commit was found for this pull request."
            : `${count} · oldest first, with the sessions linked to each.`
        }
      />
      <div className="min-h-0 flex-1 xl:overflow-y-auto">
        {shown.length === 0 ? (
          <Empty title="No commits">This pull request has no commits on this machine.</Empty>
        ) : (
          <ol className="flex flex-col gap-3">
            {shown.map((commit) => (
              <CommitCard
                key={commit.id}
                commit={commit}
                actions={actions}
                openable
                detail={answeredDetail(loaders, commit.id)}
              />
            ))}
          </ol>
        )}
      </div>
    </>
  );
}
