import { type ReactNode, useEffect, useRef, useState } from "react";

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
  const header = useRef<HTMLDivElement>(null);
  const frame = useRef<HTMLDivElement>(null);
  const closer = useRef<HTMLButtonElement>(null);
  const [full, setFull] = useState(false);

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
    if (src && width && height) fitFrame(frame.current, header.current, { width, height });
  }, [src, width, height]);

  useEffect(() => {
    const element = dialog.current;
    if (!element) return;
    const follow = () => {
      if (globalThis.document.fullscreenElement !== element) setFull(false);
    };
    globalThis.document.addEventListener("fullscreenchange", follow);
    return () => globalThis.document.removeEventListener("fullscreenchange", follow);
  }, []);

  const leaveFullScreen = () => {
    setFull(false);
    if (globalThis.document.fullscreenElement === dialog.current) {
      globalThis.document.exitFullscreen?.().catch(() => {});
    }
  };
  const enterFullScreen = () => {
    setFull(true);
    // Covering the window already fills it; the browser's full screen, when
    // the host grants it, also hides the window's own chrome.
    if (globalThis.document.fullscreenEnabled) {
      dialog.current?.requestFullscreen?.().catch(() => {});
    }
  };

  return (
    <dialog
      ref={dialog}
      aria-label={label}
      data-full={full || undefined}
      onCancel={(event) => {
        if (!full) return;
        event.preventDefault();
        leaveFullScreen();
      }}
      onClose={() => {
        leaveFullScreen();
        if (frame.current) frame.current.removeAttribute("style");
        onClose();
      }}
      className={cn(
        "m-auto max-h-[calc(100dvh-2rem)] w-fit max-w-[calc(100vw-2rem)] flex-col gap-2 overflow-hidden rounded-lg border border-default open:flex",
        "bg-surface-card p-3 text-primary shadow-card",
        "backdrop:bg-surface-scrim",
        full && "h-dvh max-h-none w-screen max-w-none rounded-none border-0",
      )}
    >
      {image && (
        <>
          <div ref={header} className="flex flex-none items-start justify-between gap-3">
            <div className="min-w-0 pt-1 text-base text-secondary">{caption}</div>
            <div className="flex flex-none items-center gap-2">
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
                label={full ? "Exit full screen" : "Full screen"}
                aria-pressed={full}
                onClick={full ? leaveFullScreen : enterFullScreen}
              >
                {full ? <LeaveFullScreenIcon /> : <FullScreenIcon />}
              </IconButton>
              <CloseButton
                ref={closer}
                label="Close image"
                onClick={() => dialog.current?.close()}
              />
            </div>
          </div>
          <div
            ref={frame}
            data-image-frame=""
            className={cn(
              // The frame shrinks with the dialog when the window does. The
              // image keeps its proportions inside it, letterboxed on the
              // sunken surface when the frame's differ.
              "min-h-0 min-w-0 max-w-full shrink overflow-hidden rounded-md bg-surface-sunken",
              full ? "h-auto! w-full! flex-1" : "resize",
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
                fitFrame(frame.current, header.current, {
                  width: naturalWidth,
                  height: naturalHeight,
                });
              }}
              className="block size-full object-contain"
            />
          </div>
        </>
      )}
    </dialog>
  );
}

/** Sizes the image's frame once per opening, to fill the screen below the
 *  header; after that its size is the person's, through the resize corner. */
function fitFrame(frame: HTMLElement | null, header: HTMLElement | null, size: ImageSize) {
  if (!frame || !size.width || !size.height) return;
  const width = viewerImageWidth(
    size,
    { width: window.innerWidth, height: window.innerHeight },
    header?.offsetHeight ?? 0,
  );
  frame.style.width = `${width}px`;
  frame.style.height = `${Math.max(1, Math.round((width * size.height) / size.width))}px`;
}
