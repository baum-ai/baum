import type { ReactNode } from "react";

import { cn, FOCUS_RING } from "./classnames";
import { formatCount } from "./format";
import { ChevronDownIcon } from "./icons";
import { Button } from "./ui";

/** The dashboard's data table.
 *
 *  Extracted because three analytics views had already hand-rolled the same
 *  markup at two different paddings and two different header sizes, and six
 *  more tables were about to. `density` is what reconciles those two paddings:
 *  a caller cannot pass a competing `px-*` and expect it to win, because `cn`
 *  concatenates rather than resolves. */

export type Column<T> = {
  key: string;
  header: ReactNode;
  align?: "left" | "right";
  /** Rendered per row. Return `EM_DASH` rather than 0 for anything unmeasured —
   *  a table is where that distinction is easiest to lose. */
  cell: (row: T) => ReactNode;
  /** Applied to both the header cell and every body cell in the column. */
  className?: string;
  /** Makes the header a control that orders the table by this column.
   *  `direction` is null while another column orders it. */
  sort?: { direction: "ascending" | "descending" | null; onSort: () => void };
};

const densities = {
  standard: { head: "px-4 py-2.5", cell: "px-4 py-3" },
  compact: { head: "px-3 py-2", cell: "px-3 py-2" },
};

export function Table<T>({
  columns,
  rows,
  rowKey,
  minWidth = 720,
  density = "standard",
  fixed = false,
  empty,
  caption,
  rowClassName,
}: {
  columns: Column<T>[];
  rows: T[];
  rowKey: (row: T) => string;
  /** Marks a row apart — a totals line, say, which is a different kind of
   *  statement from the rows above it and should not read as one of them. */
  rowClassName?: (row: T) => string | undefined;
  /** Below this the table scrolls inside its own container rather than
   *  squeezing columns into unreadability. */
  minWidth?: number;
  density?: keyof typeof densities;
  /** Columns take the widths their `className` sets and share the rest,
   *  instead of growing to fit their content. Truncated text only gives way
   *  under this layout: an automatic table widens a column to the full length
   *  of anything that does not wrap. */
  fixed?: boolean;
  empty?: ReactNode;
  caption?: ReactNode;
}) {
  const spacing = densities[density];

  if (rows.length === 0 && empty) {
    return (
      <div className="rounded-(--radius-lg) border border-(--border-default) bg-(--surface-card)">
        {empty}
      </div>
    );
  }

  return (
    // `relative` is load-bearing, not decoration. An absolutely positioned
    // descendant — an `sr-only` column header, say — resolves against the
    // nearest positioned ancestor, and with none it lands on the initial
    // containing block: outside this scroll container, so it escapes the clip
    // and widens the whole document instead. On a phone that is a page which
    // scrolls sideways for a header nobody can see.
    <div className="relative overflow-x-auto rounded-(--radius-lg) border border-(--border-default) bg-(--surface-card)">
      <table
        className={cn("w-full border-collapse text-base", fixed && "table-fixed")}
        style={{ minWidth }}
      >
        {caption ? (
          <caption className="px-4 pt-3 pb-1 text-left text-base text-(--text-tertiary)">
            {caption}
          </caption>
        ) : null}
        <thead>
          <tr className="border-b border-(--border-subtle) text-left text-xs tracking-[0.08em] text-(--text-tertiary) uppercase">
            {columns.map((column) => (
              <th
                key={column.key}
                scope="col"
                aria-sort={column.sort?.direction ?? undefined}
                className={cn(
                  spacing.head,
                  "font-medium",
                  column.align === "right" && "text-right",
                  column.className,
                )}
              >
                {column.sort ? (
                  <SortButton sort={column.sort}>{column.header}</SortButton>
                ) : (
                  column.header
                )}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr
              key={rowKey(row)}
              className={cn(
                "border-b border-(--border-subtle) last:border-b-0 hover:bg-(--surface-hover)",
                rowClassName?.(row),
              )}
            >
              {columns.map((column) => (
                <td
                  key={column.key}
                  className={cn(
                    spacing.cell,
                    "align-top",
                    column.align === "right" && "text-right tabular-nums",
                    column.className,
                  )}
                >
                  {column.cell(row)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** A sortable header. The arrow shows only on the column that orders the
 *  table, so the header row does not read as a row of controls; the column
 *  that does not yet order it shows one on hover. */
function SortButton({
  sort,
  children,
}: {
  sort: NonNullable<Column<unknown>["sort"]>;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={sort.onSort}
      className={cn(
        "group inline-flex cursor-pointer items-center gap-1 rounded-(--radius-sm) uppercase hover:text-(--text-primary)",
        FOCUS_RING,
        sort.direction && "text-(--text-primary)",
      )}
    >
      {children}
      <ChevronDownIcon
        size={12}
        className={cn(
          "flex-none transition-transform",
          sort.direction === "ascending" && "rotate-180",
          !sort.direction && "opacity-0 group-hover:opacity-100 group-focus-visible:opacity-100",
        )}
      />
    </button>
  );
}

/** Keyset pagination controls.
 *
 *  Lifted from the review queue, where the reasoning is written down: the
 *  cursor is a position, not an offset, so there is a "next" and a "back to the
 *  start" and deliberately no page numbers — the ordering can change between
 *  two requests and a numbered page would name a place that no longer exists. */
export function TablePager({
  total,
  shown,
  nextCursor,
  onCursor,
  hasCursor,
  noun = "rows",
}: {
  total?: number;
  shown?: number;
  nextCursor?: string;
  onCursor: (cursor: string) => void;
  hasCursor: boolean;
  noun?: string;
}) {
  if (shown == null) return null;

  return (
    <div className="mt-4 flex items-center justify-between gap-4">
      <p className="text-base text-(--text-tertiary)">
        {total == null
          ? `Showing ${formatCount(shown)} ${noun}`
          : `Showing ${formatCount(shown)} of ${formatCount(total)} ${noun}`}
      </p>
      <div className="flex gap-2">
        {hasCursor ? (
          <Button variant="secondary" size="compact" onClick={() => onCursor("")}>
            Back to the start
          </Button>
        ) : null}
        {nextCursor ? (
          <Button size="compact" onClick={() => onCursor(nextCursor)}>
            Next page
          </Button>
        ) : null}
      </div>
    </div>
  );
}
