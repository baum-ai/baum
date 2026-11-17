import { createPortal } from "react-dom";
import { cn } from "@baum/viewer/components/classnames";
import { InlineMarkdown } from "@baum/viewer/components/markdown";
import { Cites } from "./cites";
import { LiveDot } from "@baum/viewer/components/ui";
import type { Anchor, ReviewDocument } from "@baum/viewer/review-document";
import { MenuButton, useContextMenu, type MenuItem } from "../context-menu";
import { TileBoundary } from "../tiles/boundary";
import { isBuiltinType } from "../tiles/custom/library";
import { useTileHost } from "../tiles/host";
import { tileFace } from "../tiles/registry";
import type { TileContext } from "../tiles/types";
import { NATIVE } from "./native-tiles";
import { addedNote, type ViewTile } from "./model";

/** The tile's place on the page, which links and the keys reach. */
export const tileElementId = (prefix: string, tile: string) => `${prefix}tile-${tile}`;

/** The tile the keys (j and k) are on, if any. */
export function activeTile(): string | null {
  return (
    document.querySelector<HTMLElement>("[data-review-block-active][data-tile]")?.dataset.tile ??
    null
  );
}

const anchorsOf = (data: unknown): Anchor[] => {
  const source = (data as { source?: unknown } | null)?.source;
  return Array.isArray(source) ? (source as Anchor[]) : [];
};
const captionOf = (data: unknown): string => {
  const caption = (data as { caption?: unknown } | null)?.caption;
  return typeof caption === "string" ? caption.trim() : "";
};

function Note({ children, alert = false }: { children: string; alert?: boolean }) {
  return (
    <p
      role={alert ? "alert" : undefined}
      className="rounded-md border border-dashed border-default px-3 py-2 text-base text-secondary"
    >
      {children}
    </p>
  );
}

/** The tile as the page draws it. baum.review/2 names a library or saved
 *  type by its own name (`schema_change`) and carries its definition: any
 *  tile with a definition draws in its frame, as a custom tile. Exhibits
 *  converted from baum.review/1 (`v1_table`, `v1_code`, `v1_prose`) draw as
 *  the table, code and prose tiles do. */
export function drawnAs(tile: ViewTile): ViewTile {
  if (tile.type.startsWith("v1_")) {
    const type = tile.type.slice(3);
    if (type === "table" || type === "code" || type === "prose") return { ...tile, type };
  }
  if (tile.definition && !NATIVE[tile.type] && tile.type !== "custom")
    return { ...tile, type: "custom" };
  return tile;
}

/** One tile on the page, always under its title: a native diagram or table
 *  drawn here, an exhibit as the generic tiles draw it, and a library or
 *  chat-made tile in its sandboxed frame. A tile being written, one that
 *  failed and one that cannot be drawn say so in its place; the rest of the
 *  page is unaffected. `ctx` is what the generic and custom tiles read. */
export function ViewTileCard({
  tile: given,
  review,
  ctx,
  idPrefix = "",
  heading = "h3",
}: {
  tile: ViewTile;
  review: ReviewDocument;
  ctx: TileContext;
  idPrefix?: string;
  heading?: "h2" | "h3";
}) {
  const Heading = heading;
  const tile = drawnAs(given);
  const native = NATIVE[tile.type];
  const title = tile.title.trim() || "Untitled diagram";
  const host = useTileHost();
  const menu = useContextMenu();
  // An earlier version's tiles (their IDs prefixed) are read, never changed.
  const current = idPrefix === "" && !host.draft;
  const menuItems = (): MenuItem[] => [
    ...(host.comment && current
      ? [{ label: "Comment", hint: "c", onSelect: () => host.comment?.(tile.id) }]
      : []),
    ...(host.ask ? [{ label: "Ask about this tile", onSelect: () => host.ask?.(tile) }] : []),
    ...(host.saveToLibrary && tile.type === "custom" && !isBuiltinType(tile) && current
      ? [{ label: "Save to library…", onSelect: () => host.saveToLibrary?.(tile) }]
      : []),
    ...(host.removeAdded && tile.added && current
      ? [{ label: "Remove", danger: true, onSelect: () => host.removeAdded?.(tile) }]
      : []),
  ];
  const note = tile.added ? addedNote(tile) : null;
  const body = () => {
    if (tile.state === "pending")
      return (
        <p role="status" className="flex items-center gap-2 text-base text-secondary">
          <LiveDot />
          {tile.filling ? "Filling this tile…" : "Being written"}
        </p>
      );
    if (tile.state === "failed") return <Note>This tile couldn't be written.</Note>;
    // A frame's or exhibit's type may say why in its own words (a tile made
    // with a newer kit); a native tile says the daemon's.
    if (tile.state === "unavailable") {
      const face = native ? null : tileFace(tile);
      return (
        <Note>
          {face?.kind === "unavailable"
            ? face.why
            : (tile.problems?.[0] ?? "This tile isn't available.")}
        </Note>
      );
    }
    if (native) {
      if (!native.valid(tile.data)) return <Note>This tile's content couldn't be read.</Note>;
      const caption = captionOf(tile.data);
      const anchors = anchorsOf(tile.data);
      return (
        <>
          <div className="@container min-w-0">{native.draw(tile.data)}</div>
          {caption ? (
            <InlineMarkdown className="block text-xs leading-snug text-tertiary">
              {caption}
            </InlineMarkdown>
          ) : null}
          <Cites document={review} anchors={anchors} />
        </>
      );
    }
    // Exhibits (code, prose) and library or chat-made tiles: as the tiles
    // page drew their details.
    const face = tileFace(tile);
    if (face.kind === "pending")
      return (
        <p role="status" className="flex items-center gap-2 text-base text-secondary">
          <LiveDot />
          Being written
        </p>
      );
    if (face.kind === "unavailable") return <Note>{face.why}</Note>;
    return <face.type.Detail tile={tile} ctx={ctx} />;
  };
  const items = menuItems();
  return (
    // A figure by its role: the element's own margins would fight the
    // spacing of the list it stands in.
    <div
      role="figure"
      id={tileElementId(idPrefix, tile.id)}
      data-tile={tile.id}
      data-tile-type={tile.type}
      data-review-block=""
      data-tile-added={tile.added ? "" : undefined}
      aria-label={title}
      onContextMenu={items.length > 0 ? (event) => menu.open(event, items, title) : undefined}
      className={cn(
        "view-tile min-w-0 scroll-mt-4 space-y-3 rounded-lg border border-default bg-surface-card p-4 outline-none",
        "data-[review-block-active]:border-strong",
      )}
    >
      <div className="flex items-start gap-2">
        <div className="min-w-0 flex-1 space-y-0.5">
          <Heading className="text-base font-semibold text-primary">
            <InlineMarkdown>{title}</InlineMarkdown>
          </Heading>
          {note ? (
            <p data-added-note="" className="text-xs text-tertiary">
              {note}
            </p>
          ) : null}
        </div>
        {items.length > 0 ? (
          <span className="flex-none">
            <MenuButton label={title} items={menuItems} menu={menu} />
          </span>
        ) : null}
      </div>
      <TileBoundary fallback={<Note>This tile couldn't be shown.</Note>}>{body()}</TileBoundary>
      {/* On the body: the tab panel is a size container, which would hold
          the menu's fixed box inside it and scroll it away. */}
      {menu.menu ? createPortal(menu.menu, document.body) : null}
    </div>
  );
}
