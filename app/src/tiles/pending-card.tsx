import { cn } from "@baum/viewer/components/classnames";
import { Button, LiveDot } from "@baum/viewer/components/ui";
import { GridIcon } from "./icons";
import { spanOf } from "./card";
import { draftLine, type DraftCard } from "./drafts";

/** A pending card: the tile's title, what it is waiting on, and Remove. */
export function PendingCard({
  draft,
  narrow = false,
  remove,
}: {
  draft: DraftCard;
  narrow?: boolean;
  remove?: (tile: string) => void;
}) {
  const unfinished = draft.phase === "unfinished";
  return (
    <article
      data-tile-draft={draft.tile}
      data-draft-phase={draft.phase}
      aria-label={`${draft.title}, ${draftLine(draft)}`}
      className={cn(
        "flex min-h-32 min-w-0 flex-col gap-2.5 rounded-lg border border-dashed border-default px-4 pt-3 pb-3.5",
        unfinished ? "bg-transparent" : "bg-surface-wash",
        spanOf(draft.size ?? "half", narrow),
      )}
    >
      <header className="flex min-h-7 items-center gap-2">
        <GridIcon size={14} className="flex-none text-tertiary" />
        <span className="min-w-0 truncate text-xs font-medium tracking-[0.08em] text-tertiary uppercase">
          Custom · {draft.title}
        </span>
        <span className="ml-auto flex flex-none items-center gap-1.5">
          {unfinished ? null : (
            <span className="flex items-center" title="Being made">
              <LiveDot />
            </span>
          )}
          {remove ? (
            <Button
              variant="secondary"
              size="tiny"
              aria-label={`Remove ${draft.title}`}
              onClick={() => remove(draft.tile)}
            >
              Remove
            </Button>
          ) : null}
        </span>
      </header>
      <h4 className="-mt-1.5 min-w-0 truncate text-base leading-snug font-semibold text-primary">
        {draft.title}
      </h4>
      <p data-draft-line="" className="text-base text-secondary">
        {draftLine(draft)}
      </p>
    </article>
  );
}
