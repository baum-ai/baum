import {
  createContext,
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
import { InlineMarkdown, Markdown } from "@baum/viewer/components/markdown";
import {
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
  focusSession,
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
import { MenuButton, useContextMenu, type MenuItem } from "./context-menu";
import { ListHeading, type ListTone } from "./list";
import { useNow } from "./live";
import { useToast } from "./toast";
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

/** Whether a wait asks the person something (a permission or an answer),
 *  rather than only waiting for their next word (a finished turn, a quiet
 *  session): the one is Needs you, the other Waiting for you. */
export const asks = (reason?: WaitingReason) => reason === "permission" || reason === "input";

/** The sessions asking their user for a permission or an answer, which the
 *  sidebar counts in the caution tone. */
export const asksYou = (sessions: LiveSession[]) =>
  needsYou(sessions).filter((s) => asks(s.waiting_reason));

/** The sessions waiting on their user, the longest wait first: those that
 *  ask for a permission or an answer, finished their turn or went quiet. A
 *  session working, nor an ended one, is not one. */
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
      {s.branch ? (
        <code className="local-session-branch" title={s.branch}>
          {s.branch}
        </code>
      ) : null}
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

/** Brings a running session forward: its terminal tab, tmux pane or Claude
 *  desktop conversation, or only the window it runs in, as the daemon said
 *  it can (`focus`). What could not be done, or why nothing could, shows
 *  beside it. Nothing for a session it cannot bring forward. */
export function FocusSession({
  session: s,
  children,
}: {
  session: LiveSession;
  /** The actions after it; its note follows them, on its own line. */
  children?: ReactNode;
}) {
  const [state, setState] = useState<{ busy: boolean; note?: string; failed?: boolean }>({
    busy: false,
  });
  if (!s.focus || s.state === "ended") return children;
  const label = s.focus === "session" ? "Focus session" : "Show window";
  const focus = async () => {
    setState({ busy: true });
    try {
      const answer = await focusSession(s);
      setState({ busy: false, note: answer.message });
    } catch (error) {
      setState({
        busy: false,
        failed: true,
        note: error instanceof Error ? error.message : String(error),
      });
    }
  };
  return (
    <>
      <button
        type="button"
        className="inbox-action local-session-focus"
        disabled={state.busy}
        aria-busy={state.busy}
        aria-label={labelFor(label, s)}
        onClick={() => void focus()}
      >
        {state.busy ? "Focusing…" : label}
      </button>
      {children}
      {state.note ? (
        <span className="local-session-focus-note" role={state.failed ? "alert" : "status"}>
          {state.note}
        </span>
      ) : null}
    </>
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
              {s.branch ? (
                <code className="local-session-branch" title={s.branch}>
                  {s.branch}
                </code>
              ) : null}
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

/** What the page tells the person after they acted (as a toast): what
 *  happened, what it was about and on which session. */
type Notice = {
  title: string;
  detail?: string;
  agent?: string;
  session?: string;
  error?: boolean;
};

/** One choice of a request: a row with the number of its key (1–9, on the
 *  focused card), its label and what it means; pressed while chosen among
 *  several, `danger` for a refusal and `quiet` for typing one's own. */
function Choice({
  n,
  label,
  note,
  pressed,
  danger,
  quiet,
  ...button
}: {
  n?: number;
  label: string;
  note?: string;
  pressed?: boolean;
  danger?: boolean;
  quiet?: boolean;
  "aria-label": string;
  disabled: boolean;
  onClick: () => void;
}) {
  return (
    <li>
      <button
        type="button"
        className={["local-feed-choice", danger && "danger", quiet && "quiet"]
          .filter(Boolean)
          .join(" ")}
        aria-pressed={pressed}
        data-key={n ? String(n) : undefined}
        {...button}
      >
        <kbd className="local-feed-key" aria-hidden="true">
          {n ?? ""}
        </kbd>
        <span className="local-feed-choice-label">{label}</span>
        {note ? <span className="local-feed-choice-note">{note}</span> : null}
      </button>
    </li>
  );
}

/** An answer of the person's own to a question: Enter gives it, Escape
 *  closes it. */
function AnswerField({
  question,
  disabled,
  close,
  answer,
}: {
  question: string;
  disabled: boolean;
  close: () => void;
  answer: (text: string) => void;
}) {
  const id = useId();
  const input = useRef<HTMLInputElement>(null);
  const [text, setText] = useState("");
  useEffect(() => input.current?.focus(), []);
  return (
    <form
      className="local-feed-compose"
      onSubmit={(event) => {
        event.preventDefault();
        if (text.trim()) answer(text.trim());
      }}
    >
      <label htmlFor={id} className="sr-only">
        Your answer to: {question}
      </label>
      <input
        ref={input}
        id={id}
        value={text}
        maxLength={2000}
        disabled={disabled}
        placeholder="Your answer…"
        onChange={(event) => setText(event.target.value)}
        onKeyDown={(event) => {
          if (event.key !== "Escape") return;
          event.preventDefault();
          close();
        }}
      />
      <span className="local-feed-compose-hint" aria-hidden="true">
        Enter to answer · Esc to close
      </span>
      <SendButton label="Answer" busy={false} disabled={disabled || text.trim() === ""} />
    </form>
  );
}

/** What a permission request asks for, in words. */
function permissionAsk(r: AgentRequest): string {
  if (r.tool === "Bash") return "Wants to run a command";
  if (["Edit", "Write", "MultiEdit", "NotebookEdit"].includes(r.tool))
    return "Wants to edit a file";
  return `Wants to use ${r.tool || "a tool"}`;
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
function useAnswer(request: AgentRequest, onDone: (notice: Notice) => void) {
  const [phase, setPhase] = useState<Phase>({ kind: "idle" });
  useEffect(() => {
    if (phase.kind !== "gone") return;
    const timer = setTimeout(() => setPhase({ kind: "idle" }), 4000);
    return () => clearTimeout(timer);
  }, [phase.kind]);
  const answer = async (body: AnswerBody, done: Notice) => {
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

/** A request card's head, as a waiting session's: the agent's mark, the
 *  session's title (its transcript's link, when the session is listed) and
 *  branch, and how long it has waited; then what the user last typed. */
function RequestHead({
  request,
  session,
  now,
  open,
}: {
  request: AgentRequest;
  session?: LiveSession;
  now: number;
  open: (s: LiveSession, children: ReactNode) => ReactNode;
}) {
  return (
    <>
      <div className="local-feed-head">
        <AgentMark agent={request.agent} />
        <h3 className="local-feed-name">
          {session ? open(session, sessionName({ ...session, branch: undefined })) : request.tool}
        </h3>
        {session?.branch ? (
          <code className="local-feed-mono local-feed-branch" title={session.branch}>
            {session.branch}
          </code>
        ) : null}
        <span className="local-feed-time local-feed-status waiting">
          waiting {clock((now - new Date(request.created_at).getTime()) / 1000)}
        </span>
        {session ? <SessionMenu.Consumer>{(more) => more(session)}</SessionMenu.Consumer> : null}
      </div>
      <LastPrompt session={session} />
    </>
  );
}

function PermissionCard({
  request,
  session,
  now,
  onDone,
  row,
  open,
}: {
  request: AgentRequest;
  session?: LiveSession;
  now: number;
  onDone: (notice: Notice) => void;
  row: { "data-row-id": string; "data-focused"?: true };
  open: (s: LiveSession, children: ReactNode) => ReactNode;
}) {
  const { phase, answer, busy } = useAnswer(request, onDone);
  const what = subject(request);
  const name = session ? sessionName(session) : request.tool;
  const label = (text: string) => `${text}: ${agentName(request.agent)} on ${name}`;
  const notice = (title: string, detail = what): Notice => ({
    title,
    detail,
    agent: request.agent,
    session: name,
  });
  return (
    <li className="list-row local-feed-card local-feed-ask" {...row}>
      <RequestHead request={request} session={session} now={now} open={open} />
      <p className="local-feed-ask-text">{permissionAsk(request)}</p>
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
      <ul className="local-feed-choices">
        <Choice
          n={1}
          label="Allow once"
          aria-label={label("Allow once")}
          disabled={busy}
          onClick={() => void answer({ behavior: "allow" }, notice("Allowed once"))}
        />
        {request.suggestions.slice(0, 7).map((s, i) => (
          <Choice
            key={s.id}
            n={i + 2}
            label={s.label}
            aria-label={label(s.label)}
            disabled={busy}
            onClick={() =>
              void answer(
                { behavior: "allow", suggestion: s.id },
                notice("Always allowed", s.label.replace(/^Always allow /i, "")),
              )
            }
          />
        ))}
        <Choice
          n={Math.min(request.suggestions.length, 7) + 2}
          label="Deny"
          danger
          aria-label={label("Deny")}
          disabled={busy}
          onClick={() => void answer({ behavior: "deny" }, notice("Denied"))}
        />
      </ul>
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
  open,
}: {
  request: AgentRequest;
  session?: LiveSession;
  now: number;
  onDone: (notice: Notice) => void;
  row: { "data-row-id": string; "data-focused"?: true };
  open: (s: LiveSession, children: ReactNode) => ReactNode;
}) {
  const { phase, answer, busy } = useAnswer(request, onDone);
  const [picked, setPicked] = useState<Record<string, string[]>>({});
  // The question an answer is being typed for.
  const [typing, setTyping] = useState<string | null>(null);
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
      {
        title: `Answered “${Object.values(chosen)
          .map((labels) => labels.join(", "))
          .join(" · ")}”`,
        detail: questions.map((q) => q.question).join(" · "),
        agent: request.agent,
        session: name,
      },
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
    <li className="list-row local-feed-card local-feed-ask" {...row}>
      <RequestHead request={request} session={session} now={now} open={open} />
      {questions.map((q) => (
        <div key={q.question} role="group" aria-label={q.question} className="local-feed-question">
          <p className="local-feed-ask-text">
            {q.header ? <span className="local-feed-ask-header">{q.header} · </span> : null}
            {q.question}
          </p>
          {/* Each choice a row, numbered for its key when the card asks
              one thing, then a typed answer of the person's own. */}
          <ul className="local-feed-choices">
            {q.options.map((o, i) => (
              <Choice
                key={o.label}
                n={questions.length === 1 && i < 9 ? i + 1 : undefined}
                label={o.label}
                note={o.description}
                pressed={immediate ? undefined : (picked[q.question]?.includes(o.label) ?? false)}
                aria-label={`${o.label}: ${agentName(request.agent)} on ${name}`}
                disabled={busy}
                onClick={() => choose(q, o.label)}
              />
            ))}
            {(picked[q.question] ?? [])
              .filter((answer) => !q.options.some((o) => o.label === answer))
              .map((answer) => (
                <Choice
                  key={`typed:${answer}`}
                  label={`“${answer}”`}
                  pressed
                  aria-label={`${answer}: ${agentName(request.agent)} on ${name}`}
                  disabled={busy}
                  onClick={() => choose(q, answer)}
                />
              ))}
            <Choice
              n={questions.length === 1 && q.options.length < 9 ? q.options.length + 1 : undefined}
              label="Type an answer…"
              quiet
              aria-label={`Type an answer: ${agentName(request.agent)} on ${name}`}
              disabled={busy}
              onClick={() => setTyping(q.question)}
            />
          </ul>
          {typing === q.question ? (
            <AnswerField
              question={q.question}
              disabled={busy}
              close={() => setTyping(null)}
              answer={(text) => {
                setTyping(null);
                choose(q, text);
              }}
            />
          ) : null}
        </div>
      ))}
      {immediate ? null : (
        <div className="local-feed-actions">
          <Button
            aria-label={`Submit: ${agentName(request.agent)} on ${name}`}
            data-key="Enter"
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
  onDone: (notice: Notice) => void;
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
      onDone({ title: "Replied", detail: message, agent: s.agent, session: sessionName(s) });
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

/** What a session was started for, from the opening of its compaction
 *  summary (`summary`), without the label and quotes Claude Code writes it
 *  with ("Initial: \"…\""). */
const originalRequest = (summary: string) =>
  summary
    .replace(/^\s*(?:initial(?: request)?|primary request(?: and intent)?)\s*:\s*/i, "")
    .replace(/^["“](.*?)["”]?(\s*(?:…|\.\.\.))?$/s, "$1$2")
    .trim();

/** A session's preview under its row (Space), the row grown open: what it
 *  was started for, when its compaction summary says, and the agent's latest
 *  response, rendered, each beside its label. What the user last wrote is on
 *  the row already. */
function SessionPeek({ session: s }: { session: LiveSession }) {
  const said = s.activity?.last_message;
  const asked = s.summary ? originalRequest(s.summary) : "";
  return (
    <dl className="local-feed-peek" aria-label={`Preview of ${sessionName(s)}`} role="region">
      {asked ? (
        <>
          <dt>Original request</dt>
          <dd className="local-feed-peek-asked">{asked}</dd>
        </>
      ) : null}
      <dt>Latest response</dt>
      <dd>
        {said ? (
          <Markdown html={false} className="local-feed-peek-said">
            {said}
          </Markdown>
        ) : (
          <span className="local-feed-note">{agentName(s.agent)} has said nothing yet.</span>
        )}
      </dd>
    </dl>
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
  onDone: (notice: Notice) => void;
}) {
  const reason = s.waiting_reason!;
  const message = s.activity?.last_message;
  return (
    <li className="list-row local-feed-card quiet" {...row(s)}>
      {/* The agent by its mark, then the session by its title and branch:
       *  why it waits shows in what it said, or in the note below. */}
      <div className="local-feed-head">
        <AgentMark agent={s.agent} />
        <h3 className="local-feed-name">{open(s, sessionName({ ...s, branch: undefined }))}</h3>
        {s.branch ? (
          <code className="local-feed-mono local-feed-branch" title={s.branch}>
            {s.branch}
          </code>
        ) : null}
        <span
          className={`local-feed-time local-feed-status ${reason === "quiet" ? "quiet" : "waiting"}`}
          title={absolute(since)}
        >
          {reason === "quiet" ? "quiet" : "waiting"}{" "}
          {clock((now - new Date(since).getTime()) / 1000)}
        </span>
        <SessionMenu.Consumer>{(more) => more(s)}</SessionMenu.Consumer>
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
  onDone: (notice: Notice) => void;
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
        onDone({
          title: "Queued for when it finishes",
          detail: message,
          agent: s.agent,
          session: sessionName(s),
        });
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
  onDone: (notice: Notice) => void;
  row: RowProps;
  replying: boolean;
  closeReply: () => void;
}) {
  const running = s.state === "running";
  const steps = s.activity?.steps ?? [];
  const current = steps.at(-1);
  const files = s.activity?.files_touched;
  // What the agent last said says more of what it is doing than its last
  // tool call (often a shell or a script); the call shows until it speaks.
  const said = s.activity?.last_message;
  return (
    <li className="list-row local-feed-card" {...row(s)}>
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
        <SessionMenu.Consumer>{(more) => more(s)}</SessionMenu.Consumer>
      </div>
      <LastPrompt session={s} />
      {said ? (
        <div className="local-feed-said">
          <InlineMarkdown className="local-feed-said-text">{opening(said)}</InlineMarkdown>
          {running ? <span className="local-feed-caret" aria-hidden="true" /> : null}
        </div>
      ) : current ? (
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

type Section = "needs" | "waiting" | "running" | "ended";

/** A session's ⋯, which opens the menu a right click on its card opens; each
 *  card draws it at the end of its head. */
const SessionMenu = createContext<(s: LiveSession) => ReactNode>(() => null);

/** Which sections are folded, for as long as the app runs: Ended recently
 *  starts folded, since it is looked back at, not watched. */
const folded: Record<Section, boolean> = {
  needs: false,
  waiting: false,
  running: false,
  ended: true,
};
let collapsedSections = folded;
/** Folds the sections as a fresh start of the app does. */
export function forgetFoldedSections() {
  collapsedSections = folded;
}

const sectionTones: Record<Section, ListTone> = {
  needs: "caution",
  waiting: "info",
  running: "positive",
  ended: "quiet",
};

/** A section's heading, as every list's: a chevron that folds the section,
 *  its dot in the section's tone, its name and how many it holds. */
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
    <ListHeading
      title={title}
      count={count}
      tone={sectionTones[section]}
      collapsed={collapsed}
      toggle={() => toggle(section)}
      controls={`sessions-${section}`}
    />
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
  const toast = useToast();
  const notify = ({ agent, session, ...n }: Notice) => {
    const logo = agent ? agentLogos[agent] : undefined;
    toast({
      ...n,
      context: session,
      icon: logo ? { src: logo, alt: agentName(agent!) } : undefined,
    });
  };
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
  // What asks something of the person leads (Needs you); what only waits for
  // their next word follows (Waiting for you).
  const asking = waiting.filter((w) => w.request || asks(w.session?.waiting_reason));
  const idle = waiting.filter((w) => !asking.includes(w));
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
  const waitingKey = (w: Waiting) => (w.session ? sessionKey(w.session) : w.key);
  const rows = useRowFocus<HTMLDivElement>("sessions", [
    ...(collapsed.needs ? [] : asking.map(waitingKey)),
    ...(collapsed.waiting ? [] : idle.map(waitingKey)),
    ...(collapsed.running ? [] : active.map(sessionKey)),
    ...(collapsed.ended ? [] : ended.map(sessionKey)),
  ]);
  const row: RowProps = (s) => ({
    "data-row-id": sessionKey(s),
    "data-focused": rows.focused === sessionKey(s) || undefined,
  });
  // The session peeked at under its row (Space), by its key, at most one.
  const [peeking, setPeeking] = useState<string | null>(null);
  const peek = (s: LiveSession) =>
    setPeeking((current) => (current === sessionKey(s) ? null : sessionKey(s)));
  const peekRow = (s?: LiveSession) =>
    s && peeking === sessionKey(s) ? (
      <li key={`peek:${sessionKey(s)}`} className="local-feed-peek-row">
        <SessionPeek session={s} />
      </li>
    ) : null;
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
    // Escape closes a peek before it clears the row.
    if (event.key === "Escape" && peeking) {
      event.preventDefault();
      setPeeking(null);
      return;
    }
    if (event.key === " " && rows.focused) {
      const s = sessions.find((x) => sessionKey(x) === rows.focused);
      if (s) {
        event.preventDefault();
        peek(s);
        return;
      }
    }
    // A request card's choices answer to their keys (1–9, Enter to submit
    // several), before the list's own.
    const card = rows.focused
      ? [...(rows.list.current?.querySelectorAll<HTMLElement>("[data-row-id]") ?? [])].find(
          (el) => el.dataset.rowId === rows.focused,
        )
      : undefined;
    const choice = card?.querySelector<HTMLButtonElement>(
      `button[data-key="${CSS.escape(event.key)}"]`,
    );
    // Enter on a focused control already does what it says.
    const onControl =
      event.key === "Enter" &&
      event.target instanceof HTMLElement &&
      event.target.closest("a, button, input, textarea");
    if (choice && !onControl && !event.metaKey && !event.ctrlKey && !event.altKey) {
      event.preventDefault();
      if (!choice.disabled) choice.click();
      return;
    }
    if (listKeys(event, rows) || !rows.focused) return;
    if (event.key === "r") {
      const s = sessions.find((x) => sessionKey(x) === rows.focused);
      // The key opens the box; it is not typed into it.
      if (s && reply(s)) event.preventDefault();
      return;
    }
    if (event.key === "f") {
      const s = sessions.find((x) => sessionKey(x) === rows.focused);
      if (s && focusable(s)) {
        event.preventDefault();
        focus(s);
      }
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
  // A running session the daemon can bring forward is, from its menu or
  // its key; what could not be done is told as a notice.
  const focusable = (s: LiveSession) => Boolean(s.focus) && s.state !== "ended";
  const focus = (s: LiveSession) =>
    void focusSession(s).then(
      (answer) => {
        if (answer.message)
          notify({
            title: answer.focused === "session" ? "Focused the session" : "Showed the window",
            detail: answer.message,
            agent: s.agent,
            session: sessionName(s),
          });
      },
      (e: unknown) =>
        notify({
          title: "Could not bring the session forward",
          detail: e instanceof Error ? e.message : "The request failed.",
          agent: s.agent,
          session: sessionName(s),
          error: true,
        }),
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
    if (focusable(s))
      items.push({
        label: s.focus === "session" ? "Focus session" : "Show window",
        hint: "f",
        onSelect: () => focus(s),
      });
    items.push({ label: "Open transcript", onSelect: () => navigate(transcriptHref(s)) });
    items.push({ label: "Peek", hint: "Space", onSelect: () => peek(s) });
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
            () => notify({ title: "Ended the session", agent: s.agent, session: sessionName(s) }),
            (e: unknown) =>
              notify({
                title: "Could not end the session",
                detail: e instanceof Error ? e.message : "The request failed.",
                agent: s.agent,
                session: sessionName(s),
                error: true,
              }),
          ),
      });
    return items;
  };
  const cardMenu = (event: MouseEvent) => {
    const id = (event.target as HTMLElement).closest("[data-row-id]")?.getAttribute("data-row-id");
    const s = id ? sessions.find((x) => sessionKey(x) === id) : undefined;
    if (s) menu.open(event, actionsFor(s), sessionName(s));
  };
  const more = (s: LiveSession) => (
    <MenuButton label={sessionName(s)} items={() => actionsFor(s)} menu={menu} />
  );
  // What waits on the person, drawn the same in both of its sections.
  const waitingRows = (list: Waiting[]) =>
    list.flatMap((w) => [
      w.request?.kind === "permission" ? (
        <PermissionCard
          key={w.key}
          request={w.request}
          session={w.session}
          now={now}
          onDone={notify}
          row={w.session ? row(w.session) : { "data-row-id": w.key }}
          open={open}
        />
      ) : w.request ? (
        <QuestionCard
          key={w.key}
          request={w.request}
          session={w.session}
          now={now}
          onDone={notify}
          row={w.session ? row(w.session) : { "data-row-id": w.key }}
          open={open}
        />
      ) : (
        <WaitingCard
          key={w.key}
          session={w.session!}
          since={w.since}
          now={now}
          open={open}
          row={row}
          onDone={notify}
          replying={replying === sessionKey(w.session!)}
          closeReply={closeReply}
        />
      ),
      peekRow(w.session),
    ]);
  return (
    <div ref={rows.list} className="local-feed" onClick={openCard} onContextMenu={cardMenu}>
      <SessionMenu.Provider value={more}>
        {menu.menu}
        {asking.length > 0 ? (
          <section aria-label="Needs you" className="local-feed-section needs">
            <SectionHead
              section="needs"
              title="Needs you"
              count={asking.length}
              collapsed={collapsed.needs}
              toggle={toggle}
            />
            <ul id="sessions-needs" className="list local-feed-cards" hidden={collapsed.needs}>
              {waitingRows(asking)}
            </ul>
          </section>
        ) : null}
        {idle.length > 0 ? (
          <section aria-label="Waiting for you" className="local-feed-section waiting">
            <SectionHead
              section="waiting"
              title="Waiting for you"
              count={idle.length}
              collapsed={collapsed.waiting}
              toggle={toggle}
            />
            <ul id="sessions-waiting" className="list local-feed-cards" hidden={collapsed.waiting}>
              {waitingRows(idle)}
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
            <ul id="sessions-running" className="list local-feed-cards">
              {active.flatMap((s) => [
                <RunningCard
                  key={sessionKey(s)}
                  session={s}
                  now={now}
                  queued={queuedFor(s)}
                  prHref={prHref}
                  Link={Link}
                  open={open}
                  onDone={notify}
                  row={row}
                  replying={replying === sessionKey(s)}
                  closeReply={closeReply}
                />,
                peekRow(s),
              ])}
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
            <ul id="sessions-ended" className="list local-feed-cards">
              {ended.flatMap((s) => {
                const endedAt = endedAtOf(s);
                const seconds = Math.max(
                  0,
                  (new Date(endedAt).getTime() - new Date(s.started_at).getTime()) / 1000,
                );
                const files = s.activity?.files_touched;
                return [
                  <li key={sessionKey(s)} className="list-row local-feed-card compact" {...row(s)}>
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
                    {more(s)}
                  </li>,
                  peekRow(s),
                ];
              })}
            </ul>
          )}
        </section>
      </SessionMenu.Provider>
    </div>
  );
}
