// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";

import { reviewDocumentFor } from "../../test/review-document";
import { AttachmentUrlProvider } from "../host";
import type { Block, IndexedAttachment, ReviewDocument } from "../review-document";
import { viewerImageWidth } from "../review-image-layout";
import { ReviewImages } from "./review-images";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

let root: Root | null = null;

beforeAll(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  // jsdom lays nothing out, so there is nothing to observe.
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
  HTMLDialogElement.prototype.showModal ??= function (this: HTMLDialogElement) {
    this.open = true;
  };
  HTMLDialogElement.prototype.close ??= function (this: HTMLDialogElement) {
    this.open = false;
  };
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.replaceChildren();
});

const attachment = (id: string, alt: string, [width, height] = [10, 10]) => ({
  id,
  position: 1,
  source_url: `https://github.com/user-attachments/assets/${id}`,
  content_type: "image/png",
  width,
  height,
  alt,
});

function mount(
  ids: string[],
  captions: Record<string, string> = {},
  size?: [number, number],
  url?: string,
  origins?: IndexedAttachment["origins"],
) {
  const review = structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;
  review.index.attachments = ids.map((id) => ({ ...attachment(id, `Alt ${id}`, size), origins }));
  const block = {
    kind: "images",
    items: ids.map((id) =>
      id in captions ? { attachment: id, caption: captions[id] } : { attachment: id },
    ),
    source: [{ type: "pr_body", section: null }],
  } as Extract<Block, { kind: "images" }>;
  const container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  act(() =>
    root!.render(
      <AttachmentUrlProvider resolve={(id) => url ?? `/attachments/${id}`}>
        <ReviewImages document={review} block={block} />
      </AttachmentUrlProvider>,
    ),
  );
  return container;
}

function fail(container: HTMLElement, alt: string) {
  act(() => {
    container.querySelector(`img[alt="${alt}"]`)!.dispatchEvent(new Event("error"));
  });
}

describe("ReviewImages", () => {
  it("keeps human and tool provenance on a shared image and in its full-size viewer", () => {
    const container = mount(["att_shared"], {}, undefined, undefined, [
      { session: "session-one", turn: 2, input: "prompt", kind: "human_input", position: 1 },
      {
        session: "session-two",
        turn: 3,
        tool: "tool",
        tool_name: "view_image",
        kind: "tool_result",
        child_session: "child",
        position: 1,
      },
    ]);
    expect(container.textContent).toContain("Human-supplied image");
    expect(container.textContent).toContain("Agent tool result · view_image · Subagent");
    expect(container.textContent).toContain("Prompt 2");
    expect(container.textContent).toContain("Prompt 3");
    act(() =>
      container
        .querySelector<HTMLButtonElement>('[aria-label="View full size: Alt att_shared"]')!
        .click(),
    );
    const dialog = container.querySelector("dialog")!;
    expect(dialog.open).toBe(true);
    expect(dialog.textContent).toContain("Human-supplied image");
    expect(dialog.textContent).toContain("Prompt 3");
  });
  it("says in its place that an image failed to load, and retries it", () => {
    const container = mount(["att_aa", "att_bb"]);
    expect(container.querySelectorAll("img")).toHaveLength(2);

    fail(container, "Alt att_aa");
    expect([...container.querySelectorAll("img")].map((img) => img.alt)).toEqual(["Alt att_bb"]);
    expect(container.textContent).toContain("Image unavailable");
    // The caption still names what is missing.
    expect([...container.querySelectorAll("figcaption")].map((item) => item.textContent)).toEqual([
      "Alt att_aa",
      "Alt att_bb",
    ]);

    act(() => {
      container
        .querySelector<HTMLButtonElement>('[aria-label="Retry loading Alt att_aa"]')!
        .click();
    });
    const retried = container.querySelector<HTMLImageElement>('img[alt="Alt att_aa"]');
    expect(retried?.getAttribute("src")).toBe("/attachments/att_aa?attempt=1");
    expect(container.textContent).not.toContain("Image unavailable");
    // Focus follows from the retry control to the image, and back if it
    // fails again.
    const trigger = container.querySelector('[aria-label="View full size: Alt att_aa"]');
    expect(document.activeElement).toBe(trigger);
    fail(container, "Alt att_aa");
    expect(document.activeElement).toBe(
      container.querySelector('[aria-label="Retry loading Alt att_aa"]'),
    );
    expect(container.querySelector('[role="status"]')?.textContent).toBe("Image unavailable");
  });

  it("shows the alt text when a caption is empty", () => {
    const container = mount(["att_aa"], { att_aa: "" });
    expect(container.querySelector("figcaption")?.textContent).toBe("Alt att_aa");
  });

  it("marks an image that already failed before it mounted, keeping loaded ones", () => {
    const complete = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, "complete")!;
    const naturalWidth = Object.getOwnPropertyDescriptor(
      HTMLImageElement.prototype,
      "naturalWidth",
    )!;
    // att_aa failed, att_bb loaded, att_cc is still loading.
    Object.defineProperty(HTMLImageElement.prototype, "complete", {
      configurable: true,
      get(this: HTMLImageElement) {
        return this.alt !== "Alt att_cc";
      },
    });
    Object.defineProperty(HTMLImageElement.prototype, "naturalWidth", {
      configurable: true,
      get(this: HTMLImageElement) {
        return this.alt === "Alt att_bb" ? 10 : 0;
      },
    });
    try {
      const container = mount(["att_aa", "att_bb", "att_cc"]);
      expect([...container.querySelectorAll("img")].map((img) => img.alt)).toEqual([
        "Alt att_bb",
        "Alt att_cc",
      ]);
      expect(container.querySelectorAll('[aria-label^="Retry loading"]')).toHaveLength(1);
    } finally {
      Object.defineProperty(HTMLImageElement.prototype, "complete", complete);
      Object.defineProperty(HTMLImageElement.prototype, "naturalWidth", naturalWidth);
    }
  });

  it("opens the viewer larger than the image's own pixels", () => {
    const container = mount(["att_aa"], {}, [80, 40]);
    act(() => {
      container
        .querySelector<HTMLButtonElement>('[aria-label="View full size: Alt att_aa"]')!
        .click();
    });
    const frame = container.querySelector<HTMLElement>("dialog [data-image-frame]")!;
    const width = viewerImageWidth(
      { width: 80, height: 40 },
      { width: innerWidth, height: innerHeight },
      0,
    );
    expect(frame.style.width).toBe(`${width}px`);
    expect(width).toBeGreaterThan(80);
  });
});

it("resolves each compared image against its own document version", () => {
  const first = structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;
  first.review.version = 1;
  first.index.attachments = [attachment("att_aa", "Earlier")];
  const current = structuredClone(first);
  current.review.version = 2;
  const block = {
    kind: "images",
    items: [{ attachment: "att_aa" }],
    source: [{ type: "pr_body", section: null }],
  } as Extract<Block, { kind: "images" }>;
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() =>
    root!.render(
      <AttachmentUrlProvider
        resolve={(id, doc) => `/attachments/${id}?version=${doc?.review.version}`}
      >
        <ReviewImages document={first} block={block} />
        <ReviewImages document={current} block={block} />
      </AttachmentUrlProvider>,
    ),
  );
  expect([...container.querySelectorAll("img")].map((img) => img.getAttribute("src"))).toEqual([
    "/attachments/att_aa?version=1",
    "/attachments/att_aa?version=2",
  ]);
});

it("retains local document selectors and fragments when retrying an image", () => {
  const container = mount(
    ["att_aa"],
    {},
    undefined,
    "/attachments/att_aa?clone=one&pr=7&version=1#capture",
  );
  fail(container, "Alt att_aa");
  act(() =>
    container.querySelector<HTMLButtonElement>('[aria-label="Retry loading Alt att_aa"]')!.click(),
  );
  expect(container.querySelector("img")?.getAttribute("src")).toBe(
    "/attachments/att_aa?clone=one&pr=7&version=1&attempt=1#capture",
  );
});

it("plays a description video in place and says in its place when it fails", () => {
  const review = structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;
  review.index.attachments = [
    { ...attachment("att_vv", "", [1280, 720]), content_type: "video/mp4", heading: "Demo" },
  ];
  const block = {
    kind: "images",
    items: [{ attachment: "att_vv" }],
    source: [{ type: "pr_body", section: null }],
  } as Extract<Block, { kind: "images" }>;
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() =>
    root!.render(
      <AttachmentUrlProvider resolve={(id) => `/attachments/${id}`}>
        <ReviewImages document={review} block={block} />
      </AttachmentUrlProvider>,
    ),
  );
  const video = container.querySelector("video")!;
  expect(video.getAttribute("src")).toBe("/attachments/att_vv");
  expect([video.controls, video.muted, video.autoplay, video.preload]).toEqual([
    true,
    true,
    false,
    "metadata",
  ]);
  expect(container.querySelector("img, button")).toBeNull();
  expect(container.querySelector("figcaption")?.textContent).toBe("Demo");

  act(() => {
    video.dispatchEvent(new Event("error"));
  });
  expect(container.querySelector("video")).toBeNull();
  expect(container.querySelector('[role="status"]')?.textContent).toBe("Video unavailable");
  act(() =>
    container.querySelector<HTMLButtonElement>('[aria-label="Retry loading Demo"]')!.click(),
  );
  expect(container.querySelector("video")?.getAttribute("src")).toBe(
    "/attachments/att_vv?attempt=1",
  );
});
