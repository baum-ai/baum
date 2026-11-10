import pluralize from "pluralize";
import type { ReactNode } from "react";

import { harnessName } from "../agent-names";
import { humanize } from "../review-document";
import { HarnessName } from "./brand-logo";
import {
  durationSeconds,
  EM_DASH,
  formatDate,
  formatDuration,
  formatSHA,
  formatTime,
} from "./format";
import { CommitIcon } from "./icons";
import { Item, JumpLink } from "./review-parts";
import { commitLabel, sessionLabel } from "./list-labels";
import type { ReviewViewerActions } from "./review-viewer";
import { Empty, Failed, Skeleton } from "./states";
import { Badge, CardHeader } from "./ui";

/** A commit a session is linked to, as far as the session's card needs. */
export type SessionCommitView = { id: string; subject: string };

/** One session as the Sessions pane draws it. Plain data: whoever builds it
 *  (a local link, the review document) decides what it knows. */
export type SessionView = {
  id: string;
  /** The agent's vendor slug, such as `claude_code`. */
  vendor: string;
  title?: string | null;
  branch?: string | null;
  /** ISO instants. */
  startedAt?: string | null;
  endedAt?: string | null;
  outcome?: string | null;
  editedFiles?: number | null;
  /** The commits the session is linked to; each opens in the Commits pane. */
  commits?: SessionCommitView[];
};

export type SessionListState =
  | { status: "loading" }
  | { status: "failed" }
  | {
      status: "ready";
      sessions: SessionView[];
      /** Plain text shown under the heading, such as why these are the
       *  review's own sessions rather than the ones on this machine. */
      note?: string;
    };

function SessionCard({
  session,
  actions,
  openable,
}: {
  session: SessionView;
  actions: ReviewViewerActions;
  /** Whether the title opens the session alone (in the list) or not (in its
   *  own pane). */
  openable: boolean;
}) {
  const seconds = durationSeconds(session.startedAt, session.endedAt);
  const meta = [
    session.startedAt ? `${formatDate(session.startedAt)} ${formatTime(session.startedAt)}` : null,
    seconds === null ? null : formatDuration(seconds),
    session.editedFiles ? `${pluralize("file", session.editedFiles, true)} edited` : null,
  ].filter(Boolean);
  const title = sessionLabel(session);
  const commits = session.commits ?? [];
  return (
    <Item id={`session-${session.id}`} className="space-y-2">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <HarnessName vendor={session.vendor} className="font-medium" />
        {/* An outcome nobody could tell says nothing. */}
        {session.outcome && session.outcome !== "unknown" ? (
          <Badge size="compact" tone={session.outcome === "committed" ? "positive" : "neutral"}>
            {humanize(session.outcome)}
          </Badge>
        ) : null}
      </div>
      {openable ? (
        <JumpLink
          tone="primary"
          className="block font-medium [overflow-wrap:anywhere]"
          onClick={() => actions.select({ kind: "session", id: session.id })}
        >
          {title}
        </JumpLink>
      ) : (
        <p className="font-medium text-(--text-primary) [overflow-wrap:anywhere]">{title}</p>
      )}
      <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-(--text-tertiary)">
        {session.branch ? <code className="font-mono">{session.branch}</code> : null}
        {meta.length > 0 ? <span>{meta.join(" · ")}</span> : null}
        {!session.branch && meta.length === 0 ? <span>{EM_DASH}</span> : null}
      </p>
      {commits.length > 0 ? (
        <div className="space-y-1">
          <p className="text-xs text-(--text-tertiary)">
            {pluralize("linked commit", commits.length, true)}
          </p>
          <ul className="flex flex-col gap-0.5">
            {commits.map((commit) => (
              <li key={commit.id} className="flex min-w-0 items-baseline gap-2 text-base">
                <span
                  className="flex-none font-mono text-xs text-(--text-secondary)"
                  title={commit.id}
                >
                  {formatSHA(commit.id)}
                </span>
                <JumpLink
                  className="min-w-0 [overflow-wrap:anywhere]"
                  onClick={() => actions.select({ kind: "commit", id: commit.id })}
                >
                  {commitLabel(commit)}
                </JumpLink>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </Item>
  );
}

/** A session the host draws itself, such as one running on the pull request
 *  now. It is listed first, and in place of a session of `state` with the
 *  same id: one session is one row, whatever told the host about it. */
export type HostSessionView = {
  id: string;
  label: string;
  /** Marks it on the rail: `live` while it works, `attention` while it waits
   *  on the reader. */
  indicator?: "live" | "attention";
  /** Its card's content in the list; `open` shows it alone. */
  renderCard: (open: () => void) => ReactNode;
  /** The pane under its name when it is opened alone. */
  renderPane: () => ReactNode;
};

/** The Sessions pane: every session of the list, or the one `selected`. It
 *  takes the sessions as data and never fetches them; `renderSession` adds a
 *  host's own content, such as the transcript, under the one session opened.
 *  `hosted` sessions are the host's own rows (see `HostSessionView`), and
 *  `empty` says what puts a session in the list when none is. */
export function SessionList({
  state,
  selected,
  actions,
  renderSession,
  focusTurn,
  hosted = [],
  empty = "No agent session on this machine matches a commit of this pull request.",
}: {
  state: SessionListState;
  selected: string | null;
  actions: ReviewViewerActions;
  renderSession?: (session: SessionView, focusTurn?: number) => ReactNode;
  focusTurn?: number;
  hosted?: HostSessionView[];
  empty?: ReactNode;
}) {
  const own = selected ? hosted.find((session) => session.id === selected) : undefined;
  if (own) {
    return (
      <>
        <CardHeader size="pane" title={own.label} />
        <div className="min-h-0 flex-1 xl:overflow-y-auto">{own.renderPane()}</div>
      </>
    );
  }
  const hostedItems = hosted.map((session) => (
    <Item key={session.id} id={`session-${session.id}`} className="space-y-2">
      {session.renderCard(() => actions.select({ kind: "session", id: session.id }))}
    </Item>
  ));
  if (state.status !== "ready") {
    return (
      <>
        <CardHeader size="pane" title="Sessions" />
        {hosted.length > 0 ? <ol className="flex flex-col gap-3 pb-3">{hostedItems}</ol> : null}
        {state.status === "loading" ? (
          <div role="status" aria-label="Loading sessions" className="space-y-3">
            <p className="text-base text-(--text-tertiary)">
              Reading the sessions on this machine. The rest of the review is ready to read.
            </p>
            <Skeleton height={96} />
            <Skeleton height={96} />
          </div>
        ) : (
          <Failed>The sessions could not be loaded.</Failed>
        )}
      </>
    );
  }
  const { note } = state;
  const hostedIds = new Set(hosted.map((session) => session.id));
  const sessions = state.sessions.filter((session) => !hostedIds.has(session.id));
  const picked = selected ? sessions.filter((session) => session.id === selected) : [];
  // A session no longer listed (one that stopped running unlinked, say)
  // leaves the whole list in view rather than an empty pane.
  const single = picked.length > 0;
  const shown = single ? picked : sessions;
  const total = hosted.length + sessions.length;
  const count = pluralize("session", total, true);
  // One session opened with the host's own view of it (its transcript, which
  // names its agent, branch, times and size): the pane is headed by the
  // session and keeps only its linked commits, rather than a card repeating
  // what the transcript says.
  if (single && renderSession) {
    const session = shown[0]!;
    const commits = session.commits ?? [];
    const seconds = durationSeconds(session.startedAt, session.endedAt);
    const facts = [
      harnessName(session.vendor),
      session.branch,
      session.startedAt
        ? `${formatDate(session.startedAt)} ${formatTime(session.startedAt)}`
        : null,
      seconds === null ? null : formatDuration(seconds),
      session.editedFiles ? `${pluralize("file", session.editedFiles, true)} edited` : null,
    ].filter(Boolean);
    return (
      <>
        <CardHeader size="pane" title={sessionLabel(session)} detail={facts.join(" · ")} />
        {commits.length > 0 ? (
          <ul aria-label="Linked commits" className="flex flex-col gap-0.5 pb-3">
            {commits.map((commit) => (
              <li key={commit.id} className="flex min-w-0 items-center gap-2 text-xs">
                <CommitIcon size={13} className="flex-none text-(--text-tertiary)" />
                <span className="flex-none font-mono text-(--text-tertiary)" title={commit.id}>
                  {formatSHA(commit.id)}
                </span>
                <JumpLink
                  className="min-w-0 [overflow-wrap:anywhere]"
                  onClick={() => actions.select({ kind: "commit", id: commit.id })}
                >
                  {commitLabel(commit)}
                </JumpLink>
              </li>
            ))}
          </ul>
        ) : null}
        <div className="min-h-0 flex-1 xl:overflow-y-auto">{renderSession(session, focusTurn)}</div>
      </>
    );
  }
  return (
    <>
      <CardHeader
        size="pane"
        title="Sessions"
        detail={total === 0 ? undefined : single ? `1 of ${count}` : count}
        aside={
          single ? (
            <JumpLink className="text-xs" onClick={() => actions.select({ kind: "sessions" })}>
              All sessions →
            </JumpLink>
          ) : undefined
        }
      />
      {note ? <p className="pb-3 text-base text-(--text-secondary)">{note}</p> : null}
      <div className="min-h-0 flex-1 xl:overflow-y-auto">
        {total === 0 ? (
          <Empty title="No agent sessions">{empty}</Empty>
        ) : (
          <ol className="flex flex-col gap-3">
            {single ? null : hostedItems}
            {shown.map((session) => (
              <SessionCard
                key={session.id}
                session={session}
                actions={actions}
                openable={!single}
              />
            ))}
          </ol>
        )}
      </div>
    </>
  );
}
