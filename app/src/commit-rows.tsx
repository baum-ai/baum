import type { CommitListState } from "@baum/viewer/components/commit-list";
import { formatDate, formatTime } from "@baum/viewer/components/format";
import type { ReviewListSlot, ReviewViewerActions } from "@baum/viewer/components/review-viewer";
import { CardHeader } from "@baum/viewer/components/ui";

/** How a session was matched to a commit, in a few words. */
const METHODS: Record<string, string> = {
  entire_checkpoint: "recorded by Entire",
  commit_ref: "named in the session",
  start_commit: "started from this commit",
  files_overlap: "edited the same files",
};

/** The pull request's commits as the app's lists draw their rows: the
 *  subject, which opens the commit, its id, author and time, and the session
 *  linked to it, which opens in the Sessions pane. */
function CommitRows({
  commits,
  actions,
}: {
  commits: Extract<CommitListState, { status: "ready" }>["commits"];
  actions: ReviewViewerActions;
}) {
  return (
    <>
      <CardHeader
        size="pane"
        title="Commits"
        detail={
          commits.length === 0
            ? "No commit was found for this pull request."
            : `${commits.length === 1 ? "1 commit" : `${commits.length} commits`} · oldest first`
        }
      />
      {commits.length > 0 ? (
        <ul className="local-commit-list" aria-label="Commits">
          {commits.map((commit) => {
            const link = commit.links[0];
            return (
              // A row the review's keys move through (j and k).
              <li key={commit.id} className="list-row local-commit-row" data-review-block="">
                <button
                  type="button"
                  data-block-open=""
                  className="local-commit-title"
                  onClick={() => actions.select({ kind: "commit", id: commit.id })}
                >
                  {commit.subject || commit.id.slice(0, 7)}
                </button>
                <p className="local-commit-meta">
                  <code title={commit.id}>{commit.id.slice(0, 7)}</code>
                  {commit.author ? <span>{commit.author}</span> : null}
                  {commit.committedAt ? (
                    <span>
                      {formatDate(commit.committedAt)} {formatTime(commit.committedAt)}
                    </span>
                  ) : null}
                </p>
                {link ? (
                  <p className="local-commit-meta">
                    <button
                      type="button"
                      className="local-commit-session"
                      onClick={() => actions.select({ kind: "session", id: link.session })}
                    >
                      {link.label}
                    </button>
                    <span>
                      {METHODS[link.method] ?? link.method.replaceAll("_", " ")} ·{" "}
                      {Math.round(Math.min(1, Math.max(0, link.confidence)) * 100)}%
                    </span>
                    {commit.links.length > 1 ? (
                      <span>and {commit.links.length - 1} more</span>
                    ) : null}
                  </p>
                ) : null}
              </li>
            );
          })}
        </ul>
      ) : null}
    </>
  );
}

/** The Commits group with its whole list drawn as the app's lists are; a
 *  commit opened alone, and a list still loading or that failed, stay the
 *  viewer's. */
export function withCommitRows(slot: ReviewListSlot, state: CommitListState): ReviewListSlot {
  return {
    ...slot,
    renderPane: (selected, actions) =>
      selected === null && state.status === "ready" ? (
        <CommitRows commits={state.commits} actions={actions} />
      ) : (
        slot.renderPane(selected, actions)
      ),
  };
}
