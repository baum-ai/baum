import type { ReactNode } from "react";

import { type CommitListState, CommitList } from "./commit-list";
import { commitLabel, sessionLabel } from "./list-labels";
import type { ReviewListSlot } from "./review-viewer";
import { SessionList, type SessionListState, type SessionView } from "./session-list";

/** The Sessions group for `ReviewViewer`'s `sessions` prop: the rail lists the
 *  sessions by title (else by agent) and the pane is a `SessionList`, with
 *  `renderSession` under a session opened alone. A loading or failed state is
 *  passed through, so a link to the group survives while a host is still
 *  finding out. */
export function sessionsSlot(
  state: SessionListState,
  options: { renderSession?: (session: SessionView) => ReactNode } = {},
): ReviewListSlot {
  return {
    state:
      state.status === "ready"
        ? {
            status: "ready",
            items: state.sessions.map((session) => ({
              id: session.id,
              label: sessionLabel(session),
            })),
          }
        : { status: state.status },
    renderPane: (selected, actions) => (
      <SessionList
        state={state}
        selected={selected}
        actions={actions}
        renderSession={options.renderSession}
      />
    ),
  };
}

/** The Commits group for `ReviewViewer`'s `commits` prop: the rail lists the
 *  commits by subject (else by short id) and the pane is a `CommitList`. */
export function commitsSlot(state: CommitListState): ReviewListSlot {
  return {
    state:
      state.status === "ready"
        ? {
            status: "ready",
            items: state.commits.map((commit) => ({ id: commit.id, label: commitLabel(commit) })),
          }
        : { status: state.status },
    renderPane: (selected, actions) => (
      <CommitList state={state} selected={selected} actions={actions} />
    ),
  };
}
