import { cn } from "../classnames";
import { Badge } from "../ui";

import { fieldChange } from "./field-change";
import { contractSchemaDiffDefinition } from "./catalogue";
import type { CodeDiffFile } from "./code-diff";
import { TileDisclosure, TileGlance, TileShell } from "./tile-frame";
import type { TileSourceFile } from "./tile-code-links";

export type SchemaOperation = {
  kind: "add" | "remove" | "alter" | "rename" | "constraint";
  object: "table" | "column" | "index" | "constraint" | "relationship";
  subject: string;
  detail: string;
};

export type SchemaColumn = {
  name: string;
  type: string;
  key?: "primary" | "foreign";
  references?: string;
  /** The column's status relative to the schema before this pull request. */
  change?: "add" | "remove" | "alter" | "rename";
};

export type SchemaTable = {
  name: string;
  /** Whether this pull request introduces the entire table. */
  change?: "add";
  columns: readonly SchemaColumn[];
};

export type SchemaRelationship = {
  from: string;
  to: string;
  cardinality: "one-to-one" | "one-to-many" | "many-to-one" | "many-to-many";
  description?: string;
};

export type ContractSchemaDiff = {
  overview?: {
    headline: string;
    description: string;
  };
  tables: readonly SchemaTable[];
  relationships: readonly SchemaRelationship[];
  operations: readonly SchemaOperation[];
  code: readonly TileSourceFile[];
  codeDiff: readonly CodeDiffFile[];
};

const operationStyle: Record<SchemaOperation["kind"], { mark: string; className: string }> = {
  add: { mark: "+", className: "text-(--status-positive)" },
  remove: { mark: "−", className: "text-(--status-critical)" },
  alter: { mark: "~", className: "text-(--status-caution)" },
  rename: { mark: "→", className: "text-(--accent-600)" },
  constraint: { mark: "↳", className: "text-(--text-tertiary)" },
};

/** Crow's-foot (Information Engineering) notation: each end of a relationship
 *  line carries two marks. The one against the table is the maximum — a bar
 *  for one, a crow's foot for many — and the one beside it is the minimum — a
 *  ring for zero, a bar for one. So `||` reads "exactly one", `o|` "zero or
 *  one", `o<` "zero or many" and `|<` "one or many". */
type EndMark = { min: "zero" | "one" | null; max: "one" | "many" };

const endMarkLabel = (mark: EndMark): string =>
  mark.min === null
    ? mark.max
    : `${mark.min === "zero" ? "zero" : "one"} or ${mark.max}`.replace("one or one", "exactly one");

/** Whether the foreign-key column may be null, read from the column's type as
 *  this tile states it ("timestamptz nullable"). Unknown when the tile does not
 *  list the column, in which case no minimum is drawn on the referenced end:
 *  the diagram asserts only what the schema says. */
function foreignKeyNullable(tables: readonly SchemaTable[], qualified: string): boolean | null {
  const column = tables
    .find((table) => table.name === tableName(qualified, tables))
    ?.columns.find((candidate) => candidate.name === columnName(qualified, tables));
  if (!column) return null;
  return /(?<!not )\bnullable\b/i.test(column.type) || /(?<!not )\bnull\b/i.test(column.type);
}

/** Which end of the relationship carries the foreign-key column. Every
 *  cardinality puts it on `from` except one-to-many, where the "many" side
 *  holding the key is `to` — matching the end order `relationshipMarks`
 *  already computes below. */
function foreignKeySide(relationship: SchemaRelationship): "from" | "to" {
  return relationship.cardinality === "one-to-many" ? "to" : "from";
}

/** The marks at the foreign-key end and the referenced end. A referenced row
 *  can always exist with no rows pointing at it, so a "many" end starts at
 *  zero; the referenced end is exactly one when the foreign key is required
 *  and zero or one when it may be null. */
function relationshipMarks(
  relationship: SchemaRelationship,
  nullable: boolean | null,
): readonly [EndMark, EndMark] {
  const referenced: EndMark = {
    min: nullable === null ? null : nullable ? "zero" : "one",
    max: "one",
  };
  const many: EndMark = { min: "zero", max: "many" };
  switch (relationship.cardinality) {
    case "many-to-one":
      return [many, referenced];
    case "one-to-one":
      return [{ min: "zero", max: "one" }, referenced];
    case "one-to-many":
      return [referenced, many];
    case "many-to-many":
      return [many, many];
  }
}

/** The SVG for one end of a horizontal line `width` long, with its table at
 *  x = 0 (`start`) or x = width (`end`). */
function EndGlyph({ mark, side, width }: { mark: EndMark; side: "start" | "end"; width: number }) {
  const x = (distance: number) => (side === "start" ? distance : width - distance);
  const max =
    mark.max === "many" ? (
      <path d={`M${x(0)} 4 L${x(12)} 14 M${x(0)} 14 L${x(12)} 14 M${x(0)} 24 L${x(12)} 14`} />
    ) : (
      <path d={`M${x(6)} 5 L${x(6)} 23`} />
    );
  const min =
    mark.min === "zero" ? (
      <circle cx={x(mark.max === "many" ? 18 : 16)} cy={14} r={4} fill="var(--surface-card)" />
    ) : mark.min === "one" ? (
      <path d={`M${x(mark.max === "many" ? 17 : 12)} 5 L${x(mark.max === "many" ? 17 : 12)} 23`} />
    ) : null;
  return (
    <>
      {max}
      {min}
    </>
  );
}

/** Splits `table.column` against the tables this tile lists, longest name
 *  first, so a schema-qualified `public.orders.user_id` resolves to the table
 *  `public.orders` rather than to `public`. A reference to a table the tile
 *  does not list splits at its last dot, which keeps the schema with the table. */
function splitQualified(
  qualified: string,
  tables: readonly SchemaTable[],
): { table: string; column: string | null } {
  const known = tables
    .map((table) => table.name)
    .toSorted((a, b) => b.length - a.length)
    .find((name) => qualified === name || qualified.startsWith(`${name}.`));
  if (known !== undefined) {
    return { table: known, column: qualified === known ? null : qualified.slice(known.length + 1) };
  }
  const separator = qualified.lastIndexOf(".");
  return separator < 0
    ? { table: qualified, column: null }
    : { table: qualified.slice(0, separator), column: qualified.slice(separator + 1) };
}

function tableName(qualified: string, tables: readonly SchemaTable[]): string {
  return splitQualified(qualified, tables).table;
}

function columnName(qualified: string, tables: readonly SchemaTable[]): string | null {
  return splitQualified(qualified, tables).column;
}

/** The part of an operation's subject after its table name: `table.column`
 *  and `table CHECK (...)` drop the one-character `.`/` ` separator, but
 *  `table(a, b)` has no separator to drop — the `(` opens the detail itself
 *  and must stay, or the displayed text loses it. */
function subjectAfterTable(subject: string, name: string): string {
  const rest = subject.slice(name.length);
  return rest.startsWith("(") ? rest : rest.slice(1);
}

/** One card per table: entries that repeat a table name are merged, their
 *  columns combined with a repeated column kept once, and the table counted as
 *  new if any entry says so. A later occurrence of a column that carries a
 *  change marker wins over an earlier, unmarked occurrence, so a genuine
 *  change is never silently dropped in favor of an unchanged duplicate. */
function mergeTables(tables: readonly SchemaTable[]): SchemaTable[] {
  const merged = new Map<string, SchemaTable>();
  for (const table of tables) {
    const existing = merged.get(table.name);
    if (!existing) {
      merged.set(table.name, table);
      continue;
    }
    const columns = new Map(existing.columns.map((column) => [column.name, column]));
    for (const column of table.columns) {
      const seen = columns.get(column.name);
      if (!seen || (!seen.change && column.change)) columns.set(column.name, column);
    }
    merged.set(table.name, {
      name: table.name,
      change: existing.change ?? table.change,
      columns: [...columns.values()],
    });
  }
  return [...merged.values()];
}

function relationshipDescription(
  relationship: SchemaRelationship,
  tables: readonly SchemaTable[],
): string {
  if (relationship.description) return relationship.description;
  const from = tableName(relationship.from, tables);
  const to = tableName(relationship.to, tables);
  if (relationship.cardinality === "many-to-one") {
    return `Many ${from} rows can reference one ${to} row.`;
  }
  if (relationship.cardinality === "one-to-many") {
    return `One ${from} row can be referenced by many ${to} rows.`;
  }
  if (relationship.cardinality === "one-to-one") {
    return `Each ${from} row can reference one ${to} row.`;
  }
  return `Many ${from} rows can relate to many ${to} rows.`;
}

function operationLabel(operation: SchemaOperation): string {
  if (operation.kind === "constraint") return "CONSTRAINT";
  const action =
    operation.kind === "add"
      ? "NEW"
      : operation.kind === "remove"
        ? "REMOVED"
        : operation.kind === "alter"
          ? "CHANGED"
          : "RENAMED";
  return `${action} ${operation.object.toUpperCase()}`;
}

/** A table-first view of a database schema change: each table, then what changes in it. */
export function ContractSchemaDiffTile({ diff: raw }: { diff: ContractSchemaDiff }) {
  const diff = { ...raw, tables: mergeTables(raw.tables) };
  return (
    <TileShell
      title={contractSchemaDiffDefinition.title}
      source={{ code: diff.code, codeDiff: diff.codeDiff }}
    >
      <SchemaAtAGlance diff={diff} />
      <TileDisclosure
        label="Tables, columns & relationships"
        summary={`${diff.tables.length} ${diff.tables.length === 1 ? "table" : "tables"} · ${diff.relationships.length} ${diff.relationships.length === 1 ? "relationship" : "relationships"}`}
      >
        <SchemaMap
          tables={diff.tables}
          relationships={diff.relationships}
          operations={diff.operations}
        />
      </TileDisclosure>
    </TileShell>
  );
}

function SchemaAtAGlance({ diff }: { diff: ContractSchemaDiff }) {
  const newTables = diff.tables.filter((table) => table.change === "add");
  const columnsInNewTables = newTables.reduce((count, table) => count + table.columns.length, 0);
  // Only tables this pull request touches are counted: an unchanged table
  // listed so a new one has something to reference is not an update.
  const { byTable } = groupOperations(diff.tables, diff.operations);
  const changedTables = diff.tables.filter((table) =>
    tableChanged(table, byTable.get(table.name)),
  ).length;
  const headline =
    diff.overview?.headline ??
    (newTables.length > 0
      ? `Adds ${newTables.length} new ${newTables.length === 1 ? "table" : "tables"}`
      : changedTables > 0
        ? `Updates ${changedTables} ${changedTables === 1 ? "table" : "tables"}`
        : "Schema change");

  return (
    <TileGlance
      label="Schema at a glance"
      headline={headline}
      description={
        diff.overview?.description ??
        "Expand the sections below for the tables, columns and relationships this change touches."
      }
      badges={[
        // A handful of new tables is named outright, so the headline chip says
        // which tables arrive; beyond three a count reads better than a list.
        ...(newTables.length > 0 && newTables.length <= 3
          ? newTables.map((table) => (
              <Badge key={`table:${table.name}`} tone="positive" size="compact">
                New table · <span className="ml-1 font-mono">{table.name}</span>
              </Badge>
            ))
          : [
              newTables.length > 0 && (
                <Badge key="tables" tone="positive" size="compact">
                  {newTables.length} new tables
                </Badge>
              ),
            ]),
        columnsInNewTables > 0 && (
          <Badge key="columns" size="compact">
            {columnsInNewTables} columns total
          </Badge>
        ),
        diff.relationships.length > 0 && (
          <Badge key="links" size="compact">
            {diff.relationships.length} foreign-key{" "}
            {diff.relationships.length === 1 ? "link" : "links"}
          </Badge>
        ),
      ]}
    />
  );
}

const columnChangeLabel: Record<NonNullable<SchemaColumn["change"]>, string> = {
  add: "New column",
  remove: "Column removed",
  alter: "Column changed",
  rename: "Column renamed",
};

/** A column's detail is prose the review writes, and read bare beside a column
 *  a line like "Null until a metadata refresh completes" leaves the reader to
 *  guess what it describes. The lead-in names it: for a new column, the value
 *  the column holds and when; for other changes, what the change does. */
const columnDetailLabel: Record<SchemaOperation["kind"], string> = {
  add: "Stored value:",
  alter: "What changed:",
  rename: "What changed:",
  remove: "Effect of removal:",
  constraint: "Rule:",
};

const columnChangeVerb: Record<NonNullable<SchemaColumn["change"]>, string> = {
  add: "added",
  remove: "removed",
  alter: "changed",
  rename: "renamed",
};

/** What this pull request does to an existing table's columns, in one line:
 *  "1 column added", "2 columns added · 1 column removed". */
function columnSummary(table: SchemaTable): string | null {
  const parts = (["add", "remove", "alter", "rename"] as const).flatMap((change) => {
    const count = table.columns.filter((column) => column.change === change).length;
    return count > 0
      ? [`${count} ${count === 1 ? "column" : "columns"} ${columnChangeVerb[change]}`]
      : [];
  });
  return parts.length > 0 ? parts.join(" · ") : null;
}

type TableChanges = {
  /** Changes to the table as a whole: creating it, an index, a table constraint. */
  tableLevel: SchemaOperation[];
  /** Changes to one column, keyed by column name. */
  byColumn: Map<string, SchemaOperation[]>;
};

/** Operations name their subject as `table`, `table.column` or a table-level
 *  phrase such as `table UNIQUE (a, b)` or `table(a, b)`; each is filed
 *  under the table it names so a reader meets the table first and its changes
 *  beneath it. The longest matching table name wins, so `orders_archive.id`
 *  never lands under `orders`. Anything that names no table in this tile is
 *  returned as unplaced rather than dropped. */
function groupOperations(
  tables: readonly SchemaTable[],
  operations: readonly SchemaOperation[],
): { byTable: Map<string, TableChanges>; unplaced: SchemaOperation[] } {
  const byTable = new Map<string, TableChanges>(
    tables.map((table) => [table.name, { tableLevel: [], byColumn: new Map() }]),
  );
  const names = tables.map((table) => table.name).toSorted((a, b) => b.length - a.length);
  const unplaced: SchemaOperation[] = [];

  for (const operation of operations) {
    const name = names.find(
      (candidate) =>
        operation.subject === candidate ||
        [".", " ", "("].some((separator) => operation.subject.startsWith(candidate + separator)),
    );
    const changes = name === undefined ? undefined : byTable.get(name);
    const table = tables.find((candidate) => candidate.name === name);
    if (!changes || !table) {
      unplaced.push(operation);
      continue;
    }
    const column = subjectAfterTable(operation.subject, table.name);
    if (column && table.columns.some((candidate) => candidate.name === column)) {
      changes.byColumn.set(column, [...(changes.byColumn.get(column) ?? []), operation]);
    } else {
      changes.tableLevel.push(operation);
    }
  }

  return { byTable, unplaced };
}

/** Whether this pull request touches a table: the table is new, a column
 *  carries a change marker, or an operation names the table or one of its
 *  columns — the last even when that column itself carries no marker. */
function tableChanged(table: SchemaTable, changes: TableChanges | undefined): boolean {
  return (
    table.change === "add" ||
    table.columns.some((column) => column.change) ||
    (changes?.tableLevel.length ?? 0) > 0 ||
    (changes?.byColumn.size ?? 0) > 0
  );
}

/** One table and what this pull request does to it. A new table leads with a
 *  + and its "New table" chip beside the name; its columns are all new by
 *  definition, so they are not each re-marked. An existing table that only
 *  gives the new ones something to reference is shown quieter, as context. */
function TableCard({ table, changes }: { table: SchemaTable; changes: TableChanges | undefined }) {
  const isNew = table.change === "add";
  const isContext = !tableChanged(table, changes);
  const tableDetail = changes?.tableLevel.find(
    (operation) => operation.object === "table" && operation.subject === table.name,
  );
  const otherTableChanges =
    changes?.tableLevel.filter((operation) => operation !== tableDetail) ?? [];
  return (
    <article
      aria-label={`${table.name} table${isNew ? " · new table" : " · existing table"}`}
      className={cn(
        "overflow-hidden rounded-(--radius-sm) border bg-(--surface-card)",
        isContext ? "border-(--border-subtle)" : "border-(--border-default)",
      )}
    >
      <div
        className={cn(
          "border-b px-3",
          isNew ? "py-3" : "py-2",
          isContext
            ? "border-(--border-subtle) bg-(--surface-card)"
            : "border-(--border-default) bg-(--surface-wash)",
        )}
      >
        <div className="flex flex-wrap items-center gap-2">
          {isNew ? (
            <span
              aria-hidden
              className="w-3 flex-none text-center text-lg font-semibold text-(--status-positive)"
            >
              +
            </span>
          ) : null}
          <h6
            className={cn(
              "font-mono font-semibold",
              isNew ? "text-lg" : "text-base",
              isContext ? "text-(--text-secondary)" : "text-(--text-primary)",
            )}
          >
            {table.name}
          </h6>
          {isNew ? (
            <Badge tone="positive" size="compact">
              New table
            </Badge>
          ) : (
            <span className="text-3xs font-semibold tracking-[0.08em] text-(--text-tertiary)">
              {isContext ? "EXISTING TABLE · UNCHANGED" : "EXISTING TABLE"}
            </span>
          )}
        </div>
        {isNew ? (
          <p className="mt-1 pl-5 text-xs text-(--text-secondary)">
            {tableDetail ? `${tableDetail.detail} · ` : "Created by this pull request · "}
            {table.columns.length} {table.columns.length === 1 ? "column" : "columns"}
          </p>
        ) : tableDetail ? (
          <p className="mt-1 text-xs text-(--text-secondary)">{tableDetail.detail}</p>
        ) : columnSummary(table) ? (
          <p className="mt-1 text-xs text-(--text-secondary)">{columnSummary(table)}</p>
        ) : null}
      </div>
      {otherTableChanges.length > 0 && (
        <ul
          aria-label={`Changes to ${table.name}`}
          className="divide-y divide-(--border-subtle) border-b border-(--border-default)"
        >
          {otherTableChanges.map((operation, index) => (
            <OperationRow
              key={`${index}:${operation.kind}:${operation.subject}`}
              operation={operation}
              subject={
                operation.subject === table.name
                  ? operation.subject
                  : subjectAfterTable(operation.subject, table.name)
              }
            />
          ))}
        </ul>
      )}
      <ul aria-label={`${table.name} columns`} className="divide-y divide-(--border-subtle)">
        {table.columns.map((column, index) => {
          const columnChanges = changes?.byColumn.get(column.name) ?? [];
          // Every column of a new table is new; marking each one again would
          // only repeat the table's own chip, so only other changes are marked.
          const change = isNew && column.change === "add" ? undefined : column.change;
          return (
            <li key={`${index}:${column.name}`} className="px-3 py-1.5 text-xs">
              <div className="flex min-w-0 flex-wrap items-center gap-2">
                <span
                  aria-hidden
                  className={cn(
                    "w-3 flex-none text-center text-base font-semibold",
                    change && operationStyle[change].className,
                  )}
                >
                  {change ? operationStyle[change].mark : ""}
                </span>
                {column.key ? (
                  <span
                    className={cn(
                      "w-4 flex-none text-3xs font-semibold tracking-[0.05em]",
                      column.key === "primary" ? "text-(--status-caution)" : "text-(--accent-600)",
                    )}
                  >
                    {column.key === "primary" ? "PK" : "FK"}
                  </span>
                ) : (
                  <span aria-hidden className="w-4 flex-none" />
                )}
                <span
                  className={cn(
                    "min-w-0 break-words font-mono text-(--text-primary)",
                    column.change === "remove" && "line-through",
                  )}
                >
                  {column.name}
                </span>
                {change && (
                  <Badge size="compact" tone={fieldChange[change].tone}>
                    {columnChangeLabel[change]}
                  </Badge>
                )}
                <span className="ml-auto flex-none font-mono text-(--text-tertiary)">
                  {column.type}
                </span>
              </div>
              {columnChanges.map((operation, detailIndex) => (
                <p
                  key={`${detailIndex}:${operation.kind}`}
                  className="mt-1 flex flex-wrap gap-x-2 pl-11 text-xs leading-[1.45]"
                >
                  <span className="flex-none font-semibold text-(--text-tertiary)">
                    {columnDetailLabel[operation.kind]}
                  </span>
                  <span className="min-w-0 text-(--text-secondary)">{operation.detail}</span>
                </p>
              ))}
            </li>
          );
        })}
      </ul>
    </article>
  );
}

/** One change, led by its kind: the mark and label say what happened, the
 *  subject says to what, and the detail says why it matters. */
function OperationRow({ operation, subject }: { operation: SchemaOperation; subject: string }) {
  const style = operationStyle[operation.kind];
  return (
    <li className="flex gap-2 px-3 py-2 text-base">
      <span aria-hidden className={cn("w-3 flex-none text-center font-semibold", style.className)}>
        {style.mark}
      </span>
      <div className="grid min-w-0 flex-1 gap-1 sm:grid-cols-[minmax(7rem,0.8fr)_minmax(0,1fr)] sm:gap-3">
        <div className="min-w-0">
          <span className="block text-3xs font-semibold tracking-[0.08em] text-(--text-tertiary)">
            {operationLabel(operation)}
          </span>
          <span className="block break-words font-mono text-(--text-primary)">{subject}</span>
        </div>
        <span className="min-w-0 text-(--text-secondary)">{operation.detail}</span>
      </div>
    </li>
  );
}

/** Each table leads with its name, then what this pull request does to it:
 *  table-level changes first, then its columns with each change's reason
 *  beneath the column it touches. */
function SchemaMap({
  tables,
  relationships,
  operations,
}: {
  tables: readonly SchemaTable[];
  relationships: readonly SchemaRelationship[];
  operations: readonly SchemaOperation[];
}) {
  const { byTable, unplaced } = groupOperations(tables, operations);
  const relationshipEnds = relationships.map((relationship) => ({
    relationship,
    marks: relationshipMarks(
      relationship,
      foreignKeyNullable(tables, relationship[foreignKeySide(relationship)]),
    ),
  }));
  const newTables = new Set(
    tables.filter((table) => table.change === "add").map((table) => table.name),
  );
  const isChanged = (table: SchemaTable) => tableChanged(table, byTable.get(table.name));
  const groups = [
    { label: "New tables", tables: tables.filter((table) => table.change === "add") },
    {
      label: "Changed tables",
      tables: tables.filter((table) => table.change !== "add" && isChanged(table)),
    },
    {
      label: "Existing tables referenced",
      tables: tables.filter((table) => table.change !== "add" && !isChanged(table)),
    },
  ].filter((group) => group.tables.length > 0);

  return (
    <section aria-label="Schema changes" className="mt-4">
      <div className="grid gap-4">
        {groups.map((group) => (
          <section key={group.label} aria-label={group.label} className="grid gap-2">
            <h6 className="text-xs font-semibold tracking-[0.12em] uppercase text-(--text-tertiary)">
              {group.label} · {group.tables.length}
            </h6>
            {group.tables.map((table) => (
              <TableCard key={table.name} table={table} changes={byTable.get(table.name)} />
            ))}
          </section>
        ))}
      </div>
      {unplaced.length > 0 && (
        <section
          aria-label="Other schema changes"
          className="mt-3 overflow-hidden rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card)"
        >
          <h6 className="border-b border-(--border-subtle) bg-(--surface-wash) px-3 py-2 text-xs font-semibold tracking-[0.12em] uppercase text-(--text-tertiary)">
            Other changes
          </h6>
          <ul className="divide-y divide-(--border-subtle)">
            {unplaced.map((operation, index) => (
              <OperationRow
                key={`${index}:${operation.kind}:${operation.subject}`}
                operation={operation}
                subject={operation.subject}
              />
            ))}
          </ul>
        </section>
      )}
      {relationships.length > 0 && (
        <section
          aria-label="Foreign-key relationships"
          className="mt-4 border-t border-(--border-default) pt-3"
        >
          <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1.5">
            <h6 className="text-xs font-semibold tracking-[0.12em] uppercase text-(--text-tertiary)">
              Foreign-key relationships
            </h6>
            <NotationLegend marks={relationshipEnds.flatMap((entry) => entry.marks)} />
          </div>
          <ul className="mt-2 grid gap-3">
            {relationshipEnds.map(({ relationship, marks }, index) => (
              <li
                key={`${index}:${relationship.from}:${relationship.to}`}
                className="min-w-0 rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card) p-3"
              >
                <RelationshipDiagram
                  relationship={relationship}
                  marks={marks}
                  tables={tables}
                  newTables={newTables}
                />
                <div className="mt-3 flex flex-wrap items-start gap-2 border-t border-(--border-subtle) pt-2.5">
                  <Badge size="compact">
                    {tableName(relationship.from, tables)}: {endMarkLabel(marks[0])} ·{" "}
                    {tableName(relationship.to, tables)}: {endMarkLabel(marks[1])}
                  </Badge>
                  <p className="min-w-[12rem] flex-1 text-xs leading-[1.45] text-(--text-secondary)">
                    {relationshipDescription(relationship, tables)}
                  </p>
                </div>
              </li>
            ))}
          </ul>
        </section>
      )}
    </section>
  );
}

/** Two entity boxes joined by a crow's-foot line, as an ER diagram draws a
 *  foreign key: the referencing table with its FK column on one side, the
 *  referenced table with its key on the other. The notation is backed by text:
 *  the badge beneath spells out both ends, so the glyphs are never the only
 *  way to read the cardinality. */
function RelationshipDiagram({
  relationship,
  marks,
  tables,
  newTables,
}: {
  relationship: SchemaRelationship;
  marks: readonly [EndMark, EndMark];
  tables: readonly SchemaTable[];
  newTables: ReadonlySet<string>;
}) {
  const fkSide = foreignKeySide(relationship);
  return (
    <div className="grid min-w-0 items-center gap-0 sm:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)]">
      <EntityBox
        qualified={relationship.from}
        keyMark={fkSide === "from" ? "FK" : undefined}
        tables={tables}
        isNew={newTables.has(tableName(relationship.from, tables))}
      />
      <RelationshipConnector from={marks[0]} to={marks[1]} />
      <EntityBox
        qualified={relationship.to}
        keyMark={fkSide === "to" ? "FK" : undefined}
        tables={tables}
        isNew={newTables.has(tableName(relationship.to, tables))}
      />
    </div>
  );
}

/** One table as an ER entity: its name as the header, then the column this
 *  relationship joins on, with its key marker and type when the tile lists
 *  them. */
function EntityBox({
  qualified,
  keyMark,
  tables,
  isNew,
}: {
  qualified: string;
  keyMark?: "FK";
  tables: readonly SchemaTable[];
  isNew: boolean;
}) {
  const name = tableName(qualified, tables);
  const column = columnName(qualified, tables);
  const listed = tables
    .find((table) => table.name === name)
    ?.columns.find((candidate) => candidate.name === column);
  const key = keyMark ?? (listed?.key === "primary" ? "PK" : undefined);

  return (
    <div className="min-w-0 overflow-hidden rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card)">
      <div className="flex flex-wrap items-center gap-1.5 border-b border-(--border-default) bg-(--surface-wash) px-2 py-1.5">
        <span className="min-w-0 font-mono text-xs font-semibold text-(--text-primary) [overflow-wrap:anywhere]">
          {name}
        </span>
        {isNew ? (
          <Badge tone="positive" size="compact">
            New table
          </Badge>
        ) : null}
      </div>
      {column ? (
        <div className="flex min-w-0 items-center gap-2 px-2 py-1.5 text-xs">
          <span
            className={cn(
              "w-4 flex-none text-3xs font-semibold tracking-[0.05em]",
              key === "PK" ? "text-(--status-caution)" : "text-(--accent-600)",
            )}
          >
            {key ?? ""}
          </span>
          <span className="min-w-0 flex-1 font-mono text-(--text-primary) [overflow-wrap:anywhere]">
            {column}
          </span>
          {listed ? (
            <span className="flex-none font-mono text-(--text-tertiary)">{listed.type}</span>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

/** The line turns with the layout: across the boxes once they sit side by
 *  side, down between them while they are stacked. The vertical form is the
 *  same drawing rotated, so the two can never disagree. */
function RelationshipConnector({ from, to }: { from: EndMark; to: EndMark }) {
  const width = 72;
  const drawing = (
    <>
      <line x1={0} y1={14} x2={width} y2={14} />
      <EndGlyph mark={from} side="start" width={width} />
      <EndGlyph mark={to} side="end" width={width} />
    </>
  );
  return (
    <span aria-hidden className="flex items-center justify-center text-(--text-secondary)">
      <svg
        width={width}
        height="28"
        viewBox={`0 0 ${width} 28`}
        fill="none"
        stroke="currentColor"
        strokeWidth={1.25}
        strokeLinecap="round"
        focusable="false"
        className="hidden sm:block"
      >
        {drawing}
      </svg>
      <svg
        width="28"
        height={width}
        viewBox={`0 0 28 ${width}`}
        fill="none"
        stroke="currentColor"
        strokeWidth={1.25}
        strokeLinecap="round"
        focusable="false"
        className="sm:hidden"
      >
        <g transform="translate(28 0) rotate(90)">{drawing}</g>
      </svg>
    </span>
  );
}

/** The key names each end mark that appears in this tile, once, rather than
 *  leaving a reader to infer them from the diagrams. */
function NotationLegend({ marks }: { marks: readonly EndMark[] }) {
  const unique = [...new Map(marks.map((mark) => [endMarkLabel(mark), mark])).values()];
  return (
    <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-3xs font-semibold tracking-[0.08em] uppercase text-(--text-tertiary)">
      {unique.map((mark) => (
        <span key={endMarkLabel(mark)} className="flex items-center gap-1.5">
          <svg
            aria-hidden
            width="32"
            height="28"
            viewBox="0 0 32 28"
            fill="none"
            stroke="currentColor"
            strokeWidth={1.25}
            strokeLinecap="round"
            focusable="false"
            className="h-3.5 w-4 flex-none text-(--text-secondary)"
          >
            <line x1={0} y1={14} x2={32} y2={14} />
            <EndGlyph mark={mark} side="end" width={32} />
          </svg>
          {endMarkLabel(mark)}
        </span>
      ))}
    </p>
  );
}
