import { useEffect, useId, useRef, useState } from "react";

import type { Block, IndexedAttachment, ReviewDocument } from "../review-document";
import { layoutImages, sectionImageHeight, viewerImageWidth } from "../review-image-layout";
import { useAttachmentUrl } from "../host";
import { cn, FOCUS_RING } from "./classnames";
import { CloseIcon, ExternalLinkIcon } from "./icons";
import { AnchorChips, Part } from "./review-parts";
import { Button, IconButton } from "./ui";

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
}: {
  document: ReviewDocument;
  block: ImagesBlock;
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
  const dialog = useRef<HTMLDialogElement>(null);
  const header = useRef<HTMLDivElement>(null);
  // The viewer's header wraps with a long caption; the image fits below it.
  const [headerHeight, setHeaderHeight] = useState(58);
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

  useEffect(() => {
    const element = dialog.current;
    if (!element) return;
    if (viewing && !element.open) element.showModal();
    if (!viewing && element.open) element.close();
    // Only an open dialog has a laid-out header to measure. It is watched
    // while open, as a resize or rotation can wrap its caption.
    const caption = header.current;
    if (!viewing || !caption) return;
    const measure = () => {
      if (caption.offsetHeight) setHeaderHeight(caption.offsetHeight);
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(caption);
    return () => observer.disconnect();
  }, [viewing]);

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
    <Part label="Images">
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
                    <div className="flex min-h-24 flex-col items-center justify-center gap-2 rounded-(--radius-md) border border-dashed border-(--border-default) bg-(--surface-sunken) p-3 text-center text-base text-(--text-tertiary)">
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
                        "block w-full cursor-zoom-in overflow-hidden rounded-(--radius-md) border border-(--border-default) bg-(--surface-sunken)",
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
                    <figcaption className="text-base text-(--text-tertiary)">
                      {item.text}
                    </figcaption>
                  )}
                </figure>
              );
            })}
          </div>
        ))}
      </div>
      <AnchorChips document={document} anchors={block.source} className="mt-2" />
      <dialog
        ref={dialog}
        aria-label={viewing?.text ?? "Image"}
        onClose={() => {
          const key = viewing?.key;
          setViewing(null);
          if (key) triggers.current.get(key)?.focus();
        }}
        className={cn(
          "m-auto max-h-[calc(100dvh-2rem)] max-w-[calc(100vw-2rem)] overflow-auto rounded-(--radius-lg) border border-(--border-default)",
          "bg-(--surface-card) p-3 text-(--text-primary) shadow-(--shadow-card)",
          "backdrop:bg-(--surface-scrim)",
        )}
      >
        {viewing && (
          <figure className="space-y-2">
            <div
              ref={header}
              className="sticky -top-3 z-10 -mx-3 -mt-3 flex items-start justify-between gap-3 bg-(--surface-card) px-3 pt-3 pb-2"
            >
              <figcaption className="min-w-0 pt-1 text-base text-(--text-secondary)">
                {viewing.text}
              </figcaption>
              <div className="flex flex-none items-center gap-2">
                <a
                  href={source(viewing.attachment.id)}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center gap-1 text-base"
                >
                  Open original
                  <ExternalLinkIcon size={10} />
                </a>
                <IconButton label="Close image" autoFocus onClick={() => dialog.current?.close()}>
                  <CloseIcon size={14} />
                </IconButton>
              </div>
            </div>
            <img
              src={source(viewing.attachment.id)}
              alt={viewing.attachment.alt || viewing.text || ""}
              width={viewing.attachment.width}
              height={viewing.attachment.height}
              style={{
                width: viewerImageWidth(viewing.attachment, space.viewport, headerHeight),
              }}
              className="block h-auto max-w-full"
            />
          </figure>
        )}
      </dialog>
    </Part>
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
