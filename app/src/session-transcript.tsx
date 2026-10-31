import {
  type ComponentType,
  memo,
  type RefObject,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import pluralize from "pluralize";
import { formatCompact, formatCount, formatRelative } from "@baum/viewer/components/format";
import { SessionCard } from "@baum/viewer/components/session-card";
import { SessionTranscript } from "@baum/viewer/components/transcript";
import { Markdown } from "@baum/viewer/components/markdown";
import { ChevronLeftIcon } from "@baum/viewer/components/icons";
import { Alert, Button } from "@baum/viewer/components/ui";
import { parseTranscript, type LocalTranscript } from "@baum/viewer/local-transcript";
import type { TranscriptSession, TranscriptSource } from "@baum/viewer/transcript";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import {
  agentName,
  appURL,
  LocalRequestError,
  request,
  type AgentMessage,
  type LiveSession,
} from "./api";
import { useNow } from "./live";
import { LoadingStatus } from "./nine-dots";
import {
  absolute,
  Agent,
  FocusSession,
  SessionReplyBar,
  SessionState,
  SessionWhere,
} from "./sessions";
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
      /** The media types of the images pasted with each prompt, by its
       *  turn; sent with the transcript, absent when none were. */
      images?: Record<string, string[]>;
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
  const images: Record<string, string[]> = {};
  if (typeof answer.images === "object" && answer.images !== null)
    for (const [turn, types] of Object.entries(answer.images))
      if (Array.isArray(types) && types.every((type) => typeof type === "string"))
        images[turn] = types;
  return transcript
    ? {
        available: true,
        version: answer.version,
        live,
        transcript,
        ...(summary ? { summary } : {}),
        ...(Object.keys(images).length > 0 ? { images } : {}),
      }
    : null;
}

/** The transcript with each prompt's pasted images, served by the daemon
 *  beside it. */
export function withImages(
  transcript: LocalTranscript,
  images: Record<string, string[]> | undefined,
  agent: string,
  id: string,
): LocalTranscript {
  if (!images) return transcript;
  const base = `api/live-sessions/${encodeURIComponent(agent)}/${encodeURIComponent(id)}/transcript/images`;
  const prompts = transcript.session.prompts.map((prompt) => {
    const types = images[prompt.turn_id];
    if (!types?.length) return prompt;
    return {
      ...prompt,
      images: types.map((_, n) => ({
        src: appURL(`${base}/${encodeURIComponent(prompt.turn_id)}/${n}`),
        alt: `Image ${n + 1} pasted with prompt ${prompt.ordinal}`,
      })),
    };
  });
  return { ...transcript, session: { ...transcript.session, prompts } };
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
                transcript: withImages(answer.transcript, answer.images, agent, id),
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
 *  `follow` is where a reader starts: at the end, on the latest turn, unless
 *  it is false. The reader's position counts only once the first version is
 *  shown, since the page around a loading transcript scrolls by itself. */
export function useFollowScroll(version: string | undefined, follow = true) {
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

/** A message of the transcript: a prompt, or one of the agent's messages
 *  other than its progress notes on the way to a reply. */
const MESSAGE = '[data-message="human"], [data-agent-text]:not([data-agent-text="progress"])';

/** The keys that read the transcript in `body` a message at a time: j and k
 *  bring the next or previous one just under `top`, the bar that stays at
 *  the top of the page, and mark it; Space shows all of the marked one, or
 *  less again, and Enter opens it on its own (the returned `reading`). Once
 *  the reader scrolled the marked one out of view, they start from what is
 *  at the top of the view. Keys typed in a field, or with a dialog open, are
 *  not taken, nor Space on a message with nothing more to show. */
export function useMessageKeys(
  body: RefObject<HTMLElement | null>,
  top: RefObject<HTMLElement | null>,
) {
  const current = useRef<HTMLElement | null>(null);
  const [reading, setReading] = useState<HTMLElement | null>(null);
  useEffect(() => {
    const mark = (target: HTMLElement) => {
      current.current?.classList.remove("local-message-current");
      target.classList.add("local-message-current");
      current.current = target;
    };
    const onKey = (event: KeyboardEvent) => {
      if (!["j", "k", " ", "Enter"].includes(event.key)) return;
      if (event.metaKey || event.ctrlKey || event.altKey || event.target !== document.body) return;
      if (document.querySelector("dialog[open]") || !body.current) return;
      const messages = [...body.current.querySelectorAll<HTMLElement>(MESSAGE)];
      const edge = (top.current?.getBoundingClientRect().bottom ?? 0) + 12;
      const marked = current.current;
      const shown =
        marked?.isConnected &&
        marked.getBoundingClientRect().bottom > edge &&
        marked.getBoundingClientRect().top < innerHeight;
      const at = shown ? messages.indexOf(marked) : -1;
      if (event.key === " " || event.key === "Enter") {
        const target =
          at >= 0 ? marked : messages.find((m) => m.getBoundingClientRect().bottom > edge);
        if (!target) return;
        if (event.key === "Enter") {
          event.preventDefault();
          mark(target);
          setReading(target);
          return;
        }
        const toggle = target.querySelector<HTMLButtonElement>(
          "button[aria-controls][aria-expanded]",
        );
        if (!toggle) return;
        event.preventDefault();
        mark(target);
        toggle.click();
        return;
      }
      const forward = event.key === "j";
      const target =
        at >= 0
          ? messages[at + (forward ? 1 : -1)]
          : forward
            ? messages.find((m) => m.getBoundingClientRect().top > edge + 2)
            : messages.findLast((m) => m.getBoundingClientRect().top < edge - 2);
      if (!target) return;
      event.preventDefault();
      mark(target);
      const reduced = matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
      window.scrollTo({
        top: window.scrollY + target.getBoundingClientRect().top - edge,
        behavior: reduced ? "instant" : "smooth",
      });
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [body, top]);
  const close = useCallback(() => setReading(null), []);
  return { reading, close };
}

/** A message opened on its own (Enter), nearly the page's size: a copy of
 *  it as the transcript draws it, unfolded. j and k scroll it a third of its
 *  height at a time, as Space and the arrows do theirs. Escape, a click
 *  outside it or its Close button closes it. */
function MessageReader({ message, onClose }: { message: HTMLElement; onClose: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const content = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const show = () => {
      const copy = message.cloneNode(true) as HTMLElement;
      copy.classList.remove("local-message-current");
      // Unfolded: the fold's height, its fade and its control left out.
      for (const element of copy.querySelectorAll<HTMLElement>("[style]"))
        element.style.maxHeight = "";
      for (const element of copy.querySelectorAll<HTMLElement>(
        '[class*="mask-image"], [class*="max-h-"]',
      ))
        element.className = element.className
          .split(" ")
          .filter((name) => !name.includes("mask-image") && !name.startsWith("max-h-"))
          .join(" ");
      for (const button of copy.querySelectorAll("button[aria-controls][aria-expanded]"))
        button.parentElement?.remove();
      content.current?.replaceChildren(copy);
    };
    // A folded message may hold only a preview, the rest loaded once it is
    // shown in full: it is unfolded in the transcript, copied again as the
    // rest arrives, and folded again on closing.
    const fold = message.querySelector<HTMLButtonElement>(
      'button[aria-controls][aria-expanded="false"]',
    );
    fold?.click();
    show();
    const observer = new MutationObserver(show);
    observer.observe(message, { subtree: true, childList: true, characterData: true });
    dialog.current?.showModal();
    // The message, not its Close button, takes the keys, so they scroll it.
    content.current?.focus({ preventScroll: true });
    return () => {
      observer.disconnect();
      if (fold?.isConnected && fold.getAttribute("aria-expanded") === "true") fold.click();
    };
  }, [message]);
  return (
    <dialog
      ref={dialog}
      aria-label="Message"
      className="local-message-reader"
      onClose={onClose}
      onClick={(event) => {
        if (event.target === dialog.current) dialog.current.close();
      }}
    >
      <div className="local-message-reader-bar">
        <Button size="compact" variant="secondary" onClick={() => dialog.current?.close()}>
          Close
        </Button>
      </div>
      <div
        ref={content}
        tabIndex={-1}
        role="document"
        className="local-message-reader-body local-session-page"
        onKeyDown={(event) => {
          if (event.key !== "j" && event.key !== "k") return;
          if (event.metaKey || event.ctrlKey || event.altKey) return;
          event.preventDefault();
          const element = event.currentTarget;
          const reduced = matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
          element.scrollBy({
            top: (event.key === "j" ? 1 : -1) * element.clientHeight * 0.33,
            behavior: reduced ? "instant" : "smooth",
          });
        }}
      />
    </dialog>
  );
}

/** A session's transcript, opened from the Sessions page: the session as the
 *  page lists it, then its transcript drawn as a review's linked session is,
 *  following the session while it runs. `session` is null when the list does
 *  not have it. */
export function SessionTranscriptPage({
  agent,
  id,
  session,
  queued,
  tick,
  prHref,
  Link,
}: {
  agent: string;
  id: string;
  session: LiveSession | null;
  /** The message queued for the session's next stop, if any. */
  queued?: AgentMessage;
  tick: number;
  prHref: (session: LiveSession) => string | null;
  Link: ComponentType<ViewerLinkProps>;
}) {
  const now = useNow();
  const back = (
    <Link
      href={appURL("?page=sessions")}
      className="local-page-back"
      aria-label="Back to sessions"
      title="Back to sessions (Esc)"
    >
      <ChevronLeftIcon size={18} />
      <kbd aria-hidden="true" className="key-hint">
        Esc
      </kbd>
    </Link>
  );
  const poll =
    session?.state === "running" ? POLL_RUNNING : session?.state === "idle" ? POLL_IDLE : null;
  const { state, retry } = useLiveTranscript(agent, id, { tick, poll });
  useEffect(() => {
    window.scrollTo({ top: 0 });
  }, [agent, id]);
  useFollowScroll(state.kind === "ready" ? state.version : undefined);
  const top = useRef<HTMLDivElement>(null);
  const body = useRef<HTMLDivElement>(null);
  const { reading, close } = useMessageKeys(body, top);
  const shown = state.kind === "ready" ? state.transcript.session : null;
  return (
    <>
      {/* The page's bar and the session stay in view while the transcript
          scrolls under them; the row below the bar names the agent and the
          repository, so the bar does not again. */}
      <div ref={top} className="local-session-top">
        <PageBar
          title={session?.title || `${agentName(agent)} session`}
          detail={session ? undefined : "Agent session on this computer"}
          back={back}
          actions={session ? <FocusSession session={session} hotkey /> : null}
        />
        {session || shown ? (
          <div className="local-session-top-body">
            {session ? (
              <section aria-label="Session" className="local-session-line">
                <Agent agent={session.agent} />
                <SessionState session={session} />
                <SessionWhere session={session} prHref={prHref} Link={Link} />
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
                  started {formatRelative(session.started_at, now)}
                </span>
              </section>
            ) : null}
            {shown ? <SessionFacts session={shown} /> : null}
          </div>
        ) : null}
      </div>
      <div ref={body} className="local-page-body local-session-page">
        <TranscriptBody
          state={state}
          retry={retry}
          live={session !== null && session.state !== "ended"}
          title={session?.title}
        />
      </div>
      {session ? <SessionReplyBar session={session} queued={queued} /> : null}
      {reading ? <MessageReader message={reading} onClose={close} /> : null}
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
  return (
    <TranscriptReader
      transcript={state.transcript}
      version={state.version}
      summary={state.summary}
      live={live}
      title={title}
    />
  );
});

/** The transcript's loader, answering from the transcript shown. */
function useLocalSource(transcript: LocalTranscript, version: string | undefined) {
  const latest = useRef<LocalTranscript>(transcript);
  latest.current = transcript;
  return useMemo<TranscriptSource>(
    () => ({
      revision: version,
      loadTurnActivity: (_run, turn) => {
        const activity = latest.current.activity[turn];
        return activity ? Promise.resolve(activity) : Promise.reject(Error("No activity"));
      },
    }),
    [version],
  );
}

/** The session as the transcript shows it, titled by the list when the
 *  transcript holds no title. A running session's transcript has no outcome
 *  to report; it would say "unknown" where the session is simply not done. */
function shownSession(
  transcript: LocalTranscript,
  live: boolean,
  title: string | undefined,
): TranscriptSession {
  return {
    ...transcript.session,
    title: transcript.session.title || title || "",
    outcome:
      live && transcript.session.outcome === "unknown" ? "in progress" : transcript.session.outcome,
  };
}

/** The transcript as this page reads it: the agent's compaction summary
 *  when there is one, then the conversation as a log, without the card a
 *  review draws around a linked session. */
function TranscriptReader({
  transcript,
  version,
  summary,
  live,
  title,
}: {
  transcript: LocalTranscript;
  version: string;
  summary?: string;
  live: boolean;
  title?: string;
}) {
  const source = useLocalSource(transcript, version);
  const session = shownSession(transcript, live, title);
  return (
    <section aria-label="Transcript" className="local-transcript">
      {summary ? <CompactionSummary summary={summary} /> : null}
      {session.has_inner_trace ? (
        <SessionTranscript session={session} replies="ready" source={source} layout="log" />
      ) : (
        <p className="local-transcript-note">
          This session reported its outcome and usage but no tool calls, file reads or diffs.
        </p>
      )}
    </section>
  );
}

/** What the session did, as small print: its models, tokens, files, lines
 *  and tool calls. */
function SessionFacts({ session }: { session: TranscriptSession }) {
  const totals = session.totals;
  return (
    <p className="local-session-line local-session-facts">
      {session.models.length > 0 ? <span>{session.models.join(", ")}</span> : null}
      {session.tokens > 0 ? <span>{formatCompact(session.tokens)} tokens</span> : null}
      {totals ? (
        <>
          <span>
            {formatCount(totals.files_read)} read, {formatCount(totals.files_edited)} edited
          </span>
          <span>
            <span className="local-session-added">+{formatCount(totals.added_lines)}</span>{" "}
            <span className="local-session-removed">−{formatCount(totals.removed_lines)}</span>
          </span>
          <span>
            {pluralize("tool call", totals.tool_calls, true)}
            {totals.failed_calls > 0 ? (
              <span className="local-session-removed">
                {" "}
                ({formatCount(totals.failed_calls)} failed)
              </span>
            ) : null}
          </span>
        </>
      ) : null}
    </p>
  );
}

/** The summary the agent wrote when it compacted the session, closed until
 *  asked for. */
function CompactionSummary({ summary }: { summary: string }) {
  return (
    <details className="local-panel local-session-summary">
      <summary>
        <span className="local-session-summary-title">Summary of the earlier conversation</span>
        <span className="local-session-summary-note">
          Written by the agent when it compacted the session
        </span>
      </summary>
      <Markdown html={false} className="local-session-summary-body">
        {summary}
      </Markdown>
    </details>
  );
}

/** A session's local transcript as a review's linked session is drawn, for
 *  a transcript shown inside another page: the agent's compaction summary
 *  when there is one, the redaction note and the session card, open. `focusTurn` marks that turn's prompt and scrolls it
 *  into view, for a reader sent to one moment of the session. */
export function TranscriptCard({
  transcript,
  version,
  summary,
  live = false,
  title,
  focusTurn,
}: {
  transcript: LocalTranscript;
  /** Changes with the transcript, so an opened turn's activity loads again. */
  version?: string;
  summary?: string;
  /** The session still runs, so it has no outcome yet. */
  live?: boolean;
  /** The listed session's title, for a transcript that holds none. */
  title?: string;
  focusTurn?: number;
}) {
  const source = useLocalSource(transcript, version);
  const session = shownSession(transcript, live, title);
  return (
    <section aria-label="Transcript" className="local-transcript">
      {summary ? <CompactionSummary summary={summary} /> : null}
      <p className="local-transcript-note">
        Local transcript; secrets and e-mail addresses masked.
        {transcript.truncated ? " Some content was omitted." : ""}
      </p>
      <div className="local-panel local-transcript-card">
        <SessionCard
          defaultOpen={true}
          session={session}
          source={source}
          focusTurn={focusTurn}
          layout="log"
        />
      </div>
    </section>
  );
}
