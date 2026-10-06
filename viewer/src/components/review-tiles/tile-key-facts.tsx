import { cn } from "../classnames";

export type TileFactTone = "neutral" | "info" | "positive" | "caution" | "critical";

export type TileKeyFact = {
  label: string;
  value: string;
  tone: TileFactTone;
};

const toneClass: Record<TileFactTone, string> = {
  neutral: "bg-(--text-tertiary)",
  info: "bg-(--accent-600)",
  positive: "bg-(--status-positive)",
  caution: "bg-(--status-caution)",
  critical: "bg-(--status-critical)",
};

/** Facts are one deliberate reading path, not small print scattered across a grid. */
export function TileKeyFacts({ facts }: { facts: readonly TileKeyFact[] }) {
  return (
    <dl
      aria-label="Key review facts"
      className="mt-4 max-w-[680px] overflow-hidden rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card)"
    >
      {facts.map((fact, index) => (
        <div
          key={`${index}:${fact.label}`}
          className="grid gap-1 border-b border-(--border-subtle) px-3 py-2.5 last:border-b-0 sm:grid-cols-[8.5rem_minmax(0,1fr)] sm:gap-4"
        >
          <dt className="flex items-center gap-2 text-xs font-semibold tracking-[0.12em] uppercase text-(--text-tertiary)">
            <span
              aria-hidden
              className={cn("h-1.5 w-1.5 flex-none rounded-full", toneClass[fact.tone])}
            />
            {fact.label}
          </dt>
          <dd className="text-base leading-[1.45] text-(--text-secondary)">{fact.value}</dd>
        </div>
      ))}
    </dl>
  );
}
