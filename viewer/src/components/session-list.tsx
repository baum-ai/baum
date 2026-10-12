import pluralize from "pluralize";
import type { ReactNode } from "react";

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
        {session.outcome ? (
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

/** The Sessions pane: every session of the list, or the one `selected`. It
 *  takes the sessions as data and never fetches them; `renderSession` adds a
 *  host's own content, such as the transcript, under the one session opened. */
export function SessionList({
  state,
  selected,
  actions,
  renderSession,
}: {
  state: SessionListState;
  selected: string | null;
  actions: ReviewViewerActions;
  renderSession?: (session: SessionView) => ReactNode;
}) {
  if (state.status !== "ready") {
    return (
      <>
        <CardHeader size="pane" title="Sessions" />
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
  const { sessions, note } = state;
  const shown = selected ? sessions.filter((session) => session.id === selected) : sessions;
  const single = Boolean(selected) && shown.length > 0;
  const count = pluralize("session", sessions.length, true);
  return (
    <>
      <CardHeader
        size="pane"
        title="Sessions"
        detail={
          sessions.length === 0
            ? "No agent session is linked to this pull request."
            : single
              ? `1 of ${count}`
              : count
        }
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
        {shown.length === 0 ? (
          <Empty title="No agent session is linked to this">
            Either nobody used an agent here, or no session on this machine matches a commit.
          </Empty>
        ) : (
          <ol className="flex flex-col gap-3">
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
        {single && renderSession ? renderSession(shown[0]!) : null}
      </div>
    </>
  );
}
