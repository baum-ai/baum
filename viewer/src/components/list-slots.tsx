import type { ReactNode } from "react";

import { type CommitListState, type CommitLoaders, CommitList } from "./commit-list";
import { commitLabel, sessionLabel } from "./list-labels";
import type { ReviewListSlot } from "./review-viewer";
import {
  type HostSessionView,
  SessionList,
  type SessionListState,
  type SessionView,
} from "./session-list";

/** The Sessions group for `ReviewViewer`'s `sessions` prop: the rail lists the
 *  sessions by title (else by agent) and the pane is a `SessionList`, with
 *  `renderSession` under a session opened alone. The host's own `hosted`
 *  sessions (see `HostSessionView`) come first, are counted with the rest
 *  and mark the group while one is happening. A loading or failed state is
 *  passed through, so a link to the group survives while a host is still
 *  finding out. */
export function sessionsSlot(
  state: SessionListState,
  options: {
    renderSession?: (session: SessionView) => ReactNode;
    hosted?: HostSessionView[];
    empty?: ReactNode;
  } = {},
): ReviewListSlot {
  const hosted = options.hosted ?? [];
  const hostedIds = new Set(hosted.map((session) => session.id));
  const groupIndicator = hosted.some((session) => session.indicator === "attention")
    ? "attention"
    : hosted.some((session) => session.indicator === "live")
      ? "live"
      : undefined;
  return {
    state:
      state.status === "ready"
        ? {
            status: "ready",
            items: [
              ...hosted.map(({ id, label, indicator }) => ({ id, label, indicator })),
              ...state.sessions
                .filter((session) => !hostedIds.has(session.id))
                .map((session) => ({ id: session.id, label: sessionLabel(session) })),
            ],
          }
        : { status: state.status },
    indicator: groupIndicator,
    renderPane: (selected, actions) => (
      <SessionList
        state={state}
        selected={selected}
        actions={actions}
        renderSession={options.renderSession}
        hosted={hosted}
        empty={options.empty}
      />
    ),
  };
}

/** The Commits group for `ReviewViewer`'s `commits` prop: the rail lists the
 *  commits by subject (else by short id) and the pane is a `CommitList`. With
 *  the host's `loadCommit` (and `loadPatch`), a commit opened alone also shows
 *  its message body, totals and changed files, each file's diff read when it
 *  is opened. Keep the loaders stable across renders: they key what is read. */
export function commitsSlot(state: CommitListState, loaders: CommitLoaders = {}): ReviewListSlot {
  return {
    state:
      state.status === "ready"
        ? {
            status: "ready",
            items: state.commits.map((commit) => ({ id: commit.id, label: commitLabel(commit) })),
          }
        : { status: state.status },
    renderPane: (selected, actions) => (
      <CommitList state={state} selected={selected} actions={actions} loaders={loaders} />
    ),
  };
}
