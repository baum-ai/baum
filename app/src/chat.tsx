import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { HunkEvidence, HunkPatchProvider } from "@baum/viewer/components/hunk-evidence";
import type { HunkPatchSource } from "@baum/viewer/components/hunk-evidence";
import { Markdown } from "@baum/viewer/components/markdown";
import { Alert, Button, LiveDot } from "@baum/viewer/components/ui";
import type { Anchor, ReviewDocument } from "@baum/viewer/review-document";
import {
  askChat,
  chatTurnActive,
  cost,
  jobReasonText,
  listChats,
  request,
  type ChatAnchor,
  type ChatAnswer,
  type ChatCitation,
  type ChatConversation,
  type ChatFocus,
  type ChatTurn,
  type Job,
} from "./api";
import { EntryView, useJobTranscript } from "./job-transcript";
import { LoadingStatus } from "./nine-dots";

/** The most anchors a question's focus may carry. */
const MAX_ANCHORS = 20;
const MAX_QUESTION = 4000;
/** While a question is being answered, the list is read again this often in
 *  case an event was missed. */
const ACTIVE_POLL_MS = 4000;

/** The files and lines of the review's hunks, as a question's focus names
 *  them. A hunk anchor with lines narrows its hunk to them. */
export function focusAnchors(document: ReviewDocument, anchors: Anchor[]): ChatAnchor[] {
  const hunks = new Map(document.index.hunks.map((hunk) => [hunk.id, hunk]));
  const seen = new Set<string>();
  const out: ChatAnchor[] = [];
  for (const anchor of anchors) {
    if (anchor.type !== "hunk") continue;
    const hunk = hunks.get(anchor.id);
    if (!hunk) continue;
    const [start, count] = hunk.new_range;
    let from = 0;
    let to = 0;
    if (anchor.lines && anchor.lines[0] > 0 && anchor.lines[1] >= anchor.lines[0]) {
      [from, to] = anchor.lines;
    } else if (start > 0 && count > 0) {
      from = start;
      to = start + count - 1;
    }
    const place: ChatAnchor = from
      ? { path: hunk.path, start_line: from, end_line: to }
      : { path: hunk.path };
    const key = `${place.path}:${from}:${to}`;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(place);
  }
  return out.slice(0, MAX_ANCHORS);
}

/** A question about one section of the review. */
export function sectionFocus(document: ReviewDocument, id: string): ChatFocus | null {
  const section = document.sections.find((s) => s.id === id);
  if (!section) return null;
  return {
    kind: "section",
    id: section.id,
    title: section.title,
    anchors: focusAnchors(
      document,
      section.hunks.map((hunk) => ({ type: "hunk", id: hunk })),
    ),
  };
}

/** A question about one stop of the walkthrough. */
export function stopFocus(
  document: ReviewDocument,
  stop: { id: string; title: string; anchors: Anchor[] },
): ChatFocus {
  return {
    kind: "stop",
    id: stop.id,
    title: stop.title,
    anchors: focusAnchors(document, stop.anchors),
  };
}

const chatReasons: Record<string, string> = {
  needs_clone: "The repository has no local clone, which the agent needs to read the code.",
  budget: "The agent reached its time or size limit before it could answer.",
  backend: "The agent couldn't run, or its provider was unavailable.",
  validation:
    "The agent's answer didn't pass baum's checks, such as a citation to code that isn't there, even after a repair.",
  input:
    "baum couldn't prepare the question: the review, the repository or its commits could not be read.",
  internal: "Something went wrong inside baum.",
  cancelled: "You cancelled this question.",
  interrupted: "The question stopped before it was answered, because baum stopped or restarted.",
  unsupported_backend:
    "Only a review written by Claude Code or Codex on this computer can be asked about.",
};

/** What a settled turn without an answer says, in a sentence. */
export function turnReasonText(turn: ChatTurn): string {
  const reason =
    turn.reason || (turn.state === "cancelled" || turn.state === "interrupted" ? turn.state : "");
  if (!reason) return "";
  return chatReasons[reason] ?? jobReasonText(reason);
}

const turnTitles: Partial<Record<ChatTurn["state"], string>> = {
  failed: "The agent could not answer",
  cancelled: "Cancelled",
  interrupted: "Interrupted",
};

function citationLabel(c: ChatCitation) {
  return c.end_line > c.start_line
    ? `${c.path}:${c.start_line}–${c.end_line}`
    : `${c.path}:${c.start_line}`;
}

/** The review's hunks that the cited lines fall in, narrowed to those lines. */
function citedHunks(document: ReviewDocument, citation: ChatCitation): Anchor[] {
  return document.index.hunks.flatMap((hunk) => {
    const [start, count] = hunk.new_range;
    if (hunk.path !== citation.path || count < 1) return [];
    const from = Math.max(start, citation.start_line);
    const to = Math.min(start + count - 1, citation.end_line);
    return from <= to ? [{ type: "hunk", id: hunk.id, lines: [from, to] } as Anchor] : [];
  });
}

/** HunkEvidence is a disclosure; a citation the reader clicked has asked for
 *  its lines, so it opens at once. */
function OpenEvidence({ children }: { children: ReactNode }) {
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    box.current?.querySelector("details")?.setAttribute("open", "");
  }, []);
  return <div ref={box}>{children}</div>;
}

/** The cited file at the review's head on github.com, or null for another
 *  host, which the app has no link scheme for. */
function fileLink(pullRequestUrl: string, document: ReviewDocument, c: ChatCitation) {
  try {
    const url = new URL(pullRequestUrl);
    if (url.protocol !== "https:" || url.hostname !== "github.com") return null;
    const path = c.path.split("/").map(encodeURIComponent).join("/");
    return `https://github.com/${document.review.repository}/blob/${document.review.head}/${path}#L${c.start_line}-L${c.end_line}`;
  } catch {
    return null;
  }
}

/** The lines a citation names. The ones the review's own hunks hold are drawn
 *  from them; the daemon serves no other file text, so lines outside the
 *  changes are named, with a link to GitHub where the review's head is known. */
function CitationLines({
  citation,
  document,
  pullRequestUrl,
}: {
  citation: ChatCitation;
  pullRequestUrl: string;
  /** The review the conversation is about, when it is the one on screen. */
  document: ReviewDocument | null;
}) {
  const anchors = document ? citedHunks(document, citation) : [];
  if (document && anchors.length > 0)
    return (
      <OpenEvidence>
        <HunkEvidence document={document} anchors={anchors} />
      </OpenEvidence>
    );
  const link = document ? fileLink(pullRequestUrl, document, citation) : null;
  const lines =
    citation.end_line > citation.start_line
      ? `lines ${citation.start_line}–${citation.end_line}`
      : `line ${citation.start_line}`;
  return (
    <p className="ask-cite-note">
      {citation.path}, {lines}: not part of this review's changes, so baum has no excerpt to show.
      {link ? (
        <>
          {" "}
          <a href={link} target="_blank" rel="noreferrer">
            Open on GitHub
          </a>
        </>
      ) : null}
    </p>
  );
}

function Citations({
  answer,
  document,
  pullRequestUrl,
}: {
  answer: ChatAnswer;
  document: ReviewDocument | null;
  pullRequestUrl: string;
}) {
  const [open, setOpen] = useState<number | null>(null);
  if (answer.citations.length === 0) return null;
  return (
    <section aria-label="Citations" className="ask-cites">
      <h4>Cited code</h4>
      <ul>
        {answer.citations.map((citation, index) => {
          const key = citationLabel(citation);
          const shown = open === index;
          return (
            <li key={`${index}:${key}`}>
              <button
                type="button"
                className="ask-cite"
                aria-expanded={shown}
                onClick={() => setOpen(shown ? null : index)}
              >
                {key}
              </button>
              {shown ? (
                <CitationLines
                  citation={citation}
                  document={document}
                  pullRequestUrl={pullRequestUrl}
                />
              ) : null}
            </li>
          );
        })}
      </ul>
    </section>
  );
}

/** What the agent is doing for a question not yet answered: its transcript,
 *  with tool calls collapsed. */
function LiveTurn({ turn }: { turn: ChatTurn }) {
  const state = useJobTranscript(turn.job_id);
  const [error, setError] = useState("");
  const [cancelling, setCancelling] = useState(false);
  const entries = state.kind === "ready" || state.kind === "failed" ? state.entries : [];
  return (
    <div className="ask-live">
      <p role="status" className="ask-live-status">
        <LiveDot /> {turn.state === "queued" ? "Queued" : "The agent is answering"}
      </p>
      {state.kind === "loading" ? (
        <LoadingStatus block>Reading the agent's transcript…</LoadingStatus>
      ) : entries.length > 0 ? (
        <ol aria-label="Transcript" className="ask-transcript">
          {entries.map((entry) => (
            <li key={entry.id ? `${entry.kind}:${entry.id}` : entry.seq}>
              <EntryView entry={entry} />
            </li>
          ))}
        </ol>
      ) : null}
      <div className="ask-live-actions">
        <Button
          variant="secondary"
          size="compact"
          disabled={cancelling}
          onClick={() => {
            setCancelling(true);
            setError("");
            void request(`api/jobs/${encodeURIComponent(turn.job_id)}/cancel`, {})
              .catch(() => setError("The question could not be cancelled. Try again."))
              .finally(() => setCancelling(false));
          }}
        >
          {cancelling ? "Cancelling…" : "Cancel"}
        </Button>
      </div>
      {error ? <Alert>{error}</Alert> : null}
    </div>
  );
}

/** What the next step adds under an answer: Add as comment, Fix this. */
export type AnswerActions = (context: {
  conversation: ChatConversation;
  turn: ChatTurn;
  answer: ChatAnswer;
}) => ReactNode;

function TurnView({
  conversation,
  turn,
  document,
  pullRequestUrl,
  answerActions,
}: {
  conversation: ChatConversation;
  turn: ChatTurn;
  document: ReviewDocument | null;
  pullRequestUrl: string;
  answerActions?: AnswerActions;
}) {
  const reason = turnReasonText(turn);
  return (
    <article className="ask-turn" aria-label={`Question ${turn.seq}`}>
      <div className="ask-question">
        {turn.focus?.title ? <p className="ask-question-about">About {turn.focus.title}</p> : null}
        <p>{turn.question}</p>
      </div>
      {chatTurnActive(turn) ? (
        <LiveTurn turn={turn} />
      ) : turn.state === "completed" && turn.answer ? (
        <div className="ask-answer">
          <Markdown html={false}>{turn.answer.answer_markdown}</Markdown>
          <Citations answer={turn.answer} document={document} pullRequestUrl={pullRequestUrl} />
          {/* The slot of the actions under an answer (Add as comment, Fix
           *  this); empty until they are given. */}
          <div className="ask-answer-actions" data-answer-actions="">
            {answerActions?.({ conversation, turn, answer: turn.answer })}
          </div>
        </div>
      ) : (
        <div className="ask-outcome" role="status">
          <strong>{turnTitles[turn.state] ?? `Question ${turn.state}`}</strong>
          {reason ? <p>{reason}</p> : null}
        </div>
      )}
      {turn.note ? <p className="ask-note">{turn.note}</p> : null}
      {!chatTurnActive(turn) && turn.cost_usd != null ? (
        <p className="ask-cost">Cost {cost(turn.cost_usd)}</p>
      ) : null}
    </article>
  );
}

type Load =
  | { state: "loading" }
  | { state: "ready"; conversations: ChatConversation[] }
  | { state: "unavailable" }
  | { state: "failed" };

/** A conversation's name in the list: its first question. */
function conversationName(conversation: ChatConversation) {
  const question = conversation.turns[0]?.question ?? "Conversation";
  return question.length > 60 ? `${question.slice(0, 57)}…` : question;
}

/** Questions to the agent that wrote the review, docked beside it: the
 *  conversations of this pull request, the current review's first, and a box
 *  for the next question. */
export function AskPanel({
  clone,
  pr,
  pullRequestUrl,
  latestVersion,
  document,
  patches,
  focus,
  clearFocus,
  close,
  jobs = [],
  answerActions,
}: {
  clone: string;
  pr: number;
  /** The pull request's GitHub address, which decides where citations link. */
  pullRequestUrl: string;
  /** The newest review's version: conversations of older ones are outdated. */
  latestVersion: number;
  /** The review on screen; questions are about its version. */
  document: ReviewDocument;
  patches: HunkPatchSource | null;
  /** The part of the review the next question is about. */
  focus: ChatFocus | null;
  clearFocus: () => void;
  close: () => void;
  /** The pull request's chat jobs; a change in one reads the list again. */
  jobs?: Job[];
  /** Where the actions under an answer go. */
  answerActions?: AnswerActions;
}) {
  const version = document.review.version;
  const [load, setLoad] = useState<Load>({ state: "loading" });
  const [tick, setTick] = useState(0);
  const refresh = useCallback(() => setTick((n) => n + 1), []);
  // The conversation read, "new" for a blank one, null until one is picked.
  const [choice, setChoice] = useState<string | null>(null);
  const [question, setQuestion] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState("");
  const input = useRef<HTMLTextAreaElement>(null);
  const thread = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const abort = new AbortController();
    listChats(clone, pr, abort.signal)
      .then((conversations) => {
        if (!abort.signal.aborted)
          setLoad(conversations ? { state: "ready", conversations } : { state: "unavailable" });
      })
      .catch(() => {
        if (abort.signal.aborted) return;
        // A failed refresh keeps what was read.
        setLoad((current) => (current.state === "ready" ? current : { state: "failed" }));
      });
    return () => abort.abort();
  }, [clone, pr, tick]);

  // A question's job changing state (the app's own stream, through `jobs`)
  // reads the list again.
  const jobStates = jobs.map((job) => `${job.id}:${job.state}`).join(",");
  const firstJobStates = useRef(jobStates);
  useEffect(() => {
    if (jobStates !== firstJobStates.current) refresh();
  }, [jobStates, refresh]);

  const conversations = useMemo(() => (load.state === "ready" ? load.conversations : []), [load]);
  const current = conversations.filter((c) => c.version === version);
  const activeId =
    choice === "new"
      ? "new"
      : choice && conversations.some((c) => c.id === choice)
        ? choice
        : (current[0]?.id ?? "new");
  const conversation = conversations.find((c) => c.id === activeId);
  const readOnly = !!conversation && conversation.version !== version;
  const older = !!conversation && conversation.version < latestVersion;
  const busy = !!conversation?.turns.some(chatTurnActive);
  const activeTurns = conversations.some((c) => c.turns.some(chatTurnActive));

  useEffect(() => {
    if (!activeTurns) return;
    const timer = setInterval(refresh, ACTIVE_POLL_MS);
    return () => clearInterval(timer);
  }, [activeTurns, refresh]);

  // The box takes the typing when the panel opens and when a part of the
  // review is chosen to ask about; that part is asked about on the current
  // review, so an outdated conversation gives way to a new one.
  useEffect(() => {
    input.current?.focus({ preventScroll: true });
  }, []);
  const readOnlyNow = useRef(readOnly);
  readOnlyNow.current = readOnly;
  useEffect(() => {
    if (!focus) return;
    if (readOnlyNow.current) setChoice("new");
    input.current?.focus({ preventScroll: true });
  }, [focus]);

  const turnCount = conversation?.turns.length ?? 0;
  const lastState = conversation?.turns[turnCount - 1]?.state;
  useEffect(() => {
    const end = thread.current?.lastElementChild;
    end?.scrollIntoView?.({ block: "nearest" });
  }, [turnCount, lastState, activeId]);

  const send = async () => {
    const text = question.trim();
    if (!text || busy || sending || readOnly) return;
    setSending(true);
    setError("");
    try {
      const asked = await askChat({
        clone,
        pr,
        version,
        conversation_id: activeId === "new" ? undefined : activeId,
        question: text,
        focus: focus ?? undefined,
      });
      setQuestion("");
      setChoice(asked.conversation_id);
      clearFocus();
      refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "The question could not be sent.");
      refresh();
    } finally {
      setSending(false);
    }
  };

  const others = new Map<number, ChatConversation[]>();
  for (const c of conversations)
    if (c.version !== version) others.set(c.version, [...(others.get(c.version) ?? []), c]);
  const item = (c: ChatConversation) => (
    <li key={c.id}>
      <button
        type="button"
        className="ask-convo"
        aria-current={c.id === activeId ? "true" : undefined}
        onClick={() => {
          setChoice(c.id);
          setError("");
        }}
      >
        {conversationName(c)}
        {c.turns.some(chatTurnActive) ? <LiveDot className="ml-1.5 inline-flex" /> : null}
      </button>
    </li>
  );

  return (
    <aside
      aria-label="Ask about this review"
      className="ask-panel"
      onKeyDown={(event) => {
        if (event.key === "Escape" && !event.defaultPrevented) {
          event.preventDefault();
          close();
        }
      }}
    >
      <header className="ask-head">
        <h2>Ask about this review</h2>
        <Button variant="secondary" size="compact" onClick={close} aria-keyshortcuts="Escape">
          Close
        </Button>
      </header>
      {conversations.length > 0 ? (
        <nav aria-label="Conversations" className="ask-convos">
          <div className="ask-convos-head">
            <h3>Review v{version}</h3>
            <Button
              variant="secondary"
              size="compact"
              aria-pressed={activeId === "new"}
              onClick={() => {
                setChoice("new");
                setError("");
                input.current?.focus({ preventScroll: true });
              }}
            >
              New conversation
            </Button>
          </div>
          {current.length > 0 ? <ul>{current.map(item)}</ul> : null}
          {[...others].map(([other, list]) => (
            <div key={other}>
              <h3>
                {other < latestVersion ? "Outdated: asked about" : "Asked about"} review v{other}
              </h3>
              <ul>{list.map(item)}</ul>
            </div>
          ))}
        </nav>
      ) : null}
      <HunkPatchProvider source={patches}>
        <div className="ask-thread" ref={thread}>
          {load.state === "loading" ? (
            <LoadingStatus block>Loading conversations…</LoadingStatus>
          ) : load.state === "unavailable" ? (
            <p className="ask-empty">Asking about a review is not available on this computer.</p>
          ) : load.state === "failed" ? (
            <Alert>
              The conversations could not be read. <Button onClick={refresh}>Retry</Button>
            </Alert>
          ) : conversation ? (
            <>
              {conversation.seeded ? (
                <p className="ask-note">Started without the review's own context.</p>
              ) : null}
              {conversation.turns.map((turn) => (
                <TurnView
                  key={turn.seq}
                  conversation={conversation}
                  turn={turn}
                  document={conversation.version === version ? document : null}
                  pullRequestUrl={pullRequestUrl}
                  answerActions={answerActions}
                />
              ))}
            </>
          ) : (
            <p className="ask-empty">
              The agent that wrote this review answers questions about the code and its reasoning.
              It can only read: it changes nothing.
            </p>
          )}
        </div>
      </HunkPatchProvider>
      {readOnly ? (
        <div className="ask-composer">
          <p className="ask-note">
            This conversation is about {older ? "an older review" : "a newer review"}, so it is
            read-only. Start a new one to ask about the review on this page.
          </p>
          <div>
            <Button
              variant="secondary"
              size="compact"
              onClick={() => {
                setChoice("new");
                input.current?.focus({ preventScroll: true });
              }}
            >
              New conversation
            </Button>
          </div>
        </div>
      ) : (
        <form
          className="ask-composer"
          onSubmit={(event) => {
            event.preventDefault();
            void send();
          }}
        >
          {focus ? (
            <p className="ask-focus">
              <span>
                About <strong>{focus.title || "the pull request"}</strong>
              </span>
              <Button variant="secondary" size="compact" onClick={clearFocus}>
                Remove
              </Button>
            </p>
          ) : null}
          <label htmlFor="ask-question" className="sr-only">
            Your question
          </label>
          <textarea
            id="ask-question"
            ref={input}
            className="ask-input"
            rows={3}
            maxLength={MAX_QUESTION}
            value={question}
            placeholder="Ask about this review's code or reasoning"
            onChange={(event) => setQuestion(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
                event.preventDefault();
                void send();
              }
            }}
          />
          {error ? <Alert>{error}</Alert> : null}
          <div className="ask-composer-row">
            <span className="ask-hint">
              {busy ? "Waiting for the answer" : "Enter sends, Shift+Enter adds a line"}
            </span>
            <Button type="submit" size="compact" disabled={!question.trim() || busy || sending}>
              {sending ? "Sending…" : "Send"}
            </Button>
          </div>
        </form>
      )}
    </aside>
  );
}
