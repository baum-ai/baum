import {
  type ComponentType,
  memo,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { formatRelative } from "@baum/viewer/components/format";
import { SessionCard } from "@baum/viewer/components/session-card";
import { Markdown } from "@baum/viewer/components/markdown";
import { Alert, Button } from "@baum/viewer/components/ui";
import { parseTranscript, type LocalTranscript } from "@baum/viewer/local-transcript";
import type { TranscriptSource } from "@baum/viewer/transcript";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import { agentName, appURL, LocalRequestError, request, type LiveSession } from "./api";
import { useNow } from "./live";
import { LoadingStatus } from "./nine-dots";
import { absolute, Agent, SessionState, SessionWhere } from "./sessions";
import { PageBar } from "./shell";

/** How often an open transcript is read again without a `live` event: a
 *  transcript grows between hook events (the agent's replies are written
 *  without one), and the daemon answers an unchanged one in a few bytes. A
 *  session waiting for its user grows only once it is given a prompt, which
 *  is an event, so it is read less often; an ended one not at all. */
export const POLL_RUNNING = 3000;
export const POLL_IDLE = 15_000;
/** A `live` event is followed by a read this long after the last of a burst. */
export const LIVE_DEBOUNCE = 500;

/** The daemon's answer for a live session's transcript: the transcript, or
 *  null when the request named the version it has; or why there is none. */
export type LiveTranscriptAnswer =
  | {
      available: true;
      version: string;
      live: boolean;
      transcript: LocalTranscript | null;
      /** The session's latest compaction summary, masked; sent with the
       *  transcript, absent before the agent compacted the session. */
      summary?: string;
    }
  | { available: false; live: boolean; message: string };

export function parseLiveTranscript(body: unknown): LiveTranscriptAnswer | null {
  if (typeof body !== "object" || body === null) return null;
  const answer = body as Record<string, unknown>;
  const live = answer.live === true;
  if (answer.available === false)
    return {
      available: false,
      live,
      message:
        typeof answer.message === "string" ? answer.message : "The transcript is not available.",
    };
  if (answer.available !== true || typeof answer.version !== "string") return null;
  if (answer.transcript === undefined)
    return { available: true, version: answer.version, live, transcript: null };
  const transcript = parseTranscript(answer.transcript);
  const summary = typeof answer.summary === "string" && answer.summary ? answer.summary : undefined;
  return transcript
    ? {
        available: true,
        version: answer.version,
        live,
        transcript,
        ...(summary ? { summary } : {}),
      }
    : null;
}

export type LiveTranscriptState =
  | { kind: "loading" }
  | { kind: "ready"; version: string; transcript: LocalTranscript; summary?: string }
  | { kind: "unavailable"; message: string }
  | { kind: "failed"; message: string };

const transcriptPath = (agent: string, id: string, version: string | null) =>
  `api/live-sessions/${encodeURIComponent(agent)}/${encodeURIComponent(id)}/transcript${
    version ? `?version=${encodeURIComponent(version)}` : ""
  }`;

/** A live session's transcript, read when it opens, `LIVE_DEBOUNCE` after
 *  each change of `tick` (the app's count of `live` events) and every `poll`
 *  milliseconds while that is set. Each read names the version shown, and
 *  the state changes only for another version or an answer of another kind,
 *  so an unchanged transcript renders nothing again. Reads never overlap: one
 *  asked for during another follows it. A failed read keeps a transcript
 *  shown. */
export function useLiveTranscript(
  agent: string,
  id: string,
  { tick, poll }: { tick: number; poll: number | null },
): { state: LiveTranscriptState; retry: () => void } {
  const [state, setState] = useState<LiveTranscriptState>({ kind: "loading" });
  const [attempt, setAttempt] = useState(0);
  const read = useRef<() => void>(() => {});
  useEffect(() => {
    let stopped = false;
    let known: string | null = null;
    let busy = false;
    let again = false;
    setState({ kind: "loading" });
    const run = async () => {
      if (busy) {
        again = true;
        return;
      }
      busy = true;
      try {
        do {
          again = false;
          let next: LiveTranscriptState | null = null;
          try {
            const answer = parseLiveTranscript(
              await request<unknown>(transcriptPath(agent, id, known)),
            );
            if (!answer) throw Error("The daemon's answer was not a transcript.");
            if (!answer.available) {
              known = null;
              next = { kind: "unavailable", message: answer.message };
            } else if (answer.transcript) {
              known = answer.version;
              next = {
                kind: "ready",
                version: answer.version,
                transcript: answer.transcript,
                summary: answer.summary,
              };
            } else if (answer.version !== known) {
              // Told "unchanged" of a version this view does not have.
              known = null;
              again = true;
            }
          } catch (error) {
            const message =
              error instanceof LocalRequestError && error.code === "live_session_not_found"
                ? "This session is no longer listed, so its transcript is not served."
                : "The transcript could not be read.";
            next = { kind: "failed", message };
          }
          if (stopped) return;
          if (next) {
            const update = next;
            setState((current) => {
              if (update.kind === "failed" && current.kind === "ready") return current;
              if (
                update.kind === "unavailable" &&
                current.kind === "unavailable" &&
                current.message === update.message
              )
                return current;
              return update;
            });
          }
        } while (again && !stopped);
      } finally {
        busy = false;
      }
    };
    read.current = () => void run();
    void run();
    return () => {
      stopped = true;
      read.current = () => {};
    };
  }, [agent, id, attempt]);
  const first = useRef(tick);
  useEffect(() => {
    if (tick === first.current) return;
    const timer = setTimeout(() => read.current(), LIVE_DEBOUNCE);
    return () => clearTimeout(timer);
  }, [tick]);
  useEffect(() => {
    if (!poll) return;
    const timer = setInterval(() => read.current(), poll);
    return () => clearInterval(timer);
  }, [poll, agent, id]);
  const retry = useCallback(() => setAttempt((n) => n + 1), []);
  return { state, retry };
}

/** How close to the end of the page counts as at the end. */
const BOTTOM_SLACK = 48;

function atBottom() {
  const page = document.scrollingElement ?? document.documentElement;
  return window.innerHeight + window.scrollY >= page.scrollHeight - BOTTOM_SLACK;
}

/** Follow mode: while the reader is at the end of the page, each new
 *  `version` keeps them there; once they scroll up, their place is kept.
 *  `follow` is where a reader starts: at the end of a running session. The
 *  reader's position counts only once the first version is shown, since the
 *  page around a loading transcript scrolls by itself. */
export function useFollowScroll(version: string | undefined, follow: boolean) {
  const pinned = useRef(follow);
  const shown = useRef(false);
  useEffect(() => {
    const onScroll = () => {
      if (shown.current) pinned.current = atBottom();
    };
    addEventListener("scroll", onScroll, { passive: true });
    return () => removeEventListener("scroll", onScroll);
  }, []);
  useLayoutEffect(() => {
    if (!version) return;
    shown.current = true;
    if (pinned.current) {
      const page = document.scrollingElement ?? document.documentElement;
      window.scrollTo({ top: page.scrollHeight });
    }
  }, [version]);
}

/** A session's transcript, opened from the Sessions page: the session as the
 *  page lists it, then its transcript drawn as a review's linked session is,
 *  following the session while it runs. `session` is null when the list does
 *  not have it. */
export function SessionTranscriptPage({
  agent,
  id,
  session,
  tick,
  prHref,
  Link,
}: {
  agent: string;
  id: string;
  session: LiveSession | null;
  tick: number;
  prHref: (session: LiveSession) => string | null;
  Link: ComponentType<ViewerLinkProps>;
}) {
  const now = useNow();
  const back = (
    <Link href={appURL("?page=sessions")} className="inbox-action">
      Back to sessions
    </Link>
  );
  const poll =
    session?.state === "running" ? POLL_RUNNING : session?.state === "idle" ? POLL_IDLE : null;
  const { state, retry } = useLiveTranscript(agent, id, { tick, poll });
  useEffect(() => {
    window.scrollTo({ top: 0 });
  }, [agent, id]);
  useFollowScroll(state.kind === "ready" ? state.version : undefined, session?.state === "running");
  return (
    <>
      <PageBar
        title={session?.title || `${agentName(agent)} session`}
        detail={
          session ? `${agentName(agent)} · ${session.repository}` : "Agent session on this computer"
        }
        actions={back}
      />
      <div className="local-page-body">
        {session ? (
          <section aria-label="Session" className="local-panel local-session-head">
            <div className="local-session-agent">
              <Agent agent={session.agent} />
              <SessionState session={session} />
            </div>
            <SessionWhere session={session} prHref={prHref} Link={Link} />
            <div className="local-session-times">
              {session.state === "ended" && session.ended_at ? (
                <span title={absolute(session.ended_at)}>
                  Ended {formatRelative(session.ended_at, now)}
                </span>
              ) : (
                <span title={absolute(session.last_activity_at)}>
                  Active {formatRelative(session.last_activity_at, now)}
                </span>
              )}
              <span title={absolute(session.started_at)}>
                Started {formatRelative(session.started_at, now)}
              </span>
            </div>
          </section>
        ) : null}
        <TranscriptBody
          state={state}
          retry={retry}
          live={session !== null && session.state !== "ended"}
          title={session?.title}
        />
      </div>
    </>
  );
}

/** The transcript, drawn again only for another state: a read that found
 *  the same version, or a `live` event, leaves it as it is. */
const TranscriptBody = memo(function TranscriptBody({
  state,
  retry,
  live,
  title,
}: {
  state: LiveTranscriptState;
  retry: () => void;
  /** The session still runs, so it has no outcome yet. */
  live: boolean;
  /** The listed session's title, for a transcript that holds none, such as
   *  a Codex rollout or one named only by its first prompt. */
  title?: string;
}) {
  // The card's loader answers from the transcript shown, and its revision
  // has the card load an opened turn's activity again when that changes.
  const latest = useRef<LocalTranscript | null>(null);
  latest.current = state.kind === "ready" ? state.transcript : null;
  const version = state.kind === "ready" ? state.version : undefined;
  const source = useMemo<TranscriptSource>(
    () => ({
      revision: version,
      loadTurnActivity: (_run, turn) => {
        const activity = latest.current?.activity[turn];
        return activity ? Promise.resolve(activity) : Promise.reject(Error("No activity"));
      },
    }),
    [version],
  );
  switch (state.kind) {
    case "loading":
      return <LoadingStatus block>Reading redacted local transcript…</LoadingStatus>;
    case "unavailable":
      return (
        <section className="local-panel local-transcript-missing">
          <h2>No transcript to show</h2>
          <p>{state.message}</p>
        </section>
      );
    case "failed":
      return (
        <Alert>
          {state.message} <Button onClick={retry}>Retry</Button>
        </Alert>
      );
  }
  const { transcript } = state;
  // A running session's transcript has no outcome to report; the card would
  // say "unknown" where the session is simply not done.
  const session = {
    ...transcript.session,
    title: transcript.session.title || title || "",
    outcome:
      live && transcript.session.outcome === "unknown" ? "in progress" : transcript.session.outcome,
  };
  return (
    <section aria-label="Transcript" className="local-transcript">
      {state.summary ? (
        <details className="local-panel local-session-summary">
          <summary>
            <span className="local-session-summary-title">Summary of the earlier conversation</span>
            <span className="local-session-summary-note">
              Written by the agent when it compacted the session
            </span>
          </summary>
          <Markdown html={false} className="local-session-summary-body">
            {state.summary}
          </Markdown>
        </details>
      ) : null}
      <p className="local-transcript-note">
        Local transcript; secrets and e-mail addresses masked.
        {transcript.truncated ? " Some content was omitted." : ""}
      </p>
      <div className="local-panel local-transcript-card">
        <SessionCard defaultOpen={true} session={session} source={source} />
      </div>
    </section>
  );
});
