import { useEffect, useId, useRef, useState } from "react";

import type { Block, IndexedAttachment, ReviewDocument } from "../review-document";
import { layoutImages, sectionImageHeight } from "../review-image-layout";
import { useAttachmentUrl } from "../host";
import { cn, FOCUS_RING } from "./classnames";
import { ImageViewer } from "./image-viewer";
import { AnchorChips, Part } from "./review-parts";
import { Button } from "./ui";

type ImagesBlock = Extract<Block, { kind: "images" }>;

type Shown = { key: string; attachment: IndexedAttachment; text?: string };

type Space = { pane: number | null; viewport: { width: number; height: number } };

/** Description screenshots placed in a section. Each draws from the stored
 *  copy the document's own index lists, so an older version keeps its images
 *  after the description drops one. Neighbours of similar proportions share a
 *  row when the pane is wide enough; the rest take a row each. An image that
 *  fails to load says so in its place and can be retried. */
export function ReviewImages({
  document,
  block,
  heading = "Images",
}: {
  document: ReviewDocument;
  block: ImagesBlock;
  /** The heading over the images. */
  heading?: string;
}) {
  const attachmentUrl = useAttachmentUrl();
  const [failed, setFailed] = useState<ReadonlySet<string>>(() => new Set());
  // Retrying asks for the image under a new address, so no cached failure
  // answers it.
  const [attempts, setAttempts] = useState<ReadonlyMap<string, number>>(() => new Map());
  const [viewing, setViewing] = useState<Shown | null>(null);
  const [pane, setPane] = useState<HTMLElement | null>(null);
  const [space, setSpace] = useState<Space>({
    pane: null,
    viewport: { width: 1280, height: 900 },
  });
  const triggers = useRef(new Map<string, HTMLButtonElement>());
  const images = useRef(new Map<HTMLImageElement, string>());
  // Retrying swaps the placeholder for the image and a repeat failure swaps
  // it back, so focus follows to whichever control replaces the focused one.
  const focusNext = useRef<string | null>(null);
  const idPrefix = useId();
  const takeFocus = (target: string) => (element: HTMLButtonElement | null) => {
    if (element && focusNext.current === target) {
      focusNext.current = null;
      element.focus();
    }
  };

  // An image that failed before hydration never fires onError.
  useEffect(() => {
    const broken = [...images.current]
      .filter(([image]) => image.complete && image.naturalWidth === 0)
      .map(([, id]) => id);
    if (broken.length > 0) setFailed((current) => new Set([...current, ...broken]));
  }, []);

  useEffect(() => {
    if (!pane) return;
    const measure = () =>
      setSpace({
        pane: pane.clientWidth,
        viewport: { width: window.innerWidth, height: window.innerHeight },
      });
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(pane);
    window.addEventListener("resize", measure);
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", measure);
    };
  }, [pane]);

  const listed = new Map((document.index.attachments ?? []).map((item) => [item.id, item]));
  const shown: Shown[] = attachmentUrl
    ? block.items.flatMap((item, position) => {
        const attachment = listed.get(item.attachment);
        if (!attachment) return [];
        return [
          {
            key: `${position}-${item.attachment}`,
            attachment,
            text: item.caption || attachment.alt,
          },
        ];
      })
    : [];
  if (!attachmentUrl || shown.length === 0) return null;

  const source = (id: string) => {
    const url = attachmentUrl(id, document);
    const attempt = attempts.get(id);
    return attempt ? retryImageURL(url, attempt) : url;
  };
  const retry = (id: string, key: string) => {
    focusNext.current = `view:${key}`;
    setAttempts((current) => new Map(current).set(id, (current.get(id) ?? 0) + 1));
    setFailed((current) => {
      const next = new Set(current);
      next.delete(id);
      return next;
    });
  };
  const rows = layoutImages(
    shown.map((item) => item.attachment),
    space.pane,
    sectionImageHeight(space.viewport.height),
  );

  return (
    <Part label={heading}>
      <div ref={setPane} className="space-y-4">
        {rows.map((row) => (
          <div
            key={row.map(({ index }) => shown[index]?.key).join(" ")}
            className="flex items-start gap-4"
          >
            {row.map(({ index, width }) => {
              const item = shown[index] as Shown;
              const label = item.text ?? `image ${item.attachment.position}`;
              return (
                <figure key={item.key} className="min-w-0 max-w-full space-y-1.5" style={{ width }}>
                  {failed.has(item.attachment.id) ? (
                    <div className="flex min-h-24 flex-col items-center justify-center gap-2 rounded-md border border-dashed border-default bg-surface-sunken p-3 text-center text-base text-tertiary">
                      <span id={`${idPrefix}-${item.key}`} role="status">
                        Image unavailable
                      </span>
                      <Button
                        ref={takeFocus(`retry:${item.key}`)}
                        aria-describedby={`${idPrefix}-${item.key}`}
                        size="compact"
                        aria-label={`Retry loading ${label}`}
                        onClick={() => retry(item.attachment.id, item.key)}
                      >
                        Retry
                      </Button>
                    </div>
                  ) : (
                    <button
                      type="button"
                      ref={(element) => {
                        if (element) triggers.current.set(item.key, element);
                        else triggers.current.delete(item.key);
                        takeFocus(`view:${item.key}`)(element);
                      }}
                      aria-label={`View full size: ${label}`}
                      onClick={() => setViewing(item)}
                      className={cn(
                        "block w-full cursor-zoom-in overflow-hidden rounded-md border border-default bg-surface-sunken",
                        FOCUS_RING,
                      )}
                    >
                      <img
                        ref={(element) => {
                          if (!element) return;
                          images.current.set(element, item.attachment.id);
                          return () => {
                            images.current.delete(element);
                          };
                        }}
                        src={source(item.attachment.id)}
                        alt={item.attachment.alt || item.text || ""}
                        width={item.attachment.width}
                        height={item.attachment.height}
                        loading="lazy"
                        decoding="async"
                        onError={() => {
                          if (
                            globalThis.document.activeElement === triggers.current.get(item.key)
                          ) {
                            focusNext.current = `retry:${item.key}`;
                          }
                          setFailed((current) => new Set(current).add(item.attachment.id));
                        }}
                        className="block h-auto w-full"
                      />
                    </button>
                  )}
                  {item.text && (
                    <figcaption className="text-base text-tertiary">{item.text}</figcaption>
                  )}
                  <ImageProvenance document={document} attachment={item.attachment} />
                </figure>
              );
            })}
          </div>
        ))}
      </div>
      <AnchorChips document={document} anchors={block.source} className="mt-2" />
      <ImageViewer
        image={
          viewing && {
            src: source(viewing.attachment.id),
            alt: viewing.attachment.alt || viewing.text || "",
            width: viewing.attachment.width,
            height: viewing.attachment.height,
          }
        }
        label={viewing?.text ?? "Image"}
        caption={
          viewing && (
            <>
              {viewing.text}
              <ImageProvenance document={document} attachment={viewing.attachment} />
            </>
          )
        }
        onClose={() => {
          const key = viewing?.key;
          setViewing(null);
          if (key) triggers.current.get(key)?.focus();
        }}
      />
    </Part>
  );
}

function ImageProvenance({
  document,
  attachment,
}: {
  document: ReviewDocument;
  attachment: IndexedAttachment;
}) {
  if (!attachment.origins?.length) return null;
  return (
    <div className="space-y-1 text-base text-tertiary" aria-label="Image provenance">
      {attachment.origins.map((origin, index) => (
        <div key={`${origin.session}-${origin.tool || origin.input}-${origin.position}-${index}`}>
          <span title={origin.tool}>
            {origin.kind === "human_input" ? "Human-supplied image" : "Agent tool result"}
            {origin.tool_name ? ` · ${origin.tool_name}` : ""}
            {origin.child_session ? " · Subagent" : ""}
          </span>
          <AnchorChips
            document={document}
            anchors={[
              { type: "prompt", session: origin.session, index: origin.turn, input: origin.input },
            ]}
          />
        </div>
      ))}
    </div>
  );
}

function retryImageURL(url: string, attempt: number): string {
  const fragmentAt = url.indexOf("#");
  const fragment = fragmentAt < 0 ? "" : url.slice(fragmentAt);
  const target = fragmentAt < 0 ? url : url.slice(0, fragmentAt);
  const queryAt = target.indexOf("?");
  const path = queryAt < 0 ? target : target.slice(0, queryAt);
  const query = new URLSearchParams(queryAt < 0 ? "" : target.slice(queryAt + 1));
  query.set("attempt", String(attempt));
  return `${path}?${query}${fragment}`;
}
