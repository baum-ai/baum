import { Markdown } from "@baum/viewer/components/markdown";
import { AnchorChips, PROSE } from "@baum/viewer/components/review-parts";
import { type Column, Table } from "@baum/viewer/components/table";
import { CodeIcon, DiagramIcon, QuoteIcon, TableIcon } from "../icons";
import { Fill, plainText } from "../parts";
import type { CodeData, DiagramData, ProseData, TableData } from "../tile-document";
import { isRecord, type TileFaceProps, type TileType } from "../types";

/** The generic tiles a composer writes for a point no other tile fits:
 *  prose, a table, a diagram's source and a code excerpt. */

const strings = (value: unknown) =>
  Array.isArray(value) && value.every((item) => typeof item === "string");

function ProseGlance({ tile, ctx }: TileFaceProps) {
  return (
    <Fill min="4.5rem">
      <p className="text-base leading-normal text-primary">
        {plainText((tile.data as ProseData).text, ctx.labels)}
      </p>
    </Fill>
  );
}

function ProseDetail({ tile, ctx }: TileFaceProps) {
  const data = tile.data as ProseData;
  return (
    <div className="space-y-3">
      <Markdown className={PROSE}>{data.text}</Markdown>
      <AnchorChips document={ctx.review} anchors={data.anchors} />
    </div>
  );
}

type Row = { key: string; cells: string[] };

function TableView({ data, rows }: { data: TableData; rows: string[][] }) {
  const columns: Column<Row>[] = data.columns.map((column, position) => ({
    key: `${position}-${column}`,
    header: column,
    cell: (row) => row.cells[position] ?? "",
  }));
  return (
    <Table
      columns={columns}
      rows={rows.map((cells, position) => ({ key: String(position), cells }))}
      rowKey={(row) => row.key}
      density="compact"
      minWidth={240}
    />
  );
}

const GLANCE_ROWS = 4;

function TableGlance({ tile }: TileFaceProps) {
  const data = tile.data as TableData;
  // One row more than the glance holds is shown rather than counted.
  const shown = data.rows.length === GLANCE_ROWS + 1 ? GLANCE_ROWS + 1 : GLANCE_ROWS;
  return (
    <div className="space-y-1.5">
      <div className="pointer-events-none overflow-hidden">
        <TableView data={data} rows={data.rows.slice(0, shown)} />
      </div>
      {data.rows.length > shown ? (
        <p className="text-xs text-tertiary">{data.rows.length - shown} more rows</p>
      ) : null}
    </div>
  );
}

function TableDetail({ tile, ctx }: TileFaceProps) {
  const data = tile.data as TableData;
  return (
    <div className="space-y-3">
      <TableView data={data} rows={data.rows} />
      <AnchorChips document={ctx.review} anchors={data.source} />
    </div>
  );
}

/** The first lines of a text block, as code, each cut with an ellipsis
 *  where the card is too narrow for it. */
function Excerpt({ content, lines }: { content: string; lines: number }) {
  const all = content.split("\n");
  return (
    <pre className="overflow-hidden rounded-md bg-code-fill p-2.5 font-mono text-xs leading-[1.5] text-primary">
      <code className="block">
        {all.slice(0, lines).map((line, position) => (
          <span key={position} className="block overflow-hidden text-ellipsis whitespace-pre">
            {line || " "}
          </span>
        ))}
      </code>
    </pre>
  );
}

function DiagramGlance({ tile }: TileFaceProps) {
  return <Excerpt content={(tile.data as DiagramData).content} lines={8} />;
}

function DiagramDetail({ tile, ctx }: TileFaceProps) {
  const data = tile.data as DiagramData;
  return (
    <div className="space-y-2">
      <p className="text-xs text-tertiary">Diagram source ({data.format})</p>
      <pre className="overflow-x-auto rounded-md bg-code-fill p-3 font-mono text-base leading-[1.5] text-primary">
        <code>{data.content}</code>
      </pre>
      <AnchorChips document={ctx.review} anchors={data.source} />
    </div>
  );
}

function CodeGlance({ tile }: TileFaceProps) {
  const data = tile.data as CodeData;
  return (
    <div className="space-y-1.5">
      <Excerpt content={data.content} lines={8} />
      {data.caption ? <p className="truncate text-xs text-tertiary">{data.caption}</p> : null}
    </div>
  );
}

/** Text compared without its closing full stop and outer space. */
const bare = (text: string | undefined) => (text ?? "").trim().replace(/\.$/, "");

function CodeDetail({ tile, ctx }: TileFaceProps) {
  const data = tile.data as CodeData;
  // The caption is the tile's headline, which the detail's heading shows.
  const caption = data.caption && bare(data.caption) !== bare(tile.headline) ? data.caption : "";
  return (
    <div className="space-y-2">
      <pre className="overflow-x-auto rounded-md bg-code-fill p-3 font-mono text-base leading-[1.5] text-primary">
        <code>{data.content}</code>
      </pre>
      {caption ? <p className="text-base text-tertiary">{caption}</p> : null}
      <AnchorChips document={ctx.review} anchors={data.source} />
    </div>
  );
}

export const proseTile: TileType = {
  type: "prose",
  label: "Notes",
  icon: QuoteIcon,
  Glance: ProseGlance,
  Detail: ProseDetail,
  valid: (data) => isRecord(data) && typeof data.text === "string",
  commentAnchors: (_ctx, tile) => (tile.data as ProseData).anchors ?? [],
};

export const tableTile: TileType = {
  type: "table",
  label: "Table",
  icon: TableIcon,
  Glance: TableGlance,
  Detail: TableDetail,
  valid: (data) =>
    isRecord(data) &&
    strings(data.columns) &&
    Array.isArray(data.rows) &&
    data.rows.every((row) => strings(row)),
  commentAnchors: (_ctx, tile) => (tile.data as TableData).source ?? [],
};

export const diagramTile: TileType = {
  type: "diagram",
  label: "Diagram",
  icon: DiagramIcon,
  Glance: DiagramGlance,
  Detail: DiagramDetail,
  valid: (data) => isRecord(data) && typeof data.content === "string",
  commentAnchors: (_ctx, tile) => (tile.data as DiagramData).source ?? [],
};

export const codeTile: TileType = {
  type: "code",
  label: "Code",
  icon: CodeIcon,
  Glance: CodeGlance,
  Detail: CodeDetail,
  valid: (data) => isRecord(data) && typeof data.content === "string",
  commentAnchors: (_ctx, tile) => (tile.data as CodeData).source ?? [],
};
