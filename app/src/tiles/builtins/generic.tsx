import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { HighlightedCode } from "@baum/viewer/components/markdown";
import { AnchorChips, PROSE } from "@baum/viewer/components/review-parts";
import { type Column, Table } from "@baum/viewer/components/table";
import { type Lang, languageForFence, languageForPath } from "@baum/viewer/highlight";
import type { Anchor, ReviewDocument } from "@baum/viewer/review-document";
import { isRecord, type TileFaceProps, type TileType } from "../types";
import { CodeExcerpt } from "./code-excerpt";

export type ProseData = { text: string; anchors: Anchor[] };
export type TableData = { columns: string[]; rows: string[][]; source: Anchor[] };
export type DiagramData = { format: string; content: string; source: Anchor[] };
/** A code excerpt. A baum.review/2 code tile also names its hunk, the
 *  lines of its new side the excerpt shows and those it highlights, and is
 *  drawn as that hunk's diff. */
export type CodeData = {
  language: string;
  content: string;
  caption?: string;
  source: Anchor[];
  hunk?: string;
  lines?: [number, number];
  focus?: number[];
};

/** The generic tiles a composer writes for a point no other tile fits:
 *  prose, a table, a diagram's source and a code excerpt. */

const strings = (value: unknown) =>
  Array.isArray(value) && value.every((item) => typeof item === "string");

/** What a prose tile's Markdown may draw: paragraphs, emphasis, code spans
 *  and lists. Links read as their text, and raw HTML stays text (no HTML
 *  plugin), as the text is model-written. */
const PROSE_ELEMENTS = ["p", "strong", "em", "code", "ul", "ol", "li", "br", "a"];
const PROSE_COMPONENTS: Components = {
  a: ({ children }) => <>{children}</>,
  // Preflight takes lists' markers away; a prose tile's lists keep theirs.
  ul: ({ node: _node, ...props }) => <ul {...props} className="list-disc space-y-1 pl-5" />,
  ol: ({ node: _node, ...props }) => <ol {...props} className="list-decimal space-y-1 pl-5" />,
  code: ({ children }) => (
    <code className="rounded-xs bg-code-fill px-1 py-px font-mono text-base text-primary">
      {children}
    </code>
  ),
};

export function ProseText({ text }: { text: string }) {
  return (
    <div className={`space-y-2 ${PROSE}`}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        allowedElements={PROSE_ELEMENTS}
        unwrapDisallowed
        components={PROSE_COMPONENTS}
      >
        {text}
      </ReactMarkdown>
    </div>
  );
}

/** A prose tile is an exhibit, not more of its section's text: it stands on
 *  a faint wash behind a rule, in a card or bare in a section's body. */
function ProseDetail({ tile, ctx }: TileFaceProps) {
  const data = tile.data as ProseData;
  return (
    <div className="space-y-3">
      <div
        data-prose-callout=""
        className="max-w-[90ch] rounded-r-md border-l-2 border-strong bg-surface-wash px-4 py-3"
      >
        <ProseText text={data.text} />
      </div>
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

function TableDetail({ tile, ctx }: TileFaceProps) {
  const data = tile.data as TableData;
  return (
    <div className="space-y-3">
      <TableView data={data} rows={data.rows} />
      <AnchorChips document={ctx.review} anchors={data.source} />
    </div>
  );
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

/** Text compared without its closing full stop and outer space. */
const bare = (text: string | undefined) => (text ?? "").trim().replace(/\.$/, "");

/** The language a code tile's excerpt is coloured in: the one it names, else
 *  the one the path of the first hunk it cites names. */
export function codeLanguage(data: CodeData, review: ReviewDocument): Lang | null {
  const named = languageForFence(data.language);
  if (named) return named;
  for (const anchor of data.source ?? []) {
    if (anchor.type !== "hunk") continue;
    const hunk = review.index.hunks.find((each) => each.id === anchor.id);
    const lang = hunk ? languageForPath(hunk.path) : null;
    if (lang) return lang;
  }
  return null;
}

function CodeDetail({ tile, ctx }: TileFaceProps) {
  const data = tile.data as CodeData;
  // The caption is the tile's headline, which the detail's heading shows.
  const caption = data.caption && bare(data.caption) !== bare(tile.headline) ? data.caption : "";
  const hunk = data.hunk ? ctx.review.index.hunks.find((each) => each.id === data.hunk) : undefined;
  // The excerpt's header names and opens its hunk; the chips say the rest.
  const anchors = hunk
    ? (data.source ?? []).filter((anchor) => !(anchor.type === "hunk" && anchor.id === hunk.id))
    : data.source;
  return (
    <div className="space-y-2">
      {hunk ? (
        <CodeExcerpt
          review={ctx.review}
          hunk={hunk}
          text={data.content}
          lines={data.lines}
          focus={data.focus ?? []}
        />
      ) : (
        <pre className="overflow-x-auto rounded-md bg-code-fill p-3 font-mono text-base leading-[1.5] text-primary">
          <HighlightedCode code={data.content} lang={codeLanguage(data, ctx.review)} />
        </pre>
      )}
      {caption ? <p className="text-base text-tertiary">{caption}</p> : null}
      <AnchorChips document={ctx.review} anchors={anchors} />
    </div>
  );
}

export const proseTile: TileType = {
  type: "prose",
  Detail: ProseDetail,
  valid: (data) => isRecord(data) && typeof data.text === "string",
  commentAnchors: (tile) => (tile.data as ProseData).anchors ?? [],
};

export const tableTile: TileType = {
  type: "table",
  Detail: TableDetail,
  valid: (data) =>
    isRecord(data) &&
    strings(data.columns) &&
    Array.isArray(data.rows) &&
    data.rows.every((row) => strings(row)),
  commentAnchors: (tile) => (tile.data as TableData).source ?? [],
};

export const diagramTile: TileType = {
  type: "diagram",
  Detail: DiagramDetail,
  valid: (data) => isRecord(data) && typeof data.content === "string",
  commentAnchors: (tile) => (tile.data as DiagramData).source ?? [],
};

export const codeTile: TileType = {
  type: "code",
  Detail: CodeDetail,
  valid: (data) => isRecord(data) && typeof data.content === "string",
  commentAnchors: (tile) => (tile.data as CodeData).source ?? [],
};
