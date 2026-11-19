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
  Reflect.deleteProperty(document, "fullscreenEnabled");
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

describe("ImageViewer", () => {
  it("opens filling the screen and keeps a dragged size across re-renders", () => {
    const view = mount({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    expect(view.dialog.open).toBe(true);
    const width = viewerImageWidth({ width: 40, height: 20 }, viewport(), 0);
    expect(view.frame().style.width).toBe(`${width}px`);
    expect(view.frame().style.height).toBe(`${width / 2}px`);

    // The person drags the corner; a host re-render with a fresh object
    // leaves their size alone.
    act(() => view.frame().dispatchEvent(new Event("pointerdown", { bubbles: true })));
    view.frame().style.width = "300px";
    view.render({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    expect(view.frame().style.width).toBe("300px");
    // A window resize leaves it alone too.
    act(() => window.dispatchEvent(new Event("resize")));
    view.render({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
    expect(view.frame().style.width).toBe("300px");
  });

  it("refits to a resized window until the person drags the corner", () => {
    const view = mount({ src: "/a.png", alt: "Shot", width: 40, height: 20 });
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
    expect(view.frame().style.height).toBe(`${width * 2}px`);
  });

  it("goes full screen, and Escape leaves full screen before it closes", () => {
    const onClose = vi.fn();
    const requestFullscreen = vi.fn(() => Promise.resolve());
    HTMLElement.prototype.requestFullscreen = requestFullscreen;
    // jsdom has no Fullscreen API.
    Object.defineProperty(document, "fullscreenEnabled", { configurable: true, value: true });
    const view = mount({ src: "/a.png", alt: "Shot", width: 40, height: 20 }, onClose);

    act(() => view.button("Full screen").click());
    expect(requestFullscreen).toHaveBeenCalledOnce();
    // A dialog cannot go full screen itself; its content does.
    expect(requestFullscreen.mock.contexts[0]).toBe(view.dialog.firstElementChild);
    expect(view.dialog.dataset.full).toBe("true");
    expect(view.button("Full screen").getAttribute("aria-pressed")).toBe("true");

    const escape = () => {
      const cancel = new Event("cancel", { cancelable: true });
      act(() => view.dialog.dispatchEvent(cancel));
      if (!cancel.defaultPrevented) act(() => view.dialog.close());
    };
    escape();
    expect(view.dialog.dataset.full).toBeUndefined();
    expect(view.dialog.open).toBe(true);
    expect(onClose).not.toHaveBeenCalled();

    escape();
    expect(view.dialog.open).toBe(false);
    expect(onClose).toHaveBeenCalledOnce();
  });
});
