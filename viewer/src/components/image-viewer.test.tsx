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

/** Presses the frame's `handle`, moves the pointer by `dx`, `dy` and lets
 *  go. The frame's laid-out width follows its style. */
function drag(frame: HTMLElement, handle: string, dx: number, dy = 0) {
  Object.defineProperty(frame, "offsetWidth", {
    configurable: true,
    get: () => Number.parseFloat(frame.style.width) || 0,
  });
  const grip = frame.querySelector<HTMLElement>(`[data-resize="${handle}"]`)!;
  const at = (type: string, x: number, y: number) =>
    act(() => {
      const event = new MouseEvent(type, { bubbles: true, clientX: x, clientY: y });
      Object.defineProperty(event, "pointerId", { value: 1 });
      grip.dispatchEvent(event);
    });
  at("pointerdown", 100, 100);
  at("pointermove", 100 + dx, 100 + dy);
  at("pointerup", 100 + dx, 100 + dy);
}

/** Drags the right edge until the frame is `width` pixels wide. */
function dragTo(frame: HTMLElement, width: number) {
  drag(frame, "right", (width - Number.parseFloat(frame.style.width)) / 2);
}

describe("ImageViewer", () => {
  it("opens filling the screen and keeps a dragged size across re-renders", () => {
    const view = mount({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    expect(view.dialog.open).toBe(true);
    const width = viewerImageWidth({ width: 40, height: 20 }, viewport(), 0);
    expect(view.frame().style.width).toBe(`${width}px`);
    expect(view.frame().style.aspectRatio).toBe("40 / 20");

    // The person drags an edge; a host re-render with a fresh object
    // leaves their size alone.
    dragTo(view.frame(), 300);
    view.render({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    expect(view.frame().style.width).toBe("300px");
    // A window resize leaves it alone too.
    act(() => window.dispatchEvent(new Event("resize")));
    view.render({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    expect(view.frame().style.width).toBe("300px");
  });

  it("refits to a resized window until the person drags an edge", () => {
    const view = mount({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    // A press on an edge that moves nothing is not a resize.
    drag(view.frame(), "bottom-right", 0);
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
    dragTo(view.frame(), 300);
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
    dragTo(view.frame(), 300);
    act(() => view.button("Fit to screen").click());
    const width = viewerImageWidth({ width: 40, height: 20 }, viewport(), 0);
    expect(view.frame().style.width).toBe(`${width}px`);
    expect(localStorage.getItem("baum.image-viewer.scale")).toBe("1");
  });

  it("remembers nothing from a press that moves the corner a pixel", () => {
    const view = mount({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    const before = Number.parseFloat(view.frame().style.width);
    drag(view.frame(), "right", -0.5);
    expect(localStorage.getItem("baum.image-viewer.scale")).toBeNull();
    expect(view.frame().style.width).toBe(`${before}px`);
  });

  it("resizes from every edge and corner, never past the screen's fit", () => {
    const view = mount({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    const full = Math.floor(viewerImageWidth({ width: 40, height: 20 }, viewport(), 0));
    const width = () => Number.parseFloat(view.frame().style.width);
    // The dialog stays centred, so the frame changes by twice the drag.
    drag(view.frame(), "top-left", 50, 50);
    expect(width()).toBe(full - 200);
    drag(view.frame(), "left", 10);
    expect(width()).toBe(full - 220);
    // Height turns into width at the image's proportions (2:1).
    drag(view.frame(), "top", 0, 10);
    expect(width()).toBe(full - 260);
    drag(view.frame(), "bottom", 0, -10);
    expect(width()).toBe(full - 300);
    drag(view.frame(), "top-right", -10, 0);
    expect(width()).toBe(full - 320);
    drag(view.frame(), "bottom-left", 0, -10);
    expect(width()).toBe(full - 360);
    // Dragged outwards, it stops at the full fit.
    drag(view.frame(), "bottom-right", 5000, 5000);
    expect(width()).toBe(full);
  });

  it("ignores a remembered scale it cannot read", () => {
    localStorage.setItem("baum.image-viewer.scale", "nope");
    const view = mount({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    const width = viewerImageWidth({ width: 40, height: 20 }, viewport(), 0);
    expect(view.frame().style.width).toBe(`${width}px`);
  });
});
