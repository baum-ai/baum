import {
  type CSSProperties,
  type PointerEvent,
  type ReactNode,
  type RefObject,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import "./flow.css";
import "./table.css";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import { InlineMarkdown } from "@baum/viewer/components/markdown";
import { type Column, Table } from "@baum/viewer/components/table";
import { Badge, Toggle } from "@baum/viewer/components/ui";
import type {
  ColumnItem,
  ColumnsData,
  FlowData,
  FlowStep,
  SequenceData,
  TableCell,
  TableData,
  Tone,
} from "./model";
import { isRecord, isString } from "../tiles/types";

/** The diagrams and tables the view call writes, drawn by the page itself:
 *  a flow of steps in lanes, columns of items, a sequence of messages
 *  between actors, and a table. Each is held in a container, so it lays
 *  itself out by the room it has: lanes wrap and then stack, columns stack,
 *  and a sequence becomes a list of its messages, never widening the page. */

/** A step's, item's or message's box by its tone. */
const BOX: Record<Tone, string> = {
  plain: "border-default bg-surface-card",
  new: "border-status-info bg-surface-card",
  good: "border-transparent bg-status-positive-bg",
  bad: "border-transparent bg-status-critical-bg",
  muted: "border-subtle bg-surface-wash",
  missing: "border-dashed border-status-critical bg-transparent",
};

/** Its title's colour by tone. */
const INK: Record<Tone, string> = {
  plain: "text-primary",
  new: "text-primary",
  good: "text-primary",
  bad: "text-status-critical",
  muted: "text-secondary",
  missing: "text-status-critical",
};

/** A column's label colour by tone. */
const LABEL_INK: Record<Tone, string> = {
  plain: "text-tertiary",
  new: "text-status-info",
  good: "text-status-positive",
  bad: "text-status-critical",
  muted: "text-tertiary",
  missing: "text-status-critical",
};

const TONES = new Set<string>(["plain", "new", "good", "bad", "muted", "missing"]);
const toneOf = (tone: unknown): Tone =>
  isString(tone) && TONES.has(tone) ? (tone as Tone) : "plain";

/** The small uppercase label above a block of a section's body (a tile's
 *  title in the bare variant too), and above a flow's lane. */
export const BLOCK_LABEL = "text-xs font-medium tracking-[0.08em] text-tertiary uppercase";

/** One step, item or message: its title and an optional note under it. */
function Box({
  title,
  note,
  tone,
  className,
  pressed,
  onPress,
  onPeek,
  change,
  ink,
}: {
  title: string;
  note?: string;
  tone: Tone;
  /** The title's colour, where it isn't the tone's. */
  ink?: string;
  /** What the pull request does to it, marked as a sequence marks a
   *  message: a pill over the title and a bar at the left edge. */
  change?: Change | null;
  className?: string;
  /** Set when the box opens something: it is then a button, `pressed` while
   *  what it opens is shown. */
  pressed?: boolean;
  onPress?: () => void;
  /** Called with true while a mouse is over the box, false when it leaves. */
  onPeek?: (on: boolean) => void;
}) {
  const Element = onPress ? "button" : "div";
  return (
    <Element
      {...(onPress
        ? { type: "button" as const, "aria-pressed": pressed ?? false, onClick: onPress }
        : {})}
      {...(onPeek
        ? {
            onPointerEnter: (event: PointerEvent) => event.pointerType === "mouse" && onPeek(true),
            onPointerLeave: () => onPeek(false),
          }
        : {})}
      className={cn(
        "relative grid min-w-0 content-start gap-0.5 rounded-md border px-3 py-2 [overflow-wrap:anywhere]",
        BOX[tone],
        tone === "muted" && "opacity-80",
        onPress &&
          cn(
            "cursor-pointer text-left hover:bg-surface-hover",
            FOCUS_RING,
            pressed && "border-strong ring-1 ring-(--border-strong)",
          ),
        className,
      )}
    >
      {change ? (
        <>
          <ChangeBar change={change} />
          <span className="mb-1 justify-self-start">
            <ChangePill change={change} />
          </span>
        </>
      ) : null}
      <InlineMarkdown className={cn("font-medium leading-snug", ink ?? INK[tone])}>
        {title}
      </InlineMarkdown>
      {note ? (
        <InlineMarkdown className="text-xs leading-snug text-secondary">{note}</InlineMarkdown>
      ) : null}
    </Element>
  );
}

/** The connector drawn before a step: across while the lanes are rows of
 *  the grid, down while each lane is a list. A branch's first step has the
 *  turn only in the grid, where it sits under the steps after the one it
 *  leaves; the list says which one in words. */
function Join({ join }: { join: FlowStep["join"] | "branch" }) {
  if (join === "none") return <span aria-hidden className="flow-join flow-gap" />;
  const [across, down] = join === "both" ? ["⇄", "⇅"] : join === "branch" ? ["↳", ""] : ["→", "↓"];
  return (
    <span aria-hidden data-join={join ?? "arrow"} className="flow-join">
      <span className="flow-down">{down}</span>
      <span className="flow-across">{across}</span>
    </span>
  );
}

export function validFlow(data: unknown): data is FlowData {
  return (
    isRecord(data) &&
    Array.isArray(data.lanes) &&
    data.lanes.length > 0 &&
    data.lanes.every(
      (lane) =>
        isRecord(lane) &&
        Array.isArray(lane.steps) &&
        lane.steps.every(
          (step) =>
            isRecord(step) &&
            isString(step.title) &&
            (step.more === undefined || isString(step.more)),
        ),
    )
  );
}

/** Whether a flow of `columns` steps fits the lanes-as-rows layout (about
 *  8.5rem a column) and, if it has lane labels, the labels in a column of
 *  their own (6rem more, columns of 9rem) in `rem` of room. Never under 28rem,
 *  and a flow of many columns stops asking at 72rem (its labels never fit
 *  past it). Narrower, each lane is a list going down. */
export function flowFit(columns: number, labelled: boolean, rem: number) {
  const labels = columns * 9 + 6;
  return {
    across: rem >= Math.min(Math.max(columns * 8.5, 28), 72),
    labels: labelled && labels <= 72 && rem >= Math.max(labels, 28),
  };
}

/** The step of the first lane a later lane branches off (from 1), or 0 for
 *  a lane of its own. */
const branchOf = (data: FlowData, at: number) => {
  const from = data.lanes[at]?.from;
  const first = data.lanes[0]?.steps.length ?? 0;
  return at > 0 && typeof from === "number" && from >= 1 && from <= first ? from : 0;
};

/** A connector's path and the arrowheads at its ends. `kind`: a line
 *  between steps, a branch's elbow, or the rule over a lane of its own. */
type Wire = { d: string; heads: string[]; kind: "step" | "branch" | "apart" };

/** An arrowhead whose tip is at (x, y), pointing right or down. */
const head = (x: number, y: number, down: boolean) =>
  down
    ? `M${x - 4} ${y - 6} L${x} ${y} L${x + 4} ${y - 6}`
    : `M${x - 6} ${y - 4} L${x} ${y} L${x - 6} ${y + 4}`;

/** The connectors between the boxes while the lanes are rows of the grid:
 *  a line between each step and the next, and for a branch an elbow down
 *  from the step it leaves after into its first step. Measured from the
 *  laid-out boxes, so it follows any width; none while each lane is a list,
 *  where the arrows are text. `unlinked`: the branches whose elbow can't be
 *  drawn plainly (moved left of their step, or another lane in the way),
 *  which name their step in words instead. */
function useWires(
  data: FlowData,
  root: RefObject<HTMLDivElement | null>,
  layout: { across: boolean; labels: boolean },
) {
  const [drawn, setDrawn] = useState<Drawn>(NOTHING_DRAWN);
  useLayoutEffect(() => {
    const element = root.current;
    if (!element) return;
    const measure = () => {
      const steps = element.querySelector(".flow-steps");
      if (!steps || getComputedStyle(steps).display !== "contents") {
        setDrawn(NOTHING_DRAWN);
        return;
      }
      const origin = element.getBoundingClientRect();
      const box = (lane: number, step: number) => {
        const found = element.querySelector(`[data-step="${lane}:${step}"] > :last-child`);
        if (!found) return undefined;
        const rect = found.getBoundingClientRect();
        return {
          left: rect.left - origin.left,
          right: rect.right - origin.left,
          top: rect.top - origin.top,
          bottom: rect.bottom - origin.top,
        };
      };
      const next: Wire[] = [];
      const unlinked: number[] = [];
      const gap = 3;
      data.lanes.forEach((lane, at) => {
        lane.steps.forEach((step, position) => {
          if (position === 0 || step.join === "none") return;
          const a = box(at, position - 1);
          const b = box(at, position);
          if (!a || !b || b.left <= a.right) return;
          const y = (Math.max(a.top, b.top) + Math.min(a.bottom, b.bottom)) / 2;
          const [x0, x1] = [a.right + gap, b.left - gap];
          const heads = [head(x1, y, false)];
          if (step.join === "both")
            heads.push(`M${x0 + 6} ${y - 4} L${x0} ${y} L${x0 + 6} ${y + 4}`);
          next.push({ d: `M${x0} ${y} H${x1}`, heads, kind: "step" });
        });
        const from = branchOf(data, at);
        if (!from && at > 0) {
          // A later lane of its own starts a separate path: a dashed rule
          // between it and the lanes above.
          const above = Math.max(
            ...data.lanes
              .slice(0, at)
              .flatMap((each, lane) => each.steps.map((_, step) => box(lane, step)?.bottom ?? 0)),
          );
          const label = element.querySelector(`[data-lane="${at}"] > .flow-label`);
          const first = box(at, 0);
          const top = Math.min(
            first?.top ?? Infinity,
            label ? label.getBoundingClientRect().top - origin.top : Infinity,
          );
          if (Number.isFinite(above) && Number.isFinite(top) && top > above) {
            const y = Math.round((above + top) / 2) + 0.5;
            next.push({ d: `M0 ${y} H${origin.width}`, heads: [], kind: "apart" });
          }
        }
        if (!from) return;
        const a = box(0, from - 1);
        const b = box(at, 0);
        if (!a || !b) return;
        const x = (a.left + a.right) / 2;
        const y0 = a.bottom + gap;
        const y1 = (b.top + b.bottom) / 2;
        // The line drops from the step's middle: something in a lane between
        // under it, or a lane of its own between, would read as the source.
        const blocked = data.lanes.slice(1, at).some(
          (each, lane) =>
            !branchOf(data, lane + 1) ||
            each.steps.some((_, step) => {
              const other = box(lane + 1, step);
              return other !== undefined && other.left <= x && other.right >= x;
            }),
        );
        if (blocked || b.left <= a.right || y1 <= y0) {
          unlinked.push(at);
          return;
        }
        const r = Math.min(6, (y1 - y0) / 2);
        next.push({
          d: `M${x} ${y0} V${y1 - r} Q${x} ${y1} ${x + r} ${y1} H${b.left - gap}`,
          heads: [head(b.left - gap, y1, false)],
          kind: "branch",
        });
      });
      setDrawn({ grid: true, wires: next, unlinked });
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [data, root, layout]);
  return drawn;
}

/** `grid`: the lanes are rows of the grid, where the wires replace the
 *  arrows' text. */
type Drawn = { grid: boolean; wires: Wire[]; unlinked: number[] };
const NOTHING_DRAWN: Drawn = { grid: false, wires: [], unlinked: [] };

export function FlowDiagram({ data }: { data: FlowData }) {
  // The step whose detail shows under the flow, as [lane, step]: the one
  // the mouse is over, else the one clicked.
  const [picked, setPicked] = useState<[number, number] | null>(null);
  const [peeked, setPeeked] = useState<[number, number] | null>(null);
  // A new flow may have no step where the mouse was.
  useEffect(() => setPeeked(null), [data]);
  const shown = peeked ?? picked;
  const opened = shown ? data.lanes[shown[0]]?.steps[shown[1]] : undefined;
  const root = useRef<HTMLDivElement>(null);
  // Steps the pull request didn't add are faded while this is on.
  const [focused, setFocused] = useState(false);
  const clickable = data.lanes.some((lane) => lane.steps.some((step) => step.more?.trim()));
  const labelled = data.lanes.some((lane) => lane.label);
  const branches = data.lanes.map((_, at) => branchOf(data, at));
  // As many columns as the longest lane, so every step has a cell of its
  // own; a branch starts after its step and moves left if it would run past
  // the end.
  const columns = Math.max(1, ...data.lanes.map((lane) => lane.steps.length));
  const startOf = (at: number) => {
    const from = branches[at] ?? 0;
    const steps = data.lanes[at]?.steps.length ?? 1;
    return from ? Math.max(1, Math.min(from + 1, columns - steps + 1)) : 1;
  };
  // Measured, since the layout depends on the width the tile is given.
  // Before the first measure the flow is the list going down.
  const [fit, setFit] = useState({ across: false, labels: false });
  useLayoutEffect(() => {
    const el = root.current;
    if (!el) return;
    const measure = () => {
      const rem =
        el.clientWidth /
        (Number.parseFloat(getComputedStyle(document.documentElement).fontSize) || 16);
      const next = flowFit(columns, labelled, rem);
      setFit((now) => (now.across === next.across && now.labels === next.labels ? now : next));
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [columns, labelled, root]);
  const { grid: measured, wires, unlinked } = useWires(data, root, fit);
  const grid = (
    <div
      ref={root}
      className="flow"
      data-drawn={measured ? "" : undefined}
      data-across={fit.across || undefined}
      data-labels={fit.labels || undefined}
      style={{ "--flow-columns": columns } as CSSProperties}
    >
      {data.lanes.map((lane, at) => {
        const from = branches[at] ?? 0;
        const after = from ? data.lanes[0]?.steps[from - 1]?.title : undefined;
        return (
          <div
            key={at}
            data-lane={at}
            data-branch={from || undefined}
            className="flow-lane"
            style={{ "--flow-row": at + 1, "--flow-start": startOf(at) } as CSSProperties}
          >
            {labelled || (after && unlinked.includes(at)) ? (
              <span className={cn("flow-label", BLOCK_LABEL)}>
                <InlineMarkdown>{lane.label ?? ""}</InlineMarkdown>
                {after && unlinked.includes(at) ? (
                  // The grid draws no elbow for this branch: say where it
                  // starts, as the list does.
                  <span className="tracking-normal normal-case">
                    {lane.label ? " · " : ""}after <InlineMarkdown>{after}</InlineMarkdown>
                  </span>
                ) : null}
              </span>
            ) : null}
            {after ? (
              <p className="flow-after text-xs text-tertiary">
                After <InlineMarkdown>{after}</InlineMarkdown>
              </p>
            ) : null}
            <ol aria-label={lane.label || undefined} className="flow-steps">
              {lane.steps.map((step, position) => (
                <li
                  key={position}
                  data-step={`${at}:${position}`}
                  data-faded={(focused && toneOf(step.tone) !== "new") || undefined}
                  className={cn(
                    "flow-step transition-opacity",
                    focused && toneOf(step.tone) !== "new" && "opacity-35",
                  )}
                  style={
                    {
                      "--flow-column": startOf(at) + position,
                    } as CSSProperties
                  }
                >
                  {position > 0 ? <Join join={step.join} /> : from ? <Join join="branch" /> : null}
                  <Box
                    title={step.title}
                    note={step.note}
                    tone={toneOf(step.tone) === "new" ? "plain" : toneOf(step.tone)}
                    change={toneOf(step.tone) === "new" ? "added" : null}
                    {...(step.more?.trim()
                      ? {
                          pressed: picked?.[0] === at && picked[1] === position,
                          onPress: () =>
                            setPicked((now) =>
                              now?.[0] === at && now[1] === position ? null : [at, position],
                            ),
                          onPeek: (on: boolean) =>
                            setPeeked((now) =>
                              on
                                ? [at, position]
                                : now?.[0] === at && now[1] === position
                                  ? null
                                  : now,
                            ),
                        }
                      : {})}
                  />
                </li>
              ))}
            </ol>
          </div>
        );
      })}
      {wires.length ? (
        <svg
          aria-hidden
          className={cn("flow-wires text-tertiary transition-opacity", focused && "opacity-35")}
        >
          {wires.map((wire, at) => (
            <g key={at}>
              <path d={wire.d} strokeDasharray={wire.kind === "apart" ? "3 4" : undefined} />
              {wire.heads.map((each, position) => (
                <path key={position} d={each} />
              ))}
            </g>
          ))}
        </svg>
      ) : null}
    </div>
  );
  const legend = <FlowKey data={data} wires={wires} focused={focused} onFocus={setFocused} />;
  if (!clickable)
    return (
      <div className="grid min-w-0 gap-2.5">
        {grid}
        {legend}
      </div>
    );
  return (
    <div className="grid min-w-0 gap-2.5">
      {grid}
      {legend}
      <div
        aria-live={peeked ? "off" : "polite"}
        data-flow-more=""
        className="max-w-[72ch] space-y-1.5 text-base text-secondary"
      >
        {opened?.more ? (
          <div className="space-y-1.5 border-t border-subtle pt-2.5">
            <InlineMarkdown className="block font-semibold text-primary">
              {opened.title}
            </InlineMarkdown>
            <InlineMarkdown className="block">{opened.more}</InlineMarkdown>
          </div>
        ) : (
          <p className="text-xs text-tertiary">Click a step for its detail, or hover over it.</p>
        )}
      </div>
    </div>
  );
}

const isItem = (item: unknown): item is ColumnItem => isRecord(item) && isString(item.title);

export function validColumns(data: unknown): data is ColumnsData {
  if (!isRecord(data) || !Array.isArray(data.columns) || data.columns.length === 0) return false;
  const labelled = data.columns.every((column) => isRecord(column) && isString(column.label));
  if (data.rows !== undefined)
    return (
      labelled &&
      Array.isArray(data.rows) &&
      data.rows.length > 0 &&
      data.rows.every(
        (row) => Array.isArray(row) && row.every((item) => item === null || isItem(item)),
      )
    );
  return (
    labelled &&
    data.columns.every(
      (column) => Array.isArray(column.items) && (column.items as unknown[]).every(isItem),
    )
  );
}

/** An item's box in a column by the column's tone. The label and the fill
 *  carry the tone, so every title stays in the primary ink, and a plain
 *  column is tinted like the others rather than outlined. */
const COLUMN_BOX: Record<Tone, string> = {
  ...BOX,
  plain: "border-transparent bg-surface-wash",
  muted: "border-subtle bg-transparent",
};

/** An item drawn quieter for what it shares with another column. */
const SAME_BOX = "border-dashed border-default bg-transparent";

/** Text as columns compare it: the same words, whatever their case and
 *  spacing, are the same. */
const sameKey = (text: string) => text.trim().replace(/\s+/g, " ").toLowerCase();

/** The titles more than one column lists: what the columns have in common,
 *  so the items around them are what tells the columns apart. */
export function sharedTitles(data: ColumnsData): Set<string> {
  const seen = new Map<string, number>();
  for (const column of data.columns)
    for (const key of new Set((column.items ?? []).map((item) => sameKey(item.title))))
      seen.set(key, (seen.get(key) ?? 0) + 1);
  return new Set([...seen].filter(([, columns]) => columns > 1).map(([key]) => key));
}

/** Whether an arrow goes from one column to the next: from a muted column
 *  (how it was, or what is left alone) to a new or good one (what it is
 *  now). */
const leadsTo = (from: Tone, to: Tone) => from === "muted" && (to === "new" || to === "good");

export function ColumnsDiagram({ data }: { data: ColumnsData }) {
  return data.rows ? <ComparedColumns data={data} rows={data.rows} /> : <ColumnLists data={data} />;
}

/** A column's label over its items, with how many it has. */
function ColumnLabel({ label, tone, count }: { label: string; tone: Tone; count: number }) {
  return (
    <h4
      className={cn(
        BLOCK_LABEL,
        LABEL_INK[tone],
        "flex items-baseline gap-1.5 [overflow-wrap:anywhere]",
      )}
    >
      <InlineMarkdown className="min-w-0">{label}</InlineMarkdown>
      <Badge size="compact" className="tracking-normal tabular-nums normal-case">
        {count}
        <span className="sr-only"> {count === 1 ? "item" : "items"}</span>
      </Badge>
    </h4>
  );
}

/** The classes that switch at the container width columns sit side by
 *  side from: two columns from @md, three from @3xl. Spelled out, so the
 *  stylesheet has them. */
const WIDE = {
  "@md": {
    hidden: "@md:hidden",
    srOnly: "@md:sr-only",
    inline: "@md:inline",
    grid: "@md:grid",
    block: "@md:block",
    stacked: "@max-md:not-last:border-b @max-md:not-last:pb-3",
    across: "@md:[grid-template-columns:var(--columns-across)]",
  },
  "@3xl": {
    hidden: "@3xl:hidden",
    srOnly: "@3xl:sr-only",
    inline: "@3xl:inline",
    grid: "@3xl:grid",
    block: "@3xl:block",
    stacked: "@max-3xl:not-last:border-b @max-3xl:not-last:pb-3",
    across: "@3xl:[grid-template-columns:var(--columns-across)]",
  },
} as const;
type Wide = keyof typeof WIDE;

/** The arrow from one column to the next: across while they sit side by
 *  side (`wide`, the container class they do from), down while they stack. */
function ColumnArrow({ wide, className }: { wide: Wide; className?: string }) {
  return (
    <span
      aria-hidden
      className={cn(
        "justify-self-center self-center text-lg leading-none text-tertiary",
        className,
      )}
    >
      <span className={WIDE[wide].hidden}>↓</span>
      <span className={cn("hidden", WIDE[wide].inline)}>→</span>
    </span>
  );
}

/** Lists of different things side by side, an item another column lists
 *  too drawn quieter. */
function ColumnLists({ data }: { data: ColumnsData }) {
  const tones = data.columns.map((column) => toneOf(column.tone));
  const arrows = tones.map((tone, at) => at + 1 < tones.length && leadsTo(tone, tones[at + 1]!));
  const shared = sharedTitles(data);
  // With arrows the columns sit side by side only once all of them fit, and
  // stack with the arrows pointing down before then.
  const many = data.columns.length > 2;
  const across = data.columns
    .map((_, at) => (arrows[at] ? "minmax(0,1fr) auto" : "minmax(0,1fr)"))
    .join(" ");
  return (
    <div className="grid min-w-0 gap-3">
      <div
        style={{ "--columns-across": across } as CSSProperties}
        className={cn(
          "grid min-w-0 gap-4",
          !arrows.some(Boolean)
            ? cn("@md:grid-cols-2", many && "@3xl:grid-cols-3")
            : many
              ? "@3xl:gap-x-2 @3xl:[grid-template-columns:var(--columns-across)]"
              : "@md:gap-x-2 @md:[grid-template-columns:var(--columns-across)]",
        )}
      >
        {data.columns.flatMap((column, at) => {
          const tone = tones[at]!;
          const items = column.items ?? [];
          const section = (
            <section
              key={at}
              aria-label={column.label}
              className="grid min-w-0 content-start gap-1.5"
            >
              <ColumnLabel label={column.label} tone={tone} count={items.length} />
              <ul className="grid gap-1.5">
                {items.map((item, position) => {
                  const same = shared.has(sameKey(item.title));
                  return (
                    <li key={position} className="min-w-0" data-same={same || undefined}>
                      <Box
                        title={item.title}
                        note={item.note}
                        tone={tone}
                        ink={same ? "text-secondary" : "text-primary"}
                        className={cn(COLUMN_BOX[tone], "opacity-100", same && SAME_BOX)}
                      />
                    </li>
                  );
                })}
              </ul>
            </section>
          );
          return arrows[at]
            ? [section, <ColumnArrow key={`${at}-to`} wide={many ? "@3xl" : "@md"} />]
            : [section];
        })}
      </div>
      {shared.size > 0 ? (
        <p className="flex items-center gap-1.5 border-t border-subtle pt-2.5 text-xs text-tertiary">
          <span aria-hidden className={cn("h-3 w-4 rounded-sm border", SAME_BOX)} />
          also in another column
        </p>
      ) : null}
    </div>
  );
}

/** A run of text in a compared item: a word, a code span or the space
 *  between, marked when the item before or after in its row lacks it. */
type Piece = { tokens: string[]; mark?: "added" | "removed" };

const PIECES = /`[^`]*`|\s+|[^\s`]+|`/g;

/** Two texts split into pieces, with the words one has and the other lacks
 *  marked (by their longest run in common); unmarked when they share no
 *  word, where marking every word would say nothing. */
export function wordDiff(was: string, now: string): { was: Piece[]; now: Piece[] } {
  const split = (text: string) => text.match(PIECES) ?? [];
  const [a, b] = [split(was), split(now)];
  const words = (pieces: string[]) =>
    pieces.flatMap((piece, at) => (/^\s+$/.test(piece) ? [] : [at]));
  const [wa, wb] = [words(a), words(b)];
  const key = (piece: string) => piece.toLowerCase();
  // The longest common subsequence of the words, by dynamic programming.
  const table = Array.from({ length: wa.length + 1 }, () =>
    Array.from({ length: wb.length + 1 }, () => 0),
  );
  for (let i = wa.length - 1; i >= 0; i--)
    for (let j = wb.length - 1; j >= 0; j--)
      table[i]![j] =
        key(a[wa[i]!]!) === key(b[wb[j]!]!)
          ? table[i + 1]![j + 1]! + 1
          : Math.max(table[i + 1]![j]!, table[i]![j + 1]!);
  const keptA = new Set<number>();
  const keptB = new Set<number>();
  for (let i = 0, j = 0; i < wa.length && j < wb.length; ) {
    if (key(a[wa[i]!]!) === key(b[wb[j]!]!)) {
      keptA.add(wa[i]!);
      keptB.add(wb[j]!);
      i++;
      j++;
    } else if (table[i + 1]![j]! >= table[i]![j + 1]!) i++;
    else j++;
  }
  // Mark only texts mostly alike: where under half the words of the shorter
  // are shared, the item was rewritten and marking every word says nothing.
  const common = keptA.size > 0 && keptA.size * 2 >= Math.min(wa.length, wb.length);
  const mark = (pieces: string[], kept: Set<number>, as: Piece["mark"]): Piece[] => {
    const marked = (at: number) =>
      common && at >= 0 && at < pieces.length && !kept.has(at) && !/^\s+$/.test(pieces[at]!);
    const out: Piece[] = [];
    for (const [at, text] of pieces.entries()) {
      // A run of marked words, with the spaces between them, is one mark.
      const inRun = marked(at) || (marked(at - 1) && marked(at + 1));
      const last = out.at(-1);
      if (last && Boolean(last.mark) === inRun) last.tokens.push(text);
      else out.push(inRun ? { tokens: [text], mark: as } : { tokens: [text] });
    }
    return out;
  };
  return { was: mark(a, keptA, "removed"), now: mark(b, keptB, "added") };
}

/** A word or code span of a compared item, code as code. */
function Token({ text }: { text: string }) {
  if (text.length > 1 && text.startsWith("`") && text.endsWith("`"))
    return <code className="rounded-sm bg-code-fill px-1 py-0.5">{text.slice(1, -1)}</code>;
  return text;
}

/** A compared item's text as the page draws it: plain, with code spans.
 *  The host has taken out links and bold; this takes out italics: a `*` or
 *  `_` pair around words outside code that opens after a space or the start
 *  and closes before a space, punctuation or the end, around text that starts
 *  and ends with a letter or digit. Identifiers, paths and
 *  globs (`__init__`, `snake_case`, `*.go,*.ts`) stay as written. */
export function comparedText(text: string): string {
  return text
    .split(/(`[^`]*`)/)
    .map((part, at) =>
      at % 2
        ? part
        : part.replace(/(^|[\s("'])([*_])([^\W_](?:[^*_]*[^\W_])?)\2(?=$|[\s.,;:!?)"'])/g, "$1$3"),
    )
    .join("");
}

/** A compared item's pieces, the words it changed highlighted: `del` for
 *  what the item before had, `ins` for what this one adds. */
function Pieces({ pieces }: { pieces: Piece[] }) {
  return pieces.map((piece, at) => {
    const body = piece.tokens.map((text, position) => <Token key={position} text={text} />);
    if (!piece.mark) return <span key={at}>{body}</span>;
    const Mark = piece.mark === "added" ? "ins" : "del";
    return (
      <Mark
        key={at}
        data-mark={piece.mark}
        className={cn(
          "rounded-sm px-0.5",
          // Removed words keep del's strike, so colour is not the only cue.
          piece.mark === "added" && "no-underline",
          piece.mark === "added" ? "bg-status-positive-bg" : "bg-status-critical-bg",
        )}
      >
        {body}
      </Mark>
    );
  });
}

/** What a compared row's item is against the one before it in the row. */
type Compared = "same" | Change | null;

/** Each item of a row against the one before it: `added` where the column
 *  before has none, `removed` (on the earlier item) where the next has none,
 *  `changed` where the words differ, `same` where they don't. */
function compareRow(row: (ColumnItem | null)[]): Compared[] {
  const out: Compared[] = row.map(() => null);
  const text = (item: ColumnItem) =>
    `${sameKey(comparedText(item.title))}\n${sameKey(comparedText(item.note ?? ""))}`;
  for (let at = 1; at < row.length; at++) {
    const [before, item] = [row[at - 1], row[at]];
    if (before && item) {
      out[at] = text(before) === text(item) ? "same" : "changed";
      if (at === 1) out[0] = "same";
    } else if (item) out[at] = "added";
    else if (before && out[at - 1] !== "added" && out[at - 1] !== "changed")
      out[at - 1] = "removed";
  }
  return out;
}

/** States of one thing compared item by item: each row lines an item up
 *  with itself in the next column, the words that differ marked, and items
 *  the same in every column drawn quieter (or, with "Show only changes",
 *  left out). */
function ComparedColumns({ data, rows }: { data: ColumnsData; rows: (ColumnItem | null)[][] }) {
  const [focused, setFocused] = useState(false);
  const count = data.columns.length;
  const tones = data.columns.map((column) => toneOf(column.tone));
  const many = count > 2;
  const wide: Wide = many ? "@3xl" : "@md";
  const across = data.columns.map(() => "minmax(0,1fr)").join(" auto ");
  const plain = (text: string): Piece[] => [{ tokens: text.match(PIECES) ?? [] }];
  // The words each item lacks or adds against its neighbours in the row.
  const piecesOf = (
    cells: (ColumnItem | null)[],
    marks: Compared[],
    at: number,
    field: (item: ColumnItem) => string | undefined,
  ): Piece[] => {
    const [before, item, after] = [cells[at - 1], cells[at], cells[at + 1]];
    const read = (cell: ColumnItem | null | undefined) => {
      const text = cell && field(cell);
      return text ? comparedText(text) : "";
    };
    const text = read(item);
    if (!text) return [];
    const was = read(before);
    if (was && marks[at] === "changed") return wordDiff(was, text).now;
    const now = read(after);
    if (now && marks[at + 1] === "changed") return wordDiff(text, now).was;
    return plain(text);
  };
  const compared = rows.map((row) => {
    const cells = data.columns.map((_, at) => row[at] ?? null);
    const marks = compareRow(cells);
    const pieces = cells.map((_, at) => ({
      title: piecesOf(cells, marks, at, (item) => item.title),
      note: piecesOf(cells, marks, at, (item) => item.note),
    }));
    return { cells, marks, pieces, same: marks.every((mark) => mark === "same") };
  });
  const marked = compared.some((row) =>
    row.pieces.some((cell) => [...cell.title, ...cell.note].some((piece) => piece.mark)),
  );
  const someSame = compared.some((row) => row.same);
  const toggle = someSame && compared.some((row) => !row.same);
  const shown = compared.filter((row) => !(focused && row.same));
  const template = cn("grid min-w-0 gap-x-2 gap-y-1.5", WIDE[wide].across);
  const legend = [
    marked ? (
      <li key="words" className="inline-flex items-center gap-1.5">
        <span aria-hidden className="rounded-sm bg-status-critical-bg px-1 text-secondary">
          was
        </span>
        <span aria-hidden className="rounded-sm bg-status-positive-bg px-1 text-secondary">
          now
        </span>
        words that differ
      </li>
    ) : null,
    someSame ? (
      <li key="same" className="inline-flex items-center gap-1.5">
        <span aria-hidden className={cn("h-3 w-4 rounded-sm border", SAME_BOX)} />
        the same in each column
      </li>
    ) : null,
  ].filter(Boolean);
  return (
    <div className="grid min-w-0 gap-3" style={{ "--columns-across": across } as CSSProperties}>
      <div aria-hidden className={cn(template, "hidden", WIDE[wide].grid)}>
        {data.columns.flatMap((column, at) => [
          ...(at > 0 ? [<span key={`${at}-to`} className="w-5" />] : []),
          <ColumnLabel
            key={at}
            label={column.label}
            tone={tones[at]!}
            count={rows.filter((row) => row[at]).length}
          />,
        ])}
      </div>
      <ol className="grid min-w-0 gap-3">
        {shown.map(({ cells, marks, pieces, same }, position) => (
          <li
            key={position}
            data-same={same || undefined}
            className={cn(template, "border-subtle", WIDE[wide].stacked)}
          >
            {cells.flatMap((item, at) => {
              // Changed says something only beside rows that stayed the same;
              // where every row differs, the highlighted words say it.
              const change =
                marks[at] === "same" || (marks[at] === "changed" && !someSame) ? null : marks[at];
              const cell = (
                <div
                  key={at}
                  // A column with no item here keeps its place side by side
                  // and is left out while the columns stack.
                  className={cn(
                    "grid min-w-0 content-start gap-1",
                    !item && cn("hidden", WIDE[wide].block),
                  )}
                >
                  {/* The column's name: shown while stacked, read out side by side. */}
                  {item ? (
                    <span className={cn(BLOCK_LABEL, LABEL_INK[tones[at]!], WIDE[wide].srOnly)}>
                      <InlineMarkdown>{data.columns[at]!.label}</InlineMarkdown>
                    </span>
                  ) : null}
                  {item ? (
                    <div
                      data-change={change ?? undefined}
                      className={cn(
                        "relative grid min-w-0 content-start gap-0.5 rounded-md border px-3 py-2 [overflow-wrap:anywhere]",
                        same ? SAME_BOX : "border-subtle bg-surface-card",
                      )}
                    >
                      {change ? (
                        <span className="mb-1 justify-self-start">
                          <ChangePill change={change} />
                        </span>
                      ) : null}
                      <span
                        className={cn(
                          "font-medium leading-snug",
                          same ? "text-secondary" : "text-primary",
                        )}
                      >
                        <Pieces pieces={pieces[at]!.title} />
                      </span>
                      {pieces[at]!.note.length > 0 ? (
                        <span className="text-xs leading-snug text-secondary">
                          <Pieces pieces={pieces[at]!.note} />
                        </span>
                      ) : null}
                    </div>
                  ) : null}
                </div>
              );
              if (at === 0) return [cell];
              // An arrow only between two items; beside a gap, its room.
              return [
                cells[at - 1] && item ? (
                  <ColumnArrow key={`${at}-to`} wide={wide} className="w-5" />
                ) : (
                  <span
                    key={`${at}-to`}
                    aria-hidden
                    className={cn("hidden w-5", WIDE[wide].block)}
                  />
                ),
                cell,
              ];
            })}
          </li>
        ))}
      </ol>
      {legend.length > 0 || toggle ? (
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2 border-t border-subtle pt-2.5">
          <ul
            aria-label="Legend"
            className="flex flex-wrap items-center gap-x-4 gap-y-1.5 text-xs text-tertiary"
          >
            {legend}
          </ul>
          {toggle ? (
            <Toggle checked={focused} onChange={setFocused} className="ml-auto">
              Show only changes
            </Toggle>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

export function validSequence(data: unknown): data is SequenceData {
  if (!isRecord(data) || !Array.isArray(data.actors) || !Array.isArray(data.steps)) return false;
  const actors = data.actors.length;
  return (
    actors >= 2 &&
    data.actors.every(isString) &&
    data.steps.every(
      (step) =>
        isRecord(step) &&
        isString(step.text) &&
        (step.was === undefined || isString(step.was)) &&
        Number.isInteger(step.from) &&
        Number.isInteger(step.to) &&
        (step.from as number) >= 0 &&
        (step.from as number) < actors &&
        (step.to as number) >= 0 &&
        (step.to as number) < actors,
    )
  );
}

/** A message's line and mark colour by tone. */
const STROKE: Record<Tone, string> = {
  plain: "text-(--border-strong)",
  new: "text-status-info",
  good: "text-status-positive",
  bad: "text-status-critical",
  muted: "text-(--border-strong) opacity-60",
  missing: "text-status-critical opacity-60",
};

/** A message's number, in a small circle before its text. */
function StepNumber({ at, tone }: { at: number; tone: Tone }) {
  return (
    <span
      aria-hidden
      className={cn(
        "inline-flex h-5 min-w-5 shrink-0 items-center justify-center rounded-full border px-1 text-xs leading-none font-semibold tabular-nums",
        tone === "plain" || tone === "muted"
          ? "border-default bg-surface-wash text-secondary"
          : cn("border-current", STROKE[tone]),
      )}
    >
      {at + 1}
    </span>
  );
}

/** The end of a message's line: an arrowhead, or a cross for a message that
 *  is refused (`bad`). */
function Head({ refused, flip }: { refused: boolean; flip: boolean }) {
  return (
    <svg
      aria-hidden
      viewBox="0 0 10 10"
      className={cn("size-2.5 shrink-0 overflow-visible", flip && "-scale-x-100")}
    >
      {refused ? (
        <path d="M2 1 L9 9 M9 1 L2 9" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
      ) : (
        <path d="M0 0 L10 5 L0 10 Z" fill="currentColor" />
      )}
    </svg>
  );
}

type SequenceStep = SequenceData["steps"][number];
type Change = "added" | "changed" | "removed";

/** What the pull request does to a message: its `change`, or `added` for
 *  one an earlier review marked with the `new` tone. */
const changeOf = (step: SequenceStep): Change | null =>
  step.change === "added" || step.change === "changed"
    ? step.change
    : step.tone === "new"
      ? "added"
      : null;

/** A changed message's pill, and the bar at its row's left edge, by change. */
const CHANGE: Record<Change, { bar: string; pill: string; label: string }> = {
  added: { bar: "bg-status-info", pill: "bg-status-info text-inverse", label: "New" },
  changed: { bar: "bg-status-caution", pill: "bg-status-caution text-inverse", label: "Changed" },
  removed: { bar: "bg-status-critical", pill: "bg-status-critical text-inverse", label: "Removed" },
};

/** The bar at a changed message's left edge. */
function ChangeBar({ change }: { change: Change | null }) {
  return change ? (
    <span
      aria-hidden
      className={cn("absolute inset-y-1 left-0 w-1 rounded-full", CHANGE[change].bar)}
    />
  ) : null;
}

function ChangePill({ change }: { change: Change }) {
  return (
    <span
      className={cn(
        "inline-flex h-5 shrink-0 items-center rounded-full px-1.5 text-xs leading-none font-semibold",
        CHANGE[change].pill,
      )}
    >
      {CHANGE[change].label}
    </span>
  );
}

/** A message's number, its change, its text and, when it changed, what it
 *  said before, struck through. */
function MessageLabel({ step, at }: { step: SequenceStep; at: number }) {
  const tone = toneOf(step.tone);
  const change = changeOf(step);
  return (
    <span className={cn("flex min-w-0 items-start gap-1.5 text-base leading-snug", INK[tone])}>
      <StepNumber at={at} tone={tone} />
      {change ? <ChangePill change={change} /> : null}
      <span className="min-w-0">
        <InlineMarkdown className="block">{step.text}</InlineMarkdown>
        {change === "changed" && step.was ? (
          <del className="block text-tertiary">
            <InlineMarkdown>{step.was}</InlineMarkdown>
          </del>
        ) : null}
      </span>
    </span>
  );
}

/** One message: its label above a line from the sender's lifeline to the
 *  receiver's, dashed for a reply. A message an actor sends itself is a
 *  loop on its lifeline with the label beside it, on the side with room.
 *  The columns have no gap, so a lifeline is a column's middle. */
function Message({ step, at, names }: { step: SequenceStep; at: number; names: string[] }) {
  const actors = names.length;
  const tone = toneOf(step.tone);
  // What the lines and marks show, for a screen reader.
  const said = [
    step.from === step.to
      ? names[step.from]
      : `${names[step.from] ?? ""} to ${names[step.to] ?? ""}`,
    step.reply && "reply",
    tone === "bad" && step.from !== step.to && "refused",
  ]
    .filter(Boolean)
    .join(", ");
  const label = (
    <>
      <span className="sr-only">{said}: </span>
      <MessageLabel step={step} at={at} />
    </>
  );
  if (step.from === step.to) {
    const middle = ((step.from + 0.5) / actors) * 100;
    const rightward = step.from < actors - 1;
    return (
      <div data-tone={tone} data-self="" className="relative col-span-full py-1">
        <span
          aria-hidden
          style={rightward ? { left: `${middle}%` } : { right: `${100 - middle}%` }}
          className={cn(
            "absolute top-1/2 h-5 w-5 -translate-y-1/2 border-2 border-current",
            step.reply && "border-dashed",
            rightward ? "rounded-r-md border-l-0" : "rounded-l-md border-r-0",
            STROKE[tone],
          )}
        />
        {/* Beside the loop, as wide as the room before the next lifeline. */}
        <div
          style={{
            width: `calc(${100 / actors}% - 2rem)`,
            ...(rightward
              ? { marginLeft: `calc(${middle}% + 1.75rem)` }
              : { marginLeft: "auto", marginRight: `calc(${100 - middle}% + 1.75rem)` }),
          }}
          className={cn("flex min-h-7 items-center", !rightward && "justify-end")}
        >
          {label}
        </div>
      </div>
    );
  }
  const low = Math.min(step.from, step.to);
  const span = Math.abs(step.to - step.from) + 1;
  const inset = `${50 / span}%`;
  const leftward = step.to < step.from;
  const refused = tone === "bad";
  return (
    <div
      style={{ gridColumn: `${low + 1} / span ${span}`, marginLeft: inset, marginRight: inset }}
      className="grid min-w-0 gap-1 py-0.5"
      data-tone={tone}
      data-reply={step.reply ? "" : undefined}
    >
      <div className="flex justify-center px-2">{label}</div>
      <span aria-hidden className={cn("flex items-center", STROKE[tone])}>
        {leftward ? <Head refused={refused} flip /> : null}
        <span
          className={cn(
            "flex-1 border-t-2 border-current",
            step.reply && "border-dashed",
            leftward ? "-ml-px" : "-mr-px",
          )}
        />
        {leftward ? null : <Head refused={refused} flip={false} />}
      </span>
    </div>
  );
}

/** What a message's colour means, for the tones a legend explains (`new`
 *  reads as added, `plain` needs no words). */
const TONE_MEANS: Partial<Record<Tone, string>> = {
  good: "works or is allowed",
  bad: "fails or is refused",
  muted: "outside this change",
  missing: "doesn't exist yet",
};

/** A short sample of a message's line in the legend. */
function LineSample({ tone, reply = false }: { tone: Tone; reply?: boolean }) {
  return (
    <span aria-hidden className={cn("flex w-6 items-center", STROKE[tone])}>
      <span className={cn("-mr-px flex-1 border-t-2 border-current", reply && "border-dashed")} />
      <Head refused={tone === "bad"} flip={false} />
    </span>
  );
}

/** A legend swatch's edge by tone, so a pale fill still shows. */
const SWATCH: Partial<Record<Tone, string>> = {
  good: "border-status-positive",
  bad: "border-status-critical",
  muted: "border-default",
};

/** Under a flow, a legend of the marks it uses, only those: the tones'
 *  boxes, a branch and a separate path, in the sequence's style. */
function FlowKey({
  data,
  wires,
  focused,
  onFocus,
}: {
  data: FlowData;
  wires: Wire[];
  focused: boolean;
  onFocus: (on: boolean) => void;
}) {
  const tones = new Set(data.lanes.flatMap((lane) => lane.steps.map((step) => toneOf(step.tone))));
  const toggle = tones.has("new") && [...tones].some((tone) => tone !== "new");
  // Only what is drawn: the list a narrow flow becomes says these in words.
  const branch = wires.some((wire) => wire.kind === "branch");
  const apart = wires.some((wire) => wire.kind === "apart");
  const entry = (key: string, sample: ReactNode, text: string) => (
    <li key={key} className="inline-flex items-center gap-1.5">
      {sample}
      {text}
    </li>
  );
  const entries = [
    ...(["good", "bad", "muted", "missing"] as const).map((tone) =>
      tones.has(tone)
        ? entry(
            tone,
            <span
              aria-hidden
              className={cn("h-3 w-4 rounded-sm border", BOX[tone], SWATCH[tone])}
            />,
            TONE_MEANS[tone] ?? "",
          )
        : null,
    ),
    branch
      ? entry(
          "branch",
          <span aria-hidden className="h-2.5 w-3 rounded-bl-sm border-b border-l border-current" />,
          "another path from a step",
        )
      : null,
    apart
      ? entry(
          "apart",
          <span aria-hidden className="w-5 border-t border-dashed border-current" />,
          "a separate path",
        )
      : null,
  ].filter(Boolean);
  if (entries.length === 0 && !toggle) return null;
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 border-t border-subtle pt-2.5">
      <ul
        aria-label="Legend"
        className="flex flex-wrap items-center gap-x-4 gap-y-1.5 text-xs text-tertiary"
      >
        {entries}
      </ul>
      {toggle ? (
        <Toggle checked={focused} onChange={onFocus} className="ml-auto">
          Show only changes
        </Toggle>
      ) : null}
    </div>
  );
}

/** Under a sequence, a legend of the marks it uses, only those, and, when
 *  some messages changed and some did not, a switch that fades the rest. */
function SequenceKey({
  steps,
  focused,
  onFocus,
}: {
  steps: SequenceStep[];
  focused: boolean;
  onFocus: (on: boolean) => void;
}) {
  const changes = new Set(steps.map(changeOf));
  const tones = new Set(steps.map((step) => toneOf(step.tone)));
  const reply = steps.some((step) => step.reply);
  const self = steps.some((step) => step.from === step.to);
  const toggle = changes.has(null) && changes.size > 1;
  const entry = (key: string, sample: ReactNode, text: string) => (
    <li key={key} className="inline-flex items-center gap-1.5">
      {sample}
      {text}
    </li>
  );
  const entries = [
    ...(["good", "bad", "muted", "missing"] as const).map((tone) =>
      tones.has(tone) ? entry(tone, <LineSample tone={tone} />, TONE_MEANS[tone] ?? "") : null,
    ),
    reply ? entry("reply", <LineSample tone="plain" reply />, "a reply") : null,
    self
      ? entry(
          "self",
          <span
            aria-hidden
            className="h-3 w-2.5 rounded-r-sm border-2 border-l-0 border-current text-(--border-strong)"
          />,
          "a check within one actor",
        )
      : null,
  ].filter(Boolean);
  if (entries.length === 0 && !toggle) return null;
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 border-t border-subtle pt-2.5">
      <ul
        aria-label="Legend"
        className="flex flex-wrap items-center gap-x-4 gap-y-1.5 text-xs text-tertiary"
      >
        {entries}
      </ul>
      {toggle ? (
        <Toggle checked={focused} onChange={onFocus} className="ml-auto">
          Show only changes
        </Toggle>
      ) : null}
    </div>
  );
}

export function SequenceDiagram({ data }: { data: SequenceData }) {
  // Messages the pull request didn't touch are faded while this is on.
  const [focused, setFocused] = useState(false);
  const actors = data.actors.length;
  const columns = { gridTemplateColumns: `repeat(${actors}, minmax(0, 1fr))` };
  const faded = (step: SequenceStep) => focused && !changeOf(step);
  return (
    <div className="grid min-w-0 gap-3">
      {/* Wide: each actor a lifeline under its name, each message a line
          between two of them, in order and numbered; a changed message
          has a bar at its row's left edge. */}
      <div className="hidden min-w-0 @xl:block">
        <div style={columns} className="grid">
          {data.actors.map((actor, at) => (
            <div key={at} className="flex justify-center px-1">
              <InlineMarkdown className="max-w-full rounded-md border border-default bg-surface-card px-3 py-1 text-center font-semibold text-primary shadow-xs">
                {actor}
              </InlineMarkdown>
            </div>
          ))}
        </div>
        <div className="relative">
          <div aria-hidden style={columns} className="pointer-events-none absolute inset-0 grid">
            {data.actors.map((_, at) => (
              <span key={at} className="mx-auto w-px bg-(--border-default)" />
            ))}
          </div>
          <ol style={columns} className="relative grid gap-y-1.5 pt-3 pb-2">
            {data.steps.map((step, at) => {
              const change = changeOf(step);
              return (
                <li
                  key={at}
                  style={columns}
                  data-change={change ?? undefined}
                  className={cn(
                    "relative col-span-full grid py-1 transition-opacity",
                    faded(step) && "opacity-35",
                  )}
                  data-faded={faded(step) || undefined}
                >
                  <ChangeBar change={change} />
                  <Message step={step} at={at} names={data.actors} />
                </li>
              );
            })}
          </ol>
        </div>
      </div>
      {/* Narrow: the same messages as a numbered list, who to whom first. */}
      <ol className="grid gap-1.5 @xl:hidden">
        {data.steps.map((step, at) => {
          const tone = toneOf(step.tone);
          const change = changeOf(step);
          return (
            <li
              key={at}
              data-change={change ?? undefined}
              className={cn(
                "relative flex min-w-0 gap-2 rounded-md border px-3 py-2 text-base transition-opacity",
                BOX[tone],
                step.reply && "border-dashed",
                faded(step) && "opacity-35",
              )}
              data-faded={faded(step) || undefined}
            >
              <ChangeBar change={change} />
              <StepNumber at={at} tone={tone} />
              <span className="grid min-w-0 justify-items-start gap-0.5">
                <span className="font-medium text-primary">
                  <InlineMarkdown>
                    {step.from === step.to
                      ? (data.actors[step.from] ?? "")
                      : `${data.actors[step.from] ?? ""} → ${data.actors[step.to] ?? ""}`}
                  </InlineMarkdown>
                </span>
                {change ? <ChangePill change={change} /> : null}
                <InlineMarkdown className={cn("block leading-snug", INK[tone])}>
                  {step.text}
                </InlineMarkdown>
                {change === "changed" && step.was ? (
                  <del className="block text-tertiary">
                    <InlineMarkdown>{step.was}</InlineMarkdown>
                  </del>
                ) : null}
              </span>
            </li>
          );
        })}
      </ol>
      <SequenceKey steps={data.steps} focused={focused} onFocus={setFocused} />
    </div>
  );
}

const validCell = (cell: unknown): cell is TableCell =>
  isString(cell) ||
  (isRecord(cell) && isString(cell.text) && (cell.was === undefined || isString(cell.was)));

export function validTable(data: unknown): data is TableData {
  return (
    isRecord(data) &&
    Array.isArray(data.columns) &&
    data.columns.every(isString) &&
    Array.isArray(data.rows) &&
    data.rows.every((row) => Array.isArray(row) && row.every(validCell))
  );
}

type Row = { key: string; cells: TableCell[]; changed: boolean };

/** The rows a long table shows until it is opened: enough to read what it
 *  compares without it outweighing the section around it. */
const TABLE_ROWS_SHOWN = 8;

/** A table cell: its text, or what it is now (emphasised) after what it was
 *  before the change (struck through). */
function Cell({ cell }: { cell: TableCell }) {
  if (typeof cell === "string") return <InlineMarkdown>{cell}</InlineMarkdown>;
  if (!cell.was) return <InlineMarkdown>{cell.text}</InlineMarkdown>;
  return (
    <span data-changed="">
      <del className="mr-1.5 font-normal text-tertiary">
        <span className="sr-only">Was </span>
        <InlineMarkdown>{cell.was}</InlineMarkdown>
      </del>
      <strong className="font-semibold text-primary">
        <span className="sr-only">now </span>
        <InlineMarkdown>{cell.text}</InlineMarkdown>
      </strong>
    </span>
  );
}

/** A cell's text, whether or not it changed. */
const cellText = (cell: TableCell | undefined) =>
  cell === undefined ? "" : typeof cell === "string" ? cell : cell.text;

/** A short value read as a quantity: a number, with an optional unit
 *  ("65,535 bytes", "250 milliseconds", "1,000 req/s", "$2.00", "≤ 5%"). */
const QUANTITY = /^[~≈<>≤≥±+\-−]?\s?[$€£]?\d[\d,._]*(\s?[%A-Za-z/]{1,16})?$/;
const isQuantity = (text: string) => text.length <= 28 && QUANTITY.test(text);
/** A cell that stands for no value, which a number column may hold. */
const PLACEHOLDER = /^(?:[-–—−]|n\/?a|tbd|none)$/i;

/** How a column draws its cells: `label` (the first column, naming each
 *  row), `quantity` (every cell a number, right-aligned in tabular figures),
 *  `chips` (a few short values repeated down the column, each tinted with
 *  its own colour) or plain text. */
type ColumnKind = { kind: "label" | "quantity" | "text" } | { kind: "chips"; values: string[] };

/** How many tints a column has, given in order of each value's first row. */
const CHIP_TINTS = 4;

function kindOf(data: TableData, position: number): ColumnKind {
  if (position === 0 && data.columns.length > 1) return { kind: "label" };
  const texts = data.rows.map((row) => cellText(row[position]).trim()).filter(Boolean);
  if (texts.length === 0) return { kind: "text" };
  const numbers = texts.filter((text) => !PLACEHOLDER.test(text));
  if (numbers.length > 0 && numbers.every(isQuantity)) return { kind: "quantity" };
  const values = [...new Set(texts)];
  if (
    texts.length >= 4 &&
    values.length >= 2 &&
    values.length <= CHIP_TINTS &&
    values.length < texts.length &&
    values.every((value) => value.length <= 32)
  )
    return { kind: "chips", values };
  return { kind: "text" };
}

/** A repeated value, tinted so it reads apart from the column's other
 *  values. */
function Chip({ text, tint }: { text: string; tint: number }) {
  return (
    <span
      className="table-tint rounded-sm px-1 py-px box-decoration-clone text-primary"
      data-tint={tint}
    >
      <InlineMarkdown>{text}</InlineMarkdown>
    </span>
  );
}

const rowChanged = (cells: TableCell[]) =>
  cells.some((cell) => typeof cell !== "string" && Boolean(cell.was));

export function TableDiagram({ data }: { data: TableData }) {
  const [all, setAll] = useState(false);
  // Rows the pull request didn't change are faded while this is on.
  const [focused, setFocused] = useState(false);
  const kinds = data.columns.map((_, position) => kindOf(data, position));
  const changed = data.rows.map(rowChanged);
  const toggle = changed.some(Boolean) && changed.some((each) => !each);
  const columns: Column<Row>[] = data.columns.map((column, position) => {
    const kind = kinds[position] ?? { kind: "text" };
    return {
      key: `${position}-${column}`,
      header: <InlineMarkdown className="font-bold text-primary">{column}</InlineMarkdown>,
      align: kind.kind === "quantity" ? "right" : undefined,
      className: cn(
        kind.kind === "label" && "font-medium text-primary",
        kind.kind === "text" && "text-secondary",
      ),
      cell: (row) => {
        const cell = row.cells[position] ?? "";
        const text = cellText(cell).trim();
        const body =
          kind.kind === "chips" && text && (typeof cell === "string" || !cell.was) ? (
            <Chip text={text} tint={Math.max(0, kind.values.indexOf(text))} />
          ) : kind.kind === "quantity" ? (
            <span className="font-medium text-primary">
              <Cell cell={cell} />
            </span>
          ) : (
            <Cell cell={cell} />
          );
        if (position !== 0) return body;
        // A changed row has the sequence's Changed pill and a bar at its
        // left edge; while any row has, every label leaves room for the bar.
        return (
          <span className={cn("relative block", changed.some(Boolean) && "pl-3")}>
            {row.changed ? (
              <>
                <ChangeBar change="changed" />
                <span className="mb-1 block">
                  <ChangePill change="changed" />
                </span>
              </>
            ) : null}
            {body}
          </span>
        );
      },
    };
  });
  // Folding one or two rows saves nothing: a table folds only when it
  // hides at least three.
  const folds = data.rows.length > TABLE_ROWS_SHOWN + 2;
  const rows = (folds && !all ? data.rows.slice(0, TABLE_ROWS_SHOWN) : data.rows).map(
    (cells, position) => ({ key: String(position), cells, changed: changed[position] ?? false }),
  );
  return (
    <div className="space-y-2">
      <Table
        columns={columns}
        rows={rows}
        rowKey={(row) => row.key}
        rowClassName={(row) =>
          cn("transition-opacity", focused && !row.changed && "opacity-35") || undefined
        }
        density="compact"
        framed={false}
        minWidth={360}
      />
      {folds || toggle ? (
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          {folds ? (
            <button
              type="button"
              aria-expanded={all}
              onClick={() => setAll((shown) => !shown)}
              className="text-base text-link hover:underline"
            >
              {all ? "Show fewer rows" : `Show all ${data.rows.length} rows`}
            </button>
          ) : null}
          {toggle ? (
            <Toggle
              checked={focused}
              onChange={(on) => {
                setFocused(on);
                // A changed row may sit below the fold.
                if (on) setAll(true);
              }}
              className="ml-auto"
            >
              Show only changes
            </Toggle>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

/** The native tile types, by type, with the check of their data. */
export const NATIVE: Record<
  string,
  { valid: (data: unknown) => boolean; draw: (data: unknown) => ReactNode }
> = {
  flow: { valid: validFlow, draw: (data) => <FlowDiagram data={data as FlowData} /> },
  columns: { valid: validColumns, draw: (data) => <ColumnsDiagram data={data as ColumnsData} /> },
  sequence: {
    valid: validSequence,
    draw: (data) => <SequenceDiagram data={data as SequenceData} />,
  },
  table: { valid: validTable, draw: (data) => <TableDiagram data={data as TableData} /> },
};
