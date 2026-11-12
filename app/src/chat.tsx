import { memo, useEffect, useRef, useState, type ReactNode } from "react";
import { HunkEvidence, HunkPatchProvider } from "@baum/viewer/components/hunk-evidence";
import type { HunkPatchSource } from "@baum/viewer/components/hunk-evidence";
import { Markdown } from "@baum/viewer/components/markdown";
import { Button, CloseButton } from "@baum/viewer/components/ui";
import type { Anchor, ReviewDocument } from "@baum/viewer/review-document";
import {
  chatTurnActive,
  jobReasonText,
  listChats,
  type ChatAnchor,
  type ChatAnswer,
  type ChatCitation,
  type ChatConversation,
  type ChatFocus,
  type ChatTurn,
} from "./api";
import { sessionPanel } from "./session-panel-state";

/** The most anchors a focus may carry. */
const MAX_ANCHORS = 20;

/** The files and lines of the review's hunks, as an Ask's focus names them. A
 *  hunk anchor with lines narrows its hunk to them. */
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

/** An Ask about one section of the review. */
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

/** An Ask about one stop of the walkthrough. */
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
  needs_clone: "The repository had no local clone, which the agent needed to read the code.",
  budget: "The agent reached its time or size limit before it could answer.",
  backend: "The agent couldn't run, or its provider was unavailable.",
  signed_out: "The agent wasn't signed in, or its sign-in had expired.",
  validation:
    "The agent's answer didn't pass baum's checks, such as a citation to code that isn't there, even after a repair.",
  input:
    "baum couldn't prepare the question: the review, the repository or its commits could not be read.",
  internal: "Something went wrong inside baum.",
  cancelled: "The question was cancelled.",
  interrupted: "The question stopped before it was answered, because baum stopped or restarted.",
  unsupported_backend:
    "Only a review written by Claude Code or Codex on this computer could be asked about.",
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

const TurnView = memo(function TurnView({
  turn,
  document,
  pullRequestUrl,
}: {
  turn: ChatTurn;
  document: ReviewDocument | null;
  pullRequestUrl: string;
}) {
  const reason = turnReasonText(turn);
  return (
    <article className="ask-turn" aria-label={`Question ${turn.seq}`}>
      <div className="ask-question">
        {turn.focus?.title ? <p className="ask-question-about">About {turn.focus.title}</p> : null}
        <p>{turn.question}</p>
      </div>
      {turn.state === "completed" && turn.answer ? (
        <div className="ask-answer">
          <Markdown html={false}>{turn.answer.answer_markdown}</Markdown>
          <Citations answer={turn.answer} document={document} pullRequestUrl={pullRequestUrl} />
          {turn.answer.suggests_fix?.summary ? (
            <p className="ask-suggestion">
              <strong>Suggested change:</strong> {turn.answer.suggests_fix.summary}
            </p>
          ) : null}
        </div>
      ) : (
        <div className="ask-outcome" role="status">
          <strong>
            {chatTurnActive(turn)
              ? "Not answered"
              : (turnTitles[turn.state] ?? `Question ${turn.state}`)}
          </strong>
          {reason ? <p>{reason}</p> : null}
        </div>
      )}
      {turn.note ? <p className="ask-note">{turn.note}</p> : null}
    </article>
  );
});

/** A conversation's name in the list: its first question. */
function conversationName(conversation: ChatConversation) {
  const question = conversation.turns[0]?.question ?? "Conversation";
  return question.length > 60 ? `${question.slice(0, 57)}…` : question;
}

/** The pull request's earlier questions to the agent that wrote its review,
 *  newest first: an empty list when there are none, null while they are read
 *  or when they cannot be. */
export function useEarlierQuestions(clone: string, pr: number): ChatConversation[] | null {
  const [conversations, setConversations] = useState<ChatConversation[] | null>(null);
  useEffect(() => {
    const abort = new AbortController();
    setConversations(null);
    listChats(clone, pr, abort.signal)
      .then((list) => {
        if (!abort.signal.aborted) setConversations(list);
      })
      .catch(() => {
        // Nothing to show: the button that opens them stays away.
      });
    return () => abort.abort();
  }, [clone, pr]);
  return conversations;
}

/** The pull request's earlier questions to the agent that wrote its review,
 *  docked beside it and read-only: the Ask of earlier versions of the app.
 *  Questions are now asked in the session panel. */
export function EarlierQuestions({
  repository,
  pr,
  pullRequestUrl,
  conversations,
  document,
  patches,
  close,
}: {
  repository: string;
  pr: number;
  /** The pull request's GitHub address, which decides where citations link. */
  pullRequestUrl: string;
  /** The conversations, newest first; never empty. */
  conversations: ChatConversation[];
  /** The review on screen, whose hunks draw the citations of conversations
   *  about its version. */
  document: ReviewDocument | null;
  patches: HunkPatchSource | null;
  close: () => void;
}) {
  const [choice, setChoice] = useState<string | null>(null);
  const conversation = conversations.find((c) => c.id === choice) ?? conversations[0];
  const version = document?.review.version ?? 0;
  const byVersion = new Map<number, ChatConversation[]>();
  for (const c of conversations) byVersion.set(c.version, [...(byVersion.get(c.version) ?? []), c]);
  return (
    <aside
      aria-label="Earlier questions"
      className="ask-panel"
      onKeyDown={(event) => {
        if (event.key === "Escape" && !event.defaultPrevented) {
          event.preventDefault();
          close();
        }
      }}
    >
      <header className="ask-head">
        <h2>Earlier questions</h2>
        <CloseButton onClick={close} aria-keyshortcuts="Escape" />
      </header>
      <nav aria-label="Conversations" className="ask-convos">
        {[...byVersion].map(([v, list]) => (
          <div key={v}>
            <h3>
              {v === version ? "About this review" : "About review"} v{v}
            </h3>
            <ul>
              {list.map((c) => (
                <li key={c.id}>
                  <button
                    type="button"
                    className="ask-convo"
                    aria-current={c.id === conversation?.id ? "true" : undefined}
                    onClick={() => setChoice(c.id)}
                  >
                    {conversationName(c)}
                  </button>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </nav>
      <HunkPatchProvider source={patches}>
        <div className="ask-thread">
          {conversation ? (
            <>
              {conversation.seeded ? (
                <p className="ask-note">Started without the review's own context.</p>
              ) : null}
              {conversation.turns.map((turn) => (
                <TurnView
                  key={turn.seq}
                  turn={turn}
                  document={conversation.version === version ? document : null}
                  pullRequestUrl={pullRequestUrl}
                />
              ))}
            </>
          ) : null}
        </div>
      </HunkPatchProvider>
      <div className="ask-composer">
        <p className="ask-note">
          These conversations are read-only. Ask an agent about this pull request in the session
          panel, where it works in the pull request's checkout.
        </p>
        <div>
          <Button
            variant="secondary"
            size="compact"
            onClick={() => sessionPanel.ask({ repository, number: pr })}
          >
            Ask in the session panel
          </Button>
        </div>
      </div>
    </aside>
  );
}
