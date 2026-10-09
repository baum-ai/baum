import { type ReactNode, useId } from "react";
import { cn, FOCUS_RING } from "./classnames";
import { CARD_LABEL } from "./editorial";
import { ChevronDownIcon, type IconProps } from "./icons";
import { Spinner } from "./ui";

/** The rows of a vertical navigation rail, such as the review rail beside a
 *  pull request. A rail inside a page only reads as navigation if its rows are
 *  a navigation's rows — a column of bordered buttons reads as a toolbar that
 *  happens to be stacked.
 *
 *  Rows are components rather than an exported class string so that rails
 *  built from them cannot drift, and so a caller never has to remember which
 *  of the three states carries which colour. */

type RowState = "active" | "idle" | "disabled";
type Glyph = (props: IconProps) => React.ReactElement;

/** `depth` indents a nested row past its parent's glyph, so the guide line
 *  {@link SideNavNest} draws falls between the two. `gap` is a variant rather
 *  than something a caller layers on: `cn` joins classes without resolving
 *  them, so a second gap utility would leave the winner to stylesheet order. */
function rowClass(state: RowState, depth: 0 | 1, gap: "standard" | "tight" = "standard") {
  return cn(
    "relative flex w-full items-center rounded-(--radius-sm) py-[7px] pr-2.5 text-left transition-colors duration-150 hover:no-underline",
    gap === "standard" ? "gap-2.5" : "gap-1.5",
    depth === 0 ? "pl-2.5 text-base" : "pl-2 text-base",
    state !== "disabled" && FOCUS_RING,
    state === "active" && "bg-(--surface-selected) font-medium text-(--text-primary)",
    state === "idle" &&
      "text-(--text-secondary) hover:bg-(--surface-hover) hover:text-(--text-primary)",
    state === "disabled" && "cursor-default text-(--text-disabled)",
  );
}

/** What colour a row's glyph wears. `neutral` is the rail's own grey, which
 *  brightens on the active row; the others name what the row is about —
 *  accent for the review's own reading (summary, changes), critical for
 *  risks, teal for sessions — and hold their colour in every state, because
 *  the colour is how a reader tells the groups apart at a glance. */
export type RowTint = "neutral" | "accent" | "critical" | "teal";

const TINTS: Record<Exclude<RowTint, "neutral">, string> = {
  accent: "text-(--action-accent)",
  critical: "text-(--status-critical)",
  teal: "text-(--data-3)",
};

function glyphClass(state: RowState, tint: RowTint = "neutral") {
  return cn(
    "flex-none transition-colors duration-150",
    state === "disabled"
      ? "text-(--text-disabled)"
      : tint !== "neutral"
        ? cn(TINTS[tint], state === "idle" && "opacity-80")
        : state === "active"
          ? "text-(--text-secondary)"
          : "text-(--text-tertiary)",
  );
}

/** The mark on the row you are on: a hairline of accent on its left edge, the
 *  same device as the application rail, so "where am I" reads one way. */
function ActiveMark({ active }: { active: boolean }) {
  return (
    <span
      aria-hidden
      className={cn(
        "absolute top-1/2 left-0 h-4 w-0.5 -translate-y-1/2 bg-(--action-accent) transition-opacity duration-150",
        active ? "opacity-100" : "opacity-0",
      )}
    />
  );
}

/** How far through a group the reader is, as a bar under the row: the count
 *  says it in words, the bar says it at a glance. */
function ProgressBar({ read, total }: { read: number; total: number }) {
  const share = total > 0 ? Math.min(1, read / total) : 0;
  return (
    <span
      aria-hidden
      className="absolute inset-x-2.5 bottom-0 h-[2px] overflow-hidden rounded-full bg-(--border-subtle)"
    >
      <span
        className={cn(
          "block h-full rounded-full transition-[width] duration-300 ease-out",
          share >= 1 ? "bg-(--status-positive)" : "bg-(--action-accent)",
        )}
        style={{ width: `${share * 100}%` }}
      />
    </span>
  );
}

/** Long titles truncate rather than wrap: a rail whose rows are three lines
 *  tall stops looking like a list of places to go. `wrap` allows a second
 *  line, for rows whose titles are sentences and read as nothing cut to one. */
function Label({ children, wrap = false }: { children: string; wrap?: boolean }) {
  return (
    <span
      className={cn("min-w-0 flex-1", wrap ? "line-clamp-2 leading-snug" : "truncate")}
      title={children}
    >
      {children}
    </span>
  );
}

/** The heading over a group of rows: small, uppercase, and not a control. */
export function SideNavGroup({
  label,
  children,
  className,
}: {
  label: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={className}>
      <p className={cn(CARD_LABEL, "px-2.5 pb-1.5")}>{label}</p>
      <div className="flex flex-col gap-0.5">{children}</div>
    </div>
  );
}

/** Rows that belong to the row above them. The guide line is what makes the
 *  nesting visible once the rows are narrower than the rail. */
export function SideNavNest({ id, children }: { id?: string; children: ReactNode }) {
  return (
    <div
      id={id}
      className="mt-0.5 ml-[17px] flex flex-col gap-0.5 border-l border-(--border-subtle) pl-1.5"
    >
      {children}
    </div>
  );
}

/** A row that folds a group of rows away and, in the same click, shows what
 *  the whole group holds. Two behaviours in one control because they are one
 *  intent: a reader opening "Risks" wants to see the risks, and a reader who
 *  has finished with them wants them out of the way. Clicking folds the group
 *  only once it is already the row you are on, so the first click never hides
 *  what it was asked to show. */
export function SideNavDisclosure({
  label,
  icon: Icon,
  expanded,
  active,
  progress,
  read,
  count,
  tint,
  busy = false,
  list,
  controls,
  onSelect,
}: {
  label: string;
  icon: Glyph;
  tint?: RowTint;
  /** The group's contents are being fetched or refreshed right now. */
  busy?: boolean;
  expanded: boolean;
  /** Whether the pane is showing this group. Said as `aria-pressed`, the way
   *  every other row of this rail says it: two vocabularies for one selection
   *  model is what a reader has to unpick on the way down. */
  active: boolean;
  /** How far down this group the reader has got — "3 of 6" — as part of the
   *  row's name, not written on the row (the bar under it shows it): a reader
   *  who cannot see the rail still wants to know how much of it is left. */
  progress?: string;
  /** The same progress as a pair of numbers, for the bar under the row. */
  read?: { done: number; total: number };
  /** How many rows the group holds — linked sessions, say. Shown at the row's
   *  end. Without `progress` it is part of the row's name too; with it, the
   *  progress already names the total. */
  count?: number;
  /** The id of the list this row folds. Named only while that list is on the
   *  page, so the row never points at an element that is not there. */
  list: string;
  /** The id of the pane this row fills. */
  controls: string;
  /** Called with whether the group should now be open. */
  onSelect: (expanded: boolean) => void;
}) {
  const state = active ? "active" : "idle";
  // Progress is carried by the bar and the row's name; only a count is
  // written out at the row's end.
  const trailing = count === undefined ? undefined : String(count);
  return (
    <button
      type="button"
      aria-label={
        progress
          ? `${label}, ${progress} read`
          : count === undefined
            ? undefined
            : `${label}, ${count}`
      }
      aria-expanded={expanded}
      aria-pressed={active}
      aria-controls={expanded ? `${list} ${controls}` : controls}
      onClick={() => onSelect(active ? !expanded : true)}
      className={cn(rowClass(state, 0, "tight"), "cursor-pointer")}
    >
      <ActiveMark active={active} />
      <ChevronDownIcon
        size={12}
        className={cn(glyphClass(state), !expanded && "-rotate-90", "transition-transform")}
      />
      <Icon className={glyphClass(state, tint)} />
      <Label>{label}</Label>
      {busy ? <Spinner size={11} className="text-(--text-tertiary)" /> : null}
      {trailing ? (
        <span aria-hidden className="flex-none text-xs tabular-nums text-(--text-tertiary)">
          {trailing}
        </span>
      ) : null}
      {read && read.total > 0 ? <ProgressBar read={read.done} total={read.total} /> : null}
    </button>
  );
}

/** A row that switches what the pane beside it shows. */
export function SideNavButton({
  label,
  icon: Icon,
  tint,
  active,
  depth = 0,
  controls,
  aside,
  description,
  wrap,
  onSelect,
}: {
  label: string;
  /** Left off for rows whose parent's glyph already says what they are. */
  icon?: Glyph;
  tint?: RowTint;
  active: boolean;
  depth?: 0 | 1;
  /** Let the label take a second line instead of truncating. */
  wrap?: boolean;
  /** The id of the pane this row drives, for readers who arrive at the row
   *  first and need to know where its answer appears. */
  controls?: string;
  /** A small mark at the row's end — a risk tier dot, say. Decoration that the
   *  label already states in words, so it is hidden from assistive technology. */
  aside?: ReactNode;
  /** What the aside says that the label does not, for assistive technology. */
  description?: string;
  onSelect: () => void;
}) {
  const state = active ? "active" : "idle";
  const descriptionId = useId();
  return (
    <button
      type="button"
      aria-pressed={active}
      aria-controls={controls}
      aria-describedby={description ? descriptionId : undefined}
      onClick={onSelect}
      className={cn(rowClass(state, depth), "cursor-pointer")}
    >
      <ActiveMark active={active} />
      {Icon ? <Icon className={glyphClass(state, tint)} /> : null}
      <Label wrap={wrap}>{label}</Label>
      {aside ? (
        <span aria-hidden className="flex flex-none items-center">
          {aside}
        </span>
      ) : null}
      {/* Hidden, so it describes the row without joining its name. */}
      {description ? (
        <span id={descriptionId} hidden>
          {description}
        </span>
      ) : null}
    </button>
  );
}
