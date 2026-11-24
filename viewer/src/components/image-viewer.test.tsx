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

  it("opens later images at the share of the screen the person last dragged to", () => {
    const view = mount({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    const fit = (width: number, height: number) =>
      viewerImageWidth({ width, height }, viewport(), 0);
    const share = 300 / fit(40, 20);
    drag(view.frame(), 95, 95, "300px");
    expect(Number(localStorage.getItem("baum.image-viewer.scale"))).toBeCloseTo(share);
    view.render(null);

    // Images of other shapes open at the same share of their own fit.
    const width = () => Number.parseFloat(view.frame().style.width);
    view.render({ src: "/b.png", alt: "Wide", width: 80, height: 20 });
    expect(Math.abs(width() - fit(80, 20) * share)).toBeLessThanOrEqual(1);
    view.render(null);
    view.render({ src: "/c.png", alt: "Tall", width: 20, height: 40 });
    expect(Math.abs(width() - fit(20, 40) * share)).toBeLessThanOrEqual(1);
  });

  it("fits to the screen again and forgets the remembered share", () => {
    const view = mount({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    drag(view.frame(), 95, 95, "300px");
    act(() => view.button("Fit to screen").click());
    const width = viewerImageWidth({ width: 40, height: 20 }, viewport(), 0);
    expect(view.frame().style.width).toBe(`${width}px`);
    expect(localStorage.getItem("baum.image-viewer.scale")).toBe("1");
  });

  it("remembers nothing from a press that moves the corner a pixel", () => {
    const view = mount({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    const before = Number.parseFloat(view.frame().style.width);
    drag(view.frame(), 95, 95, `${before - 1}px`);
    expect(localStorage.getItem("baum.image-viewer.scale")).toBeNull();
    expect(view.frame().style.width).toBe(`${before}px`);
  });

  it("ignores a remembered scale it cannot read", () => {
    localStorage.setItem("baum.image-viewer.scale", "nope");
    const view = mount({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    const width = viewerImageWidth({ width: 40, height: 20 }, viewport(), 0);
    expect(view.frame().style.width).toBe(`${width}px`);
  });
});
