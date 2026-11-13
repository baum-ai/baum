import pluralize from "pluralize";
import { useAttachmentUrl } from "@baum/viewer/host";
import { cn } from "@baum/viewer/components/classnames";
import { ExternalLinkIcon } from "@baum/viewer/components/icons";
import { ReviewImages } from "@baum/viewer/components/review-images";
import { AnchorChips, JumpLink, Part } from "@baum/viewer/components/review-parts";
import { repositoryLink, type Block } from "@baum/viewer/review-document";
import { ImageIcon } from "../icons";
import { Quiet } from "../parts";
import { changeById, type MediaData, type MediaItem } from "../tile-document";
import { isRecord, type TileContext, type TileFaceProps, type TileType } from "../types";

const valid = (data: unknown) =>
  isRecord(data) &&
  Array.isArray(data.items) &&
  data.items.every((item) => isRecord(item) && isRecord(item.block));

type ImagesBlock = Extract<Block, { kind: "images" }>;
type MediaBlock = Extract<Block, { kind: "media" }>;

const itemsOf = (data: unknown) => (data as MediaData).items;

/** Every image the tile holds, in order, with its caption. */
function images(items: MediaItem[]) {
  return items.flatMap((item) =>
    item.block.kind === "images" ? (item.block as ImagesBlock).items : [],
  );
}

const THUMBNAILS = 3;

/** The first images as thumbnails and how many there are, so a reviewer sees
 *  whether a screenshot is worth opening. */
function MediaGlance({ tile, ctx }: TileFaceProps) {
  const items = itemsOf(tile.data);
  const url = useAttachmentUrl();
  const all = images(items);
  const links = items.filter((item) => item.block.kind === "media").length;
  if (all.length === 0 && links === 0) return <Quiet>No screenshots or recordings.</Quiet>;
  const alt = (id: string) => ctx.review.index.attachments?.find((each) => each.id === id)?.alt;
  const shown = all.slice(0, THUMBNAILS);
  return (
    <div className="flex h-full min-h-0 flex-col gap-2.5">
      {url && shown.length > 0 ? (
        <ul
          className={cn(
            "grid min-h-0 flex-1 gap-1.5",
            shown.length === 1 ? "grid-cols-1" : shown.length === 2 ? "grid-cols-2" : "grid-cols-3",
          )}
        >
          {shown.map((image, position) => (
            <li
              key={`${image.attachment}-${position}`}
              className="relative min-h-0 overflow-hidden rounded-md border border-subtle bg-surface-sunken"
            >
              <img
                src={url(image.attachment, ctx.review)}
                alt={image.caption ?? alt(image.attachment) ?? "Screenshot"}
                loading="lazy"
                className="h-full w-full object-cover object-top"
              />
              {position === shown.length - 1 && all.length > shown.length ? (
                <span className="absolute inset-0 flex items-center justify-center bg-surface-scrim text-base font-semibold text-primary">
                  +{all.length - shown.length}
                </span>
              ) : null}
            </li>
          ))}
        </ul>
      ) : null}
      <p className="text-xs text-tertiary">
        {[
          all.length > 0 ? pluralize("image", all.length, true) : "",
          links > 0 ? pluralize("recording", links, true) : "",
        ]
          .filter(Boolean)
          .join(" · ")}
        {shown[0]?.caption ? ` · ${shown[0].caption}` : ""}
      </p>
    </div>
  );
}

/** Every image and recording, grouped by the change that carries it. */
function MediaDetail({ tile, ctx }: TileFaceProps) {
  const items = itemsOf(tile.data);
  if (items.length === 0) return <Quiet>No screenshots or recordings.</Quiet>;
  const groups = [...new Set(items.map((item) => item.change ?? ""))].map((change) => ({
    change,
    items: items.filter((item) => (item.change ?? "") === change),
  }));
  return (
    <div className="space-y-8">
      {groups.map((group) => (
        <MediaGroup key={group.change || "document"} ctx={ctx} {...group} />
      ))}
    </div>
  );
}

function MediaGroup({
  ctx,
  change,
  items,
}: {
  ctx: TileContext;
  change: string;
  items: MediaItem[];
}) {
  const title = change ? changeById(ctx.doc, change)?.title : undefined;
  return (
    <Part
      label={title ?? "The pull request"}
      aside={
        change ? (
          <JumpLink onClick={() => ctx.openChange(change)}>Open the change →</JumpLink>
        ) : undefined
      }
    >
      <div className="space-y-5">
        {items.map((item, position) =>
          item.block.kind === "images" ? (
            <ReviewImages key={position} document={ctx.review} block={item.block as ImagesBlock} />
          ) : item.block.kind === "media" ? (
            <MediaLink key={position} ctx={ctx} block={item.block as MediaBlock} />
          ) : null,
        )}
      </div>
    </Part>
  );
}

function MediaLink({ ctx, block }: { ctx: TileContext; block: MediaBlock }) {
  const href = repositoryLink(ctx.review, block.url);
  return (
    <div className="space-y-2">
      {href ? (
        <a
          href={href}
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1 text-base"
        >
          {block.caption ?? "Open attachment"}
          <ExternalLinkIcon size={10} />
        </a>
      ) : (
        <p className="text-base text-primary">{block.caption ?? "Attachment"}</p>
      )}
      <AnchorChips document={ctx.review} anchors={block.source} />
    </div>
  );
}

export const mediaTile: TileType = {
  type: "media",
  label: "Media",
  icon: ImageIcon,
  Glance: MediaGlance,
  Detail: MediaDetail,
  valid,
};
