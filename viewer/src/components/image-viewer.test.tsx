// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import { viewerImageWidth } from "../review-image-layout";
import { ImageViewer, type ViewedImage } from "./image-viewer";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

let root: Root | null = null;

beforeAll(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  HTMLDialogElement.prototype.showModal ??= function (this: HTMLDialogElement) {
    this.open = true;
  };
  HTMLDialogElement.prototype.close ??= function (this: HTMLDialogElement) {
    this.open = false;
    this.dispatchEvent(new Event("close"));
  };
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.replaceChildren();
  vi.restoreAllMocks();
  localStorage.clear();
});

function mount(image: ViewedImage | null, onClose = () => {}) {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  const render = (next: ViewedImage | null) =>
    act(() => root!.render(<ImageViewer image={next} label="Shot" onClose={onClose} />));
  render(image);
  return {
    render,
    dialog: container.querySelector("dialog")!,
    frame: () => container.querySelector<HTMLElement>("[data-image-frame]")!,
    button: (name: string) => container.querySelector<HTMLButtonElement>(`[aria-label="${name}"]`)!,
  };
}

const viewport = () => ({ width: innerWidth, height: innerHeight });

/** Presses at a point of a frame laid out at 0,0–100,100, whose resize grip
 *  is the bottom-right corner, drags it to `width` and lets go. */
function drag(frame: HTMLElement, x: number, y: number, width?: string) {
  frame.getBoundingClientRect = () => new DOMRect(0, 0, 100, 100);
  Object.defineProperty(frame, "offsetWidth", {
    configurable: true,
    get: () => Number.parseFloat(frame.style.width) || 0,
  });
  act(() => {
    frame.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true, clientX: x, clientY: y }));
  });
  if (width) frame.style.width = width;
  act(() => window.dispatchEvent(new Event("pointerup")));
}

describe("ImageViewer", () => {
  it("opens filling the screen and keeps a dragged size across re-renders", () => {
    const view = mount({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    expect(view.dialog.open).toBe(true);
    const width = viewerImageWidth({ width: 40, height: 20 }, viewport(), 0);
    expect(view.frame().style.width).toBe(`${width}px`);
    expect(view.frame().style.aspectRatio).toBe("40 / 20");

    // The person drags the corner; a host re-render with a fresh object
    // leaves their size alone.
    drag(view.frame(), 95, 95, "300px");
    view.render({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    expect(view.frame().style.width).toBe("300px");
    // A window resize leaves it alone too.
    act(() => window.dispatchEvent(new Event("resize")));
    view.render({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    expect(view.frame().style.width).toBe("300px");
  });

  it("refits to a resized window until the person drags the corner", () => {
    const view = mount({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    // A click on the image itself is not a resize, nor is a press on the
    // grip that moves nothing.
    drag(view.frame(), 50, 50);
    expect(view.dialog.style.top).toBe("");
    drag(view.frame(), 95, 95);
    expect(view.dialog.style.top).toBe("");
    const before = view.frame().style.width;
    const inner = Object.getOwnPropertyDescriptor(window, "innerWidth")!;
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 500 });
    try {
      act(() => window.dispatchEvent(new Event("resize")));
      const width = viewerImageWidth({ width: 40, height: 20 }, viewport(), 0);
      expect(view.frame().style.width).toBe(`${width}px`);
      expect(view.frame().style.width).not.toBe(before);
    } finally {
      Object.defineProperty(window, "innerWidth", inner);
    }
  });

  it("fits an image of unknown size once it loads", () => {
    const view = mount({ src: "/a.png", alt: "Shot" });
    expect(view.frame().style.width).toBe("");
    const image = view.dialog.querySelector("img")!;
    vi.spyOn(image, "naturalWidth", "get").mockReturnValue(30);
    vi.spyOn(image, "naturalHeight", "get").mockReturnValue(60);
    act(() => image.dispatchEvent(new Event("load")));
    const width = viewerImageWidth({ width: 30, height: 60 }, viewport(), 0);
    expect(view.frame().style.width).toBe(`${width}px`);
    expect(view.frame().style.aspectRatio).toBe("30 / 60");
  });

  it("opens later images within the size the person last dragged to", () => {
    const view = mount({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    drag(view.frame(), 95, 95, "300px");
    expect(JSON.parse(localStorage.getItem("baum.image-viewer.size")!)).toEqual({
      width: 300,
      height: 150,
    });
    view.render(null);

    // A wide image takes the remembered width; a tall one, its height.
    view.render({ src: "/b.png", alt: "Wide", width: 80, height: 20 });
    expect(view.frame().style.width).toBe("300px");
    view.render(null);
    view.render({ src: "/c.png", alt: "Tall", width: 20, height: 40 });
    expect(view.frame().style.width).toBe("75px");
  });

  it("ignores a remembered size it cannot read", () => {
    localStorage.setItem("baum.image-viewer.size", "{nope");
    const view = mount({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    const width = viewerImageWidth({ width: 40, height: 20 }, viewport(), 0);
    expect(view.frame().style.width).toBe(`${width}px`);
  });
});
