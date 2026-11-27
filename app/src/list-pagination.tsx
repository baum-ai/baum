import { Button } from "@baum/viewer/components/ui";
import { formatCount } from "@baum/viewer/components/format";
import { useState } from "react";

const PAGE_SIZE = 30;

const pageOf = (index: number) => Math.floor(Math.max(0, index) / PAGE_SIZE);

export function useListPage(scope: string, total: number, initialIndex = 0) {
  const [position, setPosition] = useState({ scope, page: pageOf(initialIndex) });
  const last = Math.max(0, Math.ceil(total / PAGE_SIZE) - 1);
  const page = position.scope === scope ? Math.min(position.page, last) : 0;
  if (page !== position.page || scope !== position.scope) setPosition({ scope, page });
  const start = page * PAGE_SIZE;
  return {
    page,
    start,
    end: Math.min(start + PAGE_SIZE, total),
    last,
    total,
    move: (page: number) => setPosition({ scope, page }),
    reveal: (index: number) => setPosition({ scope, page: pageOf(index) }),
    slice: <T,>(rows: T[], offset: number) =>
      rows.slice(Math.max(0, start - offset), Math.max(0, start + PAGE_SIZE - offset)),
  };
}

export function ListPagination({
  pagination,
  label,
  onMove,
}: {
  pagination: ReturnType<typeof useListPage>;
  label: string;
  onMove: () => void;
}) {
  const { page, start, end, last, total, move } = pagination;
  if (!last) return null;
  const go = (next: number) => {
    move(next);
    onMove();
  };
  return (
    <nav aria-label={label} className="flex flex-wrap items-center justify-center gap-2 px-2 py-3">
      <Button size="compact" variant="secondary" disabled={page === 0} onClick={() => go(page - 1)}>
        Previous
      </Button>
      <span className="text-xs text-secondary" aria-live="polite">
        {formatCount(start + 1)}–{formatCount(end)} of {formatCount(total)}
      </span>
      <Button
        size="compact"
        variant="secondary"
        disabled={page === last}
        onClick={() => go(page + 1)}
      >
        Next
      </Button>
    </nav>
  );
}
