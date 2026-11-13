import { useEffect, useRef } from "react";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import { centerJump, flashJump } from "@baum/viewer/components/jump-focus";
import { ChevronLeftIcon } from "@baum/viewer/components/icons";
import { Badge, CardHeader } from "@baum/viewer/components/ui";
import { TileBoundary } from "./card";
import { GridIcon } from "./icons";
import { Quiet, Writing } from "./parts";
import { tileFace } from "./registry";
import { changeById, type Tile } from "./tile-document";
import type { TileContext } from "./types";

/** One tile's detail: everything about it, under its kind and title. A
 *  Change draws the section as the full review does, title and all. */
export function TileDetail({
  tile,
  item,
  ctx,
  back,
}: {
  tile: Tile;
  item?: string;
  ctx: TileContext;
  back: () => void;
}) {
  const face = tileFace(tile);
  const Icon = face.type?.icon ?? GridIcon;
  const ownHeader =
    face.kind === "ready" && (tile.type === "change" || tile.type === "everything_else");
  const root = useRef<HTMLDivElement>(null);
  useItemFocus(root, tile, item, ctx);
  return (
    <div ref={root} className="flex min-w-0 flex-1 flex-col">
      <div className="mb-3 flex flex-wrap items-center gap-x-3 gap-y-1">
        <button
          type="button"
          onClick={back}
          aria-keyshortcuts="Escape u"
          className={cn(
            "-ml-1 inline-flex cursor-pointer items-center gap-1 rounded-sm px-1 text-base text-secondary hover:text-primary",
            FOCUS_RING,
          )}
        >
          <ChevronLeftIcon size={14} />
          Summary
        </button>
        <span className="inline-flex items-center gap-1.5 text-xs font-medium tracking-[0.08em] text-tertiary uppercase">
          <Icon size={13} />
          {face.type?.label ?? "Tile"}
        </span>
        {tile.state === "pending" ? (
          <Badge size="compact" tone="info">
            Being written
          </Badge>
        ) : tile.state === "failed" ? (
          <Badge size="compact" tone="critical">
            Details unavailable
          </Badge>
        ) : null}
        {ctx.compare?.added.has(tile.id) ? (
          <Badge size="compact" tone="info">
            New since compared
          </Badge>
        ) : ctx.compare?.changed.has(tile.id) ? (
          <Badge size="compact" tone="caution">
            Changed since compared
          </Badge>
        ) : null}
      </div>
      {ownHeader ? null : (
        <CardHeader
          size="pane"
          title={<span data-tile-heading="">{tile.title}</span>}
          detail={tile.headline && tile.headline !== tile.title ? tile.headline : undefined}
        />
      )}
      <div className="min-h-0 flex-1">
        <TileBoundary
          fallback={<Quiet>This tile couldn't be shown. The full review still shows it.</Quiet>}
        >
          {face.kind === "ready" ? (
            <face.type.Detail tile={tile} ctx={ctx} item={item} />
          ) : face.kind === "pending" ? (
            <Writing />
          ) : (
            <Unavailable why={face.why} problems={tile.problems} />
          )}
        </TileBoundary>
      </div>
    </div>
  );
}

function Unavailable({ why, problems }: { why: string; problems?: string[] }) {
  return (
    <div className="space-y-2 rounded-md border border-dashed border-default px-4 py-3">
      <p className="text-base text-secondary">{why}</p>
      {problems && problems.length > 1 ? (
        <ul className="list-disc space-y-0.5 pl-5 text-xs text-tertiary">
          {problems.slice(1).map((problem) => (
            <li key={problem}>{problem}</li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

/** Brings an item of the tile into view and marks it, as a link to it asks;
 *  without one, puts the keys on the tile's heading, so they never drop to
 *  the page. */
function useItemFocus(
  root: React.RefObject<HTMLDivElement | null>,
  tile: Tile,
  item: string | undefined,
  ctx: TileContext,
) {
  useEffect(() => {
    if (ctx.historical && ctx.comparing) return;
    const element = root.current;
    if (!element) return;
    let cancel: (() => void) | undefined;
    const frame = requestAnimationFrame(() => {
      const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
      // A change's own items sit under its section's IDs; the aggregate
      // tiles' under theirs.
      const change = changeById(ctx.doc, tile.id)?.id ?? tileChange(tile);
      const ids = item
        ? [
            `${ctx.idPrefix}tile-item-${item}`,
            ...(change ? [`${ctx.idPrefix}${change}-${item}`] : []),
          ]
        : [];
      const target = ids.map((id) => document.getElementById(id)).find(Boolean);
      if (target && element.contains(target)) {
        for (
          let own = target.closest("details");
          own;
          own = own.parentElement?.closest("details") ?? null
        )
          own.open = true;
        cancel = centerJump(target, reduced);
        flashJump(target, reduced);
        return;
      }
      if (ctx.historical) return;
      const active = document.activeElement;
      if (active && active !== document.body && element.contains(active)) return;
      const heading = element.querySelector<HTMLElement>("h2");
      if (heading) {
        heading.tabIndex = -1;
        heading.focus({ preventScroll: true });
      }
    });
    return () => {
      cancelAnimationFrame(frame);
      cancel?.();
    };
  }, [tile.id, item, ctx.historical, ctx.comparing, ctx.idPrefix]);
}

function tileChange(tile: Tile): string | undefined {
  const data = tile.data as { change?: unknown } | null;
  return typeof data?.change === "string" ? data.change : undefined;
}
