/** The categorical palette, as CSS custom-property references (`--data-*` in
 *  `viewer.css`). Slots are assigned in order and never cycled: a ninth
 *  series folds into MUTED. */

export const SERIES = [
  "var(--data-1)",
  "var(--data-2)",
  "var(--data-3)",
  "var(--data-4)",
  "var(--data-5)",
  "var(--data-6)",
  "var(--data-7)",
  "var(--data-8)",
] as const;

/** The deliberate absence of identity: unattributed commits, "Other" rollups. */
export const MUTED = "var(--data-muted)";

export function seriesColor(index: number): string {
  return index >= 0 && index < SERIES.length ? SERIES[index]! : MUTED;
}
