import type { ReactNode } from "react";

import { cn } from "./classnames";
import { Spinner } from "./ui";

/** The states every pane that loads something has to be able to render.
 *
 *  `Empty` and a zero value are deliberately different components rather than
 *  the same widget with a 0 in it: "0%" next to "no data yet" is the
 *  difference between a page that looks broken on day one and one that
 *  explains itself. */

type StateProps = {
  className?: string;
  children?: ReactNode;
};

/** Nothing has arrived yet — no rows, not zero rows. */
export function Empty({
  title,
  children,
  className,
}: StateProps & {
  title: string;
}) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center gap-1.5 px-6 py-10 text-center",
        className,
      )}
    >
      <p className="font-medium text-(--text-secondary)">{title}</p>
      {children ? <p className="max-w-sm text-base text-(--text-tertiary)">{children}</p> : null}
    </div>
  );
}

/** The measurement ran and the answer is genuinely zero. Says so in words, so it
 *  is not read as a failed load. */
export function ZeroValue({ children, className }: StateProps) {
  return (
    <div className={cn("px-6 py-10 text-center text-base text-(--text-tertiary)", className)}>
      {children ?? "No activity in this window."}
    </div>
  );
}

/** A pane failed on its own. Panes load apart so that one failure cannot
 *  blank the page, and this is what makes that visible. */
export function Failed({ children, className }: StateProps) {
  return (
    <div
      role="alert"
      className={cn(
        "flex flex-col items-center gap-1.5 px-6 py-10 text-center text-base text-(--status-critical)",
        className,
      )}
    >
      {children ?? "This panel could not be loaded."}
    </div>
  );
}

/** First-load placeholder. It reserves the same geometry the real content will
 *  take, because a skeleton that changes height re-lays out the whole grid the
 *  moment data lands. A band of light passes across it rather than the whole
 *  block pulsing, which reads as "arriving" rather than "broken". */
export function Skeleton({ className, height = 200 }: { className?: string; height?: number }) {
  return (
    <div
      aria-hidden
      className={cn(
        "animate-shimmer rounded-(--radius-sm) bg-[linear-gradient(90deg,var(--surface-sunken)_0%,var(--surface-sunken)_35%,var(--surface-wash)_50%,var(--surface-sunken)_65%,var(--surface-sunken)_100%)] bg-[length:200%_100%] motion-reduce:animate-none",
        className,
      )}
      style={{ height }}
    />
  );
}

/** Holds the previous render at reduced opacity while a filter change is in
 *  flight. Flashing skeletons on every filter change makes the page feel
 *  like it is reloading rather than refining. */
export function Stale({ pending, children }: { pending: boolean; children: ReactNode }) {
  return (
    <div className="relative" aria-busy={pending || undefined}>
      <div className={cn("transition-opacity duration-150", pending && "opacity-55")}>
        {children}
      </div>
      {pending ? (
        <Spinner label="Refreshing" className="absolute top-3 right-3 text-(--text-tertiary)" />
      ) : null}
    </div>
  );
}
