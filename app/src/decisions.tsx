import { createContext, useContext, useEffect, useState } from "react";
import { formatRelative, formatDateTime } from "@baum/viewer/components/format";
import { Badge } from "@baum/viewer/components/ui";
import {
  agentName,
  request,
  skipReasonText,
  type CapturedDecision,
  type LiveSession,
  type PullRequestDecisions,
  type SessionDecisions,
} from "./api";
import { useNow } from "./live";

/** Counts the daemon's `decisions` events, which carry no data: a reader of
 *  `api/decisions` reads again when it changes. */
export const DecisionsRevision = createContext(0);

/** Reads `path` again on every `decisions` event, keeping the last answer
 *  meanwhile, and again when `again` changes. Null while nothing was read,
 *  and for a daemon without decision capture. */
function useDecisions<T extends object>(
  path: string,
  pick: (body: unknown) => T | null,
  again = 0,
): T | null {
  const revision = useContext(DecisionsRevision);
  const [answer, setAnswer] = useState<{ path: string; value: T | null } | null>(null);
  useEffect(() => {
    const abort = new AbortController();
    request<unknown>(path, undefined, "GET", abort.signal).then(
      (body) => {
        if (!abort.signal.aborted) setAnswer({ path, value: pick(body) });
      },
      () => {
        // A failed read keeps what is shown.
      },
    );
    return () => abort.abort();
  }, [path, pick, revision, again]);
  return answer?.path === path ? answer.value : null;
}

const parseSession = (body: unknown) =>
  (body as { session?: SessionDecisions } | null)?.session?.decisions
    ? (body as { session: SessionDecisions }).session
    : null;
const parsePullRequest = (body: unknown) =>
  Array.isArray((body as PullRequestDecisions | null)?.sessions)
    ? (body as PullRequestDecisions)
    : null;

const captureOff: Record<string, string> = {
  paused: "Capture is paused with automatic reviews.",
  off: "Decision capture is off in Settings.",
  repository_off: "Capture is off for this repository.",
  not_picked: "Capture runs only in picked repositories.",
};

/** Where a session's capture stands: a call in flight, why it will not run,
 *  why it was skipped, when it was last looked at, or not yet. */
export function captureState(s: SessionDecisions, now: number): string {
  if (s.capturing) return "Capturing…";
  if (s.capture_off) return captureOff[s.capture_off] ?? "Capture is off.";
  if (s.coverage === "skipped") return skipReasonText(s.skip_reason);
  if (s.updated_at) return `Updated ${formatRelative(s.updated_at, now)}`;
  return "Not captured yet: decisions are captured once the session pauses.";
}

const who: Record<string, string> = {
  human: "You decided",
  agent: "Agent decided",
  agent_proposed_human_approved: "Agent proposed, you approved",
};
const label = (value: string) =>
  value ? value[0]!.toUpperCase() + value.slice(1).replaceAll("_", " ") : value;

/** "Decisions (n)" for a session in the PR page's Sessions in progress panel:
 *  its captured decisions' titles behind a disclosure, and where its capture
 *  stands. */
export function LiveSessionDecisions({ session }: { session: LiveSession }) {
  const now = useNow();
  const s = useDecisions(
    `api/decisions?agent=${encodeURIComponent(session.agent)}&session=${encodeURIComponent(session.session_id)}`,
    parseSession,
  );
  if (!s) return null;
  const state = captureState(s, now);
  if (s.decisions.length === 0)
    return (
      <p className="local-live-decisions">
        Decisions (0) <span className="local-live-decisions-state">· {state}</span>
      </p>
    );
  return (
    <details className="local-live-decisions">
      <summary>
        Decisions ({s.decisions.length}){" "}
        <span className="local-live-decisions-state">· {state}</span>
      </summary>
      <ul>
        {s.decisions.map((d) => (
          <li key={d.decision_id}>{d.title || d.summary}</li>
        ))}
      </ul>
    </details>
  );
}

function DecisionRow({ decision: d }: { decision: CapturedDecision }) {
  const details = [d.rationale, d.rejected_alternative && `Rejected: ${d.rejected_alternative}`]
    .filter(Boolean)
    .join(" ");
  return (
    <li className="local-decision">
      <div className="local-decision-head">
        <span className="local-decision-title">{d.title || d.summary}</span>
        {d.provisional ? (
          <Badge
            tone="caution"
            size="compact"
            title="Made after the saved review was generated; the review has not seen it."
          >
            Provisional
          </Badge>
        ) : null}
        {d.superseded ? (
          <Badge size="compact" title="A later decision in the session replaced it.">
            Superseded
          </Badge>
        ) : null}
      </div>
      {d.title && d.summary ? <p className="local-decision-summary">{d.summary}</p> : null}
      {details ? <p className="local-decision-why">{details}</p> : null}
      <p className="local-decision-meta">
        {label(d.category)} · {who[d.who] ?? label(d.who)} · {formatDateTime(d.decided_at)}
        {d.files.length
          ? ` · ${d.files.length === 1 ? d.files[0] : `${d.files.length} files`}`
          : ""}
      </p>
    </li>
  );
}

/** The decisions of the sessions linked to a pull request, on its page:
 *  those in progress and those its saved review links. A decision made after
 *  the saved review was generated is provisional. Nothing is drawn until a
 *  session has decisions or a capture state worth showing. */
export function PullRequestDecisionsList({
  repository,
  number,
  live,
  revision,
}: {
  repository: string;
  number: number;
  /** Titles of the sessions in progress, which the answer leaves out. */
  live: LiveSession[];
  /** Changes when the page's linked sessions may have changed. */
  revision: number;
}) {
  const now = useNow();
  const answer = useDecisions(
    `api/decisions?repository=${encodeURIComponent(repository)}&pr=${number}`,
    parsePullRequest,
    revision,
  );
  const sessions = (answer?.sessions ?? []).filter(
    (s) => s.decisions.length > 0 || s.coverage || s.capturing,
  );
  if (sessions.length === 0) return null;
  const total = sessions.reduce((n, s) => n + s.decisions.length, 0);
  const provisional = sessions.reduce(
    (n, s) => n + s.decisions.filter((d) => d.provisional).length,
    0,
  );
  return (
    <section aria-labelledby="pr-decisions-heading" className="mb-3 local-panel local-decisions">
      <h3 id="pr-decisions-heading">Decisions from sessions ({total})</h3>
      {provisional ? (
        <p className="local-decisions-note">
          {provisional === 1 ? "One decision was" : `${provisional} decisions were`} made after the
          saved review was generated
          {answer?.review_generated_at ? ` (${formatDateTime(answer.review_generated_at)})` : ""}:
          the review has not seen {provisional === 1 ? "it" : "them"}.
        </p>
      ) : null}
      {sessions.map((s) => {
        const title =
          s.title ||
          live.find((l) => l.agent === s.agent && l.session_id === s.session_id)?.title ||
          s.session_id;
        return (
          <div key={`${s.agent}:${s.session_id}`} className="local-decisions-session">
            <h4>
              <span>{agentName(s.agent)}</span>
              <span className="local-decisions-session-title">{title}</span>
              {s.live ? (
                <Badge tone="info" size="compact">
                  In progress
                </Badge>
              ) : null}
            </h4>
            <p className="local-decisions-state">{captureState(s, now)}</p>
            {s.decisions.length ? (
              <ul>
                {s.decisions.map((d) => (
                  <DecisionRow key={d.decision_id} decision={d} />
                ))}
              </ul>
            ) : null}
          </div>
        );
      })}
    </section>
  );
}
