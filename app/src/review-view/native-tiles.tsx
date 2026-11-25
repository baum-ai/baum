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
import { Toggle } from "@baum/viewer/components/ui";
import type {
  ColumnsData,
  FlowData,
  FlowStep,
  SequenceData,
  TableCell,
  TableData,
  Tone,
} from "./model";
import { toneOf } from "./document";
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
}: {
  title: string;
  note?: string;
  tone: Tone;
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
      <InlineMarkdown className={cn("font-medium leading-snug", INK[tone])}>{title}</InlineMarkdown>
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

export function validColumns(data: unknown): data is ColumnsData {
  return (
    isRecord(data) &&
    Array.isArray(data.columns) &&
    data.columns.length > 0 &&
    data.columns.every(
      (column) =>
        isRecord(column) &&
        isString(column.label) &&
        Array.isArray(column.items) &&
        column.items.every((item) => isRecord(item) && isString(item.title)),
    )
  );
}

export function ColumnsDiagram({ data }: { data: ColumnsData }) {
  return (
    <div
      className={cn(
        "grid min-w-0 gap-4 @md:grid-cols-2",
        data.columns.length > 2 && "@3xl:grid-cols-3",
      )}
    >
      {data.columns.map((column, at) => {
        const tone = toneOf(column.tone);
        return (
          <section
            key={at}
            aria-label={column.label}
            className="grid min-w-0 content-start gap-1.5"
          >
            <h4 className={cn(BLOCK_LABEL, LABEL_INK[tone])}>
              <InlineMarkdown>{column.label}</InlineMarkdown>
            </h4>
            <ul className="grid gap-1.5">
              {column.items.map((item, position) => (
                <li key={position} className="min-w-0">
                  <Box title={item.title} note={item.note} tone={tone} />
                </li>
              ))}
            </ul>
          </section>
        );
      })}
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
type Change = "added" | "changed";

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
export const TONE_MEANS: Partial<Record<Tone, string>> = {
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
