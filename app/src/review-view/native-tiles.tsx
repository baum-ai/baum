import { type CSSProperties, type ReactNode, useState } from "react";
import "./flow.css";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import { InlineMarkdown } from "@baum/viewer/components/markdown";
import { type Column, Table } from "@baum/viewer/components/table";
import type {
  ColumnsData,
  FlowData,
  FlowStep,
  SequenceData,
  TableCell,
  TableData,
  Tone,
} from "./model";

/** The diagrams and tables the view call writes, drawn by the page itself:
 *  a flow of steps in lanes, columns of items, a sequence of messages
 *  between actors, and a table. Each is held in a container, so it lays
 *  itself out by the room it has: lanes wrap and then stack, columns stack,
 *  and a sequence becomes a list of its messages, never widening the page. */

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);
const isString = (value: unknown): value is string => typeof value === "string";

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

const LANE_LABEL = "text-xs font-medium tracking-[0.08em] text-tertiary uppercase";

/** One step, item or message: its title and an optional note under it. */
function Box({
  title,
  note,
  tone,
  className,
  pressed,
  onPress,
}: {
  title: string;
  note?: string;
  tone: Tone;
  className?: string;
  /** Set when the box opens something: it is then a button, `pressed` while
   *  what it opens is shown. */
  pressed?: boolean;
  onPress?: () => void;
}) {
  const Element = onPress ? "button" : "div";
  return (
    <Element
      {...(onPress
        ? { type: "button" as const, "aria-pressed": pressed ?? false, onClick: onPress }
        : {})}
      className={cn(
        "grid min-w-0 content-start gap-0.5 rounded-md border px-3 py-2 [overflow-wrap:anywhere]",
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
      <InlineMarkdown className={cn("text-sm font-medium leading-snug", INK[tone])}>
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

/** The container widths (rem) flow.css has rules for: from `wide`, the
 *  lanes are rows of one grid under their labels (about 8.5rem a column);
 *  from `wider`, the labels have a column of their own. Narrower, each lane
 *  is a list going down. */
const WIDTHS = [28, 32, 36, 40, 44, 48, 52, 56, 64, 72];
const widthFor = (rem: number) => WIDTHS.find((width) => width >= rem);

/** The step of the first lane a later lane branches off (from 1), or 0 for
 *  a lane of its own. */
const branchOf = (data: FlowData, at: number) => {
  const from = data.lanes[at]?.from;
  const first = data.lanes[0]?.steps.length ?? 0;
  return at > 0 && typeof from === "number" && from >= 1 && from <= first ? from : 0;
};

export function FlowDiagram({ data }: { data: FlowData }) {
  // The step whose detail shows under the flow, as [lane, step].
  const [picked, setPicked] = useState<[number, number] | null>(null);
  const opened = picked ? data.lanes[picked[0]]?.steps[picked[1]] : undefined;
  const clickable = data.lanes.some((lane) => lane.steps.some((step) => step.more?.trim()));
  const labelled = data.lanes.some((lane) => lane.label);
  const branches = data.lanes.map((_, at) => branchOf(data, at));
  // As many columns as the longest lane that is not a branch; a branch
  // starts after its step and moves left if it would run past the end.
  const columns = Math.max(
    1,
    ...data.lanes.map((lane, at) => (branches[at] ? 0 : lane.steps.length)),
  );
  const startOf = (at: number) => {
    const from = branches[at] ?? 0;
    const steps = data.lanes[at]?.steps.length ?? 1;
    return from ? Math.max(1, Math.min(from + 1, columns - steps + 1)) : 1;
  };
  const grid = (
    <div
      className="flow"
      data-wide={widthFor(columns * 8.5) ?? WIDTHS[WIDTHS.length - 1]}
      data-wider={labelled ? widthFor(columns * 9 + 6) : undefined}
      style={{ "--flow-columns": columns } as CSSProperties}
    >
      {data.lanes.map((lane, at) => {
        const from = branches[at] ?? 0;
        const after = from ? data.lanes[0]?.steps[from - 1]?.title : undefined;
        return (
          <div
            key={at}
            data-lane=""
            data-branch={from || undefined}
            className="flow-lane"
            style={{ "--flow-row": at + 1, "--flow-start": startOf(at) } as CSSProperties}
          >
            {labelled ? (
              <InlineMarkdown className={cn("flow-label", LANE_LABEL)}>
                {lane.label ?? ""}
              </InlineMarkdown>
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
                  className="flow-step"
                  style={
                    {
                      "--flow-column": Math.min(startOf(at) + position, columns),
                    } as CSSProperties
                  }
                >
                  {position > 0 ? <Join join={step.join} /> : from ? <Join join="branch" /> : null}
                  <Box
                    title={step.title}
                    note={step.note}
                    tone={toneOf(step.tone)}
                    {...(step.more?.trim()
                      ? {
                          pressed: picked?.[0] === at && picked[1] === position,
                          onPress: () =>
                            setPicked((now) =>
                              now?.[0] === at && now[1] === position ? null : [at, position],
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
    </div>
  );
  if (!clickable) return grid;
  return (
    <div className="grid min-w-0 gap-2.5">
      {grid}
      <div
        aria-live="polite"
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
          <p className="text-xs text-tertiary">Click a step for its detail.</p>
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
            <h4 className={cn(LANE_LABEL, LABEL_INK[tone])}>
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
        Number.isInteger(step.from) &&
        Number.isInteger(step.to) &&
        (step.from as number) >= 0 &&
        (step.from as number) < actors &&
        (step.to as number) >= 0 &&
        (step.to as number) < actors,
    )
  );
}

/** A message's arrow between two actors' columns, its text above it. The
 *  arrow runs between the middles of the two columns it spans. */
function Message({ step }: { step: SequenceData["steps"][number] }) {
  const tone = toneOf(step.tone);
  const low = Math.min(step.from, step.to);
  const high = Math.max(step.from, step.to);
  const span = high - low + 1;
  const inset = `${50 / span}%`;
  const style = { gridColumn: `${low + 1} / span ${span}` };
  if (step.from === step.to)
    return (
      <div style={style} className="flex justify-center px-1">
        <Box title={step.text} tone={tone} className="w-full max-w-64 text-center" />
      </div>
    );
  const leftward = step.to < step.from;
  return (
    <div
      style={{ ...style, marginLeft: inset, marginRight: inset }}
      className="grid min-w-0 gap-0.5 py-0.5"
      data-tone={tone}
    >
      <InlineMarkdown className={cn("px-2 text-center text-base leading-snug", INK[tone])}>
        {step.text}
      </InlineMarkdown>
      <span aria-hidden className="flex items-center text-tertiary">
        {leftward ? <span className="-mr-1 text-xs leading-none">◀</span> : null}
        <span
          className={cn(
            "h-0.5 flex-1",
            tone === "bad" || tone === "missing"
              ? "bg-status-critical"
              : tone === "good"
                ? "bg-status-positive"
                : tone === "new"
                  ? "bg-status-info"
                  : "bg-(--border-strong)",
            tone === "missing" && "opacity-60",
          )}
        />
        {leftward ? null : <span className="-ml-1 text-xs leading-none">▶</span>}
      </span>
    </div>
  );
}

export function SequenceDiagram({ data }: { data: SequenceData }) {
  const columns = { gridTemplateColumns: `repeat(${data.actors.length}, minmax(0, 1fr))` };
  return (
    <>
      {/* Wide: the actors across the top, each message an arrow between them. */}
      <div className="hidden min-w-0 @xl:block">
        <div style={columns} className="grid gap-x-2 border-b border-default pb-1.5">
          {data.actors.map((actor, at) => (
            <InlineMarkdown key={at} className="text-center text-base font-semibold text-primary">
              {actor}
            </InlineMarkdown>
          ))}
        </div>
        <ol style={columns} className="mt-2 grid gap-x-2 gap-y-2">
          {data.steps.map((step, at) => (
            <li key={at} style={columns} className="col-span-full grid gap-x-2">
              <Message step={step} />
            </li>
          ))}
        </ol>
      </div>
      {/* Narrow: the same messages as a list, who to whom first. */}
      <ol className="grid gap-1.5 @xl:hidden">
        {data.steps.map((step, at) => {
          const tone = toneOf(step.tone);
          return (
            <li key={at} className={cn("min-w-0 rounded-md border px-3 py-2 text-base", BOX[tone])}>
              <span className="font-medium text-primary">
                <InlineMarkdown>
                  {step.from === step.to
                    ? (data.actors[step.from] ?? "")
                    : `${data.actors[step.from] ?? ""} → ${data.actors[step.to] ?? ""}`}
                </InlineMarkdown>
              </span>
              <InlineMarkdown className={cn("block leading-snug", INK[tone])}>
                {step.text}
              </InlineMarkdown>
            </li>
          );
        })}
      </ol>
    </>
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

type Row = { key: string; cells: TableCell[] };

/** The rows a long table shows until it is opened: enough to read what it
 *  compares without it outweighing the section around it. */
export const TABLE_ROWS_SHOWN = 8;

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

export function TableDiagram({ data }: { data: TableData }) {
  const [all, setAll] = useState(false);
  const columns: Column<Row>[] = data.columns.map((column, position) => ({
    key: `${position}-${column}`,
    header: <InlineMarkdown>{column}</InlineMarkdown>,
    cell: (row) => <Cell cell={row.cells[position] ?? ""} />,
  }));
  // Folding one or two rows saves nothing: a table folds only when it
  // hides at least three.
  const folds = data.rows.length > TABLE_ROWS_SHOWN + 2;
  const rows = folds && !all ? data.rows.slice(0, TABLE_ROWS_SHOWN) : data.rows;
  return (
    <div className="space-y-2">
      <Table
        columns={columns}
        rows={rows.map((cells, position) => ({ key: String(position), cells }))}
        rowKey={(row) => row.key}
        density="compact"
        framed={false}
        minWidth={360}
      />
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
