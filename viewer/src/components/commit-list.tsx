import pluralize from "pluralize";

import { humanize } from "../review-document";
import { EM_DASH, formatDate, formatSHA, formatTime } from "./format";
import { Item, JumpLink } from "./review-parts";
import type { ReviewViewerActions } from "./review-viewer";
import { Empty, Failed, Skeleton } from "./states";
import { Badge, CardHeader } from "./ui";

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

export type CommitListState =
  | { status: "loading" }
  | { status: "failed" }
  | { status: "ready"; commits: CommitView[] };

const METHODS: Record<string, string> = {
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
}: {
  commit: CommitView;
  actions: ReviewViewerActions;
  /** Whether the subject opens the commit alone (in the list) or not (in its
   *  own pane). */
  openable: boolean;
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
}: {
  state: CommitListState;
  selected: string | null;
  actions: ReviewViewerActions;
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
  return (
    <>
      <CardHeader
        size="pane"
        title="Commits"
        detail={
          commits.length === 0
            ? "No commit was found for this pull request."
            : `${single ? `1 of ${count}` : count} · oldest first, with the sessions linked to each.`
        }
        aside={
          single ? (
            <JumpLink className="text-xs" onClick={() => actions.select({ kind: "commits" })}>
              All commits →
            </JumpLink>
          ) : undefined
        }
      />
      <div className="min-h-0 flex-1 xl:overflow-y-auto">
        {shown.length === 0 ? (
          <Empty title="No commits">This pull request has no commits on this machine.</Empty>
        ) : (
          <ol className="flex flex-col gap-3">
            {shown.map((commit) => (
              <CommitCard key={commit.id} commit={commit} actions={actions} openable={!single} />
            ))}
          </ol>
        )}
      </div>
    </>
  );
}
