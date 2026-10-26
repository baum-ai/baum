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
import { Badge, LiveDot } from "@baum/viewer/components/ui";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import {
  appURL,
  groupJob,
  isActiveJob,
  liveFor,
  rowKey,
  sameRepository,
  type Entry,
  type Job,
  type LiveSession,
  type ReviewGroup,
} from "./api";
import { LiveBadge } from "./live";

/** A review group as one inbox row, with those of its members the inbox shows. */
export type GroupEntry = { group: ReviewGroup; members: Entry[] };
type Group = {
  id: string;
  heading: string;
  hint: string;
  tone: string;
  /** The loose rows: pull requests not shown under a review group. */
  rows: Entry[];
  groups: GroupEntry[];
};
/** Discovery flags describe these groups; none infer approval or merge readiness.
 *  A review group with a shown member is one row in the bucket of its
 *  highest-priority shown member, and its shown members leave the loose rows. */
export function inboxGroups(rows: Entry[], reviewGroups: ReviewGroup[] = []): Group[] {
  const groups: Group[] = [
    {
      id: "requested",
      heading: "Review requested",
      hint: "asking for your review",
      tone: "text-(--status-caution)",
      rows: [],
      groups: [],
    },
    {
      id: "mine",
      heading: "Your pull requests",
      hint: "authored by you",
      tone: "text-(--status-info)",
      rows: [],
      groups: [],
    },
    {
      id: "other",
      heading: "Other pull requests",
      hint: "across your registered repositories",
      tone: "text-(--text-tertiary)",
      rows: [],
      groups: [],
    },
    {
      id: "history",
      heading: "Saved history",
      hint: "reviews retained outside the open inbox",
      tone: "text-(--text-tertiary)",
      rows: [],
      groups: [],
    },
  ];
  const bucket = (row: Entry) =>
    row.historical
      ? 3
      : row.pull_request.review_requested_from_me
        ? 0
        : row.pull_request.authored_by_me
          ? 1
          : 2;
  const grouped = new Set<Entry>();
  for (const group of reviewGroups) {
    const members = rows.filter(
      (row) =>
        sameRepository(row.pull_request.repository, group.repository) &&
        group.members.includes(row.pull_request.number),
    );
    if (members.length === 0) continue;
    for (const member of members) grouped.add(member);
    // Saved history is the lowest bucket, so the highest-priority member
    // puts the group there only when every shown member is historical.
    groups[Math.min(...members.map(bucket))]!.groups.push({ group, members });
  }
  for (const row of rows) if (!grouped.has(row)) groups[bucket(row)]!.rows.push(row);
  // Saved history reads most recently reviewed first; a review without a
  // generation time goes last.
  const reviewed = (row: Entry) => Date.parse(row.latest?.generated_at ?? "") || 0;
  groups[3]!.rows.sort((a, b) => reviewed(b) - reviewed(a));
  return groups.filter((group) => group.rows.length + group.groups.length > 0);
}
export function Inbox({
  rows,
  cloneFor,
  selection,
  setSelection,
  Link,
  live = [],
  groups: reviewGroups = [],
  jobs = [],
}: {
  rows: Entry[];
  cloneFor: (entry: Entry) => string;
  selection: Set<string>;
  setSelection: Dispatch<SetStateAction<Set<string>>>;
  Link: ComponentType<ViewerLinkProps>;
  /** Agent sessions in progress; a row shows those of its pull request. */
  live?: LiveSession[];
  /** Review groups; each with a shown member is one row. */
  groups?: ReviewGroup[];
  jobs?: Job[];
}) {
  const [collapsed, setCollapsed] = useState(new Set<string>());
  // Review groups start collapsed.
  const [expanded, setExpanded] = useState(new Set<string>());
  const groups = inboxGroups(rows, reviewGroups);
  const visible = groups
    .flatMap((group) =>
      collapsed.has(group.id)
        ? []
        : [
            ...group.rows,
            ...group.groups.flatMap((entry) => (expanded.has(entry.group.id) ? entry.members : [])),
          ],
    )
    .filter((row) => !row.needs_clone);
  const toggle = (key: string, checked: boolean) =>
    setSelection((previous) => {
      const next = new Set(previous);
      if (checked) next.add(key);
      else next.delete(key);
      return next;
    });
  const renderRow = (entry: Entry, nested: boolean) => (
    <InboxRow
      key={rowKey(entry)}
      entry={entry}
      clone={cloneFor(entry)}
      selected={selection.has(rowKey(entry))}
      toggle={(checked) => toggle(rowKey(entry), checked)}
      Link={Link}
      live={liveFor(live, entry.pull_request)}
      nested={nested}
    />
  );
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
        {["Review time", "Unresolved", "Review", "Checks", "Merge"].map((column) => (
          <span key={column} className="inbox-detail-heading">
            {column}
          </span>
        ))}
      </div>
      {groups.map((group) => (
        <Bucket
          key={group.id}
          group={group}
          count={group.rows.length + group.groups.length}
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
          {group.groups.map(({ group: reviewGroup, members }) => (
            <GroupRow
              key={reviewGroup.id}
              group={reviewGroup}
              job={groupJob(reviewGroup, jobs)}
              expanded={expanded.has(reviewGroup.id)}
              onToggle={() =>
                setExpanded((previous) => {
                  const next = new Set(previous);
                  if (next.has(reviewGroup.id)) next.delete(reviewGroup.id);
                  else next.add(reviewGroup.id);
                  return next;
                })
              }
              Link={Link}
            >
              {members.map((entry) => renderRow(entry, true))}
            </GroupRow>
          ))}
          {group.rows.map((entry) => renderRow(entry, false))}
        </Bucket>
      ))}
    </section>
  );
}
function Bucket({
  group,
  count,
  collapsed,
  onToggle,
  children,
}: {
  group: Group;
  count: number;
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
            "inbox-bucket flex w-full cursor-pointer items-center gap-2 py-2.5 text-left text-xs font-semibold text-(--text-primary) hover:bg-(--surface-hover)",
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
          <span className={cn("tabular-nums", group.tone)}>{formatCount(count)}</span>
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
      <span className="inbox-activity text-(--status-info)">
        <LiveDot />
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
// Red only where something needs the author: changes requested, failing
// checks, a conflict.
const REVIEW: Record<string, StateStyle> = {
  approved: { label: "Approved", tone: POSITIVE, icon: CheckIcon },
  changes_requested: { label: "Changes requested", tone: CRITICAL, icon: CircleCrossIcon },
  commented: { label: "Commented", tone: MUTED, icon: CommentsIcon },
  unreviewed: { label: "Unreviewed", tone: MUTED, icon: CircleDashedIcon },
};
const CHECKS: Record<string, StateStyle> = {
  passing: { label: "Passing", tone: MUTED, iconTone: POSITIVE, icon: CircleCheckIcon },
  failing: { label: "Failing", tone: CRITICAL, icon: CircleCrossIcon },
  running: { label: "Running", tone: "text-(--status-caution)", icon: CircleDashedIcon },
};
const MERGE: Record<string, StateStyle> = {
  ready: { label: "Ready", tone: POSITIVE, icon: MergeIcon },
  conflict: { label: "Conflict", tone: CRITICAL, icon: WarningIcon },
  blocked: { label: "Blocked", tone: MUTED, icon: CircleCrossIcon },
  draft: { label: "Draft", tone: MUTED, icon: CircleDashedIcon },
};
/** One of the status columns: an icon and a word, or a dash. Its column's
 *  name is read before it, since the headings are drawn for the eye only. */
function StatusCell({
  column,
  label,
  style,
  title,
}: {
  column: string;
  label?: string;
  style?: StateStyle;
  title?: string;
}) {
  const Icon = style?.icon;
  const text = label ?? style?.label;
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
      {text ?? "—"}
    </span>
  );
}
function StatusColumns({ entry }: { entry: Entry }) {
  const unresolved = entry.unresolved;
  return (
    <div className="inbox-statuses">
      <StatusCell
        column="Review time"
        label={
          entry.review_minutes == null ? undefined : `~${formatCount(entry.review_minutes)} min`
        }
      />
      <StatusCell
        column="Unresolved"
        style={unresolved == null ? undefined : { label: "", tone: MUTED, icon: CommentsIcon }}
        label={
          unresolved == null
            ? undefined
            : `${formatCount(unresolved)}${entry.unresolved_capped ? "+" : ""}`
        }
        title={
          unresolved == null
            ? undefined
            : entry.unresolved_capped
              ? `At least ${formatCount(unresolved)} unresolved threads; only the first 100 threads were fetched`
              : `${formatCount(unresolved)} unresolved threads on GitHub`
        }
      />
      <StatusCell column="Review" style={REVIEW[entry.review]} />
      <StatusCell column="Checks" style={CHECKS[entry.checks]} />
      <StatusCell column="Merge" style={MERGE[entry.merge]} />
    </div>
  );
}
/** The risk column: the score out of 5, or a dash. */
function RiskCell({ risk }: { risk: Entry["risk_score"] }) {
  const score = risk?.score;
  const lowerBound = risk?.status === "lower_bound";
  const riskLabel =
    score == null
      ? "Risk not scored"
      : lowerBound
        ? `Risk at least ${score} of 5; incomplete coverage may hide a higher risk.`
        : `Risk ${score} of 5`;
  const tone =
    score == null
      ? "text-(--text-disabled)"
      : score >= 5
        ? "text-(--status-critical)"
        : score === 4 || lowerBound
          ? "text-(--status-caution)"
          : score === 3
            ? "text-(--text-secondary)"
            : "text-(--status-positive)";
  return (
    <span
      role="img"
      aria-label={riskLabel}
      title={riskLabel}
      className={cn("inbox-risk font-mono text-base font-medium tabular-nums", tone)}
    >
      <span className="sr-only">Risk </span>
      {score ?? "—"}
      {score != null && lowerBound ? "+" : null}
      {score != null ? <span className="text-(--text-tertiary)">/5</span> : null}
    </span>
  );
}
/** The state words of a pull request, as the inbox's status cells say them. */
export function stateWords(entry: Entry): string[] {
  const state = entry.pull_request.state.toLowerCase();
  return [
    state ? state[0]!.toUpperCase() + state.slice(1) : "",
    REVIEW[entry.review]?.label ?? "",
    CHECKS[entry.checks]?.label ? `Checks ${CHECKS[entry.checks]!.label.toLowerCase()}` : "",
    MERGE[entry.merge]?.label ?? "",
  ].filter(Boolean);
}
/** A review group's one row; its shown members nest under it when expanded. */
function GroupRow({
  group,
  job,
  expanded,
  onToggle,
  Link,
  children,
}: {
  group: ReviewGroup;
  job: Job | undefined;
  expanded: boolean;
  onToggle: () => void;
  Link: ComponentType<ViewerLinkProps>;
  children: ReactNode;
}) {
  const id = useId();
  const metadata = ["Review group", `${group.members.length} pull requests`, group.repository].join(
    " · ",
  );
  return (
    <li>
      <div className="inbox-columns inbox-row inbox-group-row">
        <button
          type="button"
          aria-expanded={expanded}
          aria-controls={id}
          aria-label={`${expanded ? "Collapse" : "Expand"} ${group.name}`}
          onClick={onToggle}
          className={cn("inbox-group-toggle", FOCUS_RING)}
        >
          <ChevronRightIcon
            size={12}
            className={cn(
              "text-(--text-tertiary) transition-transform motion-reduce:transition-none",
              expanded && "rotate-90",
            )}
          />
        </button>
        <RiskCell risk={group.risk_score} />
        <div className="min-w-0">
          <div className="flex min-w-0 items-baseline gap-2">
            <Link
              href={appURL(`?group=${encodeURIComponent(group.id)}`)}
              title={group.name}
              className="inbox-pr-title min-w-0 font-medium text-(--text-primary) hover:underline"
            >
              {group.name}
            </Link>
          </div>
          <p title={metadata} className="inbox-meta text-xs text-(--text-tertiary)">
            {metadata}
          </p>
          {isActiveJob(job) ? (
            <span className="inbox-activity text-(--status-info)">
              <LiveDot />
              Generating group review
            </span>
          ) : group.outdated ? (
            <span className="inbox-activity">Group review outdated</span>
          ) : group.version_count === 0 ? (
            <span className="inbox-activity">No group review yet</span>
          ) : null}
        </div>
      </div>
      <ul id={id} hidden={!expanded}>
        {children}
      </ul>
    </li>
  );
}
function InboxRow({
  entry,
  clone,
  selected,
  toggle,
  Link,
  live,
  nested = false,
}: {
  entry: Entry;
  clone: string;
  selected: boolean;
  toggle: (checked: boolean) => void;
  Link: ComponentType<ViewerLinkProps>;
  live: LiveSession[];
  /** Shown under its review group's row. */
  nested?: boolean;
}) {
  const pr = entry.pull_request;
  const metadata = [pr.repository, `#${pr.number}`, pr.author].filter(Boolean);
  return (
    <li
      className={cn(
        "inbox-columns inbox-row",
        nested && "inbox-nested",
        selected && "bg-(--surface-selected)",
      )}
    >
      <input
        type="checkbox"
        aria-label={`Select ${pr.repository} #${pr.number}`}
        checked={selected}
        disabled={entry.needs_clone}
        onChange={(e) => toggle(e.target.checked)}
      />
      <RiskCell risk={entry.risk_score} />
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
          <LiveBadge sessions={live} />
        </div>
        <p title={metadata.join(" · ")} className="inbox-meta text-xs text-(--text-tertiary)">
          {metadata.join(" · ")}
        </p>
        <ReviewActivity entry={entry} />
      </div>
      <StatusColumns entry={entry} />
    </li>
  );
}
