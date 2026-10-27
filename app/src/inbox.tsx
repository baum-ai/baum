import {
  useEffect,
  useId,
  useRef,
  useState,
  type ComponentType,
  type ReactNode,
  type Dispatch,
  type SetStateAction,
} from "react";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import { formatCount, formatRelative } from "@baum/viewer/components/format";
import {
  CheckIcon,
  ChevronRightIcon,
  CircleCheckIcon,
  CircleCrossIcon,
  CircleDashedIcon,
  CommentsIcon,
  MergeIcon,
  SparkleIcon,
  WarningIcon,
  type IconProps,
} from "@baum/viewer/components/icons";
import { Lines } from "@baum/viewer/components/review-parts";
import { Badge } from "@baum/viewer/components/ui";
import {
  riskScoreTone,
  sectionsInReviewOrder,
  type RiskScoreLevel,
} from "@baum/viewer/review-document";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import {
  appURL,
  fixJob,
  fixLabel,
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
import { LiveBadge, useNow } from "./live";
import { LoadingStatus, NineDots } from "./nine-dots";
import { canMerge, MergeDialog } from "./merge";
import { inboxAction, moveFocus, onBody, useShortcuts } from "./shortcuts";
import { MergeReadyDialog, useSavedReview } from "./up-next";

/** One row the keyboard can reach: a pull request or a review group. */
type Item = { id: string; entry?: Entry; group?: ReviewGroup };
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
/** Groups by who acts next. Discovery flags place requested, authored and
 *  other pull requests; the person's own split by GitHub's state into what
 *  needs their changes, what GitHub would merge now and the rest. A review group with a shown member is one row in the bucket of its
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
      id: "changes",
      heading: "Needs changes",
      hint: "yours, with changes requested, failing checks or a conflict",
      tone: "text-(--status-critical)",
      rows: [],
      groups: [],
    },
    {
      id: "ready",
      heading: "Ready to merge",
      hint: "yours, and GitHub would merge it now",
      tone: "text-(--status-positive)",
      rows: [],
      groups: [],
    },
    {
      id: "mine",
      heading: "Your pull requests",
      hint: "drafts and those waiting on reviews or checks",
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
  const own = (row: Entry) =>
    row.review === "changes_requested" || row.checks === "failing" || row.merge === "conflict"
      ? 1
      : row.merge === "ready"
        ? 2
        : 3;
  const bucket = (row: Entry) =>
    row.historical
      ? 5
      : row.pull_request.review_requested_from_me
        ? 0
        : row.pull_request.authored_by_me
          ? own(row)
          : 4;
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
  groups[5]!.rows.sort((a, b) => reviewed(b) - reviewed(a));
  return groups.filter((group) => group.rows.length + group.groups.length > 0);
}
export function Inbox({
  rows,
  cloneFor,
  selection,
  setSelection,
  Link,
  live = [],
  changed,
  groups: reviewGroups = [],
  fix,
  jobs = [],
  generate,
  refresh,
  bulk,
}: {
  rows: Entry[];
  cloneFor: (entry: Entry) => string;
  selection: Set<string>;
  setSelection: Dispatch<SetStateAction<Set<string>>>;
  Link: ComponentType<ViewerLinkProps>;
  /** Agent sessions in progress; a row shows those of its pull request. */
  live?: LiveSession[];
  /** Reloads the inbox after a merge. */
  changed?: () => Promise<void>;
  /** Review groups; each with a shown member is one row. */
  groups?: ReviewGroup[];
  /** Opens the fix confirmation for a broken pull request. */
  fix?: (entry: Entry) => void;
  /** The queue's jobs, for the fixes running. */
  jobs?: Job[];
  /** Starts the review of one pull request, as its page's Generate does. */
  generate?: (entry: Entry) => void;
  /** Refreshes from GitHub. */
  refresh?: () => void;
  /** The page's own actions on the selection, drawn in the selection bar
   *  after Merge. */
  bulk?: ReactNode;
}) {
  const section = useRef<HTMLElement>(null);
  const [collapsed, setCollapsed] = useState(new Set<string>());
  const [merging, setMerging] = useState<Entry | null>(null);
  const [mergingAll, setMergingAll] = useState<Entry[] | null>(null);
  // The rows whose peek is open, by row key.
  const [peeked, setPeeked] = useState(new Set<string>());
  const changedRows = useChangedRows(rows);
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
  // The rows the keyboard moves through, in the order they are drawn.
  const items: Item[] = groups.flatMap((group) =>
    collapsed.has(group.id)
      ? []
      : [
          ...group.groups.flatMap(({ group: reviewGroup, members }) => [
            { id: `group:${reviewGroup.id}`, group: reviewGroup },
            ...(expanded.has(reviewGroup.id)
              ? members.map((entry) => ({ id: `pr:${rowKey(entry)}`, entry }))
              : []),
          ]),
          ...group.rows.map((entry) => ({ id: `pr:${rowKey(entry)}`, entry })),
        ],
  );
  const [focusedId, setFocusedId] = useState<string | null>(null);
  // A row that left the list (collapsed, filtered out) is no longer focused.
  const focused = items.find((item) => item.id === focusedId);
  const focusedKey = focused?.id ?? null;
  const rowLink = (id: string) =>
    Array.from(section.current?.querySelectorAll<HTMLElement>("[data-row-id]") ?? [])
      .find((element) => element.dataset.rowId === id)
      ?.querySelector<HTMLElement>(".inbox-pr-title");
  // Focus goes to the row's own link, so a screen reader reads the row as it
  // moves, and the row scrolls into view.
  useEffect(() => {
    if (!focusedKey) return;
    const link = rowLink(focusedKey);
    link?.focus({ preventScroll: true });
    link?.closest("[data-row-id]")?.scrollIntoView?.({ block: "nearest" });
  }, [focusedKey]);
  useShortcuts((event) => {
    if (event.key === "Escape") {
      if (!focusedKey) return;
      event.preventDefault();
      setFocusedId(null);
      if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
      return;
    }
    const action = inboxAction(event);
    if (!action) return;
    // Enter on a link or button already does what it says.
    if (event.key === "Enter" && !onBody(event)) {
      if (event.target instanceof HTMLElement && event.target.closest("a, button")) return;
    }
    const entry = focused?.entry;
    switch (action) {
      case "next":
      case "previous":
        event.preventDefault();
        setFocusedId(
          moveFocus(
            items.map((item) => item.id),
            focusedKey,
            action === "next" ? 1 : -1,
          ),
        );
        return;
      case "refresh":
        if (!refresh) return;
        event.preventDefault();
        refresh();
        return;
      case "open":
        if (!focusedKey) return;
        event.preventDefault();
        rowLink(focusedKey)?.click();
        return;
      case "select":
        if (!entry || entry.needs_clone) return;
        event.preventDefault();
        toggle(rowKey(entry), !selection.has(rowKey(entry)));
        return;
      case "peek":
        // Space still presses a button or ticks a box it is on.
        if (!entry) return;
        if (event.target instanceof HTMLElement && event.target.closest("button, input")) return;
        event.preventDefault();
        peek(rowKey(entry));
        return;
      case "merge":
        if (!entry || !canMerge(entry)) return;
        event.preventDefault();
        setMerging(entry);
        return;
      case "fix":
        // Merge takes the row's action when both could apply.
        if (!entry || canMerge(entry) || !fixLabel(entry) || !fix || !cloneFor(entry)) return;
        if (isActiveJob(fixJob(entry, jobs))) return;
        event.preventDefault();
        fix(entry);
        return;
      case "generate":
        if (!entry || !generate || entry.status === "generating") return;
        event.preventDefault();
        generate(entry);
        return;
    }
  });
  const toggle = (key: string, checked: boolean) =>
    setSelection((previous) => {
      const next = new Set(previous);
      if (checked) next.add(key);
      else next.delete(key);
      return next;
    });
  const peek = (key: string) =>
    setPeeked((previous) => {
      const next = new Set(previous);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  const selected = rows.filter((row) => selection.has(rowKey(row)));
  const mergeable = selected.filter(canMerge);
  const renderRow = (entry: Entry, nested: boolean) => (
    <InboxRow
      key={rowKey(entry)}
      rowId={`pr:${rowKey(entry)}`}
      focused={focusedKey === `pr:${rowKey(entry)}`}
      entry={entry}
      clone={cloneFor(entry)}
      selected={selection.has(rowKey(entry))}
      toggle={(checked) => toggle(rowKey(entry), checked)}
      Link={Link}
      live={liveFor(live, entry.pull_request)}
      nested={nested}
      merge={canMerge(entry) ? () => setMerging(entry) : undefined}
      fix={fix && cloneFor(entry) ? () => fix(entry) : undefined}
      fixing={isActiveJob(fixJob(entry, jobs))}
      peeked={peeked.has(rowKey(entry))}
      peek={() => peek(rowKey(entry))}
      changed={changedRows.has(rowKey(entry))}
      generate={generate && cloneFor(entry) ? () => generate(entry) : undefined}
    />
  );
  return (
    <section
      ref={section}
      aria-label="Pull request inbox"
      className={cn("local-inbox", selection.size > 0 && "selecting")}
    >
      {selection.size > 0 ? (
        <div role="toolbar" aria-label="Selected pull requests" className="inbox-selection-bar">
          <span className="inbox-selection-count">{formatCount(selection.size)} selected</span>
          {mergeable.length > 0 ? (
            <button
              type="button"
              className="inbox-bulk inbox-bulk-merge"
              onClick={() =>
                mergeable.length === 1 ? setMerging(mergeable[0]!) : setMergingAll(mergeable)
              }
            >
              Merge {formatCount(mergeable.length)}
            </button>
          ) : null}
          {bulk}
          <button
            type="button"
            className="inbox-bulk inbox-bulk-clear"
            onClick={() => setSelection(new Set())}
          >
            Clear
          </button>
        </div>
      ) : null}
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
        <span className="sr-only">Actions</span>
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
              focused={focusedKey === `group:${reviewGroup.id}`}
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
      <footer className="inbox-keys" aria-hidden="true">
        <span>
          <kbd>j</kbd> <kbd>k</kbd> move
        </span>
        <span>
          <kbd>x</kbd> select
        </span>
        <span>
          <kbd>space</kbd> peek
        </span>
        <span>
          <kbd>o</kbd> open
        </span>
        <span>
          <kbd>m</kbd> merge
        </span>
        <span>
          <kbd>f</kbd> fix
        </span>
        <span>
          <kbd>?</kbd> every key
        </span>
      </footer>
      {merging ? (
        <MergeDialog
          key={rowKey(merging)}
          entry={merging}
          close={() => setMerging(null)}
          merged={changed ?? (async () => {})}
        />
      ) : null}
      {mergingAll ? (
        <MergeReadyDialog
          entries={mergingAll}
          close={() => setMergingAll(null)}
          merged={async () => {
            setSelection(new Set());
            await changed?.();
          }}
        />
      ) : null}
    </section>
  );
}

/** What a row shows that can change while the list is open; a row whose
 *  state differs from the last read flashes once. */
const rowState = (entry: Entry) =>
  [
    entry.status,
    entry.review,
    entry.checks,
    entry.merge,
    entry.unresolved,
    entry.risk_score?.score,
    entry.review_minutes,
    entry.latest?.version,
  ].join("|");
const FLASH_MS = 1200;

/** The row keys whose state changed in the latest read, for a moment. Rows
 *  seen for the first time are not changes. */
function useChangedRows(rows: Entry[]) {
  const seen = useRef<Map<string, string> | null>(null);
  const [changed, setChanged] = useState(new Set<string>());
  const states = rows.map((row) => [rowKey(row), rowState(row)] as const);
  const signature = states.map(([key, state]) => `${key}=${state}`).join("\n");
  useEffect(() => {
    const previous = seen.current;
    seen.current = new Map(states);
    if (!previous) return;
    const now = states.filter(([key, state]) => previous.has(key) && previous.get(key) !== state);
    if (now.length === 0) return;
    setChanged(new Set(now.map(([key]) => key)));
    const timer = setTimeout(() => setChanged(new Set()), FLASH_MS);
    return () => clearTimeout(timer);
    // `signature` stands for the rows' states.
  }, [signature]);
  return changed;
}

/** When the list last read the daemon, kept current while the page is open;
 *  its mark turns while a read is under way. */
export function UpdatedAgo({ at, busy }: { at: number | null; busy: boolean }) {
  const now = useNow(5_000);
  if (at == null && !busy) return null;
  return (
    <span className="inbox-updated" title={at == null ? undefined : new Date(at).toLocaleString()}>
      <span aria-hidden="true" className={cn("inbox-updated-mark", busy && "busy")} />
      {busy || at == null
        ? "Updating…"
        : `Updated ${now - at < 10_000 ? "just now" : formatRelative(new Date(at).toISOString(), now)}`}
    </span>
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
          <span className="inbox-bucket-count">{formatCount(count)}</span>
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
  if (entry.needs_clone)
    return (
      <span className="inbox-activity text-(--status-caution)">
        <WarningIcon size={12} className="flex-none" />
        Needs a local clone
      </span>
    );
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
  // GitHub blocks a branch behind its base when protection requires it up to
  // date; the PR page offers Update branch for it.
  if (entry.pull_request.behind) return { label: "Behind base", tone: CAUTION, icon: WarningIcon };
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
        "inbox-status inline-flex flex-wrap items-center gap-x-1.5 text-xs tabular-nums",
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
/** Opens the merge confirmation; shown on the row being pointed at or
 *  focused, and always on a touch screen. */
function MergeAction({ entry, merge }: { entry: Entry; merge: () => void }) {
  const pr = entry.pull_request;
  return (
    <button
      type="button"
      className={cn("inbox-action inbox-merge", FOCUS_RING)}
      aria-label={`Merge ${pr.repository} #${pr.number}`}
      onClick={merge}
    >
      Merge
      <kbd aria-hidden="true" className="key-hint">
        m
      </kbd>
    </button>
  );
}

/** The small fix action in a broken pull request's row. */
function FixAction({ label, fix, fixing }: { label: string; fix?: () => void; fixing: boolean }) {
  if (fixing)
    return (
      <span className="inbox-action inbox-fix">
        <SparkleIcon size={12} className="flex-none" />
        Fixing…
      </span>
    );
  if (!fix) return null;
  return (
    <button
      type="button"
      className={cn("inbox-action inbox-fix", FOCUS_RING)}
      aria-label={label}
      title={label}
      onClick={fix}
    >
      <SparkleIcon size={12} className="flex-none" />
      Fix
      <kbd aria-hidden="true" className="key-hint">
        f
      </kbd>
    </button>
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
/** The row's actions: the one thing to do about its state (merge a ready
 *  pull request, or fix a broken one) and Peek. */
function RowActions({
  entry,
  merge,
  fix,
  fixing,
  peeked,
  peek,
  peekId,
}: {
  entry: Entry;
  merge?: () => void;
  fix?: () => void;
  fixing: boolean;
  peeked: boolean;
  peek: () => void;
  peekId: string;
}) {
  const label = fixLabel(entry);
  const pr = entry.pull_request;
  return (
    <span className="inbox-row-actions">
      {merge ? (
        <MergeAction entry={entry} merge={merge} />
      ) : label ? (
        <FixAction label={label} fix={fix} fixing={fixing} />
      ) : null}
      <button
        type="button"
        className={cn("inbox-action inbox-peek", FOCUS_RING)}
        aria-label={`Peek at ${pr.repository} #${pr.number}`}
        aria-expanded={peeked}
        aria-controls={peekId}
        onClick={peek}
      >
        {peeked ? "Close" : "Peek"}
        <kbd aria-hidden="true" className="key-hint">
          space
        </kbd>
      </button>
    </span>
  );
}
const PEEK_SECTIONS = 4;
/** A look at the saved review under its row: the summary, the first reason
 *  for its risk and the first sections with their lines, and a way in. */
function RowPeek({
  id,
  entry,
  clone,
  href,
  Link,
  generate,
}: {
  id: string;
  entry: Entry;
  clone: string;
  href: string;
  Link: ComponentType<ViewerLinkProps>;
  generate?: () => void;
}) {
  const saved = useSavedReview(entry, clone);
  const doc = saved && saved !== "failed" ? saved : null;
  const pr = entry.pull_request;
  const reason = doc?.summary.risk_score?.reasons[0];
  const reasonTitle = reason
    ? doc?.sections.find((section) => section.id === reason.section)?.title
    : undefined;
  const sections = doc
    ? sectionsInReviewOrder(doc.sections).filter((section) => section.derived.state !== "stale")
    : [];
  const score = entry.risk_score?.score ?? 0;
  const lead = doc
    ? doc.summary.text
    : entry.needs_clone
      ? `No review yet: baum needs a local clone of ${pr.repository}. Choose a folder or clone it in Repositories, and the review can be written.`
      : entry.status === "generating"
        ? "baum is writing the review. It shows here once it is saved."
        : entry.status === "failed"
          ? `The review failed${entry.failure_reason ? `: ${entry.failure_reason}` : "."}`
          : saved === "failed"
            ? "The saved review could not be read."
            : entry.version_count > 0 && clone
              ? null
              : "No review yet.";
  return (
    <div id={id} className="inbox-peek-panel" role="region" aria-label={`Peek at #${pr.number}`}>
      <div className="inbox-peek-summary">
        <h3>Summary</h3>
        {lead == null ? <LoadingStatus>Reading the review…</LoadingStatus> : <p>{lead}</p>}
        {reason ? (
          <p className={cn("inbox-peek-risk", score >= 4 && "high")}>
            {reasonTitle ? <strong>{reasonTitle}: </strong> : null}
            {reason.text}
          </p>
        ) : null}
      </div>
      <div className="inbox-peek-changes">
        {sections.length > 0 ? (
          <>
            <h3>Key changes</h3>
            <ul>
              {sections.slice(0, PEEK_SECTIONS).map((section) => (
                <li key={section.id}>
                  <span title={section.title}>{section.title}</span>
                  <Lines
                    additions={section.derived.additions}
                    deletions={section.derived.deletions}
                    format={formatCount}
                  />
                </li>
              ))}
            </ul>
            {sections.length > PEEK_SECTIONS ? (
              <p className="inbox-peek-more">
                and {formatCount(sections.length - PEEK_SECTIONS)} more
              </p>
            ) : null}
          </>
        ) : null}
        <div className="inbox-peek-actions">
          <Link href={href} className={cn("inbox-peek-open", FOCUS_RING)}>
            {doc ? "Open the review" : "Open the pull request"}
          </Link>
          {!doc && generate && entry.status !== "generating" && !entry.needs_clone ? (
            <button type="button" className={cn("inbox-action", FOCUS_RING)} onClick={generate}>
              Generate the review
            </button>
          ) : null}
        </div>
      </div>
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
  // The tones of the hosted risk pill; a lower bound may hide a higher risk,
  // so it never reads as calm.
  const tone =
    score == null
      ? undefined
      : lowerBound && score < 4
        ? "caution"
        : riskScoreTone(score as RiskScoreLevel);
  return (
    <span role="img" aria-label={riskLabel} title={riskLabel} className="inbox-risk">
      {tone ? (
        <Badge tone={tone} className="font-medium tabular-nums">
          {lowerBound ? "≥" : ""}
          {score}
          <span className="opacity-60">/5</span>
        </Badge>
      ) : null}
    </span>
  );
}
/** What PullRequestState draws; empty when there is nothing to say. */
export function pullRequestStates(entry: Entry): StateStyle[] {
  return [REVIEW[entry.review], mergeStatus(entry)].filter((style): style is StateStyle =>
    Boolean(style),
  );
}
/** A pull request's GitHub review and merge status on one line, drawn as the
 *  inbox's cells draw them, for places that list pull requests outside it. */
export function PullRequestState({ entry }: { entry: Entry }) {
  const parts = pullRequestStates(entry);
  if (parts.length === 0) return null;
  return (
    <span className="inline-flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
      {parts.map(({ label, tone, icon: Icon, iconTone }) => (
        <span key={label} className={cn("inline-flex items-center gap-1", tone)}>
          <Icon size={13} className={cn("flex-none", iconTone)} />
          {label}
        </span>
      ))}
    </span>
  );
}
/** A review group's one row; its shown members nest under it when expanded. */
function GroupRow({
  group,
  focused,
  job,
  expanded,
  onToggle,
  Link,
  children,
}: {
  group: ReviewGroup;
  /** The row the keyboard is on. */
  focused: boolean;
  job: Job | undefined;
  expanded: boolean;
  onToggle: () => void;
  Link: ComponentType<ViewerLinkProps>;
  children: ReactNode;
}) {
  const id = useId();
  const metadata = [group.repository, group.members.map((n) => `#${n}`).join(", ")].join(" · ");
  return (
    <li>
      <div
        className="inbox-columns inbox-row inbox-group-row"
        data-row-id={`group:${group.id}`}
        data-focused={focused || undefined}
      >
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
              href={appURL(`?page=pulls&group=${encodeURIComponent(group.id)}`)}
              title={group.name}
              className="inbox-pr-title min-w-0 font-medium text-(--text-primary) hover:underline"
            >
              {group.name}
            </Link>
            <Badge size="compact">Group of {group.members.length}</Badge>
          </div>
          <p title={metadata} className="inbox-meta text-xs text-(--text-tertiary)">
            {metadata}
          </p>
          {isActiveJob(job) ? (
            <span className="inbox-activity text-(--text-secondary)">
              <NineDots size={14} />
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
  rowId,
  focused,
  entry,
  clone,
  selected,
  toggle,
  Link,
  live,
  nested = false,
  merge,
  fix,
  fixing,
  peeked,
  peek,
  changed,
  generate,
}: {
  rowId: string;
  /** The row the keyboard is on. */
  focused: boolean;
  entry: Entry;
  clone: string;
  selected: boolean;
  toggle: (checked: boolean) => void;
  Link: ComponentType<ViewerLinkProps>;
  live: LiveSession[];
  /** Shown under its review group's row. */
  nested?: boolean;
  merge?: () => void;
  fix?: () => void;
  fixing: boolean;
  peeked: boolean;
  peek: () => void;
  /** Its state changed in the latest read. */
  changed: boolean;
  generate?: () => void;
}) {
  const pr = entry.pull_request;
  const metadata = [pr.repository, `#${pr.number}`, pr.author].filter(Boolean);
  const peekId = useId();
  const href = appURL(
    `?page=pulls&${clone ? `clone=${encodeURIComponent(clone)}` : `repository=${encodeURIComponent(pr.repository)}`}&pr=${pr.number}`,
  );
  return (
    <li
      data-row-id={rowId}
      data-focused={focused || undefined}
      data-changed={changed || undefined}
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
            href={href}
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
      <RowActions
        entry={entry}
        merge={merge}
        fix={fix}
        fixing={fixing}
        peeked={peeked}
        peek={peek}
        peekId={peekId}
      />
      {peeked ? (
        <RowPeek
          id={peekId}
          entry={entry}
          clone={clone}
          href={href}
          Link={Link}
          generate={generate}
        />
      ) : null}
    </li>
  );
}
