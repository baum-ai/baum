import { cn } from "@baum/viewer/components/classnames";
import { Button, LiveDot } from "@baum/viewer/components/ui";
import { GridIcon } from "./icons";
import { draftLine, type DraftCard } from "./drafts";

/** A pending card, on the section the tile will stand on: the tile's title,
 *  what it is waiting on, and Remove. */
export function PendingCard({
  draft,
  remove,
}: {
  draft: DraftCard;
  remove?: (tile: string) => void;
}) {
  const unfinished = draft.phase === "unfinished";
  return (
    <article
      data-tile-draft={draft.tile}
      data-draft-phase={draft.phase}
      aria-label={`${draft.title}, ${draftLine(draft)}`}
      className={cn(
        "flex min-w-0 flex-col gap-2 rounded-lg border border-dashed border-default p-4",
        unfinished ? "bg-transparent" : "bg-surface-wash",
      )}
    >
      <header className="flex min-h-7 items-center gap-2">
        <GridIcon size={14} className="flex-none text-tertiary" />
        <h3 className="min-w-0 truncate text-base font-semibold text-primary">{draft.title}</h3>
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
      <p data-draft-line="" className="text-base text-secondary">
        {draftLine(draft)}
      </p>
    </article>
  );
}
