import type { ReactNode } from "react";

import { cn } from "./classnames";

/** One labelled figure in a card's strip of totals, with an optional note
 *  beside it. */
export function Stat({
  label,
  value,
  detail,
  truncate,
}: {
  label: string;
  value: ReactNode;
  detail?: string;
  truncate?: boolean;
}) {
  return (
    <div className={cn("min-w-0", truncate && "max-w-80")}>
      <dt className="text-xs tracking-[0.08em] text-(--text-tertiary) uppercase">{label}</dt>
      <dd className={cn("mt-0.5 tabular-nums text-(--text-primary)", truncate && "truncate")}>
        {value}
        {detail ? (
          <span className="ml-1.5 text-xs text-(--text-tertiary) normal-case">{detail}</span>
        ) : null}
      </dd>
    </div>
  );
}
