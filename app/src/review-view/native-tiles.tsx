import type { ReactNode } from "react";
import { cn } from "@baum/viewer/components/classnames";
import { InlineMarkdown } from "@baum/viewer/components/markdown";
import { type Column, Table } from "@baum/viewer/components/table";
import type { ColumnsData, FlowData, FlowStep, SequenceData, TableData, Tone } from "./model";

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
  new: "border-status-info bg-status-info-bg",
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
}: {
  title: string;
  note?: string;
  tone: Tone;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "grid min-w-0 gap-0.5 rounded-md border px-3 py-2 [overflow-wrap:anywhere]",
        BOX[tone],
        tone === "muted" && "opacity-80",
        className,
      )}
    >
      <InlineMarkdown className={cn("text-base font-medium leading-snug", INK[tone])}>
        {title}
      </InlineMarkdown>
      {note ? (
        <InlineMarkdown className="text-xs leading-snug text-secondary">{note}</InlineMarkdown>
      ) : null}
    </div>
  );
}

/** The connector drawn before a step: across while the lane is a row, down
 *  once it stacks. */
function Join({ join }: { join: FlowStep["join"] }) {
  if (join === "none") return <span aria-hidden className="h-2 w-2 flex-none" />;
  const [across, down] = join === "both" ? ["⇄", "⇅"] : ["→", "↓"];
  return (
    <span
      aria-hidden
      data-join={join ?? "arrow"}
      className="flex flex-none items-center justify-center self-center px-1.5 py-0.5 text-base font-semibold text-tertiary"
    >
      <span className="@lg:hidden">{down}</span>
      <span className="hidden @lg:inline">{across}</span>
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
        lane.steps.every((step) => isRecord(step) && isString(step.title)),
    )
  );
}

/** A lane too long for one row of a section's width breaks into rows of
 *  three, so a row never ends with one step on its own, and keeps one row
 *  where the diagram has the room (the overview). A shorter lane keeps one
 *  row and its steps narrow to fit. */
const long = (steps: FlowStep[]) => steps.length > 4;

export function FlowDiagram({ data }: { data: FlowData }) {
  const labelled = data.lanes.some((lane) => lane.label);
  return (
    <div className="space-y-3">
      {data.lanes.map((lane, at) => (
        <div
          key={at}
          data-lane=""
          className={cn(
            "grid min-w-0 gap-1.5",
            labelled && "@lg:grid-cols-[7rem_minmax(0,1fr)] @lg:items-center @lg:gap-3",
          )}
        >
          {labelled ? (
            <InlineMarkdown className={LANE_LABEL}>{lane.label ?? ""}</InlineMarkdown>
          ) : null}
          <ol
            aria-label={lane.label || undefined}
            className={cn(
              "flex min-w-0 flex-col @lg:flex-row @lg:items-stretch @lg:gap-y-2",
              long(lane.steps) && "@lg:flex-wrap @4xl:flex-nowrap",
            )}
          >
            {lane.steps.map((step, position) => (
              <li
                key={position}
                className={cn(
                  "flex min-w-0 flex-col items-stretch @lg:flex-row",
                  long(lane.steps)
                    ? "@lg:basis-1/3 @4xl:flex-1 @4xl:basis-0"
                    : "@lg:flex-1 @lg:basis-0",
                )}
              >
                {position > 0 ? <Join join={step.join} /> : null}
                <Box
                  title={step.title}
                  note={step.note}
                  tone={toneOf(step.tone)}
                  className="@lg:flex-1"
                />
              </li>
            ))}
          </ol>
        </div>
      ))}
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

export function validTable(data: unknown): data is TableData {
  return (
    isRecord(data) &&
    Array.isArray(data.columns) &&
    data.columns.every(isString) &&
    Array.isArray(data.rows) &&
    data.rows.every((row) => Array.isArray(row) && row.every(isString))
  );
}

type Row = { key: string; cells: string[] };

export function TableDiagram({ data }: { data: TableData }) {
  const columns: Column<Row>[] = data.columns.map((column, position) => ({
    key: `${position}-${column}`,
    header: <InlineMarkdown>{column}</InlineMarkdown>,
    cell: (row) => <InlineMarkdown>{row.cells[position] ?? ""}</InlineMarkdown>,
  }));
  return (
    <Table
      columns={columns}
      rows={data.rows.map((cells, position) => ({ key: String(position), cells }))}
      rowKey={(row) => row.key}
      density="compact"
      minWidth={360}
    />
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
