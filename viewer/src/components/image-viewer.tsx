import { type PointerEvent, type ReactNode, useEffect, useId, useRef } from "react";

import { type ImageSize, viewerImageWidth } from "../review-image-layout";
import { useStoredSetting } from "../use-stored-setting";
import { cn } from "./classnames";
import { ExternalLinkIcon, FitScreenIcon } from "./icons";
import { CloseButton, IconButton } from "./ui";

/** An image to show large: where it is served, its description, and its
 *  pixel size when the host knows it (otherwise it is read once it loads). */
export type ViewedImage = { src: string; alt: string; width?: number; height?: number };

/** Where the scale the person last dragged an image to is remembered: the
 *  share of the screen's fit the image took, so images of any shape and
 *  screens of any size open at the same share. */
const SCALE_KEY = "baum.image-viewer.scale";

/** The remembered scale, or 1 (the full fit) when there is none or it is
 *  unreadable. */
function storedScale(value: string | null): number {
  const scale = Number(value);
  return value && Number.isFinite(scale) && scale > 0 ? Math.min(1, scale) : 1;
}

/** A modal viewer for one image. It opens as large as the screen allows,
 *  past the image's own pixels, and keeps its proportions. Any edge or
 *  corner drags to resize the image; the share of the screen dragged to is remembered,
 *  and later images open at it; "Fit to screen" returns to the full fit and
 *  forgets it. `image` set opens it and `null` closes it; `onClose`
 *  follows any close, including Escape. */
export function ImageViewer({
  image,
  label,
  caption,
  onClose,
}: {
  image: ViewedImage | null;
  /** The dialog's accessible name. */
  label: string;
  /** Shown above the image, beside the controls. */
  caption?: ReactNode;
  onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const header = useRef<HTMLDivElement>(null);
  const frame = useRef<HTMLDivElement>(null);
  const closer = useRef<HTMLButtonElement>(null);
  const captionId = useId();
  const [stored, store] = useStoredSetting(SCALE_KEY);
  const scale = useRef(1);
  scale.current = storedScale(stored);
  // The size the frame fits to, until the person drags an edge; from then
  // on the size is theirs.
  const fitted = useRef<ImageSize | null>(null);
  const dragged = useRef(false);
  // The drag under way: the pointer, where it pressed, the frame's width
  // then and the handle it holds.
  const drag = useRef<{
    pointer: number;
    x: number;
    y: number;
    width: number;
    along: Handle;
  } | null>(null);

  // The header's height at the full fit: one row beside the controls when
  // the image is wide enough. Remembered shares are of that fit, so a
  // smaller image whose header wraps still opens at the same share.
  const fittedHeader = useRef(0);
  const fit = (size: ImageSize) => {
    fitted.current = size;
    fitFrame(frame.current, header.current, size);
    fittedHeader.current = header.current?.offsetHeight ?? 0;
    if (frame.current && scale.current < 1) {
      const full = viewerImageWidth(size, viewport(), fittedHeader.current);
      frame.current.style.width = `${Math.max(1, Math.floor(full * scale.current))}px`;
      // Narrower, the header may wrap and take room from the image.
      const room = viewerImageWidth(size, viewport(), header.current?.offsetHeight ?? 0);
      if (room < frame.current.offsetWidth)
        frame.current.style.width = `${Math.max(1, Math.floor(room))}px`;
    }
  };

  // Hosts pass a fresh object each render; only a different image refits,
  // so a re-render keeps the size the person dragged to.
  const src = image?.src;
  const width = image?.width;
  const height = image?.height;
  useEffect(() => {
    const element = dialog.current;
    if (!element) return;
    if (src && !element.open) {
      element.showModal();
      // The close control takes focus, not the first link in the header.
      closer.current?.focus();
    }
    if (!src && element.open) element.close();
    dragged.current = false;
    drag.current = null;
    fitted.current = null;
    if (src && width && height) {
      fit({ width, height });
    }
  }, [src, width, height]);

  // A window that changes size, or a phone that turns, refits the image
  // until the person has sized it themselves.
  useEffect(() => {
    if (!src) return;
    const refit = () => {
      if (!dragged.current && fitted.current) {
        fit(fitted.current);
      }
    };
    window.addEventListener("resize", refit);
    return () => window.removeEventListener("resize", refit);
  }, [src]);

  // A click on the backdrop, outside the dialog's box, closes it; Escape is
  // the keyboard's way out. The press must start there too, as a drag of
  // an edge can end outside.
  useEffect(() => {
    const element = dialog.current;
    if (!element) return;
    let pressed = false;
    const down = (event: globalThis.PointerEvent) => {
      pressed = event.target === element && outside(element, event);
    };
    const click = (event: MouseEvent) => {
      if (pressed && outside(element, event)) closeRef.current();
      pressed = false;
    };
    element.addEventListener("pointerdown", down);
    element.addEventListener("click", click);
    return () => {
      element.removeEventListener("pointerdown", down);
      element.removeEventListener("click", click);
    };
  }, []);

  const close = () => dialog.current?.close();

  // One click back to the full fit, forgetting the remembered share.
  const fitToScreen = () => {
    store("1");
    scale.current = 1;
    dragged.current = false;
    if (fitted.current) fit(fitted.current);
  };
  const closeRef = useRef(close);
  closeRef.current = close;

  // A press on any edge or corner of the image resizes it. The dialog stays
  // centred, so it grows on both sides at once: the frame changes by twice
  // the pointer's movement, which keeps the handle under the pointer. It
  // grows no further than the screen's fit and keeps the image's
  // proportions.
  const press = (along: Handle) => (event: PointerEvent<HTMLDivElement>) => {
    const target = frame.current;
    if (!target || !fitted.current || event.button !== 0 || drag.current) return;
    event.preventDefault();
    event.stopPropagation();
    event.currentTarget.setPointerCapture?.(event.pointerId);
    drag.current = {
      pointer: event.pointerId,
      x: event.clientX,
      y: event.clientY,
      width: target.offsetWidth,
      along,
    };
  };
  const move = (event: PointerEvent<HTMLDivElement>) => {
    const from = drag.current;
    const size = fitted.current;
    if (!from || from.pointer !== event.pointerId || !size || !frame.current) return;
    const across = 2 * from.along.x * (event.clientX - from.x);
    const down = (2 * from.along.y * (event.clientY - from.y) * size.width) / size.height;
    const change = Math.abs(across) >= Math.abs(down) ? across : down;
    const full = Math.floor(viewerImageWidth(size, viewport(), fittedHeader.current));
    const next = Math.round(Math.min(full, Math.max(MIN_WIDTH, from.width + change)));
    frame.current.style.width = `${next}px`;
  };
  const release = (event: PointerEvent<HTMLDivElement>) => {
    const from = drag.current;
    if (from?.pointer !== event.pointerId) return;
    const size = fitted.current;
    drag.current = null;
    if (!from || !size || !frame.current) return;
    // A press that moves the image a pixel or less is not a drag.
    const drawn = frame.current.offsetWidth;
    if (Math.abs(drawn - from.width) <= 1) {
      frame.current.style.width = `${from.width}px`;
      return;
    }
    dragged.current = true;
    // Later images open at the same share of the screen's fit.
    const full = viewerImageWidth(size, viewport(), fittedHeader.current);
    store(String(Math.min(1, drawn / full)));
  };

  return (
    <dialog
      ref={dialog}
      aria-label={label}
      aria-describedby={caption ? captionId : undefined}
      onClose={() => {
        frame.current?.removeAttribute("style");
        onClose();
      }}
      className={cn(
        "m-auto max-h-[calc(100dvh-2rem)] w-fit min-w-72 max-w-[calc(100vw-2rem)] overflow-hidden rounded-lg border border-default open:flex",
        "bg-surface-card p-3 text-primary shadow-card",
        "backdrop:bg-surface-scrim",
      )}
    >
      {image && (
        <div className="flex min-h-0 min-w-0 flex-auto flex-col gap-2 bg-surface-card">
          {/* The header takes the image's width rather than widening the
              dialog past it. Where that leaves too little room beside the
              controls, as on a phone or above a tall, narrow image, the
              caption takes its own row under them. */}
          <div ref={header} className="@container flex-none">
            <div className="flex flex-col-reverse gap-2 @md:flex-row @md:items-start @md:justify-between @md:gap-3">
              <div id={captionId} className="min-w-0 pt-1 text-base text-secondary">
                {caption}
              </div>
              <div className="flex flex-none items-center gap-2 self-end @md:self-auto">
                <a
                  href={image.src}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center gap-1 text-base"
                >
                  Open original
                  <ExternalLinkIcon size={10} />
                </a>
                <IconButton label="Fit to screen" onClick={fitToScreen}>
                  <FitScreenIcon />
                </IconButton>
                <CloseButton ref={closer} label="Close image" onClick={close} />
              </div>
            </div>
          </div>
          <div
            ref={frame}
            data-image-frame=""
            // The frame takes the image's proportions, so dragging any of
            // its edges or corners scales the image. It shrinks with the
            // dialog when the window does, letterboxing the image on the
            // sunken surface.
            className="group relative min-h-0 min-w-0 max-w-full shrink self-center overflow-hidden rounded-md bg-surface-sunken"
          >
            <img
              src={image.src}
              alt={image.alt}
              width={image.width}
              height={image.height}
              decoding="async"
              onLoad={(event) => {
                if (image.width && image.height) return;
                const { naturalWidth, naturalHeight } = event.currentTarget;
                fit({ width: naturalWidth, height: naturalHeight });
              }}
              className="block size-full object-contain"
            />
            {HANDLES.map((handle) => (
              <div
                key={handle.name}
                data-resize={handle.name}
                aria-hidden
                onPointerDown={press(handle)}
                onPointerMove={move}
                onPointerUp={release}
                onPointerCancel={release}
                onLostPointerCapture={release}
                className={cn("absolute touch-none", handle.className)}
              />
            ))}
          </div>
        </div>
      )}
    </dialog>
  );
}

/** Whether a pointer event falls outside an element's box. */
function outside(element: Element, event: { clientX: number; clientY: number }) {
  const box = element.getBoundingClientRect();
  return (
    event.clientX < box.left ||
    event.clientX > box.right ||
    event.clientY < box.top ||
    event.clientY > box.bottom
  );
}

const viewport = () => ({ width: window.innerWidth, height: window.innerHeight });

/** The narrowest a drag makes the image. */
const MIN_WIDTH = 64;

/** A place on the frame that resizes it: which way it faces across (`x`)
 *  and down (`y`), each -1, 0 or 1. */
type Handle = { name: string; x: number; y: number; className: string };

/** The frame's corners carry a visible bracket, so people see the image
 *  resizes; its edges resize too, shown by the pointer. */
const CORNER =
  "z-10 size-5 border-surface-card opacity-80 drop-shadow-[0_0_1px_var(--surface-inverse)] transition-opacity group-hover:opacity-100";
const HANDLES: Handle[] = [
  {
    name: "top-left",
    x: -1,
    y: -1,
    className: `${CORNER} left-1 top-1 cursor-nwse-resize rounded-tl-sm border-l-2 border-t-2`,
  },
  {
    name: "top-right",
    x: 1,
    y: -1,
    className: `${CORNER} right-1 top-1 cursor-nesw-resize rounded-tr-sm border-r-2 border-t-2`,
  },
  {
    name: "bottom-left",
    x: -1,
    y: 1,
    className: `${CORNER} bottom-1 left-1 cursor-nesw-resize rounded-bl-sm border-b-2 border-l-2`,
  },
  {
    name: "bottom-right",
    x: 1,
    y: 1,
    className: `${CORNER} bottom-1 right-1 cursor-nwse-resize rounded-br-sm border-b-2 border-r-2`,
  },
  { name: "top", x: 0, y: -1, className: "inset-x-6 top-0 h-2 cursor-ns-resize" },
  { name: "bottom", x: 0, y: 1, className: "inset-x-6 bottom-0 h-2 cursor-ns-resize" },
  { name: "left", x: -1, y: 0, className: "inset-y-6 left-0 w-2 cursor-ew-resize" },
  { name: "right", x: 1, y: 0, className: "inset-y-6 right-0 w-2 cursor-ew-resize" },
];

/** Sizes the image's frame to fill the screen below the header. The
 *  header wraps to the frame's width, so the fit runs again at its new
 *  height. */
function fitFrame(frame: HTMLElement | null, header: HTMLElement | null, size: ImageSize) {
  if (!frame || !size.width || !size.height) return;
  frame.style.aspectRatio = `${size.width} / ${size.height}`;
  for (let pass = 0; pass < 2; pass += 1) {
    const width = viewerImageWidth(size, viewport(), header?.offsetHeight ?? 0);
    frame.style.width = `${Math.max(1, Math.floor(width))}px`;
  }
}
