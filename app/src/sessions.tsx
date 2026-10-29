import {
  useEffect,
  useId,
  useRef,
  useState,
  type ComponentType,
  type FormEvent,
  type MouseEvent,
  type ReactNode,
} from "react";
import { formatRelative } from "@baum/viewer/components/format";
import { InlineMarkdown } from "@baum/viewer/components/markdown";
import {
  ChevronRightIcon,
  CircleCheckIcon,
  CircleDashedIcon,
  CommentsIcon,
  type IconProps,
  PaperPlaneIcon,
  WarningIcon,
} from "@baum/viewer/components/icons";
import { Button, LiveDot } from "@baum/viewer/components/ui";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import {
  agentName,
  answerRequest,
  appURL,
  cancelMessage,
  endSession,
  messageSession,
  LocalRequestError,
  noAgentRequests,
  sendMessage,
  type AgentMessage,
  type AgentRequest,
  type AgentRequests,
  type AnswerBody,
  type LiveSession,
  type RequestQuestion,
  type WaitingReason,
} from "./api";
import claudeLogo from "./desktop/claude-code.svg";
import codexLogo from "./desktop/codex.svg";
import cursorLogo from "./desktop/cursor.svg";
import { useContextMenu, type MenuItem } from "./context-menu";
import { useNow } from "./live";
import { LiveSessionDecisions } from "./decisions";
import { listKeys, useRowFocus, useShortcuts } from "./shortcuts";

/** What marks a list row for the row focus (`useRowFocus`). */
type RowProps = (s: LiveSession) => { "data-row-id": string; "data-focused"?: true };

const agentLogos: Record<string, string> = {
  "claude-code": claudeLogo,
  codex: codexLogo,
  cursor: cursorLogo,
};

/** How long a session ran, in whole units a person reads at a glance:
 *  "40 s", "25 min", "1 h 5 min". */
export function lasted(seconds: number): string {
  if (seconds < 60) return `${Math.max(0, Math.round(seconds))} s`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} min`;
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return rest ? `${hours} h ${rest} min` : `${hours} h`;
}

/** How long a session has waited: "under a minute", then as `lasted`. */
export function waited(seconds: number): string {
  return seconds < 60 ? "under a minute" : lasted(seconds);
}

/** The tooltip of a relative time: when it was, in the viewer's locale. */
export const absolute = (iso: string) => new Date(iso).toLocaleString();

/** Why a session waits on its user, in words. */
export const waitingReasons: Record<WaitingReason, string> = {
  permission: "Asking permission",
  input: "Waiting for your answer",
  turn_finished: "Finished its turn",
  quiet: "Went quiet",
};

const waitingSince = (s: LiveSession) => s.waiting_since ?? s.last_activity_at;

/** The sessions waiting on their user, the longest wait first: those that
 *  ask for a permission or an answer, or finished their turn. A session only
 *  silent for a while is not one, nor is an ended one. */
export function needsYou(sessions: LiveSession[]): LiveSession[] {
  return sessions
    .filter((s) => s.state !== "ended" && s.waiting_reason && waitingReasons[s.waiting_reason])
    .sort(
      (a, b) =>
        new Date(waitingSince(a)).getTime() - new Date(waitingSince(b)).getTime() ||
        sessionKey(a).localeCompare(sessionKey(b)),
    );
}

/** The key a session goes by in the app's URL: `agent:id`. */
export const sessionKey = (s: Pick<LiveSession, "agent" | "session_id">) =>
  `${s.agent}:${s.session_id}`;

/** Where a session's transcript opens in the app. */
export const transcriptHref = (s: LiveSession) =>
  appURL(`?page=sessions&session=${encodeURIComponent(sessionKey(s))}`);

export function Agent({ agent }: { agent: string }) {
  const logo = agentLogos[agent];
  return (
    <span className="flex min-w-0 items-center gap-2 font-medium text-(--text-primary)">
      {logo ? <img src={logo} alt="" width="18" height="18" className="shrink-0" /> : null}
      {agentName(agent)}
    </span>
  );
}

/** The agent by its mark alone, its name for assistive technology; by name
 *  when it has no mark. */
function AgentMark({ agent }: { agent: string }) {
  const logo = agentLogos[agent];
  return logo ? (
    <img src={logo} alt={agentName(agent)} width="18" height="18" className="flex-none" />
  ) : (
    <span className="local-feed-agent">{agentName(agent)}</span>
  );
}

/** What the session is doing: working (with the live dot), waiting for its
 *  user, or ended. */
export function SessionState({ session }: { session: LiveSession }) {
  return (
    <span className="local-session-state">
      {session.state === "running" ? (
        <>
          <LiveDot className="text-(--status-positive)" /> Working
        </>
      ) : session.state === "idle" ? (
        <>
          <CircleDashedIcon size={12} className="flex-none text-(--status-caution)" /> Waiting for
          you
        </>
      ) : (
        "Ended"
      )}
    </span>
  );
}

/** Where the session works: the repository, its branch as a chip and the
 *  pull request it is on, linked into the app when the inbox knows it. */
export function SessionWhere({
  session: s,
  prHref,
  Link,
}: {
  session: LiveSession;
  prHref: (session: LiveSession) => string | null;
  Link: ComponentType<ViewerLinkProps>;
}) {
  const href = s.pr ? prHref(s) : null;
  return (
    <div className="local-session-where">
      <span className="local-session-repo">{s.repository}</span>
      {s.branch ? <code className="local-session-branch">{s.branch}</code> : null}
      {s.pr ? (
        href ? (
          <Link href={href} className="local-session-pr">
            PR #{s.pr}
          </Link>
        ) : (
          <span className="local-session-pr">PR #{s.pr}</span>
        )
      ) : null}
    </div>
  );
}

const reasonIcons: Record<WaitingReason, ComponentType<IconProps>> = {
  permission: WarningIcon,
  input: CommentsIcon,
  turn_finished: CircleCheckIcon,
  quiet: CircleDashedIcon,
};

/** Why the session waits, with its icon: a permission or a question in the
 *  caution tone, a finished turn quieter. */
function WaitingFor({ reason }: { reason: WaitingReason }) {
  const Icon = reasonIcons[reason];
  return (
    <span className={`local-session-state local-needs-reason ${reason}`}>
      <Icon size={12} className="flex-none" /> {waitingReasons[reason]}
    </span>
  );
}

/** The agent sessions in progress on a pull request's branch, on its page,
 *  each with the way into its transcript. Once one ends the page reads its
 *  full session from disk, as for any finished session. */
export function LiveSessions({
  sessions,
  Link,
}: {
  sessions: LiveSession[];
  Link: ComponentType<ViewerLinkProps>;
}) {
  const now = useNow();
  if (sessions.length === 0) return null;
  return (
    <section aria-label="Sessions in progress" className="mb-3 local-panel local-pr-sessions">
      <h3>Sessions in progress</h3>
      <ul>
        {sessions.map((s) => (
          <li key={sessionKey(s)}>
            <span className="local-pr-session-who">
              <Agent agent={s.agent} />
              {s.state !== "running" && s.waiting_reason ? (
                <WaitingFor reason={s.waiting_reason} />
              ) : (
                <SessionState session={s} />
              )}
            </span>
            {s.title ? (
              <span className="local-pr-session-title" title={s.title}>
                {s.title}
              </span>
            ) : null}
            <span className="local-pr-session-meta">
              {s.branch ? <code className="local-session-branch">{s.branch}</code> : null}
              <span title={absolute(s.last_activity_at)}>
                Last activity {formatRelative(s.last_activity_at, now)}
              </span>
              <Link
                href={transcriptHref(s)}
                aria-label={`Open transcript: ${agentName(s.agent)} on ${s.branch ?? s.repository}`}
              >
                Open transcript
              </Link>
            </span>
            <LiveSessionDecisions session={s} />
          </li>
        ))}
      </ul>
    </section>
  );
}

/** A duration as a clock reads it: "41s", "7m 02s", "1h 05m". */
function clock(seconds: number): string {
  const total = Math.max(0, Math.floor(seconds));
  if (total < 60) return `${total}s`;
  const minutes = Math.floor(total / 60);
  if (minutes < 60) return `${minutes}m ${String(total % 60).padStart(2, "0")}s`;
  return `${Math.floor(minutes / 60)}h ${String(minutes % 60).padStart(2, "0")}m`;
}

/** How long an ended session lasted, as the compact row shows it: "40 s",
 *  "25 min", "2.4 h". */
function span(seconds: number): string {
  if (seconds < 3600) return lasted(seconds);
  return `${(seconds / 3600).toFixed(1)} h`;
}

const filesTouched = (n: number) => `${n} ${n === 1 ? "file" : "files"} touched`;

/** What names the session on screen: its title, else its branch or repository. */
const sessionName = (s: Pick<LiveSession, "title" | "branch" | "repository">) =>
  s.title ?? s.branch ?? s.repository;

/** The label a repeated button carries for assistive technology. */
const labelFor = (what: string, s: LiveSession) =>
  `${what}: ${agentName(s.agent)} on ${sessionName(s)}`;

const firstLine = (text: string, max = 60) => {
  const line = text.split("\n")[0]!.trim();
  return line.length > max ? `${line.slice(0, max - 1)}…` : line;
};

/** What a permission request would do, short enough for a notice. */
const subject = (r: AgentRequest) => firstLine(r.input[0]?.value ?? "") || r.tool || "the request";

/** The tool a step used, as its chip colour: Read, Edit (and the other
 *  writers), Bash, or neutral. */
function toolTone(tool: string): string {
  if (tool === "Read") return "read";
  if (tool === "Edit" || tool === "Write" || tool === "MultiEdit" || tool === "NotebookEdit")
    return "edit";
  if (tool === "Bash") return "bash";
  return "other";
}

function permissionTitle(r: AgentRequest): string {
  const who = agentName(r.agent);
  if (r.tool === "Bash") return `${who} wants to run a command`;
  if (["Edit", "Write", "MultiEdit", "NotebookEdit"].includes(r.tool))
    return `${who} wants to edit a file`;
  return `${who} wants to use ${r.tool || "a tool"}`;
}

type Phase =
  | { kind: "idle" }
  | { kind: "pending" }
  | { kind: "sent" }
  | { kind: "gone" }
  | { kind: "error"; message: string };

/** Posts an answer, showing at once that it is on its way: the card's
 *  buttons are disabled before the request starts. It stays "sent" until the
 *  next `live` refresh drops the request and the card with it. */
function useAnswer(request: AgentRequest, onDone: (text: string) => void) {
  const [phase, setPhase] = useState<Phase>({ kind: "idle" });
  useEffect(() => {
    if (phase.kind !== "gone") return;
    const timer = setTimeout(() => setPhase({ kind: "idle" }), 4000);
    return () => clearTimeout(timer);
  }, [phase.kind]);
  const answer = async (body: AnswerBody, done: string) => {
    setPhase({ kind: "pending" });
    try {
      await answerRequest(request.id, body);
      setPhase({ kind: "sent" });
      onDone(done);
    } catch (e) {
      if (e instanceof LocalRequestError && e.code === "request_gone") setPhase({ kind: "gone" });
      else
        setPhase({
          kind: "error",
          message: e instanceof Error ? e.message : "Could not send the answer.",
        });
    }
  };
  return {
    phase,
    answer,
    busy: phase.kind === "pending" || phase.kind === "sent" || phase.kind === "gone",
  };
}

function PhaseNote({ phase }: { phase: Phase }) {
  if (phase.kind === "pending") return <p className="local-feed-note">Sending…</p>;
  if (phase.kind === "sent") return <p className="local-feed-note">Sent</p>;
  if (phase.kind === "gone")
    return <p className="local-feed-note">Already answered in the terminal</p>;
  if (phase.kind === "error")
    return (
      <p role="alert" className="local-feed-note error">
        {phase.message}
      </p>
    );
  return null;
}

/** The latest message the user typed to the session, on one line. */
function LastPrompt({ session }: { session?: LiveSession }) {
  const prompt = session?.activity?.last_prompt;
  return prompt ? (
    <p className="local-feed-prompt" title={prompt}>
      <span className="local-feed-prompt-label">You</span> {prompt}
    </p>
  ) : null;
}

/** What an agent's message opens with: its first paragraph, where an agent
 *  says what it did or asks. The transcript has the rest. */
const opening = (message: string) => message.trim().split(/\n\s*\n/)[0]!;

/** The session a card is about: its title and branch in one line, the
 *  opening of the agent's last words as a quote. */
function CardContext({ session, quote }: { session?: LiveSession; quote?: boolean }) {
  const message = session?.activity?.last_message;
  return (
    <>
      {session ? (
        <p className="local-feed-where">
          {sessionName({ ...session, branch: undefined })}
          {session.branch ? (
            <>
              {" · "}
              <code className="local-feed-mono">{session.branch}</code>
            </>
          ) : null}
        </p>
      ) : null}
      <LastPrompt session={session} />
      {message && quote ? (
        <blockquote className="local-feed-quote">
          “<InlineMarkdown>{opening(message)}</InlineMarkdown>”
        </blockquote>
      ) : null}
    </>
  );
}

function PermissionCard({
  request,
  session,
  now,
  onDone,
  row,
}: {
  request: AgentRequest;
  session?: LiveSession;
  now: number;
  onDone: (text: string) => void;
  row: { "data-row-id": string; "data-focused"?: true };
}) {
  const { phase, answer, busy } = useAnswer(request, onDone);
  const what = subject(request);
  const name = session ? sessionName(session) : request.tool;
  const label = (text: string) => `${text}: ${agentName(request.agent)} on ${name}`;
  return (
    <li className="local-feed-card local-feed-ask" {...row}>
      <div className="local-feed-head">
        <h3>{permissionTitle(request)}</h3>
        <span className="local-feed-time">
          waiting {clock((now - new Date(request.created_at).getTime()) / 1000)}
        </span>
      </div>
      <CardContext session={session} quote />
      {request.input.length > 0 ? (
        <div className="local-feed-inputs">
          {request.input.map((row, i) =>
            row.code && row.label === "Command" ? (
              <pre key={i} className="local-feed-code command">
                <span className="sr-only">{row.label}: </span>
                {row.value}
              </pre>
            ) : row.code ? (
              // Any other code row says what it is: "Other input" changes
              // what an approval does as much as the command.
              <div key={i} className="local-feed-code-row">
                <p className="local-feed-row">
                  <span>{row.label}</span>
                </p>
                <pre className="local-feed-code">{row.value}</pre>
              </div>
            ) : (
              <p key={i} className="local-feed-row">
                <span>{row.label}</span> {row.value}
              </p>
            ),
          )}
        </div>
      ) : null}
      <div className="local-feed-actions">
        <Button
          aria-label={label("Allow once")}
          disabled={busy}
          onClick={() => void answer({ behavior: "allow" }, `You allowed ${what} once.`)}
        >
          Allow once
        </Button>
        {request.suggestions.map((s) => (
          <Button
            key={s.id}
            variant="secondary"
            aria-label={label(s.label)}
            disabled={busy}
            onClick={() =>
              void answer(
                { behavior: "allow", suggestion: s.id },
                `${s.label.replace(/^Always allow /i, "")} is now always allowed.`,
              )
            }
          >
            {s.label}
          </Button>
        ))}
        <Button
          variant="secondary"
          className="local-feed-deny"
          aria-label={label("Deny")}
          disabled={busy}
          onClick={() => void answer({ behavior: "deny" }, `You denied ${what}.`)}
        >
          Deny
        </Button>
      </div>
      <PhaseNote phase={phase} />
    </li>
  );
}

function QuestionCard({
  request,
  session,
  now,
  onDone,
  row,
}: {
  request: AgentRequest;
  session?: LiveSession;
  now: number;
  onDone: (text: string) => void;
  row: { "data-row-id": string; "data-focused"?: true };
}) {
  const { phase, answer, busy } = useAnswer(request, onDone);
  const [picked, setPicked] = useState<Record<string, string[]>>({});
  const questions = request.questions;
  // One single-choice question is answered by its option; any other needs a
  // choice for every question and a Submit.
  const immediate = questions.length === 1 && !questions[0]!.multi_select;
  const name = session ? sessionName(session) : request.tool;
  const send = (chosen: Record<string, string[]>) =>
    answer(
      {
        behavior: "allow",
        answers: Object.fromEntries(
          Object.entries(chosen).map(([q, labels]) => [q, labels.join(", ")]),
        ),
      },
      `You answered ${agentName(request.agent)}: “${Object.values(chosen)
        .map((labels) => labels.join(", "))
        .join(" · ")}”.`,
    );
  const choose = (q: RequestQuestion, option: string) => {
    if (immediate) {
      void send({ [q.question]: [option] });
      return;
    }
    setPicked((current) => {
      const had = current[q.question] ?? [];
      const next = q.multi_select
        ? had.includes(option)
          ? had.filter((o) => o !== option)
          : [...had, option]
        : [option];
      return { ...current, [q.question]: next };
    });
  };
  const ready = questions.every((q) => (picked[q.question]?.length ?? 0) > 0);
  return (
    <li className="local-feed-card local-feed-ask" {...row}>
      <div className="local-feed-head">
        <h3>{agentName(request.agent)} has a question</h3>
        <span className="local-feed-time">
          waiting {clock((now - new Date(request.created_at).getTime()) / 1000)}
        </span>
      </div>
      <CardContext session={session} quote />
      {questions.map((q) => (
        <div key={q.question} role="group" aria-label={q.question} className="local-feed-question">
          {q.header ? <span className="local-feed-chip">{q.header}</span> : null}
          <p className="local-feed-ask-text">{q.question}</p>
          <div className="local-feed-actions">
            {q.options.map((o) => {
              const on = picked[q.question]?.includes(o.label) ?? false;
              return (
                <Button
                  key={o.label}
                  variant="secondary"
                  className="local-feed-option"
                  title={o.description}
                  aria-pressed={immediate ? undefined : on}
                  aria-label={`${o.label}: ${agentName(request.agent)} on ${name}`}
                  data-on={on || undefined}
                  disabled={busy}
                  onClick={() => choose(q, o.label)}
                >
                  <span>{o.label}</span>
                  {o.description ? <small>{o.description}</small> : null}
                </Button>
              );
            })}
          </div>
        </div>
      ))}
      {immediate ? null : (
        <div className="local-feed-actions">
          <Button
            aria-label={`Submit: ${agentName(request.agent)} on ${name}`}
            disabled={busy || !ready}
            onClick={() => void send(picked)}
          >
            Submit
          </Button>
        </div>
      )}
      <PhaseNote phase={phase} />
    </li>
  );
}

/** A composer's send: a paper plane in a small green square, faded until there is
 *  something to send, named for assistive technology. */
function SendButton({
  label,
  busy,
  disabled,
}: {
  label: string;
  busy: boolean;
  disabled: boolean;
}) {
  return (
    <button
      type="submit"
      className="local-feed-send-button"
      aria-label={label}
      title="Send"
      aria-busy={busy || undefined}
      disabled={disabled}
    >
      <PaperPlaneIcon size={14} />
    </button>
  );
}

/** A reply to a session waiting at its prompt, where the daemon offers it
 *  (`messageable`), opened with r or from the card's menu: Enter sends it,
 *  starting the session's next turn in its own terminal (the card moves to
 *  Running now when its prompt hook arrives), and Escape closes it. */
function Reply({
  session: s,
  onDone,
  close,
}: {
  session: LiveSession;
  onDone: (text: string) => void;
  close: () => void;
}) {
  const id = useId();
  const input = useRef<HTMLInputElement>(null);
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => input.current?.focus(), []);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const message = text.trim();
    if (!message || busy) return;
    setBusy(true);
    setError("");
    try {
      await messageSession(s.agent, s.session_id, message);
      onDone(`You replied to ${sessionName(s)}: “${message}”.`);
      close();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not reach the session.");
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="local-feed-send">
      <form className="local-feed-compose" onSubmit={(event) => void submit(event)}>
        <label htmlFor={id} className="sr-only">
          Reply
        </label>
        <input
          ref={input}
          id={id}
          value={text}
          maxLength={10000}
          disabled={busy}
          placeholder="Reply…"
          onChange={(event) => setText(event.target.value)}
          onKeyDown={(event) => {
            if (event.key !== "Escape") return;
            event.preventDefault();
            close();
          }}
        />
        <span className="local-feed-compose-hint" aria-hidden="true">
          Enter to send · Esc to close
        </span>
        <SendButton
          label={labelFor("Send reply", s)}
          busy={busy}
          disabled={busy || text.trim() === ""}
        />
      </form>
      {error ? (
        <p role="alert" className="local-feed-note error">
          {error}
        </p>
      ) : null}
    </div>
  );
}

/** What a waiting card says of a wait only its terminal can answer. A
 *  finished turn needs no note, nor a quiet one, whose muted dot and time say
 *  it. */
const waitNotes: Partial<Record<WaitingReason, string>> = {
  permission: "Asks permission in its terminal",
  input: "Asks a question in its terminal",
};

/** A session waiting without a request the app can answer: it finished its
 *  turn, asks for something only its terminal can answer, or went quiet. */
function WaitingCard({
  session: s,
  since,
  now,
  open,
  row,
  onDone,
  replying,
  closeReply,
}: {
  session: LiveSession;
  since: string;
  now: number;
  open: (s: LiveSession, children: ReactNode) => ReactNode;
  row: RowProps;
  /** Whether its reply is open (Reply). */
  replying: boolean;
  closeReply: () => void;
  onDone: (text: string) => void;
}) {
  const reason = s.waiting_reason!;
  const message = s.activity?.last_message;
  return (
    <li className="local-feed-card quiet" {...row(s)}>
      {/* The agent by its mark, then the session by its title and branch:
       *  why it waits shows in what it said, or in the note below. */}
      <div className="local-feed-head">
        <AgentMark agent={s.agent} />
        <h3 className="local-feed-name">{open(s, sessionName({ ...s, branch: undefined }))}</h3>
        {s.branch ? <code className="local-feed-mono local-feed-branch">{s.branch}</code> : null}
        <span
          className={`local-feed-time local-feed-status ${reason === "quiet" ? "quiet" : "waiting"}`}
          title={absolute(since)}
        >
          {reason === "quiet" ? "quiet" : "waiting"}{" "}
          {clock((now - new Date(since).getTime()) / 1000)}
        </span>
      </div>
      <LastPrompt session={s} />
      {message ? (
        reason === "turn_finished" || reason === "quiet" ? (
          <p className="local-feed-soft">
            <InlineMarkdown>{opening(message)}</InlineMarkdown>
          </p>
        ) : (
          <blockquote className="local-feed-quote">
            “<InlineMarkdown>{opening(message)}</InlineMarkdown>”
          </blockquote>
        )
      ) : null}
      {s.waiting_message ? <p className="local-needs-message">{s.waiting_message}</p> : null}
      {waitNotes[reason] ? <p className="local-feed-note">{waitNotes[reason]}</p> : null}
      {s.messageable && replying ? <Reply session={s} onDone={onDone} close={closeReply} /> : null}
    </li>
  );
}

/** The message queued for a running session, or the box to write one: it is
 *  delivered when the session's turn ends. */
function SendWhenFinished({
  session: s,
  queued,
  onDone,
  replying,
  close,
}: {
  session: LiveSession;
  queued?: AgentMessage;
  onDone: (text: string) => void;
  /** Whether its box is open: with r or from the card's menu, as a waiting
   *  session's Reply. A queued message shows either way. */
  replying: boolean;
  close: () => void;
}) {
  const id = useId();
  const input = useRef<HTMLInputElement>(null);
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // What this card queued or cancelled shows until the `live` refresh
  // brings the daemon's own record.
  const [local, setLocal] = useState<string | null | undefined>(undefined);
  useEffect(() => setLocal(undefined), [queued?.text]);
  const shown = local !== undefined ? local : (queued?.text ?? null);
  useEffect(() => {
    if (replying) input.current?.focus();
  }, [replying]);
  const run = async (task: () => Promise<unknown>, after: () => void) => {
    setBusy(true);
    setError("");
    try {
      await task();
      after();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not reach the session.");
    } finally {
      setBusy(false);
    }
  };
  const submit = (event: FormEvent) => {
    event.preventDefault();
    const message = text.trim();
    if (!message || busy) return;
    void run(
      () => sendMessage(s.agent, s.session_id, message),
      () => {
        setLocal(message);
        setText("");
        onDone(`You queued a message for ${sessionName(s)}: “${message}”.`);
        close();
      },
    );
  };
  return (
    <div className="local-feed-send">
      {shown !== null ? (
        <div className="local-feed-queued">
          <p>Queued for when it finishes: “{shown}”</p>
          <Button
            variant="secondary"
            size="compact"
            aria-label={labelFor("Cancel queued message", s)}
            disabled={busy}
            onClick={() =>
              void run(
                () => cancelMessage(s.agent, s.session_id),
                () => setLocal(null),
              )
            }
          >
            {busy ? "Cancelling…" : "Cancel"}
          </Button>
        </div>
      ) : replying ? (
        <form className="local-feed-compose" onSubmit={submit}>
          <label htmlFor={id} className="sr-only">
            Send when it finishes
          </label>
          <input
            ref={input}
            id={id}
            value={text}
            maxLength={4000}
            disabled={busy}
            placeholder="Message for when its turn ends…"
            onChange={(event) => setText(event.target.value)}
            onKeyDown={(event) => {
              if (event.key !== "Escape") return;
              event.preventDefault();
              close();
            }}
          />
          <span className="local-feed-compose-hint" aria-hidden="true">
            Enter to send · Esc to close
          </span>
          <SendButton
            label={labelFor("Send message", s)}
            busy={busy}
            disabled={busy || text.trim() === ""}
          />
        </form>
      ) : null}
      {error ? (
        <p role="alert" className="local-feed-note error">
          {error}
        </p>
      ) : null}
    </div>
  );
}

function RunningCard({
  session: s,
  now,
  queued,
  prHref,
  Link,
  open,
  onDone,
  row,
  replying,
  closeReply,
}: {
  session: LiveSession;
  now: number;
  queued?: AgentMessage;
  prHref: (session: LiveSession) => string | null;
  Link: ComponentType<ViewerLinkProps>;
  open: (s: LiveSession, children: ReactNode) => ReactNode;
  onDone: (text: string) => void;
  row: RowProps;
  replying: boolean;
  closeReply: () => void;
}) {
  const running = s.state === "running";
  const steps = s.activity?.steps ?? [];
  const current = steps.at(-1);
  const files = s.activity?.files_touched;
  return (
    <li className="local-feed-card" {...row(s)}>
      <div className="local-feed-head">
        {open(s, <AgentMark agent={s.agent} />)}
        {s.title ? (
          <span className="local-feed-title" title={s.title}>
            {s.title}
          </span>
        ) : null}
        <SessionWhere session={s} prHref={prHref} Link={Link} />
        <span className="local-feed-time" title={absolute(s.started_at)}>
          {files ? <span className="local-feed-files">{filesTouched(files)} · </span> : null}
          {running
            ? clock((now - new Date(s.started_at).getTime()) / 1000)
            : `Quiet for ${lasted((now - new Date(s.last_activity_at).getTime()) / 1000)}`}
        </span>
      </div>
      <LastPrompt session={s} />
      {current ? (
        <div className="local-feed-step" title={current.target}>
          <span className={`local-feed-tool ${toolTone(current.tool)}`}>{current.tool}</span>
          {current.target ? <span className="local-feed-target"> {current.target}</span> : null}
          {running ? <span className="local-feed-caret" aria-hidden="true" /> : null}
        </div>
      ) : (
        <p className="local-feed-note" title={absolute(s.last_activity_at)}>
          Active {formatRelative(s.last_activity_at, now)}
        </p>
      )}
      {running ? (
        <SendWhenFinished
          session={s}
          queued={queued}
          onDone={onDone}
          replying={replying}
          close={closeReply}
        />
      ) : null}
    </li>
  );
}

/** One thing that waits on the person: a request to answer, or a session
 *  that finished or asks in its terminal. */
type Waiting = {
  key: string;
  since: string;
  session?: LiveSession;
  request?: AgentRequest;
};

type Section = "needs" | "running" | "ended";

/** Which sections are folded, for as long as the app runs: Ended recently
 *  starts folded, since it is looked back at, not watched. */
const folded: Record<Section, boolean> = { needs: false, running: false, ended: true };
let collapsedSections = folded;
/** Folds the sections as a fresh start of the app does. */
export function forgetFoldedSections() {
  collapsedSections = folded;
}

/** A section's heading, drawn as an inbox bucket's: a chevron that folds the
 *  section, its dot in the section's tone, its name and how many it holds. */
function SectionHead({
  section,
  title,
  count,
  collapsed,
  toggle,
}: {
  section: Section;
  title: string;
  count: number;
  collapsed: boolean;
  toggle: (section: Section) => void;
}) {
  return (
    <h2>
      <button
        type="button"
        className="local-feed-toggle"
        aria-expanded={!collapsed}
        aria-controls={`sessions-${section}`}
        onClick={() => toggle(section)}
      >
        <ChevronRightIcon
          size={12}
          className={collapsed ? "local-feed-chevron" : "local-feed-chevron open"}
        />
        <span className="local-feed-section-dot" aria-hidden="true" />
        {title}
        {count > 0 ? (
          <>
            {" "}
            <span className="local-feed-count">{count}</span>
          </>
        ) : null}
      </button>
    </h2>
  );
}

/** The sessions page: what waits on the person (answerable from here), what
 *  agents are doing on this computer now, and what ended in the last day.
 *  `prHref` is where a session's pull request opens in the app, or null when
 *  it is not one the app knows. Each card opens the session's transcript. */
export function SessionsPage({
  sessions,
  requests = noAgentRequests,
  prHref,
  Link,
  navigate = (href) => window.location.assign(href),
}: {
  sessions: LiveSession[];
  requests?: AgentRequests;
  prHref: (session: LiveSession) => string | null;
  Link: ComponentType<ViewerLinkProps>;
  /** Goes to a page of the app, as its links do. */
  navigate?: (href: string) => void;
}) {
  const now = useNow(1000);
  const [action, setAction] = useState("");
  const menu = useContextMenu();
  const live = sessions.filter((s) => s.state !== "ended");
  const find = (agent: string, id: string) =>
    live.find((s) => s.agent === agent && s.session_id === id);
  // A request is answered where it is; its session shows no second card.
  const asked = new Set<string>();
  const waiting: Waiting[] = [];
  for (const r of requests.requests) {
    const session = find(r.agent, r.session_id);
    if (!session && sessions.some((s) => s.agent === r.agent && s.session_id === r.session_id))
      continue;
    asked.add(`${r.agent}:${r.session_id}`);
    waiting.push({ key: `request:${r.id}`, since: r.created_at, session, request: r });
  }
  for (const s of needsYou(sessions))
    if (!asked.has(sessionKey(s)))
      waiting.push({ key: sessionKey(s), since: waitingSince(s), session: s });
  // What the app can answer leads, then the longest wait.
  waiting.sort(
    (a, b) =>
      Number(!a.request) - Number(!b.request) ||
      new Date(a.since).getTime() - new Date(b.since).getTime() ||
      a.key.localeCompare(b.key),
  );
  const waitingKeys = new Set(waiting.flatMap((w) => (w.session ? [sessionKey(w.session)] : [])));
  // A session in Needs you is not listed again under Running now.
  const active = live.filter((s) => !waitingKeys.has(sessionKey(s)));
  const endedAtOf = (s: LiveSession) => s.ended_at ?? s.last_activity_at;
  const ended = sessions
    .filter((s) => s.state === "ended")
    .sort((a, b) => endedAtOf(b).localeCompare(endedAtOf(a)));
  const queuedFor = (s: LiveSession) =>
    requests.messages.find((m) => m.agent === s.agent && m.session_id === s.session_id);
  const [collapsed, setCollapsed] = useState(collapsedSections);
  const toggle = (section: Section) => {
    collapsedSections = { ...collapsed, [section]: !collapsed[section] };
    setCollapsed(collapsedSections);
  };
  // The keys move through the rows on show only.
  const rows = useRowFocus<HTMLDivElement>("sessions", [
    ...(collapsed.needs ? [] : waiting.map((w) => (w.session ? sessionKey(w.session) : w.key))),
    ...(collapsed.running ? [] : active.map(sessionKey)),
    ...(collapsed.ended ? [] : ended.map(sessionKey)),
  ]);
  const row: RowProps = (s) => ({
    "data-row-id": sessionKey(s),
    "data-focused": rows.focused === sessionKey(s) || undefined,
  });
  // The reply open on a card, by its session's key, at most one.
  const [replying, setReplying] = useState<string | null>(null);
  // Closing a reply puts the keys back on its card.
  const closeReply = () => {
    if (replying) rows.rowLink(replying)?.focus();
    setReplying(null);
  };
  // A waiting session takes a reply now; a working one, when it finishes.
  const working = (s: LiveSession) => s.state === "running" && active.includes(s);
  const reply = (s: LiveSession) => {
    if (!s.messageable && !working(s)) return false;
    setReplying(sessionKey(s));
    return true;
  };
  useShortcuts((event) => {
    if (listKeys(event, rows) || !rows.focused) return;
    if (event.key === "r") {
      const s = sessions.find((x) => sessionKey(x) === rows.focused);
      // The key opens the box; it is not typed into it.
      if (s && reply(s)) event.preventDefault();
      return;
    }
    if (event.key !== "p") return;
    const pr = rows
      .rowLink(rows.focused)
      ?.closest("[data-row-id]")
      ?.querySelector<HTMLElement>(".local-session-pr");
    if (!pr) return;
    event.preventDefault();
    pr.click();
  });
  // A session's card opens its transcript: by its link, which the keys
  // focus and follow, or by a click anywhere else on the card, unless it
  // lands on something of its own to press or type in, or selects text.
  const open = (s: LiveSession, children: ReactNode) => (
    <Link
      href={transcriptHref(s)}
      data-row-link=""
      className="local-feed-open"
      aria-label={labelFor("Open transcript", s)}
    >
      {children}
    </Link>
  );
  const openCard = (event: MouseEvent) => {
    const target = event.target as HTMLElement;
    if (target.closest("a, button, input, textarea, select, label, summary")) return;
    if (window.getSelection()?.toString()) return;
    target.closest("[data-row-id]")?.querySelector<HTMLElement>(".local-feed-open")?.click();
  };
  // A right click on a session's card offers what can be done with it.
  const actionsFor = (s: LiveSession): MenuItem[] => {
    const items: MenuItem[] = [];
    if (s.messageable) items.push({ label: "Reply", hint: "r", onSelect: () => reply(s) });
    else if (working(s))
      items.push({ label: "Reply when it finishes", hint: "r", onSelect: () => reply(s) });
    items.push({ label: "Open transcript", onSelect: () => navigate(transcriptHref(s)) });
    const pr = prHref(s);
    if (pr)
      items.push({
        label: s.pr ? `Open pull request #${s.pr}` : "Open pull request",
        onSelect: () => navigate(pr),
      });
    if (s.endable)
      items.push({
        label: "End session",
        danger: true,
        onSelect: () =>
          void endSession(s.agent, s.session_id).then(
            () => setAction(`You ended ${sessionName(s)}.`),
            (e: unknown) =>
              setAction(
                `${sessionName(s)} was not ended: ${e instanceof Error ? e.message : "the request failed."}`,
              ),
          ),
      });
    return items;
  };
  const cardMenu = (event: MouseEvent) => {
    const id = (event.target as HTMLElement).closest("[data-row-id]")?.getAttribute("data-row-id");
    const s = id ? sessions.find((x) => sessionKey(x) === id) : undefined;
    if (s) menu.open(event, actionsFor(s));
  };
  return (
    <div ref={rows.list} className="local-feed" onClick={openCard} onContextMenu={cardMenu}>
      {menu.menu}
      <div aria-live="polite" className="local-feed-live">
        {action ? (
          <p className="local-feed-done">
            {waiting.length === 0 ? "Nobody is waiting on you. " : ""}
            {action}
          </p>
        ) : null}
      </div>
      {waiting.length > 0 ? (
        <section aria-label="Needs you" className="local-feed-section needs">
          <SectionHead
            section="needs"
            title="Needs you"
            count={waiting.length}
            collapsed={collapsed.needs}
            toggle={toggle}
          />
          <ul id="sessions-needs" className="local-feed-cards" hidden={collapsed.needs}>
            {waiting.map((w) =>
              w.request?.kind === "permission" ? (
                <PermissionCard
                  key={w.key}
                  request={w.request}
                  session={w.session}
                  now={now}
                  onDone={setAction}
                  row={w.session ? row(w.session) : { "data-row-id": w.key }}
                />
              ) : w.request ? (
                <QuestionCard
                  key={w.key}
                  request={w.request}
                  session={w.session}
                  now={now}
                  onDone={setAction}
                  row={w.session ? row(w.session) : { "data-row-id": w.key }}
                />
              ) : (
                <WaitingCard
                  key={w.key}
                  session={w.session!}
                  since={w.since}
                  now={now}
                  open={open}
                  row={row}
                  onDone={setAction}
                  replying={replying === sessionKey(w.session!)}
                  closeReply={closeReply}
                />
              ),
            )}
          </ul>
        </section>
      ) : null}
      <section aria-label="Running now" className="local-feed-section running">
        <SectionHead
          section="running"
          title="Running now"
          count={active.length}
          collapsed={collapsed.running}
          toggle={toggle}
        />
        {collapsed.running ? null : active.length === 0 ? (
          <p className="local-sessions-empty">
            {waiting.length > 0
              ? "No other agent sessions are running."
              : "No agent sessions are running. Sessions of connected agents in your repositories show here while they work."}
          </p>
        ) : (
          <ul id="sessions-running" className="local-feed-cards">
            {active.map((s) => (
              <RunningCard
                key={sessionKey(s)}
                session={s}
                now={now}
                queued={queuedFor(s)}
                prHref={prHref}
                Link={Link}
                open={open}
                onDone={setAction}
                row={row}
                replying={replying === sessionKey(s)}
                closeReply={closeReply}
              />
            ))}
          </ul>
        )}
      </section>
      <section aria-label="Ended recently" className="local-feed-section ended">
        <SectionHead
          section="ended"
          title="Ended recently"
          count={ended.length}
          collapsed={collapsed.ended}
          toggle={toggle}
        />
        {collapsed.ended ? null : ended.length === 0 ? (
          <p className="local-sessions-empty">No sessions ended in the last day.</p>
        ) : (
          <ul id="sessions-ended" className="local-feed-cards">
            {ended.map((s) => {
              const endedAt = endedAtOf(s);
              const seconds = Math.max(
                0,
                (new Date(endedAt).getTime() - new Date(s.started_at).getTime()) / 1000,
              );
              const files = s.activity?.files_touched;
              return (
                <li key={sessionKey(s)} className="local-feed-card compact" {...row(s)}>
                  {open(s, <AgentMark agent={s.agent} />)}
                  {s.title ? (
                    <span className="local-feed-title" title={s.title}>
                      {s.title}
                    </span>
                  ) : null}
                  <span className="local-feed-sub">
                    {span(seconds)}
                    {files ? ` · ${filesTouched(files)}` : ""}
                  </span>
                  <span className="local-feed-sub" title={absolute(endedAt)}>
                    Ended {formatRelative(endedAt, now)}
                  </span>
                </li>
              );
            })}
          </ul>
        )}
      </section>
    </div>
  );
}
