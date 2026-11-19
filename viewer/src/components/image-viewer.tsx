import { type PointerEvent, type ReactNode, useEffect, useId, useRef, useState } from "react";

import { type ImageSize, viewerImageWidth } from "../review-image-layout";
import { cn } from "./classnames";
import { ExternalLinkIcon, FullScreenIcon, LeaveFullScreenIcon } from "./icons";
import { CloseButton, IconButton } from "./ui";

/** An image to show large: where it is served, its description, and its
 *  pixel size when the host knows it (otherwise it is read once it loads). */
export type ViewedImage = { src: string; alt: string; width?: number; height?: number };

/** A modal viewer for one image. It opens as large as the screen allows,
 *  past the image's own pixels, and keeps its proportions. Its corner drags
 *  to resize the image, and it can fill the screen: through the browser's
 *  full screen where the host allows it, otherwise by covering the window.
 *  Escape leaves full screen first, then closes. `image` set opens it and
 *  `null` closes it; `onClose` follows any close, including Escape. */
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
  const content = useRef<HTMLDivElement>(null);
  const header = useRef<HTMLDivElement>(null);
  const frame = useRef<HTMLDivElement>(null);
  const closer = useRef<HTMLButtonElement>(null);
  const fullButton = useRef<HTMLButtonElement>(null);
  const [full, setFull] = useState(false);
  const captionId = useId();
  // The size the frame fits to, until the person drags its corner; from then
  // on the size is theirs.
  const fitted = useRef<ImageSize | null>(null);
  const dragged = useRef(false);
  // Whether this viewer holds the browser's full screen, so it only leaves
  // a full screen it entered.
  const holdsFullScreen = useRef(false);

  // The header's height when the image was fitted: one row beside the
  // controls when the image is wide enough.
  const fittedHeader = useRef(0);
  const fit = (size: ImageSize) => {
    fitted.current = size;
    fitFrame(frame.current, header.current, size);
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

  useEffect(() => {
    const follow = () => {
      if (holdsFullScreen.current && !globalThis.document.fullscreenElement) {
        holdsFullScreen.current = false;
        setFull(false);
      }
    };
    globalThis.document.addEventListener("fullscreenchange", follow);
    return () => globalThis.document.removeEventListener("fullscreenchange", follow);
  }, []);

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

  // Leaving the browser's full screen is asynchronous and moves focus, so a
  // close from full screen waits for it; the host can then restore focus.
  const close = async () => {
    if (holdsFullScreen.current && globalThis.document.fullscreenElement) {
      holdsFullScreen.current = false;
      await globalThis.document.exitFullscreen?.().catch(() => {});
    }
    dialog.current?.close();
  };
  const closeRef = useRef(close);
  closeRef.current = close;

  const leaveFullScreen = () => {
    setFull(false);
    if (holdsFullScreen.current && globalThis.document.fullscreenElement) {
      holdsFullScreen.current = false;
      globalThis.document.exitFullscreen?.().catch(() => {});
    }
  };
  const enterFullScreen = () => {
    setFull(true);
    // Covering the window already fills it; the browser's full screen, when
    // the host grants it, also hides the window's own chrome. A dialog
    // cannot itself go full screen, so its content does.
    const target = content.current;
    if (!globalThis.document.fullscreenEnabled || globalThis.document.fullscreenElement) return;
    if (!target?.requestFullscreen) return;
    holdsFullScreen.current = true;
    target.requestFullscreen().catch(() => {
      holdsFullScreen.current = false;
    });
  };

  // Dragging the corner of a centred dialog would move the corner at half
  // the pointer's speed, as the dialog re-centres; pinning the dialog where
  // it stands keeps the corner under the pointer. Only a press on the
  // corner's grip counts, so a click on the image leaves the fit alone,
  // and a press that resizes nothing lets the dialog go again.
  const pin = (event: PointerEvent<HTMLDivElement>) => {
    const element = dialog.current;
    const box = event.currentTarget.getBoundingClientRect();
    const onGrip = event.clientX >= box.right - GRIP && event.clientY >= box.bottom - GRIP;
    if (!element || full || !onGrip) return;
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
        const drawn = target.offsetWidth;
        target.style.removeProperty("max-width");
        target.style.width = `${drawn}px`;
        if (drawn !== start) dragged.current = true;
        else if (!dragged.current) element.removeAttribute("style");
      },
      { once: true },
    );
  };

  return (
    <dialog
      ref={dialog}
      aria-label={label}
      aria-describedby={caption ? captionId : undefined}
      data-full={full || undefined}
      onCancel={(event) => {
        if (!full) return;
        event.preventDefault();
        leaveFullScreen();
        // Leaving the cover can drop focus out of the dialog; it returns to
        // the control that entered full screen.
        if (!dialog.current?.contains(globalThis.document.activeElement)) {
          fullButton.current?.focus();
        }
      }}
      onClose={() => {
        leaveFullScreen();
        dialog.current?.removeAttribute("style");
        frame.current?.removeAttribute("style");
        onClose();
      }}
      className={cn(
        "m-auto max-h-[calc(100dvh-2rem)] w-fit min-w-72 max-w-[calc(100vw-2rem)] overflow-hidden rounded-lg border border-default open:flex",
        "bg-surface-card p-3 text-primary shadow-card",
        "backdrop:bg-surface-scrim",
        full && "inset-0! m-0! h-dvh max-h-none! w-screen max-w-none! rounded-none border-0",
      )}
    >
      {image && (
        <div
          ref={content}
          className="flex min-h-0 min-w-0 flex-auto flex-col gap-2 bg-surface-card [&:fullscreen]:p-3"
        >
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
                <IconButton
                  ref={fullButton}
                  label="Full screen"
                  aria-pressed={full}
                  onClick={full ? leaveFullScreen : enterFullScreen}
                >
                  {full ? <LeaveFullScreenIcon /> : <FullScreenIcon />}
                </IconButton>
                <CloseButton ref={closer} label="Close image" onClick={close} />
              </div>
            </div>
          </div>
          <div
            ref={frame}
            data-image-frame=""
            onPointerDown={pin}
            className={cn(
              // The frame takes the image's proportions, so dragging its
              // corner scales the image. It shrinks with the dialog when the
              // window does, letterboxing the image on the sunken surface.
              "min-h-0 min-w-0 max-w-full shrink self-center overflow-hidden rounded-md bg-surface-sunken",
              full ? "aspect-auto! h-auto! w-full! max-w-none! flex-1" : "resize-x",
            )}
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

/** Keeps a pinned dialog inside the window, so a corner dragged outwards
 *  stops at its edge and the controls stay in view. */
function bound(dialog: HTMLElement) {
  const left = Number.parseFloat(dialog.style.left) || 0;
  const top = Number.parseFloat(dialog.style.top) || 0;
  dialog.style.maxWidth = `${Math.max(160, window.innerWidth - left - 16)}px`;
  dialog.style.maxHeight = `${Math.max(160, window.innerHeight - top - 16)}px`;
}

/** Sizes the image's frame to fill the screen below the header. The header
 *  wraps to the frame's width, so the fit runs again at its new height. */
function fitFrame(frame: HTMLElement | null, header: HTMLElement | null, size: ImageSize) {
  if (!frame || !size.width || !size.height) return;
  frame.style.aspectRatio = `${size.width} / ${size.height}`;
  for (let pass = 0; pass < 2; pass += 1) {
    const width = viewerImageWidth(
      size,
      { width: window.innerWidth, height: window.innerHeight },
      header?.offsetHeight ?? 0,
    );
    frame.style.width = `${width}px`;
  }
}
