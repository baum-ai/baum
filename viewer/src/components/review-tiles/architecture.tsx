import { type ReactNode, useId } from "react";

import { cn } from "../classnames";
import { Badge } from "../ui";

import { architectureDefinition } from "./catalogue";
import { TileGlance, TileShell } from "./tile-frame";
import { TileQuestions } from "./tile-questions";
import type { CodeDiffFile } from "./code-diff";
import type { TileSourceFile } from "./tile-code-links";

export type ArchitectureNode = {
  id: string;
  label: string;
  kind: "person" | "client" | "service" | "worker" | "store" | "external" | "runtime";
  change?: "add" | "remove";
};

export type ArchitectureEdge = {
  from: string;
  to: string;
  label: string;
  change?: "add" | "remove";
};

export type ArchitectureSequence = {
  title: string;
  steps: readonly {
    from: string;
    to: string;
    message: string;
    change?: "add";
  }[];
};

export type ArchitectureChange = {
  product: string;
  containers: readonly ArchitectureNode[];
  edges: readonly ArchitectureEdge[];
  /** Include one entry for each important runtime interaction. */
  sequences?: readonly ArchitectureSequence[];
  boundary: string;
  runtime: string;
  failurePath: string;
  code: readonly TileSourceFile[];
  codeDiff: readonly CodeDiffFile[];
};

const plural = (count: number, one: string, many = `${one}s`) =>
  `${count} ${count === 1 ? one : many}`;

/** "a", "a and b", "a, b and c". */
const listOf = (items: readonly string[]) =>
  items.length <= 2 ? items.join(" and ") : `${items.slice(0, -1).join(", ")} and ${items.at(-1)}`;

/** Leads with what the pull request does to the architecture. When the review
 *  marks nothing as added or removed, the tile says so rather than implying a
 *  change, and describes the shape it shows instead. */
export function ArchitectureTile({ change: raw }: { change: ArchitectureChange }) {
  // A component listed twice is drawn once, under its first entry.
  const byId = new Map<string, ArchitectureNode>();
  for (const node of raw.containers) if (!byId.has(node.id)) byId.set(node.id, node);
  const change = { ...raw, containers: [...byId.values()] };
  const edges = change.edges.filter((edge) => byId.has(edge.from) && byId.has(edge.to));
  // Connections naming a component the tile does not list cannot be drawn, but
  // they are still part of what the review says, so they are listed, not lost.
  const dangling = change.edges.filter((edge) => !byId.has(edge.from) || !byId.has(edge.to));

  return (
    <TileShell
      title={architectureDefinition.title}
      source={{ code: change.code, codeDiff: change.codeDiff }}
    >
      <ArchitectureGlance change={change} edges={edges} byId={byId} />
      <ContainerDiagram nodes={change.containers} edges={edges} byId={byId} />
      {dangling.length > 0 ? <DanglingEdges edges={dangling} byId={byId} /> : null}
      {(change.sequences ?? []).map((sequence, index) => (
        <SequenceDiagram key={`${index}:${sequence.title}`} sequence={sequence} />
      ))}
      <TileQuestions
        title="How it works"
        rows={[
          { question: "Scope of the change", answer: change.boundary },
          { question: "How it behaves at runtime", answer: change.runtime },
          { question: "If something goes wrong", answer: change.failurePath },
        ]}
      />
    </TileShell>
  );
}

function ArchitectureGlance({
  change,
  edges,
  byId,
}: {
  change: ArchitectureChange;
  edges: readonly ArchitectureEdge[];
  byId: Map<string, ArchitectureNode>;
}) {
  const addedNodes = change.containers.filter((node) => node.change === "add").length;
  const removedNodes = change.containers.filter((node) => node.change === "remove").length;
  // Counted over every connection the review lists, drawn or not, so the lead
  // never says "nothing marked as changed" about a change it could not draw.
  const addedEdges = change.edges.filter((edge) => edge.change === "add").length;
  const removedEdges = change.edges.filter((edge) => edge.change === "remove").length;
  const addedSteps = (change.sequences ?? []).reduce(
    (count, sequence) => count + sequence.steps.filter((step) => step.change === "add").length,
    0,
  );
  const marked = addedNodes + removedNodes + addedEdges + removedEdges + addedSteps > 0;

  let headline: string;
  if (marked) {
    const adds = [
      addedNodes > 0 && plural(addedNodes, "component"),
      addedEdges > 0 && plural(addedEdges, "connection"),
      addedSteps > 0 && plural(addedSteps, "runtime step"),
    ].filter((item): item is string => Boolean(item));
    const removes = [
      removedNodes > 0 && plural(removedNodes, "component"),
      removedEdges > 0 && plural(removedEdges, "connection"),
    ].filter((item): item is string => Boolean(item));
    headline = [
      adds.length > 0 && `Adds ${listOf(adds)}`,
      removes.length > 0 && `${adds.length > 0 ? "removes" : "Removes"} ${listOf(removes)}`,
    ]
      .filter(Boolean)
      .join("; ");
  } else {
    // The shape worth naming first: one part that several others all go through.
    const incoming = new Map<string, string[]>();
    for (const edge of edges) {
      incoming.set(edge.to, [...(incoming.get(edge.to) ?? []), edge.from]);
    }
    const hub = [...incoming.entries()]
      .filter(([, from]) => new Set(from).size >= 2)
      .toSorted((a, b) => b[1].length - a[1].length)[0];
    if (hub) {
      const sources = [...new Set(hub[1])].map((id) => byId.get(id)?.label ?? id);
      const target = byId.get(hub[0])?.label ?? hub[0];
      headline =
        sources.length === 2
          ? `${sources[0]} and ${sources[1]} both go through ${target}`
          : `${sources.length} components all go through ${target}`;
    } else {
      headline = `${change.product}: ${plural(change.containers.length, "component")} and ${plural(edges.length, "connection")}`;
    }
  }

  return (
    <TileGlance
      label="Architecture at a glance"
      headline={headline}
      description={
        marked
          ? "New parts are marked + New and drawn in green; removed parts are dashed and struck through."
          : "The review marks nothing here as added or removed, so the diagram shows how the parts fit together after this pull request rather than what it changed."
      }
      badges={[
        addedNodes > 0 && (
          <Badge key="added-nodes" tone="positive" size="compact">
            {plural(addedNodes, "new component")}
          </Badge>
        ),
        addedEdges > 0 && (
          <Badge key="added-edges" tone="positive" size="compact">
            {plural(addedEdges, "new connection")}
          </Badge>
        ),
        removedNodes + removedEdges > 0 && (
          <Badge key="removed" tone="critical" size="compact">
            {removedNodes + removedEdges} removed
          </Badge>
        ),
        !marked && (
          <Badge key="unmarked" size="compact">
            No parts marked as changed
          </Badge>
        ),
        <Badge key="components" size="compact">
          {plural(change.containers.length, "component")}
        </Badge>,
        edges.length > 0 && (
          <Badge key="connections" size="compact">
            {plural(edges.length, "connection")}
          </Badge>
        ),
      ]}
    />
  );
}

const NODE_W = 212;
const NODE_H = 68;
const COL_GAP = 132;
const ROW_GAP = 28;
const PAD = 16;

type Placed = { node: ArchitectureNode; x: number; y: number; rank: number };

/** Places each component in a column by the longest chain of connections that
 *  leads to it, so whatever starts an interaction sits on the left and what it
 *  depends on sits to its right. Cycles are cut by capping the depth. */
function layout(nodes: readonly ArchitectureNode[], edges: readonly ArchitectureEdge[]) {
  const rank = new Map(nodes.map((node) => [node.id, 0]));
  for (let pass = 0; pass < nodes.length; pass++) {
    let moved = false;
    for (const edge of edges) {
      const next = Math.min((rank.get(edge.from) ?? 0) + 1, nodes.length - 1);
      if (edge.from !== edge.to && next > (rank.get(edge.to) ?? 0)) {
        rank.set(edge.to, next);
        moved = true;
      }
    }
    if (!moved) break;
  }
  const columns: ArchitectureNode[][] = [];
  for (const node of nodes) {
    const column = rank.get(node.id) ?? 0;
    (columns[column] ??= []).push(node);
  }
  const filled = columns.filter((column) => column !== undefined);
  const tallest = Math.max(...filled.map((column) => column.length));
  const height = PAD * 2 + tallest * NODE_H + (tallest - 1) * ROW_GAP;
  const placed = new Map<string, Placed>();
  filled.forEach((column, index) => {
    const columnHeight = column.length * NODE_H + (column.length - 1) * ROW_GAP;
    const top = PAD + (height - PAD * 2 - columnHeight) / 2;
    column.forEach((node, row) => {
      placed.set(node.id, {
        node,
        rank: index,
        x: PAD + index * (NODE_W + COL_GAP),
        y: top + row * (NODE_H + ROW_GAP),
      });
    });
  });
  const width = PAD * 2 + filled.length * NODE_W + (filled.length - 1) * COL_GAP;
  return { placed, width, height };
}

const edgeStroke = (change: ArchitectureEdge["change"]) =>
  change === "add"
    ? "text-(--status-positive)"
    : change === "remove"
      ? "text-(--status-critical)"
      : "text-(--text-secondary)";

/** Components as boxes and connections as numbered arrows between them. The
 *  numbers key each arrow to its full description beneath, so the drawing
 *  stays legible however long the review's wording is. */
function ContainerDiagram({
  nodes,
  edges,
  byId,
}: {
  nodes: readonly ArchitectureNode[];
  edges: readonly ArchitectureEdge[];
  byId: Map<string, ArchitectureNode>;
}) {
  const marker = useId().replace(/:/g, "");
  const { placed, width, height: baseHeight } = layout(nodes, edges);
  const backEdges = edges.filter(
    (edge) => (placed.get(edge.to)?.rank ?? 0) <= (placed.get(edge.from)?.rank ?? 0),
  );
  const height = baseHeight + (backEdges.length > 0 ? 48 : 0);

  // Spread the ends of several arrows along a box's side so they never overlap.
  const ends = new Map<string, number>();
  const endCount = new Map<string, number>();
  for (const edge of edges) {
    for (const key of [`out:${edge.from}`, `in:${edge.to}`]) {
      endCount.set(key, (endCount.get(key) ?? 0) + 1);
    }
  }
  const slot = (key: string) => {
    const index = ends.get(key) ?? 0;
    ends.set(key, index + 1);
    return (index + 1) / ((endCount.get(key) ?? 1) + 1);
  };

  const paths = edges.map((edge, index) => {
    const from = placed.get(edge.from)!;
    const to = placed.get(edge.to)!;
    const outAt = slot(`out:${edge.from}`);
    const inAt = slot(`in:${edge.to}`);
    let d: string;
    let mid: { x: number; y: number };
    if (to.rank > from.rank) {
      const x1 = from.x + NODE_W;
      const y1 = from.y + NODE_H * outAt;
      const x2 = to.x - 6;
      const y2 = to.y + NODE_H * inAt;
      const bend = (x2 - x1) / 2;
      d = `M${x1} ${y1} C${x1 + bend} ${y1} ${x2 - bend} ${y2} ${x2} ${y2}`;
      mid = { x: (x1 + x2) / 2, y: (y1 + y2) / 2 };
    } else {
      // Back or sideways: loop under the boxes rather than through them.
      const x1 = from.x + NODE_W * outAt;
      const y1 = from.y + NODE_H;
      const x2 = to.x + NODE_W * inAt;
      const y2 = to.y + NODE_H + 6;
      const low = baseHeight + 24;
      d = `M${x1} ${y1} C${x1} ${low} ${x2} ${low} ${x2} ${y2}`;
      mid = { x: (x1 + x2) / 2, y: low - 6 };
    }
    return { edge, index, d, mid };
  });

  return (
    <section aria-label="Components and connections" className="mt-4">
      <h5 className="text-xs font-semibold tracking-[0.12em] uppercase text-(--text-tertiary)">
        Components and connections
      </h5>
      <div className="mt-2 overflow-x-auto rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card)">
        <svg
          role="img"
          aria-label={`${plural(nodes.length, "component")} and ${plural(edges.length, "connection")}`}
          viewBox={`0 0 ${width} ${height}`}
          width={width}
          height={height}
          // Drawn at its own width: a wide diagram scrolls inside its frame
          // rather than shrinking until its labels cannot be read.
          className="mx-auto block"
          style={{ minWidth: width }}
        >
          <ArrowMarkers id={marker} />
          {paths.map(({ edge, index, d }) => (
            <path
              key={`${index}:${edge.from}:${edge.to}`}
              d={d}
              fill="none"
              stroke="currentColor"
              strokeWidth={1.5}
              strokeDasharray={edge.change === "remove" ? "5 4" : undefined}
              markerEnd={`url(#${marker}-${edge.change ?? "same"})`}
              className={edgeStroke(edge.change)}
              data-edge={index + 1}
            />
          ))}
          {[...placed.values()].map(({ node, x, y }) => (
            <foreignObject key={node.id} x={x} y={y} width={NODE_W} height={NODE_H}>
              <NodeBox node={node} />
            </foreignObject>
          ))}
          {paths.map(({ edge, index, mid }) => (
            <g key={`n:${index}:${edge.from}:${edge.to}`} className={edgeStroke(edge.change)}>
              <circle
                cx={mid.x}
                cy={mid.y}
                r={10}
                fill="var(--surface-card)"
                stroke="currentColor"
              />
              <text
                x={mid.x}
                y={mid.y}
                textAnchor="middle"
                dominantBaseline="central"
                fill="currentColor"
                className="text-3xs font-semibold"
              >
                {index + 1}
              </text>
            </g>
          ))}
        </svg>
      </div>
      {edges.length > 0 ? (
        <ol className="mt-2 divide-y divide-(--border-subtle) rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card)">
          {edges.map((edge, index) => (
            <li
              key={`${index}:${edge.from}:${edge.to}`}
              className="flex gap-3 px-3 py-2 text-xs leading-[1.45]"
            >
              <span
                aria-hidden
                className={cn(
                  "flex h-5 w-5 flex-none items-center justify-center rounded-full border text-3xs font-semibold",
                  edge.change === "add"
                    ? "border-(--status-positive) text-(--status-positive)"
                    : edge.change === "remove"
                      ? "border-(--status-critical) text-(--status-critical)"
                      : "border-(--border-default) text-(--text-secondary)",
                )}
              >
                {index + 1}
              </span>
              <span className="min-w-0 flex-1">
                <span className="font-medium text-(--text-primary)">
                  {byId.get(edge.from)?.label}
                </span>
                <span aria-hidden className="text-(--text-tertiary)">
                  {" "}
                  →{" "}
                </span>
                <span className="font-medium text-(--text-primary)">
                  {byId.get(edge.to)?.label}
                </span>
                <span
                  className={cn(
                    "block text-(--text-secondary)",
                    edge.change === "remove" && "line-through",
                  )}
                >
                  {edge.label}
                </span>
              </span>
              {edge.change ? <ChangeMark change={edge.change} /> : null}
            </li>
          ))}
        </ol>
      ) : null}
    </section>
  );
}

function NodeBox({ node }: { node: ArchitectureNode }) {
  const outside = node.kind === "external" || node.kind === "person";
  return (
    <div
      title={node.label}
      className={cn(
        "flex h-full flex-col justify-center gap-0.5 rounded-(--radius-sm) border px-2.5 py-1.5",
        node.change === "add"
          ? "border-(--status-positive)"
          : node.change === "remove"
            ? "border-dashed border-(--status-critical)"
            : "border-(--border-default)",
        outside ? "bg-(--surface-wash)" : "bg-(--surface-card)",
      )}
    >
      <span className="flex min-w-0 items-center gap-1.5">
        {node.change === "add" ? (
          <span aria-hidden className="flex-none font-semibold text-(--status-positive)">
            +
          </span>
        ) : null}
        <span
          className={cn(
            "line-clamp-2 min-w-0 text-xs font-medium leading-[1.3] text-(--text-primary)",
            node.change === "remove" && "line-through",
          )}
        >
          {node.label}
        </span>
      </span>
      <span className="flex items-center gap-1.5">
        <span className="text-3xs tracking-[0.08em] uppercase text-(--text-tertiary)">
          {node.kind}
        </span>
        {node.change ? <ChangeMark change={node.change} /> : null}
      </span>
    </div>
  );
}

const LANE_W = 168;
const HEAD_H = 44;
const STEP_H = 56;

/** A runtime interaction drawn as a sequence diagram: one lifeline per
 *  participant, and each message an arrow between lifelines in the order it
 *  happens, so waiting and ordering are visible rather than implied. */
function SequenceDiagram({ sequence }: { sequence: ArchitectureSequence }) {
  const marker = useId().replace(/:/g, "");
  const participants: string[] = [];
  for (const step of sequence.steps) {
    for (const name of [step.from, step.to]) {
      if (!participants.includes(name)) participants.push(name);
    }
  }
  const lane = (name: string) => PAD + participants.indexOf(name) * LANE_W + LANE_W / 2;
  const width = PAD * 2 + participants.length * LANE_W;
  const height = HEAD_H + PAD + sequence.steps.length * STEP_H + PAD;

  return (
    <section aria-label={`Sequence: ${sequence.title}`} className="mt-4">
      <div className="flex flex-wrap items-baseline gap-2">
        <h5 className="text-xs font-semibold tracking-[0.12em] uppercase text-(--text-tertiary)">
          Sequence
        </h5>
        <span className="text-xs text-(--text-secondary)">{sequence.title}</span>
      </div>
      <div className="mt-2 overflow-x-auto rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card)">
        <svg
          role="img"
          aria-label={`${sequence.title}: ${plural(sequence.steps.length, "step")}`}
          viewBox={`0 0 ${width} ${height}`}
          width={width}
          height={height}
          // Drawn at its own width: a wide diagram scrolls inside its frame
          // rather than shrinking until its labels cannot be read.
          className="mx-auto block"
          style={{ minWidth: width }}
        >
          <ArrowMarkers id={marker} />
          {participants.map((name) => (
            <g key={name}>
              <line
                x1={lane(name)}
                y1={PAD + HEAD_H}
                x2={lane(name)}
                y2={height - PAD / 2}
                stroke="var(--border-default)"
                strokeDasharray="4 4"
              />
              <foreignObject
                x={lane(name) - LANE_W / 2 + 6}
                y={PAD}
                width={LANE_W - 12}
                height={HEAD_H - 8}
              >
                <div
                  title={name}
                  className="flex h-full items-center justify-center rounded-(--radius-sm) border border-(--border-default) bg-(--surface-wash) px-2 text-center"
                >
                  <span className="line-clamp-2 text-xs font-medium leading-[1.25] text-(--text-primary)">
                    {name}
                  </span>
                </div>
              </foreignObject>
            </g>
          ))}
          {sequence.steps.map((step, index) => {
            const y = PAD + HEAD_H + PAD + index * STEP_H + STEP_H - 12;
            const x1 = lane(step.from);
            const x2 = lane(step.to);
            const self = step.from === step.to;
            const left = Math.min(x1, x2);
            const span = self ? LANE_W - 12 : Math.abs(x2 - x1) - 12;
            return (
              <g
                key={`${index}:${step.from}:${step.to}`}
                className={
                  step.change === "add" ? "text-(--status-positive)" : "text-(--text-secondary)"
                }
              >
                <path
                  d={
                    self
                      ? `M${x1} ${y - 14} h28 v14 h-24`
                      : `M${x1} ${y} L${x2 + (x2 > x1 ? -4 : 4)} ${y}`
                  }
                  fill="none"
                  stroke="currentColor"
                  strokeWidth={1.5}
                  markerEnd={`url(#${marker}-${step.change ?? "same"})`}
                />
                <foreignObject
                  x={self ? x1 + 32 : left + 6}
                  y={y - STEP_H + 16}
                  width={span}
                  height={STEP_H - 20}
                >
                  <div
                    title={step.message}
                    className="flex h-full items-end justify-center text-center"
                  >
                    <span className="line-clamp-2 text-3xs leading-[1.3] text-(--text-secondary)">
                      <span className="font-semibold text-(--text-primary)">{index + 1}.</span>{" "}
                      {step.change === "add" ? (
                        <span className="font-semibold text-(--status-positive)">+ </span>
                      ) : null}
                      {step.message}
                    </span>
                  </div>
                </foreignObject>
              </g>
            );
          })}
        </svg>
      </div>
    </section>
  );
}

function ChangeMark({ change }: { change: "add" | "remove" }): ReactNode {
  return (
    <Badge tone={change === "add" ? "positive" : "critical"} size="compact">
      {change === "add" ? "New" : "Removed"}
    </Badge>
  );
}

/** One arrowhead per line colour: a marker is painted in its own context, so
 *  it cannot pick up the colour of the line that uses it. */
function ArrowMarkers({ id }: { id: string }) {
  const tones = {
    same: "var(--text-secondary)",
    add: "var(--status-positive)",
    remove: "var(--status-critical)",
  };
  return (
    <defs>
      {Object.entries(tones).map(([tone, fill]) => (
        <marker
          key={tone}
          id={`${id}-${tone}`}
          viewBox="0 0 10 10"
          refX="9"
          refY="5"
          markerWidth="7"
          markerHeight="7"
          orient="auto-start-reverse"
        >
          <path d="M0 0 L10 5 L0 10 z" fill={fill} />
        </marker>
      ))}
    </defs>
  );
}

/** Connections the review lists but the diagram cannot draw, because one end
 *  names a component this tile does not include. */
function DanglingEdges({
  edges,
  byId,
}: {
  edges: readonly ArchitectureEdge[];
  byId: Map<string, ArchitectureNode>;
}) {
  const name = (id: string) => byId.get(id)?.label ?? id;
  return (
    <section
      aria-label="Connections not drawn"
      className="mt-2 rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card) px-3 py-2 text-xs leading-[1.45]"
    >
      <p className="text-(--text-secondary)">
        {plural(edges.length, "connection")} {edges.length === 1 ? "names" : "name"} a component
        this tile does not list, so {edges.length === 1 ? "it is" : "they are"} not drawn:
      </p>
      <ul className="mt-1 grid gap-1">
        {edges.map((edge, index) => (
          <li
            key={`${index}:${edge.from}:${edge.to}`}
            className="flex flex-wrap items-center gap-1.5"
          >
            <span className="font-medium text-(--text-primary)">{name(edge.from)}</span>
            <span aria-hidden className="text-(--text-tertiary)">
              →
            </span>
            <span className="font-medium text-(--text-primary)">{name(edge.to)}</span>
            <span className="text-(--text-secondary)">· {edge.label}</span>
            {edge.change ? <ChangeMark change={edge.change} /> : null}
          </li>
        ))}
      </ul>
    </section>
  );
}
