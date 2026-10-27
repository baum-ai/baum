import { harnessName } from "../agent-names";
import {
  CATEGORIES,
  type DecisionCategory,
  type DecisionDetail,
  type DecisionPerson,
  type DecisionPullRequest,
  type DecisionSummary,
  personLabel,
  reviewStateLabel,
  whoPresentation,
} from "../decision-log";
import { ViewerLink } from "../navigation";
import { humanize, pullRequestStateTone } from "../review-document";
import { MUTED_STRONG, SERIES } from "../series";
import { Avatar } from "./avatar";
import { QuotedPrompts } from "./chat";
import { cn, FOCUS_RING } from "./classnames";
import { formatCount, formatDate, formatTime, participantColor } from "./format";
import { CheckIcon, ChevronDownIcon } from "./icons";
import { InlineMarkdown } from "./markdown";
import { DecisionByBadge } from "./review-parts";
import { Badge, Button, Spinner } from "./ui";
import { type ReactNode, useEffect, useId, useRef, useState } from "react";

/** The pieces of a decision log's timeline: a decision's row and what
 *  expanding it shows. The host supplies the rows and details, where a pull
 *  request opens and how a session is loaded and drawn; nothing here
 *  fetches. */

export type DetailState =
  | { status: "loading" }
  | { status: "ready"; detail: DecisionDetail }
  | { status: "missing" }
  | { status: "failed" };

/** The chips of the review viewer's anchors (`chipClass` in its
 *  `review-parts.tsx`), as a button: a file here filters the log rather than
 *  opening the file. */
const FILE_CHIP = cn(
  "inline-flex max-w-full min-w-0 cursor-pointer items-center gap-1 rounded-(--radius-sm) border border-(--border-subtle) bg-(--surface-sunken) px-1.5 py-0.5 text-xs leading-[1.4] text-(--text-secondary) hover:text-(--text-primary)",
  FOCUS_RING,
);

/** A category is a kind of choice, not a state, so it is drawn the way an
 *  origin is (`OriginTag`): a categorical dot beside its name, never a status
 *  badge. The ninth category takes the muted step, as a ninth series does. */
export function CategoryTag({ category }: { category: DecisionCategory }) {
  const index = CATEGORIES.indexOf(category);
  return (
    <span className="inline-flex items-center gap-1.5 text-xs whitespace-nowrap text-(--text-secondary)">
      <span
        aria-hidden
        className="h-2 w-2 flex-none rounded-full"
        style={{ background: SERIES[index] ?? MUTED_STRONG }}
      />
      {humanize(category)}
    </span>
  );
}

/** Who drove a session nothing names: a cloud agent's session with no pull
 *  request yet, say. Said rather than left out, so a row without a name does
 *  not read as one nobody drove. */
export function UnknownPerson() {
  return <span className="text-(--text-tertiary) italic">Unknown author</span>;
}

/** A person by name, with their picture or initials. Somebody the platform
 *  does not know reads as the address their session was recorded under. */
export function PersonName({
  person,
  size = 20,
  className,
}: {
  person: DecisionPerson;
  size?: number;
  className?: string;
}) {
  const label = personLabel(person);
  return (
    <span
      className={cn("inline-flex min-w-0 items-center gap-1.5", className)}
      title={person.email}
    >
      <Avatar src={person.avatar_url} name={label} size={size} />
      <span className="truncate">{label}</span>
    </span>
  );
}

const SUMMARY_WEIGHT = {
  strong: "text-base font-medium text-(--text-primary)",
  normal: "text-base text-(--text-primary)",
  quiet: "text-xs text-(--text-tertiary)",
} as const;

/** One decision in the timeline: a disclosure whose panel holds the detail. */
export function DecisionRow({
  decision,
  expanded,
  onToggle,
  children,
  rowRef,
  withDate = false,
  repository,
}: {
  decision: DecisionSummary;
  expanded: boolean;
  onToggle: () => void;
  /** The detail, rendered only while expanded. */
  children: ReactNode;
  rowRef?: (element: HTMLLIElement | null) => void;
  /** Says the day as well as the time, for a row not under a day's header. */
  withDate?: boolean;
  /** The repository's name, for a log over several. */
  repository?: string;
}) {
  const id = useId();
  const who = whoPresentation(decision.who, decision.person);
  const quiet = who.weight === "quiet";
  return (
    <li
      ref={rowRef}
      data-decision={decision.id}
      data-who={decision.who}
      className={cn(
        "border-b border-(--border-subtle) last:border-b-0",
        expanded && "bg-(--surface-wash)",
      )}
    >
      <button
        type="button"
        id={`${id}-button`}
        aria-expanded={expanded}
        aria-controls={`${id}-panel`}
        onClick={onToggle}
        className={cn(
          "flex w-full cursor-pointer items-start gap-2.5 px-3 text-left hover:bg-(--surface-hover) sm:px-4",
          quiet ? "py-1.5" : "py-2.5",
          FOCUS_RING,
        )}
      >
        <ChevronDownIcon
          size={14}
          className={cn(
            "mt-1 flex-none text-(--text-tertiary) transition-transform motion-reduce:transition-none",
            !expanded && "-rotate-90",
          )}
        />
        <span
          className={cn(
            "flex-none pt-0.5 text-xs text-(--text-tertiary) tabular-nums",
            withDate ? "w-24" : "w-16",
          )}
        >
          {withDate ? (
            <>
              {formatDate(decision.decided_at)}
              <br />
              {formatTime(decision.decided_at)}
            </>
          ) : (
            formatTime(decision.decided_at)
          )}
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex flex-wrap items-center gap-x-2.5 gap-y-1 text-xs text-(--text-secondary)">
            <DecisionByBadge decision={who.marker} />
            {decision.person ? (
              <PersonName person={decision.person} size={quiet ? 16 : 20} />
            ) : (
              <UnknownPerson />
            )}
            <CategoryTag category={decision.category} />
            {repository ? <span className="text-(--text-tertiary)">{repository}</span> : null}
            {decision.superseded ? <Badge size="compact">Superseded</Badge> : null}
          </span>
          <span
            className={cn(
              "mt-1 block break-words",
              SUMMARY_WEIGHT[who.weight],
              decision.superseded && "line-through decoration-(--text-tertiary)",
            )}
          >
            <InlineMarkdown>{decision.summary}</InlineMarkdown>
          </span>
          {who.approvedBy ? (
            <span className="mt-0.5 inline-flex items-center gap-1 text-xs text-(--text-secondary)">
              <CheckIcon size={12} />
              approved by {who.approvedBy}
            </span>
          ) : null}
        </span>
      </button>
      <div
        id={`${id}-panel`}
        role="region"
        aria-labelledby={`${id}-button`}
        hidden={!expanded}
        className="px-3 pb-4 sm:pr-4 sm:pl-[6.75rem]"
      >
        {expanded ? children : null}
      </div>
    </li>
  );
}

/** What expanding a decision shows: its files, pull requests, session and the
 *  prompts it cites. `decisionId` picks it among those the detail carries. */
export function DecisionDetailBody<T>({
  state,
  decisionId,
  at,
  withSummary = false,
  pullRequestHref,
  openSession,
  onFile,
  onRetry,
}: {
  state: DetailState | undefined;
  decisionId: string;
  /** Its session and first cited turn, when known without the detail. */
  at?: SessionTurn;
  /** Repeat the decision itself, for a detail shown away from its row. */
  withSummary?: boolean;
  /** Where the host opens one of the detail's pull requests, by number. */
  pullRequestHref: (number: number) => string;
  /** Loads the session a decision was made in and draws it at the cited
   *  turn; see {@link OpenSessionSource}. */
  openSession: OpenSessionSource<T>;
  onFile: (path: string) => void;
  onRetry: () => void;
}) {
  if (!state || state.status === "loading") {
    return (
      <p className="flex items-center gap-2 py-2 text-base text-(--text-tertiary) italic">
        <Spinner />
        Loading this decision…
      </p>
    );
  }
  if (state.status === "failed") {
    return (
      <p role="alert" className="flex flex-wrap items-center gap-2 py-2 text-base">
        <span className="text-(--status-critical)">This decision could not be loaded.</span>
        <Button variant="secondary" size="tiny" onClick={onRetry}>
          Try again
        </Button>
      </p>
    );
  }
  if (state.status === "missing") {
    return (
      <div className="flex flex-col gap-3 py-2">
        <p role="status" className="text-base text-(--text-secondary)">
          This decision is no longer available.
        </p>
        {at ? <OpenSession at={at} source={openSession} /> : null}
      </div>
    );
  }

  const { detail } = state;
  const decision = detail.decisions.find((each) => each.id === decisionId);
  if (!decision) return null;
  const turns = decision.turns
    .map((turn) => detail.turns.find((each) => each.ordinal === turn))
    .filter((turn) => turn !== undefined);
  const who = whoPresentation(decision.who, detail.session.person);

  return (
    <div className="flex flex-col gap-4 pt-1">
      {withSummary ? (
        <div>
          <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1 text-xs text-(--text-secondary)">
            <DecisionByBadge decision={who.marker} />
            <CategoryTag category={decision.category} />
            {decision.superseded ? <Badge size="compact">Superseded</Badge> : null}
            <span className="text-(--text-tertiary)">
              {formatDate(decision.decided_at)} {formatTime(decision.decided_at)}
            </span>
          </div>
          <p
            className={cn(
              "mt-1 break-words",
              SUMMARY_WEIGHT[who.weight],
              decision.superseded && "line-through decoration-(--text-tertiary)",
            )}
          >
            <InlineMarkdown>{decision.summary}</InlineMarkdown>
          </p>
          {who.approvedBy ? (
            <p className="mt-0.5 inline-flex items-center gap-1 text-xs text-(--text-secondary)">
              <CheckIcon size={12} />
              approved by {who.approvedBy}
            </p>
          ) : null}
        </div>
      ) : null}

      {decision.rejected_alternative ? (
        <p className="text-base text-(--text-secondary)">
          <span className="text-(--text-tertiary)">Instead of: </span>
          <InlineMarkdown>{decision.rejected_alternative}</InlineMarkdown>
        </p>
      ) : null}

      {decision.files.length > 0 ? (
        <DetailPart label="Files">
          <ul className="flex flex-wrap gap-1.5">
            {decision.files.map((path) => (
              <li key={path} className="min-w-0">
                <button
                  type="button"
                  className={FILE_CHIP}
                  title={`Show decisions about ${path}`}
                  onClick={() => onFile(path)}
                >
                  <span className="truncate font-mono">{path}</span>
                </button>
              </li>
            ))}
          </ul>
        </DetailPart>
      ) : null}

      {detail.pull_requests.length > 0 ? (
        <DetailPart label="Pull requests">
          <ul className="flex flex-col gap-1.5">
            {detail.pull_requests.map((pull) => {
              return (
                <li
                  key={pull.number}
                  className="flex flex-wrap items-center gap-x-2 gap-y-1 text-base"
                >
                  <ViewerLink
                    href={pullRequestHref(pull.number)}
                    className="min-w-0 text-(--text-primary) hover:underline"
                  >
                    <span className="text-(--text-tertiary) tabular-nums">#{pull.number}</span>{" "}
                    {pull.title}
                  </ViewerLink>
                  <PullRequestState pull={pull} />
                </li>
              );
            })}
          </ul>
        </DetailPart>
      ) : null}

      <DetailPart label="Session">
        <dl className="flex flex-wrap gap-x-5 gap-y-1.5 text-base">
          <SessionFact label="Author">
            {detail.session.person ? (
              <PersonName person={detail.session.person} />
            ) : (
              <UnknownPerson />
            )}
          </SessionFact>
          <SessionFact label="Agent">{harnessName(detail.session.vendor)}</SessionFact>
          {detail.session.branch ? (
            <SessionFact label="Branch">
              <span className="font-mono break-all">{detail.session.branch}</span>
            </SessionFact>
          ) : null}
          {detail.session.started_at ? (
            <SessionFact label="Started">
              {formatDate(detail.session.started_at)} {formatTime(detail.session.started_at)}
            </SessionFact>
          ) : null}
        </dl>
      </DetailPart>

      {detail.prompts_withheld ? (
        <p className="text-base text-(--text-tertiary) italic">
          Prompt text is hidden: this session also belongs to repositories you can't read.
        </p>
      ) : turns.length > 0 ? (
        <DetailPart label={turns.length === 1 ? "Prompt" : "Prompts"}>
          <CitedPrompts turns={turns} person={detail.session.person} />
        </DetailPart>
      ) : null}

      {/* The session refuses a reader who cannot read every repository it
          belongs to, the same rule that withholds the prompts. */}
      {detail.prompts_withheld ? null : (
        <OpenSession
          at={{ sessionId: detail.session.id, turn: decision.turn }}
          source={openSession}
        />
      )}
    </div>
  );
}

/** A pull request's state, whether it is a draft, and its review's state. */
export function PullRequestState({ pull }: { pull: DecisionPullRequest }) {
  const state = pull.merged ? "merged" : pull.state;
  return (
    <>
      <Badge size="compact" tone={pullRequestStateTone(state)}>
        {state}
      </Badge>
      {pull.draft ? <Badge size="compact">draft</Badge> : null}
      <span className="text-xs text-(--text-tertiary)">{reviewStateLabel(pull.review_state)}</span>
    </>
  );
}

export function DetailPart({ label, children }: { label: string; children: ReactNode }) {
  return (
    <section className="min-w-0">
      <h4 className="mb-1.5 text-xs tracking-[0.08em] text-(--text-tertiary) uppercase">{label}</h4>
      {children}
    </section>
  );
}

/** The prompts a decision cites, drawn as the session's transcript draws
 *  them: bubbles in their author's name, shown as typed, with a pill for each
 *  new day. The detail carries no replies, so none are drawn. */
function CitedPrompts({
  turns,
  person,
}: {
  turns: DecisionDetail["turns"];
  person: DecisionPerson | null;
}) {
  const author = person ? personLabel(person) : null;
  return (
    <QuotedPrompts
      prompts={turns.map((turn) => ({
        id: String(turn.ordinal),
        index: turn.ordinal,
        text: turn.text,
        at: turn.triggered_at,
      }))}
      author={author}
      avatar={
        person && author ? (
          <Avatar
            src={person.avatar_url}
            name={author}
            size={28}
            tint={participantColor(person.email)}
          />
        ) : null
      }
    />
  );
}

function SessionFact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-(--text-tertiary)">{label}</dt>
      <dd className="mt-0.5 text-(--text-primary)">{children}</dd>
    </div>
  );
}

/** A session and the turn a decision cites in it. */
export type SessionTurn = { sessionId: string; turn: number };

/** How the host opens a decision's session. `load` fetches the session by
 *  id, as data only the host reads; `render` draws it at the turn the decision
 *  cites, such as the host's transcript card, and is called on every render
 *  with the current turn, so the panel follows the decision shown. The panel
 *  loads once per session, the first time the reader opens it, and keeps the
 *  session while it is closed and opened again; another session loads afresh.
 *  A rejection reads as a session that could not be loaded. */
export type OpenSessionSource<T> = {
  load: (sessionId: string, signal: AbortSignal) => Promise<T>;
  render: (session: T, at: SessionTurn) => ReactNode;
};

type LoadedSession<T> =
  | { status: "loading" }
  | { status: "failed" }
  | { status: "ready"; session: T };

/** The session opens in place, at the turn the decision cites, as the host
 *  draws it. */
function OpenSession<T>({ at, source }: { at: SessionTurn; source: OpenSessionSource<T> }) {
  const [open, setOpen] = useState(false);
  const [loaded, setLoaded] = useState<LoadedSession<T>>({ status: "loading" });
  // Another session starts over; a new turn in the same one does not.
  const [loadedFor, setLoadedFor] = useState(at.sessionId);
  if (loadedFor !== at.sessionId) {
    setLoadedFor(at.sessionId);
    setLoaded({ status: "loading" });
  }
  const panelId = useId();
  // The host's latest loader, so a new function each render does not restart
  // a load in flight.
  const latest = useRef(source.load);
  latest.current = source.load;
  const { sessionId } = at;
  useEffect(() => {
    if (!open || loaded.status !== "loading") return;
    const controller = new AbortController();
    latest
      .current(sessionId, controller.signal)
      .then((session) => setLoaded({ status: "ready", session }))
      .catch(() => {
        if (!controller.signal.aborted) setLoaded({ status: "failed" });
      });
    return () => controller.abort();
  }, [open, loaded, sessionId]);
  return (
    <div className="min-w-0">
      <Button
        variant="secondary"
        size="tiny"
        aria-expanded={open}
        aria-controls={panelId}
        onClick={() => setOpen((value) => !value)}
      >
        {open ? "Hide session" : `Open session at turn ${formatCount(at.turn)}`}
      </Button>
      <div id={panelId} hidden={!open} className="mt-3">
        {!open ? null : loaded.status === "loading" ? (
          <p className="flex items-center gap-2 text-base text-(--text-tertiary) italic">
            <Spinner />
            Loading this session…
          </p>
        ) : loaded.status === "failed" ? (
          <p role="alert" className="text-base text-(--status-critical)">
            This session could not be loaded. It may also belong to repositories you can't read.
          </p>
        ) : (
          source.render(loaded.session, at)
        )}
      </div>
    </div>
  );
}
