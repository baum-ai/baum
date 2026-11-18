import type { ComponentProps } from "react";

import { cn } from "./classnames";
import { LIST_ROW } from "./primitive-classes";

/** A row of a full-width list (`LIST_ROW`, `primitive-classes.ts`).
 *  `data-list-row` is what a row's ⋯ button keys on to show itself while the
 *  row is pointed at or focused. */
export function ListRow({ className, ...props }: ComponentProps<"li">) {
  return <li data-list-row="" {...props} className={cn(LIST_ROW, className)} />;
}
