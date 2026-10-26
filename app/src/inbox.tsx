import {
  useId,
  useState,
  type ComponentType,
  type ReactNode,
  type Dispatch,
  type SetStateAction,
} from "react";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import { formatCount } from "@baum/viewer/components/format";
import {
  CheckIcon,
  ChevronRightIcon,
  CircleCheckIcon,
  CircleCrossIcon,
  CircleDashedIcon,
  CommentsIcon,
  MergeIcon,
  WarningIcon,
  type IconProps,
} from "@baum/viewer/components/icons";
import { Badge } from "@baum/viewer/components/ui";
import { riskScoreTone, type RiskScoreLevel } from "@baum/viewer/review-document";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import { appURL, liveFor, rowKey, type Entry, type LiveSession } from "./api";
import { LiveBadge } from "./live";
import { NineDots } from "./nine-dots";

type Group = { id: string; heading: string; hint: string; tone: string; rows: Entry[] };
/** Groups by who acts next. Discovery flags place requested, authored and
 *  other pull requests; the person's own split by GitHub's state into what
 *  needs their changes, what GitHub would merge now and the rest. */
export function inboxGroups(rows: Entry[]): Group[] {
  const groups: Group[] = [
    {
      id: "requested",
      heading: "Review requested",
      hint: "asking for your review",
      tone: "text-(--status-caution)",
      rows: [],
    },
    {
      id: "changes",
      heading: "Needs changes",
      hint: "yours, with changes requested, failing checks or a conflict",
      tone: "text-(--status-critical)",
      rows: [],
    },
    {
      id: "ready",
      heading: "Ready to merge",
      hint: "yours, and GitHub would merge it now",
      tone: "text-(--status-positive)",
      rows: [],
    },
    {
      id: "mine",
      heading: "Your pull requests",
      hint: "drafts and those waiting on reviews or checks",
      tone: "text-(--status-info)",
      rows: [],
    },
    {
      id: "other",
      heading: "Other pull requests",
      hint: "across your registered repositories",
      tone: "text-(--text-tertiary)",
      rows: [],
    },
    {
      id: "history",
      heading: "Saved history",
      hint: "reviews retained outside the open inbox",
      tone: "text-(--text-tertiary)",
      rows: [],
    },
  ];
  const own = (row: Entry) =>
    row.review === "changes_requested" || row.checks === "failing" || row.merge === "conflict"
      ? 1
      : row.merge === "ready"
        ? 2
        : 3;
  for (const row of rows) {
    const index = row.historical
      ? 5
      : row.pull_request.review_requested_from_me
        ? 0
        : row.pull_request.authored_by_me
          ? own(row)
          : 4;
    groups[index]!.rows.push(row);
  }
  // Saved history reads most recently reviewed first; a review without a
  // generation time goes last.
  const reviewed = (row: Entry) => Date.parse(row.latest?.generated_at ?? "") || 0;
  groups[5]!.rows.sort((a, b) => reviewed(b) - reviewed(a));
  return groups.filter((group) => group.rows.length > 0);
}
export function Inbox({
  rows,
  cloneFor,
  selection,
  setSelection,
  Link,
  live = [],
}: {
  rows: Entry[];
  cloneFor: (entry: Entry) => string;
  selection: Set<string>;
  setSelection: Dispatch<SetStateAction<Set<string>>>;
  Link: ComponentType<ViewerLinkProps>;
  /** Agent sessions in progress; a row shows those of its pull request. */
  live?: LiveSession[];
}) {
  const [collapsed, setCollapsed] = useState(new Set<string>());
  const groups = inboxGroups(rows);
  const visible = groups
    .flatMap((group) => (collapsed.has(group.id) ? [] : group.rows))
    .filter((row) => !row.needs_clone);
  const toggle = (key: string, checked: boolean) =>
    setSelection((previous) => {
      const next = new Set(previous);
      if (checked) next.add(key);
      else next.delete(key);
      return next;
    });
  return (
    <section
      aria-label="Pull request inbox"
      className={cn("local-inbox", selection.size > 0 && "selecting")}
    >
      <div className="inbox-columns inbox-columns-head">
        <input
          type="checkbox"
          aria-label="Select all visible pull requests"
          disabled={visible.length === 0}
          checked={visible.length > 0 && visible.every((row) => selection.has(rowKey(row)))}
          onChange={(event) =>
            setSelection((previous) => {
              const next = new Set(previous);
              for (const row of visible) {
                if (event.target.checked) next.add(rowKey(row));
                else next.delete(rowKey(row));
              }
              return next;
            })
          }
        />
        <span>Risk</span>
        <span className="inbox-title-heading">Pull request</span>
        {["Review time", "Review", "Status"].map((column) => (
          <span key={column} className="inbox-detail-heading">
            {column}
          </span>
        ))}
      </div>
      {groups.map((group) => (
        <Bucket
          key={group.id}
          group={group}
          collapsed={collapsed.has(group.id)}
          onToggle={() =>
            setCollapsed((previous) => {
              const next = new Set(previous);
              if (next.has(group.id)) next.delete(group.id);
              else next.add(group.id);
              return next;
            })
          }
        >
          {group.rows.map((entry) => (
            <InboxRow
              key={rowKey(entry)}
              entry={entry}
              clone={cloneFor(entry)}
              selected={selection.has(rowKey(entry))}
              toggle={(checked) => toggle(rowKey(entry), checked)}
              Link={Link}
              live={liveFor(live, entry.pull_request)}
            />
          ))}
        </Bucket>
      ))}
    </section>
  );
}
function Bucket({
  group,
  collapsed,
  onToggle,
  children,
}: {
  group: Group;
  collapsed: boolean;
  onToggle: () => void;
  children: ReactNode;
}) {
  const id = useId();
  return (
    <section aria-label={group.heading}>
      <h2 className="border-b border-(--border-subtle)">
        <button
          type="button"
          aria-expanded={!collapsed}
          aria-controls={id}
          onClick={onToggle}
          className={cn(
            "inbox-bucket flex w-full cursor-pointer items-center gap-2 text-left text-xs font-semibold text-(--text-primary) hover:bg-(--surface-hover)",
            FOCUS_RING,
          )}
        >
          <ChevronRightIcon
            size={12}
            className={cn(
              "flex-none text-(--text-tertiary) transition-transform motion-reduce:transition-none",
              !collapsed && "rotate-90",
            )}
          />
          <span
            aria-hidden
            className={cn("size-1.5 flex-none rounded-full bg-current", group.tone)}
          />
          <span>{group.heading}</span>
          <span className="inbox-bucket-count">{formatCount(group.rows.length)}</span>
          <span className="hidden min-w-0 truncate font-normal text-(--text-tertiary) sm:inline">
            · {group.hint}
          </span>
        </button>
      </h2>
      <ul id={id} hidden={collapsed}>
        {children}
      </ul>
    </section>
  );
}
/** The baum review's state, only when it asks for something: a review being
 *  generated, or one that failed. A current or outdated review needs nothing
 *  from the reader here; its risk is in the first column. */
function ReviewActivity({ entry }: { entry: Entry }) {
  if (entry.needs_clone) return <span className="inbox-activity">Needs a local clone</span>;
  if (entry.status === "generating")
    return (
      <span className="inbox-activity text-(--text-secondary)">
        <NineDots size={14} />
        Generating review
      </span>
    );
  if (entry.status === "failed")
    return (
      <span
        className="inbox-activity text-(--status-critical)"
        title={entry.failure_reason ?? entry.store_error}
      >
        <WarningIcon size={14} className="flex-none" />
        Review failed
      </span>
    );
  return null;
}
type StateStyle = {
  label: string;
  tone: string;
  icon: ComponentType<IconProps>;
  iconTone?: string;
};
const MUTED = "text-(--text-tertiary)";
const POSITIVE = "text-(--status-positive)";
const CRITICAL = "text-(--status-critical)";
const CAUTION = "text-(--status-caution)";
// Colour only where a state asks something of someone: red where it needs the
// author, amber while checks run, green when it can merge. The normal states
// (no GitHub review yet, nothing to report) leave the cell empty.
const REVIEW: Record<string, StateStyle> = {
  approved: { label: "Approved", tone: POSITIVE, icon: CheckIcon },
  changes_requested: { label: "Changes requested", tone: CRITICAL, icon: CircleCrossIcon },
  commented: { label: "Commented", tone: MUTED, icon: CommentsIcon },
};
/** Checks and merge state answer one question, what stands between the pull
 *  request and merging, so the row shows the state that answers it first. */
function mergeStatus(entry: Entry): StateStyle | undefined {
  if (entry.merge === "conflict") return { label: "Conflict", tone: CRITICAL, icon: WarningIcon };
  if (entry.checks === "failing")
    return { label: "Checks failing", tone: CRITICAL, icon: CircleCrossIcon };
  if (entry.merge === "draft") return { label: "Draft", tone: MUTED, icon: CircleDashedIcon };
  if (entry.checks === "running")
    return { label: "Checks running", tone: CAUTION, icon: CircleDashedIcon };
  if (entry.merge === "ready") return { label: "Ready", tone: POSITIVE, icon: MergeIcon };
  if (entry.merge === "blocked") return { label: "Blocked", tone: MUTED, icon: CircleCrossIcon };
  if (entry.checks === "passing")
    return { label: "Checks passing", tone: MUTED, iconTone: POSITIVE, icon: CircleCheckIcon };
  return undefined;
}
/** One of the status columns: an icon and a word, or nothing when there is
 *  nothing to say. Its column's name is read before it, since the headings are
 *  drawn for the eye only; an empty cell says what it means aloud only. */
function StatusCell({
  column,
  label,
  style,
  title,
  empty,
  children,
}: {
  column: string;
  label?: string;
  style?: StateStyle;
  title?: string;
  empty: string;
  children?: ReactNode;
}) {
  const Icon = style?.icon;
  const text = label ?? style?.label;
  if (!text && !children)
    return (
      <span className="inbox-status inbox-status-empty">
        <span className="sr-only">
          {column}: {empty}
        </span>
      </span>
    );
  return (
    <span
      title={title}
      className={cn(
        "inbox-status inline-flex items-center gap-1.5 text-xs tabular-nums",
        style?.tone ?? MUTED,
      )}
    >
      <span className="inbox-cell-label">{column}: </span>
      {Icon && text ? <Icon size={14} className={cn("flex-none", style?.iconTone)} /> : null}
      {text}
      {children}
    </span>
  );
}
function StatusColumns({ entry }: { entry: Entry }) {
  const unresolved = entry.unresolved;
  const status = mergeStatus(entry);
  // Unresolved threads belong to the GitHub review, beside its decision; none
  // is the normal state and shows nothing.
  const threads =
    unresolved == null || unresolved === 0 ? null : (
      <span
        className="inbox-threads"
        title={
          entry.unresolved_capped
            ? `At least ${formatCount(unresolved)} unresolved threads; only the first 100 threads were fetched`
            : `${formatCount(unresolved)} unresolved threads on GitHub`
        }
      >
        <CommentsIcon size={12} className="flex-none" />
        {formatCount(unresolved)}
        {entry.unresolved_capped ? "+" : ""}
        <span className="sr-only"> unresolved</span>
      </span>
    );
  return (
    <div className="inbox-statuses">
      <StatusCell
        column="Review time"
        empty="not estimated"
        label={
          entry.review_minutes == null ? undefined : `~${formatCount(entry.review_minutes)} min`
        }
      />
      <StatusCell
        column="Review"
        empty={entry.review === "unreviewed" ? "Unreviewed" : "unknown"}
        style={REVIEW[entry.review]}
      >
        {threads}
      </StatusCell>
      <StatusCell
        column="Status"
        empty="unknown"
        style={status}
        title={
          status
            ? [
                entry.checks === "none" ? "No checks" : entry.checks && `Checks ${entry.checks}`,
                entry.merge && `merge ${entry.merge}`,
              ]
                .filter(Boolean)
                .join(", ")
            : undefined
        }
      />
    </div>
  );
}
function InboxRow({
  entry,
  clone,
  selected,
  toggle,
  Link,
  live,
}: {
  entry: Entry;
  clone: string;
  selected: boolean;
  toggle: (checked: boolean) => void;
  Link: ComponentType<ViewerLinkProps>;
  live: LiveSession[];
}) {
  const pr = entry.pull_request;
  const score = entry.risk_score?.score;
  const lowerBound = entry.risk_score?.status === "lower_bound";
  const riskLabel =
    score == null
      ? "Risk not scored"
      : lowerBound
        ? `Risk at least ${score} of 5; incomplete coverage may hide a higher risk.`
        : `Risk ${score} of 5`;
  // The tones of the hosted risk pill; a lower bound may hide a higher risk,
  // so it never reads as calm.
  const tone =
    score == null
      ? undefined
      : lowerBound && score < 4
        ? "caution"
        : riskScoreTone(score as RiskScoreLevel);
  const metadata = [pr.repository, `#${pr.number}`, pr.author].filter(Boolean);
  return (
    <li className={cn("inbox-columns inbox-row", selected && "bg-(--surface-selected)")}>
      <input
        type="checkbox"
        aria-label={`Select ${pr.repository} #${pr.number}`}
        checked={selected}
        disabled={entry.needs_clone}
        onChange={(e) => toggle(e.target.checked)}
      />
      <span role="img" aria-label={riskLabel} title={riskLabel} className="inbox-risk">
        {tone ? (
          <Badge tone={tone} className="font-medium tabular-nums">
            {lowerBound ? "≥" : ""}
            {score}
            <span className="opacity-60">/5</span>
          </Badge>
        ) : null}
      </span>
      <div className="min-w-0">
        <div className="flex min-w-0 items-baseline gap-2">
          <Link
            href={appURL(
              `?${clone ? `clone=${encodeURIComponent(clone)}` : `repository=${encodeURIComponent(pr.repository)}`}&pr=${pr.number}`,
            )}
            title={pr.title}
            className="inbox-pr-title min-w-0 font-medium text-(--text-primary) hover:underline"
          >
            {pr.title || "Saved review"}
          </Link>
          {pr.draft ? <Badge size="compact">Draft</Badge> : null}
          {pr.fork ? <Badge size="compact">Fork</Badge> : null}
        </div>
        <p title={metadata.join(" · ")} className="inbox-meta text-xs text-(--text-tertiary)">
          {metadata.join(" · ")}
        </p>
        {/* What is happening on the pull request now: baum's review and the
            agents working on its branch. */}
        <div className="inbox-activities">
          <ReviewActivity entry={entry} />
          <LiveBadge sessions={live} />
        </div>
      </div>
      <StatusColumns entry={entry} />
    </li>
  );
}
