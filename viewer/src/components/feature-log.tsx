import { harnessName } from "../agent-names";
import {
  activitySpan,
  type DecisionPerson,
  type DecisionSummary,
  type FeatureDetail,
  featureCounts,
  type FeaturePullRequest,
  type FeatureSession,
  featureStatusLabel,
  featureStatusTone,
  type FeatureSummary,
  type KeyDecision,
  locateDecision,
  personLabel,
  pullGroup,
  sessionGroup,
  whoPresentation,
} from "../decision-log";
import { ViewerLink } from "../navigation";
import { Avatar } from "./avatar";
import { cn, FOCUS_RING } from "./classnames";
import {
  CategoryTag,
  DecisionRow,
  DetailPart,
  PersonName,
  PullRequestState,
  UnknownPerson,
} from "./decision-log";
import { formatBucket, formatCount, formatDate, formatTime } from "./format";
import { ChevronDownIcon, CloseIcon } from "./icons";
import { InlineMarkdown, Markdown } from "./markdown";
import { DecisionByBadge } from "./review-parts";
import { Badge, Button, Spinner } from "./ui";
import { type ReactNode, useEffect, useId, useRef, useState } from "react";

/** The Features tab of a decision log: its features, newest activity first,
 *  each expanding to its pull requests and sessions and their decisions. The
 *  host supplies the features, their details and where a pull request opens;
 *  nothing here fetches. */

export type FeatureState =
  | { status: "loading" }
  | { status: "ready"; detail: FeatureDetail }
  | { status: "moved"; id: string }
  | { status: "archived" }
  | { status: "missing" }
  | { status: "failed" };

/** How a decision inside a feature opens: the page owns the open decision
 *  and the detail requests, the same ones the timeline uses. */
export type DecisionOpening = {
  /** The open decision's id. */
  open: string | null;
  onToggle: (decision: DecisionSummary, expanded: boolean) => void;
  /** The expanded decision's detail. */
  renderDetail: (decision: DecisionSummary) => ReactNode;
  /** An opened decision the feature does not list. */
  renderOpened: (id: string) => ReactNode;
  /** Counts key decisions chosen, so choosing the one already open shows it
   *  again: its group reopens and it comes into view. */
  reveals: number;
};

const PEOPLE_SHOWN = 4;

/** Who worked on a feature, as a row of pictures named for assistive
 *  technology and on hover. */
function People({ people }: { people: DecisionPerson[] }) {
  if (people.length === 0) return null;
  const names = people.map(personLabel);
  const extra = people.length - PEOPLE_SHOWN;
  return (
    <span
      className="inline-flex items-center"
      role="img"
      aria-label={`People: ${names.join(", ")}`}
    >
      {people.slice(0, PEOPLE_SHOWN).map((person, index) => (
        <span
          key={person.email}
          title={personLabel(person)}
          className={cn(
            "inline-flex rounded-full ring-2 ring-(--surface-card)",
            index > 0 && "-ml-1.5",
          )}
        >
          <Avatar src={person.avatar_url} name={personLabel(person)} size={20} />
        </span>
      ))}
      {extra > 0 ? (
        <span className="ml-1.5 text-xs text-(--text-tertiary)">+{formatCount(extra)}</span>
      ) : null}
    </span>
  );
}

/** A feature with its text withheld or not generated yet reads as neither a
 *  title nor stale text. */
function FeatureTitle({ feature }: { feature: FeatureSummary }) {
  if (feature.summary_pending)
    return <span className="text-(--text-secondary) italic">Summary being updated</span>;
  if (!feature.title)
    return <span className="text-(--text-secondary) italic">Untitled feature</span>;
  return (
    <InlineMarkdown className="font-medium text-(--text-primary)">{feature.title}</InlineMarkdown>
  );
}

/** The dot each feature wears on the timeline, by status, in its badge's
 *  colours: in progress a ring on the info wash, shipped solid positive,
 *  abandoned hollow. */
const STATUS_DOTS: Record<FeatureSummary["status"], string> = {
  in_progress: "border-2 border-(--status-info) bg-(--status-info-bg)",
  shipped: "bg-(--status-positive)",
  abandoned: "border-2 border-(--border-strong) bg-(--surface-page)",
};

/** One row of the timeline: from `md` a date column, then the rail's column,
 *  then the content; below it the date column goes and days get their own
 *  rows. Every row draws its own stretch of the rail, so the content keeps
 *  its spacing inside itself (never as margin on the row) and the rail runs
 *  unbroken, starting at the first row's mark and ending at the last one's. */
export const TIMELINE_ROW =
  "group/entry grid min-w-0 grid-cols-[1.25rem_minmax(0,1fr)] gap-x-4 md:grid-cols-[4.5rem_1.25rem_minmax(0,1fr)]";

export function TimelineRail({ children }: { children?: ReactNode }) {
  return (
    <div className="relative flex justify-center">
      <span
        aria-hidden
        className="absolute top-0 bottom-0 w-px bg-(--border-default) group-first/entry:top-4 group-last/entry:bottom-auto group-last/entry:h-4"
      />
      {children}
    </div>
  );
}

/** The date column from `md`: the day, level with the entry's mark, on the
 *  first entry of each day only, so a day's entries read as one run. */
function TimelineDate({ day }: { day?: string | null }) {
  return (
    <div className="hidden pt-3.5 text-right md:block">
      {day ? (
        <time dateTime={day} className="text-xs font-medium text-(--text-tertiary) tabular-nums">
          {formatBucket(day, "day")}
        </time>
      ) : null}
    </div>
  );
}

/** The month heading's height, fixed so the panel beside the timeline can
 *  line up with the first entry under it: the list's `pt-1` and this. */
const MONTH_HEADING = "h-12";
const TIMELINE_FIRST_ENTRY = "mt-13";

/** A month heading on the timeline, or below `md`, where there is no date
 *  column, a day's: a small mark on the rail with the date beside it. A
 *  ranked search has one heading instead, "Best matches first", in the
 *  month's place. */
export function TimelineHeading({
  level,
  iso,
}: { level: "month" | "day"; iso: string } | { level: "relevance"; iso?: undefined }) {
  return (
    <li className={cn(TIMELINE_ROW, level === "day" && "md:hidden")}>
      <TimelineDate />
      <TimelineRail>
        {level === "day" ? (
          <span
            aria-hidden
            className="relative mt-[0.8rem] h-2 w-2 rounded-full border border-(--border-strong) bg-(--surface-page)"
          />
        ) : null}
      </TimelineRail>
      {level !== "day" ? (
        <h3 className={cn("pt-1 text-lg font-medium text-(--text-primary)", MONTH_HEADING)}>
          {level === "relevance" ? "Best matches first" : formatBucket(iso, "month")}
        </h3>
      ) : (
        <time
          dateTime={iso}
          className="block pt-2 pb-2.5 text-xs font-medium text-(--text-tertiary)"
        >
          {formatBucket(iso, "day")}
        </time>
      )}
    </li>
  );
}

/** One feature: a disclosure whose panel is its drill-down, with its key
 *  decisions beneath the header, each opening that decision. */
export function FeatureItem({
  feature,
  expanded,
  onToggle,
  onKeyDecision,
  children,
  itemRef,
  day,
  panelElsewhere,
  repository,
}: {
  feature: FeatureSummary;
  expanded: boolean;
  onToggle: () => void;
  onKeyDecision: (decision: KeyDecision) => void;
  /** The drill-down, rendered only while expanded. */
  children: ReactNode;
  itemRef?: (element: HTMLLIElement | null) => void;
  /** The day to name beside the entry, on the first entry of each day. */
  day?: string | null;
  /** The id of a panel beside the timeline that shows the drill-down instead,
   *  on a wide screen: the entry then only marks itself selected. */
  panelElsewhere?: string;
  /** The repository's name, for a log over several. */
  repository?: string;
}) {
  const id = useId();
  const span = activitySpan(feature.first_activity_at, feature.last_activity_at);
  const keys = feature.summary_pending ? [] : feature.key_decisions.slice(0, 3);
  // Opened into a panel beside the timeline: the entry carries the selection.
  const selected = expanded && panelElsewhere !== undefined;
  return (
    <li ref={itemRef} data-feature={feature.id} className={TIMELINE_ROW}>
      <TimelineDate day={day} />
      <TimelineRail>
        <span
          aria-hidden
          className={cn(
            "relative mt-4 h-3 w-3 rounded-full ring-4 ring-(--surface-page) transition-transform motion-reduce:transition-none",
            STATUS_DOTS[feature.status],
            selected && "scale-150",
          )}
        />
      </TimelineRail>
      <div className="min-w-0 pb-3">
        <div
          className={cn(
            "min-w-0 overflow-hidden rounded-(--radius-lg) border shadow-(--shadow-card)",
            selected
              ? "border-(--action-accent) bg-(--surface-selected) ring-1 ring-(--action-accent)"
              : expanded
                ? "border-(--border-strong) bg-(--surface-card)"
                : "border-(--border-default) bg-(--surface-card)",
          )}
        >
          <button
            type="button"
            id={`${id}-button`}
            aria-expanded={expanded}
            aria-controls={panelElsewhere ?? `${id}-panel`}
            onClick={onToggle}
            className={cn(
              "flex w-full cursor-pointer items-start gap-2.5 px-4 pt-4 text-left hover:bg-(--surface-hover) sm:px-5",
              keys.length > 0 ? "pb-2" : "pb-4",
              FOCUS_RING,
            )}
          >
            <ChevronDownIcon
              size={14}
              className={cn(
                "mt-1 flex-none text-(--text-tertiary) transition-transform motion-reduce:transition-none",
                (!expanded || panelElsewhere) && "-rotate-90",
              )}
            />
            <span className="min-w-0 flex-1">
              <span className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
                <span className="min-w-0 text-base break-words">
                  <FeatureTitle feature={feature} />
                </span>
                <Badge size="compact" tone={featureStatusTone(feature.status)}>
                  {featureStatusLabel(feature.status)}
                </Badge>
                {span ? <span className="text-xs text-(--text-tertiary)">{span}</span> : null}
              </span>
              {!feature.summary_pending && feature.description ? (
                <InlineMarkdown className="mt-1 line-clamp-2 text-base break-words text-(--text-secondary)">
                  {feature.description}
                </InlineMarkdown>
              ) : null}
              <span className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-(--text-tertiary)">
                {repository ? <span>{repository}</span> : null}
                <People people={feature.people} />
                <span data-testid="feature-counts">{featureCounts(feature)}</span>
              </span>
            </span>
          </button>
          {keys.length > 0 ? <KeyDecisions keys={keys} onChoose={onKeyDecision} /> : null}
          {panelElsewhere ? null : (
            <div
              id={`${id}-panel`}
              role="region"
              aria-labelledby={`${id}-button`}
              hidden={!expanded}
              className="border-t border-(--border-subtle) bg-(--surface-wash) px-4 py-4 sm:px-5 sm:pl-11"
            >
              {expanded ? children : null}
            </div>
          )}
        </div>
      </div>
    </li>
  );
}

/** A feature's key decisions, folded under a count until the reader opens
 *  them; each opens that decision inside the feature. */
function KeyDecisions({
  keys,
  onChoose,
}: {
  keys: KeyDecision[];
  onChoose: (decision: KeyDecision) => void;
}) {
  const id = useId();
  const [open, setOpen] = useState(false);
  return (
    <div className="px-4 pb-4 pl-10 sm:pr-5 sm:pl-11">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={`${id}-keys`}
        onClick={() => setOpen((current) => !current)}
        className={cn(
          "-ml-1.5 flex cursor-pointer items-center gap-1.5 rounded-(--radius-sm) px-1.5 py-1 text-xs text-(--text-secondary) hover:bg-(--surface-hover) hover:text-(--text-primary)",
          FOCUS_RING,
        )}
      >
        <ChevronDownIcon
          size={12}
          className={cn(
            "flex-none transition-transform motion-reduce:transition-none",
            !open && "-rotate-90",
          )}
        />
        {formatCount(keys.length)} key {keys.length === 1 ? "decision" : "decisions"}
      </button>
      <ul
        id={`${id}-keys`}
        aria-label="Key decisions"
        hidden={!open}
        className="mt-1 flex flex-col gap-1"
      >
        {keys.map((key) => (
          <li key={key.id} className="min-w-0">
            <button
              type="button"
              onClick={() => onChoose(key)}
              className={cn(
                "flex w-full min-w-0 cursor-pointer flex-wrap items-center gap-x-2 gap-y-1 rounded-(--radius-sm) px-1.5 py-1 text-left hover:bg-(--surface-hover)",
                FOCUS_RING,
              )}
            >
              <span className="min-w-0 basis-full text-base break-words text-(--text-primary) sm:basis-auto sm:flex-1">
                <InlineMarkdown>{key.summary}</InlineMarkdown>
              </span>
              <DecisionByBadge decision={whoPresentation(key.who, null).marker} />
              <CategoryTag category={key.category} />
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** The opened feature's drill-down beside the timeline on a wide screen:
 *  its title, status and dates, a close button, then the drill-down. It stays
 *  in view as the timeline scrolls and scrolls on its own when longer. */
export function FeatureSidePanel({
  id,
  feature,
  onClose,
  children,
}: {
  id: string;
  feature: FeatureSummary;
  onClose: () => void;
  children: ReactNode;
}) {
  const span = activitySpan(feature.first_activity_at, feature.last_activity_at);
  return (
    <aside
      id={id}
      aria-label="Opened feature"
      className={cn(
        "sticky top-4 flex max-h-[calc(100vh-2rem)] min-w-0 flex-col overflow-hidden rounded-(--radius-lg) border border-(--border-default) bg-(--surface-card) shadow-(--shadow-card)",
        TIMELINE_FIRST_ENTRY,
      )}
    >
      <div className="flex items-start gap-3 border-b border-(--border-subtle) px-5 py-4">
        <div className="min-w-0 flex-1">
          <h2 className="text-lg font-medium break-words text-(--text-primary)">
            <FeatureTitle feature={feature} />
          </h2>
          <div className="mt-1 flex flex-wrap items-center gap-x-2.5 gap-y-1">
            <Badge size="compact" tone={featureStatusTone(feature.status)}>
              {featureStatusLabel(feature.status)}
            </Badge>
            {span ? <span className="text-xs text-(--text-tertiary)">{span}</span> : null}
            <span className="text-xs text-(--text-tertiary)">{featureCounts(feature)}</span>
          </div>
        </div>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close feature"
          className={cn(
            "flex-none cursor-pointer rounded-(--radius-sm) p-1.5 text-(--text-tertiary) hover:bg-(--surface-hover) hover:text-(--text-primary)",
            FOCUS_RING,
          )}
        >
          <CloseIcon size={14} />
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto bg-(--surface-wash) px-5 py-4">{children}</div>
    </aside>
  );
}

/** What expanding a feature shows: its digest, its pull requests and the
 *  sessions it holds without one, each expanding to every decision attributed
 *  to it, whatever the filters. */
export function FeatureDrillDown({
  state,
  decisions,
  pullRequestHref,
  onRetry,
}: {
  state: FeatureState | undefined;
  decisions: DecisionOpening;
  /** Where the host opens one of the feature's pull requests, by number. */
  pullRequestHref: (number: number) => string;
  onRetry: () => void;
}) {
  if (!state || state.status === "loading" || state.status === "moved") {
    return (
      <p className="flex items-center gap-2 py-2 text-base text-(--text-tertiary) italic">
        <Spinner />
        Loading this feature…
      </p>
    );
  }
  if (state.status === "failed") {
    return (
      <p role="alert" className="flex flex-wrap items-center gap-2 py-2 text-base">
        <span className="text-(--status-critical)">This feature could not be loaded.</span>
        <Button variant="secondary" size="tiny" onClick={onRetry}>
          Try again
        </Button>
      </p>
    );
  }
  if (state.status === "missing") {
    return (
      <p role="status" className="py-2 text-base text-(--text-secondary)">
        This feature no longer exists.
      </p>
    );
  }
  if (state.status === "archived") {
    return (
      <p role="status" className="py-2 text-base text-(--text-secondary)">
        This feature is archived: it is from an earlier grouping of this repository&rsquo;s work.
      </p>
    );
  }
  return (
    <FeatureContents
      detail={state.detail}
      decisions={decisions}
      pullRequestHref={pullRequestHref}
    />
  );
}

function FeatureContents({
  detail,
  decisions,
  pullRequestHref,
}: {
  detail: FeatureDetail;
  decisions: DecisionOpening;
  pullRequestHref: (number: number) => string;
}) {
  const { open } = decisions;
  const located = open ? locateDecision(detail, open) : null;
  const locatedKey = located ? `${located.decision.id}|${decisions.reveals}` : null;
  // A group opens by itself to show a decision opened in it, whatever the
  // reader chose for it before.
  const [toggled, setToggled] = useState<Record<string, boolean>>({});
  const [toggledFor, setToggledFor] = useState(locatedKey);
  if (toggledFor !== locatedKey) {
    setToggledFor(locatedKey);
    // Held open, so closing the decision later leaves its group open.
    if (located && toggled[located.group] !== true)
      setToggled({ ...toggled, [located.group]: true });
  }
  const expanded = (group: string) => toggled[group] ?? located?.group === group;
  const toggle = (group: string) =>
    setToggled((current) => ({ ...current, [group]: !expanded(group) }));

  // An opened decision is brought into view once, when it first shows here.
  const scrolledFor = useRef<string | null>(null);
  const openedRow = useRef<HTMLLIElement | null>(null);
  useEffect(() => {
    if (!locatedKey || scrolledFor.current === locatedKey) return;
    scrolledFor.current = locatedKey;
    openedRow.current?.scrollIntoView({ block: "center" });
  }, [locatedKey]);

  const rows = (list: DecisionSummary[]) =>
    list.length === 0 ? (
      <p className="py-2 text-base text-(--text-tertiary)">No decisions attributed here.</p>
    ) : (
      <ol className="rounded-(--radius-md) border border-(--border-subtle) bg-(--surface-card)">
        {list.map((decision) => {
          const isOpen = located?.decision === decision;
          return (
            <DecisionRow
              key={decision.id}
              decision={decision}
              expanded={isOpen}
              withDate
              onToggle={() => {
                // The reader opened it here, where they are already looking.
                if (!isOpen) scrolledFor.current = `${decision.id}|${decisions.reveals}`;
                decisions.onToggle(decision, isOpen);
              }}
              rowRef={
                isOpen
                  ? (element) => {
                      openedRow.current = element;
                    }
                  : undefined
              }
            >
              {decisions.renderDetail(decision)}
            </DecisionRow>
          );
        })}
      </ol>
    );

  const { feature } = detail;
  return (
    <div className="flex min-w-0 flex-col gap-4 pt-1">
      {feature.summary_pending ? (
        <p role="status" className="text-base text-(--text-tertiary) italic">
          Summary being updated. The pull requests and decisions below are current.
        </p>
      ) : feature.digest ? (
        <Markdown html={false} className="text-base break-words text-(--text-primary)">
          {feature.digest}
        </Markdown>
      ) : null}

      {open && !located ? decisions.renderOpened(open) : null}

      {detail.pull_requests.length > 0 ? (
        <DetailPart label="Pull requests">
          <ul className="flex flex-col gap-2">
            {detail.pull_requests.map((pull) => (
              <Group
                key={pull.number}
                kind="pull"
                expanded={expanded(pullGroup(pull.number))}
                onToggle={() => toggle(pullGroup(pull.number))}
                header={<PullHeader pull={pull} />}
                aside={
                  <ViewerLink
                    href={pullRequestHref(pull.number)}
                    className={cn(
                      "w-fit rounded-(--radius-sm) text-xs text-(--text-secondary) hover:text-(--text-primary) hover:underline",
                      FOCUS_RING,
                    )}
                  >
                    Open pull request #{pull.number}
                  </ViewerLink>
                }
              >
                {pull.summary ? (
                  <Markdown
                    html={false}
                    className="mb-2 px-1 text-base break-words text-(--text-secondary) sm:pl-5"
                  >
                    {pull.summary}
                  </Markdown>
                ) : null}
                {rows(pull.decisions)}
              </Group>
            ))}
          </ul>
        </DetailPart>
      ) : null}

      {detail.sessions.length > 0 ? (
        <DetailPart label="Sessions without a pull request">
          <ul className="flex flex-col gap-2">
            {detail.sessions.map((session) => (
              <Group
                key={session.id}
                kind="session"
                expanded={expanded(sessionGroup(session.id))}
                onToggle={() => toggle(sessionGroup(session.id))}
                header={<SessionHeader session={session} />}
              >
                {rows(session.decisions)}
              </Group>
            ))}
          </ul>
        </DetailPart>
      ) : null}

      {detail.pull_requests.length === 0 && detail.sessions.length === 0 ? (
        <p className="text-base text-(--text-tertiary)">
          Nothing this repository can show is in this feature yet.
        </p>
      ) : null}
    </div>
  );
}

/** A pull request or session of a feature: a disclosure of its decisions. */
function Group({
  kind,
  expanded,
  onToggle,
  header,
  aside,
  children,
}: {
  kind: "pull" | "session";
  expanded: boolean;
  onToggle: () => void;
  header: ReactNode;
  /** Shown under the header whether or not it is expanded. */
  aside?: ReactNode;
  children: ReactNode;
}) {
  const id = useId();
  return (
    <li
      data-feature-group={kind}
      className="min-w-0 rounded-(--radius-md) border border-(--border-subtle) bg-(--surface-card)"
    >
      <button
        type="button"
        id={`${id}-button`}
        aria-expanded={expanded}
        aria-controls={`${id}-panel`}
        onClick={onToggle}
        className={cn(
          "flex w-full cursor-pointer items-start gap-2 rounded-(--radius-md) px-3 py-2 text-left hover:bg-(--surface-hover)",
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
        <span className="min-w-0 flex-1">{header}</span>
      </button>
      {aside ? <div className="flex flex-col gap-1.5 px-3 pb-2 pl-8">{aside}</div> : null}
      <div
        id={`${id}-panel`}
        role="region"
        aria-labelledby={`${id}-button`}
        hidden={!expanded}
        className="px-2 pb-2 sm:px-3 sm:pb-3"
      >
        {expanded ? children : null}
      </div>
    </li>
  );
}

function PullHeader({ pull }: { pull: FeaturePullRequest }) {
  return (
    <span className="flex flex-wrap items-center gap-x-2 gap-y-1 text-base">
      <span className="min-w-0 break-words text-(--text-primary)">
        <span className="text-(--text-tertiary) tabular-nums">#{pull.number}</span> {pull.title}
      </span>
      <PullRequestState pull={pull} />
      <DecisionCount count={pull.decisions.length} />
    </span>
  );
}

function SessionHeader({ session }: { session: FeatureSession }) {
  return (
    <span className="flex flex-wrap items-center gap-x-2.5 gap-y-1 text-base">
      {session.person ? <PersonName person={session.person} /> : <UnknownPerson />}
      <span className="text-xs text-(--text-secondary)">{harnessName(session.vendor)}</span>
      {session.branch ? (
        <span className="min-w-0 font-mono text-xs break-all text-(--text-secondary)">
          {session.branch}
        </span>
      ) : null}
      {session.started_at ? (
        <span className="text-xs text-(--text-tertiary)">
          {formatDate(session.started_at)} {formatTime(session.started_at)}
        </span>
      ) : null}
      <DecisionCount count={session.decisions.length} />
    </span>
  );
}

function DecisionCount({ count }: { count: number }) {
  return (
    <span className="text-xs text-(--text-tertiary)">
      {formatCount(count)} {count === 1 ? "decision" : "decisions"}
    </span>
  );
}
