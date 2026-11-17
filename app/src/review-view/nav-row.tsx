import type { ReactNode } from "react";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import { InlineMarkdown } from "@baum/viewer/components/markdown";

/** A row of a tab's own list (the sections, the changed files): its title,
 *  a quiet line under it, and what it holds at its end. It looks like a rail
 *  row, the one read marked as the rail marks it. */
export function NavRow({
  title,
  line,
  aside,
  active,
  controls,
  mono = false,
  muted = false,
  wideLine = false,
  onSelect,
}: {
  title: string;
  line?: string;
  aside?: ReactNode;
  active: boolean;
  controls: string;
  mono?: boolean;
  muted?: boolean;
  /** The line shows only while the list stands beside what it opens, so a
   *  list stacked over it on a narrow pane stays short. */
  wideLine?: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      aria-controls={controls}
      title={mono ? undefined : title}
      onClick={onSelect}
      className={cn(
        "relative grid w-full cursor-pointer grid-cols-[minmax(0,1fr)_auto] items-center gap-x-2 gap-y-0.5 rounded-sm py-1.5 pr-2 pl-2.5 text-left transition-colors duration-150",
        FOCUS_RING,
        active
          ? "bg-surface-selected text-primary"
          : "text-secondary hover:bg-surface-hover hover:text-primary",
        muted && !active && "opacity-60",
      )}
    >
      <span
        aria-hidden
        className={cn(
          "absolute top-2 left-0 h-4 w-0.5 bg-action-accent transition-opacity duration-150",
          active ? "opacity-100" : "opacity-0",
        )}
      />
      <span
        data-row-title=""
        className={cn(
          "min-w-0",
          mono ? "truncate font-mono text-xs font-medium" : "text-base leading-snug",
          active && !mono && "font-medium",
        )}
      >
        {mono ? title : <InlineMarkdown>{title}</InlineMarkdown>}
      </span>
      {aside ? <span className="row-span-2 flex-none text-xs text-tertiary">{aside}</span> : null}
      {line ? (
        <span className={cn("col-start-1 min-w-0", wideLine && "hidden @3xl:block")}>
          {mono ? (
            <span className="block truncate text-xs text-tertiary">{line}</span>
          ) : (
            <InlineMarkdown className="line-clamp-2 text-xs text-tertiary">{line}</InlineMarkdown>
          )}
        </span>
      ) : null}
    </button>
  );
}
