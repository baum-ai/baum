import {
  useId,
  useState,
  type ComponentType,
  type ReactNode,
  type Dispatch,
  type SetStateAction,
} from "react";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import { formatCount, formatRelative } from "@baum/viewer/components/format";
import {
  ChevronRightIcon,
  CircleCheckIcon,
  CircleDashedIcon,
  WarningIcon,
} from "@baum/viewer/components/icons";
import { Badge, LiveDot } from "@baum/viewer/components/ui";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import { cost, rowKey, type Entry } from "./api";

type Group = { id: string; heading: string; hint: string; tone: string; rows: Entry[] };
/** Discovery flags describe these groups; none infer approval or merge readiness. */
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
      id: "mine",
      heading: "Your pull requests",
      hint: "authored by you",
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
  for (const row of rows) {
    const index = row.historical
      ? 3
      : row.pull_request.review_requested_from_me
        ? 0
        : row.pull_request.authored_by_me
          ? 1
          : 2;
    groups[index]!.rows.push(row);
  }
  return groups.filter((group) => group.rows.length > 0);
}
export function Inbox({
  rows,
  cloneFor,
  selection,
  setSelection,
  Link,
}: {
  rows: Entry[];
  cloneFor: (entry: Entry) => string;
  selection: Set<string>;
  setSelection: Dispatch<SetStateAction<Set<string>>>;
  Link: ComponentType<ViewerLinkProps>;
}) {
  const [collapsed, setCollapsed] = useState(new Set<string>());
  const groups = inboxGroups(rows);
  const visible = groups.flatMap((group) => (collapsed.has(group.id) ? [] : group.rows));
  const toggle = (key: string, checked: boolean) =>
    setSelection((previous) => {
      const next = new Set(previous);
      if (checked) next.add(key);
      else next.delete(key);
      return next;
    });
  return (
    <section aria-label="Pull request inbox" className="local-inbox">
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
        <span className="hidden md:inline">Risk</span>
        <span className="inbox-title-heading">
          Pull request <span className="md:hidden">· {formatCount(visible.length)} visible</span>
        </span>
        <span className="hidden md:inline">Review</span>
        <span className="hidden md:inline">Generated</span>
        <span className="hidden md:inline">Cost</span>
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
          <span className={cn("tabular-nums", group.tone)}>{formatCount(group.rows.length)}</span>
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
function ReviewState({ entry }: { entry: Entry }) {
  const tone =
    entry.status === "failed"
      ? "text-(--status-critical)"
      : entry.status === "outdated"
        ? "text-(--status-caution)"
        : entry.status === "current"
          ? "text-(--text-secondary)"
          : "text-(--text-tertiary)";
  return (
    <span
      className={cn("inline-flex min-w-0 items-center gap-1.5 text-xs", tone)}
      title={entry.failure_reason ?? entry.store_error}
    >
      <span className="sr-only">Review: </span>
      {entry.status === "generating" ? (
        <LiveDot />
      ) : entry.status === "current" ? (
        <CircleCheckIcon size={14} className="flex-none text-(--status-positive)" />
      ) : entry.status === "failed" || entry.status === "outdated" ? (
        <WarningIcon size={14} className="flex-none" />
      ) : (
        <CircleDashedIcon size={14} className="flex-none" />
      )}
      <span>{entry.status.replaceAll("_", " ")}</span>
      {entry.version_count > 0 ? (
        <span className="text-(--text-tertiary)">
          · v{entry.latest?.version ?? entry.version_count}
        </span>
      ) : null}
    </span>
  );
}
function InboxRow({
  entry,
  clone,
  selected,
  toggle,
  Link,
}: {
  entry: Entry;
  clone: string;
  selected: boolean;
  toggle: (checked: boolean) => void;
  Link: ComponentType<ViewerLinkProps>;
}) {
  const pr = entry.pull_request;
  const score = entry.risk_score?.score;
  const tone =
    score == null
      ? "text-(--text-disabled)"
      : score >= 5
        ? "text-(--status-critical)"
        : score === 4
          ? "text-(--status-caution)"
          : score === 3
            ? "text-(--text-secondary)"
            : "text-(--status-positive)";
  const metadata = [
    pr.repository,
    `#${pr.number}`,
    pr.author,
    entry.stale ? "Stale discovery" : null,
    !pr.team_membership_known ? "Team requests unknown" : null,
    entry.failure_reason,
    entry.store_error,
  ].filter(Boolean);
  return (
    <li className={cn("inbox-columns inbox-row", selected && "bg-(--surface-selected)")}>
      <input
        type="checkbox"
        aria-label={`Select ${pr.repository} #${pr.number}`}
        checked={selected}
        onChange={(e) => toggle(e.target.checked)}
      />
      <span
        title={score == null ? "Not scored" : `Risk ${score} of 5`}
        className={cn("inbox-risk font-mono text-base font-medium tabular-nums", tone)}
      >
        <span className="sr-only">Risk </span>
        {score ?? "—"}
        {score != null ? <span className="text-(--text-tertiary)">/5</span> : null}
      </span>
      <div className="min-w-0">
        <div className="flex min-w-0 items-baseline gap-2">
          <Link
            href={`/?clone=${encodeURIComponent(clone)}&pr=${pr.number}`}
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
      </div>
      <div className="inbox-review flex flex-wrap items-center gap-x-3 gap-y-1 md:block">
        <ReviewState entry={entry} />
        <span className="text-xs text-(--text-tertiary) md:hidden">
          Generated {entry.latest?.generated_at ? formatRelative(entry.latest.generated_at) : "—"} ·
          Cost {cost(entry.latest?.recorded_cost_usd)}
        </span>
      </div>
      <div className="inbox-generation min-w-0 text-xs text-(--text-tertiary)">
        <span className="md:hidden">Generated </span>
        <span title={entry.latest?.generated_at}>
          {entry.latest?.generated_at ? formatRelative(entry.latest.generated_at) : "—"}
        </span>
        {entry.latest ? (
          <div
            className="inbox-model truncate"
            title={`${entry.latest.backend} / ${entry.latest.model}`}
          >
            {entry.latest.backend} / {entry.latest.model}
          </div>
        ) : null}
      </div>
      <div
        className="inbox-cost text-xs tabular-nums text-(--text-tertiary)"
        title={entry.latest?.recorded_cost_usd == null ? "Cost was not recorded" : undefined}
      >
        <span className="sr-only">Recorded cost: </span>
        {cost(entry.latest?.recorded_cost_usd)}
      </div>
    </li>
  );
}
