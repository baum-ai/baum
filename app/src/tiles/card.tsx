import { Component, useId, type ReactNode } from "react";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import { CommentsIcon, RiskIcon } from "@baum/viewer/components/icons";
import { Badge, LiveDot } from "@baum/viewer/components/ui";
import { MenuButton, type MenuItem, type useContextMenu } from "../context-menu";
import { GridIcon } from "./icons";
import { Fade, Quiet, Writing } from "./parts";
import { tileFace } from "./registry";
import { mustShowRisks, type Tile, type TileSize } from "./tile-document";
import type { TileContext } from "./types";

/** The grid's spans: six units once wide, two columns below, one on a phone. */
export const SPAN: Record<TileSize, string> = {
  full: "@min-[36rem]/tiles:col-span-2 @min-[52rem]/tiles:col-span-6",
  half: "@min-[52rem]/tiles:col-span-3",
  third: "@min-[52rem]/tiles:col-span-2",
};

/** One height for each size once the grid has columns, so it keeps its rows
 *  however much a tile has to say; in one column a card is as tall as what it
 *  says, up to that height. What does not fit fades out at the foot. */
export const CARD_HEIGHT: Record<TileSize, string> = {
  full: "max-h-[13rem] @min-[36rem]/tiles:h-[13rem]",
  half: "max-h-[17rem] @min-[36rem]/tiles:h-[17rem]",
  third: "max-h-[16rem] @min-[36rem]/tiles:h-[16rem]",
};

/** A tile on the landing page: its kind, its title and its glance face, the
 *  whole card one link into its detail, with a menu of what else to do. */
export function GlanceCard({
  tile,
  size,
  ctx,
  menu,
  menuItems,
  comments,
  grows = false,
}: {
  tile: Tile;
  size: TileSize;
  ctx: TileContext;
  menu: ReturnType<typeof useContextMenu>;
  menuItems: () => MenuItem[];
  comments: number;
  /** A card in the row grows to show what it must, so this one grows with it
   *  rather than keep its height. */
  grows?: boolean;
}) {
  const titleId = useId();
  const face = tileFace(tile);
  const type = face.type;
  const Icon = type?.icon ?? GridIcon;
  const urgent =
    tile.type === "change" && mustShowRisks(ctx.doc).some((risk) => risk.change === tile.id);
  const tall = face.kind === "ready" && face.type.tall?.(tile, ctx.doc);
  const added = ctx.compare?.added.has(tile.id);
  const changed = ctx.compare?.changed.has(tile.id);
  return (
    <article
      data-review-block=""
      data-tile={tile.id}
      aria-labelledby={titleId}
      onContextMenu={(event) => menu.open(event, menuItems(), tile.title)}
      className={cn(
        "tile-card group relative flex min-w-0 flex-col gap-2.5 rounded-lg border bg-surface-card px-4 pt-3 pb-3.5 outline-none",
        "transition-[border-color,box-shadow] duration-150 hover:border-strong hover:shadow-card motion-reduce:transition-none",
        "data-[review-block-active]:border-focus data-[review-block-active]:shadow-card",
        urgent ? "border-status-critical/40" : "border-default",
        face.kind === "unavailable" && "border-dashed",
        tall || grows ? "@min-[36rem]/tiles:min-h-[17rem]" : CARD_HEIGHT[size],
        SPAN[size],
      )}
    >
      <header className="flex min-h-7 items-center gap-2">
        <Icon size={14} className="flex-none text-tertiary" />
        <span className="min-w-0 truncate text-xs font-medium tracking-[0.08em] text-tertiary uppercase">
          {type?.label ?? "Tile"}
        </span>
        {urgent ? (
          <span role="img" aria-label="Holds a high or critical risk" className="flex-none">
            <RiskIcon size={13} className="text-status-critical" />
          </span>
        ) : null}
        <span className="ml-auto flex flex-none items-center gap-1.5">
          {added ? (
            <Badge size="compact" tone="info">
              New
            </Badge>
          ) : changed ? (
            <Badge size="compact" tone="caution">
              Changed
            </Badge>
          ) : null}
          {tile.state === "pending" ? (
            <span className="flex items-center" title="Being written">
              <LiveDot />
            </span>
          ) : null}
          {comments > 0 ? (
            <span
              className="inline-flex items-center gap-1 text-xs text-secondary tabular-nums"
              title={`${comments} pending ${comments === 1 ? "comment" : "comments"}`}
            >
              <CommentsIcon size={13} />
              {comments}
              <span className="sr-only">{comments === 1 ? " comment" : " comments"}</span>
            </span>
          ) : null}
          <span className="relative z-10">
            <MenuButton label={tile.title} items={menuItems} menu={menu} />
          </span>
        </span>
      </header>
      <h3 id={titleId} className="-mt-1.5 min-w-0">
        <button
          type="button"
          data-block-open=""
          onClick={() => ctx.open(tile.id)}
          className={cn(
            "line-clamp-2 w-full cursor-pointer rounded-sm text-left text-base leading-snug font-semibold text-primary",
            // The whole card answers the click, its menu and retry above it.
            "after:absolute after:inset-0 after:rounded-lg after:content-['']",
            FOCUS_RING,
          )}
        >
          {tile.title}
        </button>
      </h3>
      <Fade>
        <TileBoundary fallback={<Quiet>This tile couldn't be shown.</Quiet>}>
          {face.kind === "ready" ? (
            <face.type.Glance tile={tile} ctx={ctx} />
          ) : face.kind === "pending" ? (
            <Writing />
          ) : (
            <Quiet>{face.why}</Quiet>
          )}
        </TileBoundary>
      </Fade>
    </article>
  );
}

/** Keeps a tile that throws while drawing from taking the page with it. */
export class TileBoundary extends Component<
  { children: ReactNode; fallback: ReactNode },
  { failed: boolean }
> {
  override state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  override render() {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}
