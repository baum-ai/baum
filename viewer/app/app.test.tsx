// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import example from "../../schema/review-example.json" with { type: "json" };
import type { ReviewDocument } from "../src/review-document";
import { App } from "./app";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

const current = example as unknown as ReviewDocument;

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  readonly url: string;
  closed = false;
  private listeners = new Map<string, (() => void)[]>();

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  addEventListener(name: string, listener: () => void) {
    this.listeners.set(name, [...(this.listeners.get(name) ?? []), listener]);
  }

  close() {
    this.closed = true;
  }

  emit(name: string) {
    for (const listener of this.listeners.get(name) ?? []) listener();
  }
}

let root: Root | null = null;
let container: HTMLElement;
let fetchMock: ReturnType<typeof vi.fn>;

function respond(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status });
}

async function mount() {
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => root!.render(<App />));
}

beforeAll(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
  globalThis.IntersectionObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
    takeRecords() {
      return [];
    }
  } as unknown as typeof IntersectionObserver;
  Element.prototype.scrollIntoView ??= () => {};
  HTMLElement.prototype.hidePopover ??= () => {};
});

beforeEach(() => {
  FakeEventSource.instances = [];
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  vi.stubGlobal("EventSource", FakeEventSource);
  window.history.replaceState(null, "", "/");
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.replaceChildren();
  localStorage.clear();
  vi.unstubAllGlobals();
});

describe("App", () => {
  it("renders the document and titles the page by the pull request", async () => {
    fetchMock.mockResolvedValue(respond(current));
    await mount();

    expect(container.textContent).toContain(current.summary.title);
    expect(document.title).toBe(`${current.summary.title} · #${current.review.pull_request}`);
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/document",
      expect.objectContaining({ cache: "no-store" }),
    );
  });

  it("shows the failure on a 500 and recovers on Retry", async () => {
    fetchMock.mockResolvedValueOnce(respond({}, 500));
    await mount();

    const alert = container.querySelector('[role="alert"]');
    expect(alert?.textContent).toContain("500");

    fetchMock.mockResolvedValueOnce(respond(current));
    const retry = [...container.querySelectorAll("button")].find((b) => b.textContent === "Retry");
    await act(async () => retry!.click());

    expect(container.querySelector('[role="alert"]')).toBeNull();
    expect(container.textContent).toContain(current.summary.title);
  });

  it("refetches on a document event and keeps the URL", async () => {
    window.history.replaceState(null, "", "/?pane=summary");
    fetchMock.mockResolvedValueOnce(respond(current));
    await mount();

    const changed = structuredClone(current);
    changed.summary.title = "A title that changed on disk";
    fetchMock.mockResolvedValueOnce(respond(changed));
    const [events] = FakeEventSource.instances;
    expect(events?.url).toBe("/api/events");
    await act(async () => events!.emit("document"));

    expect(container.textContent).toContain("A title that changed on disk");
    expect(document.title).toContain("A title that changed on disk");
    expect(window.location.search).toBe("?pane=summary");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("keeps the document on screen when a refetch fails", async () => {
    fetchMock.mockResolvedValueOnce(respond(current));
    await mount();

    fetchMock.mockResolvedValueOnce(respond({}, 500));
    await act(async () => FakeEventSource.instances[0]!.emit("document"));

    expect(container.querySelector('[role="alert"]')).toBeNull();
    expect(container.textContent).toContain(current.summary.title);
  });

  it("requests nothing but the document", async () => {
    fetchMock.mockImplementation(async () => respond(current));
    await mount();
    await act(async () => FakeEventSource.instances[0]!.emit("document"));

    const urls = fetchMock.mock.calls.map(([url]) => String(url));
    expect(urls.length).toBeGreaterThan(1);
    expect(new Set(urls)).toEqual(new Set(["/api/document"]));
    expect(FakeEventSource.instances.map((source) => source.url)).toEqual(["/api/events"]);
  });

  it("closes the event stream when it unmounts", async () => {
    fetchMock.mockResolvedValue(respond(current));
    await mount();
    act(() => root?.unmount());
    root = null;

    expect(FakeEventSource.instances.every((source) => source.closed)).toBe(true);
  });
});
