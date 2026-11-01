import { createContext, useContext, useEffect, useId, useState } from "react";
import { formatRelative, formatDateTime, formatTime } from "@baum/viewer/components/format";
import { ChevronRightIcon } from "@baum/viewer/components/icons";
import { InlineMarkdown } from "@baum/viewer/components/markdown";
import { Item } from "@baum/viewer/components/review-parts";
import { Badge, CardHeader } from "@baum/viewer/components/ui";
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
  again: number | string = 0,
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

/** Where a session's capture stands: a call in flight, why it was skipped,
 *  when it was last looked at, or not yet; and why capture will not run
 *  again, beside what it did before. */
export function captureState(s: SessionDecisions, now: number): string {
  if (s.capturing) return "Capturing…";
  const off = s.capture_off ? (captureOff[s.capture_off] ?? "Capture is off.") : "";
  const state =
    s.coverage === "skipped"
      ? skipReasonText(s.skip_reason)
      : s.updated_at
        ? `Updated ${formatRelative(s.updated_at, now)}`
        : off
          ? ""
          : "Not captured yet: decisions are captured once the session pauses.";
  return [state, off].filter(Boolean).join(" · ");
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

/** A session's decisions split by who made them: the person's own and those
 *  they approved first, then the agent's alone; a group with none is left
 *  out, and each keeps the session's order. */
function decisionGroups(decisions: CapturedDecision[]) {
  const yours = decisions.filter((d) => d.who !== "agent");
  const agent = decisions.filter((d) => d.who === "agent");
  return [
    { key: "you", title: "Decided by you", decisions: yours },
    { key: "agent", title: "Decided by the agent", decisions: agent },
  ].filter((group) => group.decisions.length > 0);
}

/** One decision as a row to scan: its title, kind and time, with its summary
 *  on a line under it; opened, the whole summary, why it was made and the
 *  files it touched. Who decided is the group it is listed under. */
function DecisionRow({ decision: d }: { decision: CapturedDecision }) {
  const [open, setOpen] = useState(false);
  const id = useId();
  const title = d.title || d.summary;
  const summary = d.title && d.summary ? d.summary : "";
  return (
    <li className="local-decision" data-review-block="">
      <button
        type="button"
        className="local-decision-head"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => setOpen((value) => !value)}
      >
        <ChevronRightIcon size={12} className="local-decision-chevron" />
        <span className="local-decision-text">
          <span className="local-decision-title">
            <InlineMarkdown>{title}</InlineMarkdown>
          </span>
          {summary && !open ? (
            <span className="local-decision-glance">
              <InlineMarkdown>{summary}</InlineMarkdown>
            </span>
          ) : null}
        </span>
        <span className="local-decision-tags">
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
          <Badge size="compact">{label(d.category)}</Badge>
          <time
            className="local-decision-when"
            dateTime={d.decided_at}
            title={formatDateTime(d.decided_at)}
          >
            {formatTime(d.decided_at)}
          </time>
        </span>
      </button>
      {open ? (
        <div id={id} className="local-decision-body">
          {summary ? (
            <p className="local-decision-summary">
              <InlineMarkdown>{summary}</InlineMarkdown>
            </p>
          ) : null}
          <dl className="local-decision-facts">
            {d.rationale ? (
              <div>
                <dt>Why</dt>
                <dd>
                  <InlineMarkdown>{d.rationale}</InlineMarkdown>
                </dd>
              </div>
            ) : null}
            {d.files.length ? (
              <div>
                <dt>{d.files.length === 1 ? "File" : "Files"}</dt>
                <dd className="local-decision-files">
                  {d.files.map((file) => (
                    <code key={file} title={file}>
                      {file}
                    </code>
                  ))}
                </dd>
              </div>
            ) : null}
          </dl>
        </div>
      ) : null}
    </li>
  );
}

/** What `api/decisions` answers for a pull request: its sessions with
 *  decisions or a capture state worth showing, and the decisions counted. */
export type PullRequestDecisionsRead = {
  sessions: NonNullable<ReturnType<typeof parsePullRequest>>["sessions"];
  total: number;
  /** Those made after the saved review was generated, which it has not seen. */
  provisional: number;
  reviewGeneratedAt?: string;
};

/** The decisions of the sessions linked to a pull request: those in progress
 *  and those its saved review links. Null until read, and while no session has
 *  decisions or a capture state worth showing. `revision` changes when the
 *  page's linked sessions or its latest saved review may have changed. */
export function usePullRequestDecisions(
  repository: string,
  number: number,
  revision: number | string,
): PullRequestDecisionsRead | null {
  const answer = useDecisions(
    `api/decisions?repository=${encodeURIComponent(repository)}&pr=${number}`,
    parsePullRequest,
    revision,
  );
  const sessions = (answer?.sessions ?? []).filter(
    (s) => s.decisions.length > 0 || s.coverage || s.capturing,
  );
  if (sessions.length === 0) return null;
  return {
    sessions,
    total: sessions.reduce((n, s) => n + s.decisions.length, 0),
    provisional: sessions.reduce((n, s) => n + s.decisions.filter((d) => d.provisional).length, 0),
    reviewGeneratedAt: answer?.review_generated_at,
  };
}

/** The decisions of several pull requests' sessions, as a stack's page shows
 *  them: a session several members link is one, with its decisions merged by
 *  id. One read per member; a member whose read fails is left out while
 *  another answers. Null until read and while nothing is worth showing. */
export function useGroupDecisions(
  repository: string,
  numbers: number[],
  revision: number | string,
): PullRequestDecisionsRead | null {
  const events = useContext(DecisionsRevision);
  const key = `${repository}\n${numbers.join(",")}`;
  const [answer, setAnswer] = useState<{ key: string; reads: PullRequestDecisions[] } | null>(null);
  useEffect(() => {
    if (numbers.length === 0) return;
    const abort = new AbortController();
    void Promise.allSettled(
      numbers.map((number) =>
        request<unknown>(
          `api/decisions?repository=${encodeURIComponent(repository)}&pr=${number}`,
          undefined,
          "GET",
          abort.signal,
        ).then(parsePullRequest),
      ),
    ).then((results) => {
      if (abort.signal.aborted) return;
      const reads = results.flatMap((r) =>
        r.status === "fulfilled" && r.value ? [r.value] : [],
      );
      // Every read failing keeps what is shown.
      if (reads.length > 0) setAnswer({ key, reads });
    });
    return () => abort.abort();
    // `key` stands for repository and numbers.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, events, revision]);
  if (answer?.key !== key) return null;
  const merged = new Map<string, PullRequestDecisions["sessions"][number]>();
  for (const read of answer.reads) {
    for (const s of read.sessions) {
      const id = `${s.agent}:${s.session_id}`;
      const have = merged.get(id);
      if (!have) {
        merged.set(id, { ...s, decisions: [...s.decisions] });
        continue;
      }
      const known = new Set(have.decisions.map((d) => d.decision_id));
      have.decisions.push(...s.decisions.filter((d) => !known.has(d.decision_id)));
      have.live = have.live || s.live;
      have.capturing = have.capturing || s.capturing;
      have.title ||= s.title;
      if (!have.coverage && s.coverage) {
        have.coverage = s.coverage;
        have.skip_reason = s.skip_reason;
      }
    }
  }
  const sessions = [...merged.values()].filter(
    (s) => s.decisions.length > 0 || s.coverage || s.capturing,
  );
  if (sessions.length === 0) return null;
  const generated = answer.reads
    .map((read) => read.review_generated_at)
    .filter((at): at is string => Boolean(at))
    .sort()
    .at(-1);
  return {
    sessions,
    total: sessions.reduce((n, s) => n + s.decisions.length, 0),
    provisional: sessions.reduce((n, s) => n + s.decisions.filter((d) => d.provisional).length, 0),
    reviewGeneratedAt: generated,
  };
}

/** A pull request's decisions, session by session, as its page's Decisions
 *  pane shows them. A decision made after the saved review was generated is
 *  provisional, and said so. */
export function PullRequestDecisionsPane({
  read,
  live,
  heading = false,
  scope = "this pull request",
}: {
  read: PullRequestDecisionsRead;
  /** Titles of the sessions in progress, which the answer leaves out. */
  live: LiveSession[];
  /** Headed as a pane of its own, as Commits is. */
  heading?: boolean;
  /** What the sessions are linked to, in the heading. */
  scope?: string;
}) {
  const now = useNow();
  const { sessions, total, provisional, reviewGeneratedAt } = read;
  return (
    <div className="local-decisions">
      {heading ? (
        <CardHeader
          size="pane"
          title="Decisions"
          detail={`${total === 1 ? "1 decision" : `${total} decisions`} from ${
            sessions.length === 1 ? "1 session" : `${sessions.length} sessions`
          } linked to ${scope}.`}
        />
      ) : null}
      {provisional ? (
        <p className="local-decisions-note">
          {provisional === 1 ? "One decision was" : `${provisional} decisions were`} made after the
          saved review was generated
          {reviewGeneratedAt ? ` (${formatDateTime(reviewGeneratedAt)})` : ""}: the review has not
          seen {provisional === 1 ? "it" : "them"}.
        </p>
      ) : null}
      <ol className="local-decisions-sessions">
      {sessions.map((s) => {
        const title =
          s.title ||
          live.find((l) => l.agent === s.agent && l.session_id === s.session_id)?.title ||
          s.session_id;
        return (
          <Item key={`${s.agent}:${s.session_id}`} className="local-decisions-session">
            <h3>
              <span>{agentName(s.agent)}</span>
              <span className="local-decisions-session-title">{title}</span>
              {s.live ? (
                <Badge tone="info" size="compact">
                  In progress
                </Badge>
              ) : null}
            </h3>
            <p className="local-decisions-state">{captureState(s, now)}</p>
            {decisionGroups(s.decisions).map((group) => (
              <section
                key={group.key}
                className="local-decisions-group"
                aria-label={group.title}
              >
                <h4>
                  {group.title}
                  <span className="local-decisions-group-count">{group.decisions.length}</span>
                </h4>
                <ul className="local-decisions-list">
                  {group.decisions.map((d) => (
                    <DecisionRow key={d.decision_id} decision={d} />
                  ))}
                </ul>
              </section>
            ))}
          </Item>
        );
      })}
      </ol>
    </div>
  );
}

/** The same decisions behind a closed disclosure, for a page with no
 *  Decisions pane, such as the walkthrough. */
export function PullRequestDecisionsList({
  repository,
  number,
  live,
  revision,
}: {
  repository: string;
  number: number;
  live: LiveSession[];
  revision: number | string;
}) {
  const read = usePullRequestDecisions(repository, number, revision);
  if (!read) return null;
  return (
    <section aria-labelledby="pr-decisions-heading" className="mb-3 local-panel">
      {/* Closed until asked for: a pull request's sessions can carry dozens of decisions. */}
      <details className="local-decisions-disclosure">
        <summary>
          <h3 id="pr-decisions-heading">Decisions from sessions ({read.total})</h3>
        </summary>
        <PullRequestDecisionsPane read={read} live={live} />
      </details>
    </section>
  );
}
