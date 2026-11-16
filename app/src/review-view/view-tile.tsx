import { cn } from "@baum/viewer/components/classnames";
import { InlineMarkdown } from "@baum/viewer/components/markdown";
import { AnchorChips } from "@baum/viewer/components/review-parts";
import { LiveDot } from "@baum/viewer/components/ui";
import type { Anchor, ReviewDocument } from "@baum/viewer/review-document";
import { TileBoundary } from "../tiles/card";
import { tileFace } from "../tiles/registry";
import type { Tile } from "../tiles/tile-document";
import type { TileContext } from "../tiles/types";
import { NATIVE } from "./native-tiles";
import type { ViewTile } from "./model";

/** The tile's place on the page, which links and the keys reach. */
export const tileElementId = (prefix: string, tile: string) => `${prefix}tile-${tile}`;

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

/** One tile on the page, always under its title: a native diagram or table
 *  drawn here, an exhibit as the generic tiles draw it, and a library or
 *  chat-made tile in its sandboxed frame. A tile being written, one that
 *  failed and one that cannot be drawn say so in its place; the rest of the
 *  page is unaffected. `ctx` is what the generic and custom tiles read. */
export function ViewTileCard({
  tile,
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
  const native = NATIVE[tile.type];
  const title = tile.title.trim() || "Untitled diagram";
  const body = () => {
    if (tile.state === "pending")
      return (
        <p role="status" className="flex items-center gap-2 text-base text-secondary">
          <LiveDot />
          Being written
        </p>
      );
    if (tile.state === "failed") return <Note>This tile couldn't be written.</Note>;
    if (tile.state === "unavailable")
      return <Note>{tile.problems?.[0] ?? "This tile isn't available."}</Note>;
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
          {anchors.length > 0 ? <AnchorChips document={review} anchors={anchors} /> : null}
        </>
      );
    }
    // Exhibits (code, prose) and library or chat-made tiles: as the tiles
    // page drew their details.
    const face = tileFace(tile as Tile);
    if (face.kind === "pending")
      return (
        <p role="status" className="flex items-center gap-2 text-base text-secondary">
          <LiveDot />
          Being written
        </p>
      );
    if (face.kind === "unavailable") return <Note>{face.why}</Note>;
    return <face.type.Detail tile={tile as Tile} ctx={ctx} />;
  };
  return (
    // A figure by its role: the element's own margins would fight the
    // spacing of the list it stands in.
    <div
      role="figure"
      id={tileElementId(idPrefix, tile.id)}
      data-tile={tile.id}
      data-tile-type={tile.type}
      data-review-block=""
      aria-label={title}
      className={cn(
        "min-w-0 scroll-mt-4 space-y-3 rounded-lg border border-default bg-surface-card p-4 outline-none",
        "data-[review-block-active]:border-strong",
      )}
    >
      <Heading className="text-base font-semibold text-primary">{title}</Heading>
      <TileBoundary fallback={<Note>This tile couldn't be shown.</Note>}>{body()}</TileBoundary>
    </div>
  );
}
