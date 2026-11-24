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
 *  past the image's own pixels, and keeps its proportions. Its corner drags
 *  to resize the image; the share of the screen dragged to is remembered,
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
  // The size the frame fits to, until the person drags its corner; from then
  // on the size is theirs.
  const fitted = useRef<ImageSize | null>(null);
  const dragged = useRef(false);

  // The header's height when the image was fitted: one row beside the
  // controls when the image is wide enough.
  const fittedHeader = useRef(0);
  const fit = (size: ImageSize) => {
    fitted.current = size;
    fitFrame(frame.current, header.current, size, scale.current);
    fittedHeader.current = header.current?.offsetHeight ?? 0;
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
    fitted.current = null;
    if (src && width && height) {
      fit({ width, height });
    }
  }, [src, width, height]);

  // A window that changes size, or a phone that turns, refits the image
  // until the person has sized it themselves, and re-centres a dialog
  // pinned for a drag so it cannot be left outside the window.
  useEffect(() => {
    if (!src) return;
    const refit = () => {
      dialog.current?.removeAttribute("style");
      frame.current?.style.removeProperty("max-width");
      if (!dragged.current && fitted.current) {
        fit(fitted.current);
      }
    };
    window.addEventListener("resize", refit);
    return () => window.removeEventListener("resize", refit);
  }, [src]);

  // A click on the backdrop, outside the dialog's box, closes it; Escape is
  // the keyboard's way out. The press must start there too, as a drag of
  // the corner can end outside.
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

  // A smaller dialog pinned where it opened can only grow down and right,
  // so the way back to the full fit is a control.
  const fitToScreen = () => {
    store("1");
    scale.current = 1;
    dragged.current = false;
    dialog.current?.removeAttribute("style");
    frame.current?.style.removeProperty("max-width");
    if (fitted.current) fit(fitted.current);
  };
  const closeRef = useRef(close);
  closeRef.current = close;

  // Dragging the corner of a centred dialog would move the corner at half
  // the pointer's speed, as the dialog re-centres; pinning the dialog where
  // it stands keeps the corner under the pointer. Only a press on the
  // corner's grip counts, so a click on the image leaves the fit alone,
  // and a press that resizes nothing lets the dialog go again.
  const pin = (event: PointerEvent<HTMLDivElement>) => {
    const element = dialog.current;
    const box = event.currentTarget.getBoundingClientRect();
    const onGrip = event.clientX >= box.right - GRIP && event.clientY >= box.bottom - GRIP;
    if (!element || !onGrip) return;
    const start = event.currentTarget.offsetWidth;
    const at = element.getBoundingClientRect();
    element.style.margin = "0";
    element.style.top = `${at.top}px`;
    element.style.left = `${at.left}px`;
    bound(element);
    // Past the height the window allows, a wider frame would only add bars
    // beside the image, so the drag stops there. The cap is in pixels so the
    // dialog sizes to it too. It counts the header as one row, as it was at
    // fit time, since the caption returns beside the controls as the image
    // widens; if capping narrows the frame enough to wrap the header, it is
    // worked out again with the taller header.
    const size = fitted.current;
    const target = event.currentTarget;
    if (size) {
      const cap = (headerHeight: number) => {
        const current = header.current?.offsetHeight ?? 0;
        const around = element.offsetHeight - target.offsetHeight - current + headerHeight;
        const room = window.innerHeight - at.top - 16 - around;
        const across =
          Number.parseFloat(element.style.maxWidth) - (element.offsetWidth - target.offsetWidth);
        const widest = Math.max(
          64,
          Math.floor(Math.min((room * size.width) / size.height, across)),
        );
        target.style.maxWidth = `${widest}px`;
      };
      const before = header.current?.offsetHeight ?? 0;
      const used = Math.min(before, fittedHeader.current || before);
      cap(used);
      const after = header.current?.offsetHeight ?? 0;
      if (target.offsetWidth < start && after > used) cap(after);
    }
    window.addEventListener(
      "pointerup",
      () => {
        // Keep the width as drawn: the browser's resizer writes the
        // pointer's width, past the cap, and a later window resize lifts
        // the cap.
        let drawn = target.offsetWidth;
        // Where the dialog's height squeezed the frame (the header wrapped
        // during the drag), the width follows the height it was given.
        if (size && target.offsetHeight)
          drawn = Math.min(drawn, Math.floor((target.offsetHeight * size.width) / size.height));
        target.style.removeProperty("max-width");
        target.style.width = `${drawn}px`;
        // Rounding can move the width a pixel on a press that resizes
        // nothing; that is not a drag.
        if (Math.abs(drawn - start) > 1) {
          dragged.current = true;
          // Later images open at the same share of the screen's fit.
          if (size) {
            const full = viewerImageWidth(size, viewport(), fittedHeader.current);
            store(String(Math.min(1, drawn / full)));
          }
        } else {
          target.style.width = `${start}px`;
          if (!dragged.current) element.removeAttribute("style");
        }
      },
      { once: true },
    );
  };

  return (
    <dialog
      ref={dialog}
      aria-label={label}
      aria-describedby={caption ? captionId : undefined}
      onClose={() => {
        dialog.current?.removeAttribute("style");
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
            onPointerDown={pin}
            // The frame takes the image's proportions, so dragging its
            // corner scales the image. It shrinks with the dialog when the
            // window does, letterboxing the image on the sunken surface.
            className="min-h-0 min-w-0 max-w-full shrink resize-x self-center overflow-hidden rounded-md bg-surface-sunken"
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

/** How far in from the frame's corner a press starts a resize: the
 *  browser's grip, with some slack. */
const GRIP = 20;

const viewport = () => ({ width: window.innerWidth, height: window.innerHeight });

/** Keeps a pinned dialog inside the window, so a corner dragged outwards
 *  stops at its edge and the controls stay in view. */
function bound(dialog: HTMLElement) {
  const left = Number.parseFloat(dialog.style.left) || 0;
  const top = Number.parseFloat(dialog.style.top) || 0;
  dialog.style.maxWidth = `${Math.max(160, window.innerWidth - left - 16)}px`;
  dialog.style.maxHeight = `${Math.max(160, window.innerHeight - top - 16)}px`;
}

/** Sizes the image's frame to `scale` of the screen below the header. The
 *  header wraps to the frame's width, so the fit runs again at its new
 *  height. */
function fitFrame(
  frame: HTMLElement | null,
  header: HTMLElement | null,
  size: ImageSize,
  scale: number,
) {
  if (!frame || !size.width || !size.height) return;
  frame.style.aspectRatio = `${size.width} / ${size.height}`;
  for (let pass = 0; pass < 2; pass += 1) {
    const width = viewerImageWidth(size, viewport(), header?.offsetHeight ?? 0);
    frame.style.width = `${Math.max(1, Math.floor(width * scale))}px`;
  }
}
