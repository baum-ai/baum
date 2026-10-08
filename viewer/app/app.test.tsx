// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import example from "../../schema/review-example.json" with { type: "json" };
import { useUrlState } from "../src/navigation";
import type { ReviewDocument } from "../src/review-document";
import { App, AppLink } from "./app";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

const viewer = vi.hoisted(() => ({ failure: null as Error | null }));

// The real viewer, plus a way to make it throw and a probe of the URL state it
// is given.
vi.mock("../src/components/review-viewer", async (original) => {
  const actual = await original<typeof import("../src/components/review-viewer")>();
  return {
    ...actual,
    ReviewViewer: (props: Parameters<typeof actual.ReviewViewer>[0]) => {
      if (viewer.failure) throw viewer.failure;
      return (
        <>
          <output data-testid="search">{useUrlState().search.toString()}</output>
          {actual.ReviewViewer(props)}
        </>
      );
    },
  };
});

const current = example as unknown as ReviewDocument;

class FakeEventSource {
  static readonly CLOSED = 2;
  static instances: FakeEventSource[] = [];
  readonly url: string;
  readyState = 1;
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
    this.readyState = FakeEventSource.CLOSED;
  }

  emit(name: string) {
    for (const listener of this.listeners.get(name) ?? []) listener();
  }
}

let root: Root | null = null;
let container: HTMLElement;
/** The document's requests. */
type Fetcher = (url: string, init?: RequestInit) => Promise<Response>;
let fetchMock: ReturnType<typeof vi.fn<Fetcher>>;
/** The `/api/sessions` requests. */
let sessionsMock: ReturnType<typeof vi.fn<Fetcher>>;

function respond(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status });
}

function titled(title: string): ReviewDocument {
  const document = structuredClone(current);
  document.summary.title = title;
  return document;
}

const advance = (ms: number) => act(async () => void vi.advanceTimersByTime(ms));

function deferred() {
  let resolve!: (response: Response) => void;
  const promise = new Promise<Response>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

async function mount() {
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => root!.render(<App />));
}

const source = () => FakeEventSource.instances.at(-1)!;
/** The server refused the stream, so the browser gave up on it. */
const refuse = () =>
  act(async () => {
    source().readyState = FakeEventSource.CLOSED;
    source().emit("error");
  });
const search = () => container.querySelector('[data-testid="search"]')?.textContent;
const staleNotice = () =>
  [...container.querySelectorAll('[role="alert"]')].find((alert) =>
    alert.textContent?.includes("Couldn't reload"),
  );

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
  viewer.failure = null;
  fetchMock = vi.fn<Fetcher>();
  // Nothing on this machine, unless a test says otherwise.
  sessionsMock = vi.fn<Fetcher>(async () =>
    respond({ available: false, code: "not_a_clone", reason: "this is not a clone" }),
  );
  vi.stubGlobal("fetch", (url: string, init?: RequestInit) =>
    String(url).startsWith("/api/sessions") ? sessionsMock(url, init) : fetchMock(url, init),
  );
  vi.stubGlobal("EventSource", FakeEventSource);
  window.history.replaceState(null, "", "/");
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.replaceChildren();
  localStorage.clear();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("App", () => {
  it("renders the document and titles the page by the pull request", async () => {
    fetchMock.mockImplementation(async () => respond(current));
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

  it("shows the failed state for a body that is not a review document", async () => {
    fetchMock.mockImplementation(async () => respond({ hello: "world" }));
    await mount();

    expect(container.querySelector('[role="alert"]')?.textContent).toContain("baum.review/1");
    expect(container.querySelector('[data-testid="search"]')).toBeNull();
  });

  it("refetches on a document event and keeps the URL", async () => {
    window.history.replaceState(null, "", "/?pane=summary");
    fetchMock.mockResolvedValueOnce(respond(current));
    await mount();

    fetchMock.mockResolvedValueOnce(respond(titled("A title that changed on disk")));
    expect(source().url).toBe("/api/events");
    await act(async () => source().emit("document"));

    expect(container.textContent).toContain("A title that changed on disk");
    expect(document.title).toContain("A title that changed on disk");
    expect(window.location.search).toBe("?pane=summary");
    expect(search()).toBe("pane=summary");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("refetches when the stream opens, including after a reconnect", async () => {
    fetchMock.mockResolvedValueOnce(respond(current));
    await mount();

    fetchMock.mockResolvedValueOnce(respond(titled("Changed while disconnected")));
    await act(async () => source().emit("open"));

    expect(container.textContent).toContain("Changed while disconnected");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("reopens a closed stream after a growing, capped delay", async () => {
    vi.useFakeTimers();
    fetchMock.mockImplementation(async () => respond(current));
    await mount();

    // A stream the browser is retrying itself is left alone.
    await act(async () => source().emit("error"));
    await advance(60_000);
    expect(FakeEventSource.instances).toHaveLength(1);

    await refuse();

    await advance(999);
    expect(FakeEventSource.instances).toHaveLength(1);
    await advance(1);
    expect(FakeEventSource.instances).toHaveLength(2);

    // The delay doubles while the stream keeps failing.
    await refuse();
    await advance(1999);
    expect(FakeEventSource.instances).toHaveLength(2);
    await advance(1);
    expect(FakeEventSource.instances).toHaveLength(3);

    // And is capped at 30 seconds.
    for (let delay = 4000; delay < 30_000; delay *= 2) {
      await refuse();
      await advance(delay);
    }
    const count = FakeEventSource.instances.length;
    await refuse();
    await advance(30_000);
    expect(FakeEventSource.instances).toHaveLength(count + 1);

    // A pending reopen is cancelled on unmount.
    await refuse();
    act(() => root?.unmount());
    root = null;
    await advance(60_000);
    expect(FakeEventSource.instances).toHaveLength(count + 1);
    expect(FakeEventSource.instances.every((instance) => instance.closed)).toBe(true);
  });

  it("keeps the newer document when an older response arrives last", async () => {
    const first = deferred();
    fetchMock.mockReturnValueOnce(first.promise);
    await mount();

    fetchMock.mockResolvedValueOnce(respond(titled("The newer document")));
    await act(async () => source().emit("document"));
    expect(container.textContent).toContain("The newer document");

    await act(async () => first.resolve(respond(titled("The older document"))));
    expect(container.textContent).toContain("The newer document");
    expect(container.textContent).not.toContain("The older document");
  });

  it("marks the document stale when a refetch fails, until the next success", async () => {
    fetchMock.mockResolvedValueOnce(respond(current));
    await mount();
    expect(staleNotice()).toBeUndefined();

    fetchMock.mockResolvedValueOnce(respond({}, 500));
    await act(async () => source().emit("document"));
    expect(staleNotice()?.textContent).toContain("showing the last version loaded");
    expect(container.textContent).toContain(current.summary.title);

    fetchMock.mockResolvedValueOnce(respond(titled("Back again")));
    const retry = [...staleNotice()!.querySelectorAll("button")].find(
      (button) => button.textContent === "Retry",
    );
    await act(async () => retry!.click());
    expect(staleNotice()).toBeUndefined();
    expect(container.textContent).toContain("Back again");
  });

  it("requests nothing but the document and the sessions", async () => {
    fetchMock.mockImplementation(async () => respond(current));
    await mount();
    await act(async () => source().emit("document"));

    const urls = fetchMock.mock.calls.map(([url]) => String(url));
    expect(urls.length).toBeGreaterThan(1);
    expect(new Set(urls)).toEqual(new Set(["/api/document"]));
    expect(new Set(sessionsMock.mock.calls.map(([url]) => String(url)))).toEqual(
      new Set(["/api/sessions"]),
    );
    expect(FakeEventSource.instances.map((instance) => instance.url)).toEqual(["/api/events"]);
  });

  it("closes the event stream when it unmounts", async () => {
    fetchMock.mockImplementation(async () => respond(current));
    await mount();
    act(() => root?.unmount());
    root = null;

    expect(FakeEventSource.instances.every((instance) => instance.closed)).toBe(true);
  });

  it("follows Back and Forward in the URL", async () => {
    window.history.replaceState(null, "", "/?pane=summary");
    fetchMock.mockImplementation(async () => respond(current));
    await mount();
    expect(search()).toBe("pane=summary");

    window.history.pushState(null, "", "/?pane=risks");
    await act(async () => void window.dispatchEvent(new PopStateEvent("popstate")));
    expect(search()).toBe("pane=risks");
  });

  it("shows the failed state with Retry when the viewer throws while rendering", async () => {
    fetchMock.mockImplementation(async () => respond(current));
    viewer.failure = new Error("The viewer broke");
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    await mount();

    expect(container.querySelector('[role="alert"]')?.textContent).toContain("The viewer broke");

    viewer.failure = null;
    const retry = [...container.querySelectorAll("button")].find((b) => b.textContent === "Retry");
    await act(async () => retry!.click());
    logged.mockRestore();

    expect(container.querySelector('[role="alert"]')).toBeNull();
    expect(container.textContent).toContain(current.summary.title);
  });
});

describe("sessions and commits", () => {
  const OID_ONE = "1111111111111111111111111111111111111111";
  const OID_TWO = "2222222222222222222222222222222222222222";
  const SESSION = "aaaaaaaa-0000-4000-8000-000000000001";
  const local = {
    available: true,
    sessions: [
      {
        id: SESSION,
        source: "claude_code",
        vendor: "claude_code",
        title: "Wire the retry budget",
        branch: "feature",
        started_at: "2026-07-01T10:00:00Z",
        ended_at: "2026-07-01T10:30:00Z",
        outcome: "committed",
        edited_files: 3,
        unknown_field: { ignored: true },
      },
    ],
    commits: [
      {
        oid: OID_ONE,
        subject: "Add the retry budget",
        author: "Grace Hopper",
        committed_at: "2026-07-01T10:30:00Z",
        links: [{ session: SESSION, method: "commit_ref", confidence: 0.95 }],
      },
      { oid: OID_TWO, subject: "Tidy the docs", links: [] },
    ],
  };
  const recorded = current.index.sessions[0]!;

  const rail = () => container.querySelector("nav");
  const railText = () => rail()?.textContent ?? "";
  const pane = () => container.querySelector("#review-section")?.textContent ?? "";
  const rows = (label: string) =>
    [...(rail()?.querySelectorAll("button") ?? [])].filter((button) =>
      button.textContent?.includes(label),
    );
  /** Unfolds a rail group, which the reader does by clicking its row. */
  const openGroup = (label: string) =>
    act(async () => {
      for (const button of rail()?.querySelectorAll("button") ?? []) {
        if (button.textContent?.startsWith(label)) {
          button.click();
          return;
        }
      }
      throw new Error(`no ${label} group in the rail`);
    });
  const press = (label: string) =>
    act(
      async () =>
        void [...container.querySelectorAll("button")]
          .find((button) => button.textContent?.includes(label))!
          .click(),
    );

  beforeEach(() => {
    fetchMock.mockImplementation(async () => respond(current));
  });

  it("lists the linked sessions and commits when the machine has them", async () => {
    sessionsMock.mockImplementation(async () => respond(local));
    window.history.replaceState(null, "", "/?pane=sessions");
    await mount();

    expect(sessionsMock).toHaveBeenCalledWith(
      "/api/sessions",
      expect.objectContaining({ cache: "no-store" }),
    );
    expect(railText()).toContain("Sessions1");
    expect(railText()).toContain("Commits2");
    expect(pane()).toContain("Wire the retry budget");
    expect(pane()).toContain("1 linked commit");
    expect(pane()).not.toContain("recorded in the review");

    await openGroup("Sessions");
    await openGroup("Commits");
    expect(rows("Wire the retry budget")).toHaveLength(1);
    expect(rows("Add the retry budget")).toHaveLength(1);
    expect(rows("Tidy the docs")).toHaveLength(1);
  });

  it("opens a selected session and a selected commit from the URL", async () => {
    sessionsMock.mockImplementation(async () => respond(local));
    window.history.replaceState(null, "", `/?pane=session&session=${SESSION}`);
    await mount();
    expect(pane()).toContain("1 of 1 session");
    expect(pane()).toContain("Wire the retry budget");

    window.history.pushState(null, "", `/?pane=commit&commit=${OID_ONE}`);
    await act(async () => void window.dispatchEvent(new PopStateEvent("popstate")));
    expect(pane()).toContain("1 of 2 commits");
    expect(pane()).toContain("Add the retry budget");
    expect(pane()).toContain("95% confidence");

    window.history.pushState(null, "", "/?pane=commits");
    await act(async () => void window.dispatchEvent(new PopStateEvent("popstate")));
    expect(pane()).toContain("Tidy the docs");
  });

  it("moves between a commit and its session", async () => {
    sessionsMock.mockImplementation(async () => respond(local));
    window.history.replaceState(null, "", "/?pane=commits");
    await mount();

    await press("Wire the retry budget");
    expect(search()).toBe(`pane=session&session=${SESSION}`);
    await press("Add the retry budget");
    expect(search()).toBe(`pane=commit&commit=${OID_ONE}`);
  });

  it("falls back to the document's sessions, with the reason and no Commits group", async () => {
    sessionsMock.mockImplementation(async () =>
      respond({
        available: false,
        code: "remote_mismatch",
        reason: "no remote matches <b>this</b>",
      }),
    );
    window.history.replaceState(null, "", "/?pane=sessions");
    await mount();

    expect(railText()).not.toContain("Commits");
    expect(pane()).toContain(
      "Showing the sessions recorded in the review — no remote matches <b>this</b>",
    );
    // The reason is text, never markup.
    expect(container.querySelector("#review-section b")).toBeNull();
    expect(pane()).toContain(recorded.title!);
    await openGroup("Sessions");
    expect(rows(recorded.title!)).toHaveLength(1);
  });

  it("keeps a deep link to Commits in the URL when there are none to show", async () => {
    window.history.replaceState(null, "", "/?pane=commits");
    await mount();

    expect(railText()).not.toContain("Commits");
    expect(search()).toBe("pane=commits");
  });

  it.each([
    ["a network error", () => Promise.reject(new TypeError("Failed to fetch"))],
    ["a 500", async () => respond({ error: "boom" }, 500)],
    ["text that is not JSON", async () => new Response("<html>", { status: 200 })],
    ["a body that is not an answer", async () => respond([1, 2])],
    ["an available answer without commits", async () => respond({ available: true, sessions: [] })],
    [
      "a session of the wrong type",
      async () => respond({ available: true, commits: [], sessions: [{ id: 7 }] }),
    ],
    [
      "a commit link of the wrong type",
      async () =>
        respond({
          available: true,
          sessions: [],
          commits: [{ oid: OID_ONE, subject: "x", links: [{ session: SESSION, method: 1 }] }],
        }),
    ],
  ])("falls back to the document's sessions on %s", async (_, answer) => {
    sessionsMock.mockImplementation(answer);
    window.history.replaceState(null, "", "/?pane=sessions");
    await mount();

    expect(railText()).not.toContain("Commits");
    expect(pane()).toContain("Showing the sessions recorded in the review");
    await openGroup("Sessions");
    expect(rows(recorded.title!)).toHaveLength(1);
    expect(container.querySelector('[role="alert"]')).toBeNull();
  });

  it("fails the Sessions group only when the request failed and the review has none", async () => {
    sessionsMock.mockImplementation(async () => respond({ error: "boom" }, 500));
    const bare = structuredClone(current);
    bare.index.sessions = [];
    fetchMock.mockImplementation(async () => respond(bare));
    window.history.replaceState(null, "", "/?pane=sessions");
    await mount();

    await openGroup("Sessions");
    expect(railText()).toContain("Linked sessions could not be loaded.");
    expect(container.querySelector('[role="alert"]')?.textContent).toContain(
      "The sessions could not be loaded.",
    );
  });

  it("lists an empty fallback, not a failure, when the server said it is unavailable", async () => {
    const bare = structuredClone(current);
    bare.index.sessions = [];
    fetchMock.mockImplementation(async () => respond(bare));
    window.history.replaceState(null, "", "/?pane=sessions");
    await mount();

    await openGroup("Sessions");
    expect(railText()).toContain("No linked sessions.");
    expect(pane()).toContain("this is not a clone");
  });

  it("shows the sessions loading, and the review, while the request is out", async () => {
    const pending = deferred();
    sessionsMock.mockReturnValueOnce(pending.promise);
    window.history.replaceState(null, "", "/?pane=sessions");
    await mount();

    expect(container.textContent).toContain(current.summary.title);
    expect(container.querySelector('[aria-label="Loading sessions"]')).not.toBeNull();
    expect(search()).toBe("pane=sessions");
    await openGroup("Commits");
    expect(railText()).toContain("Loading commits…");
    await openGroup("Sessions");
    expect(railText()).toContain("Loading linked sessions…");

    await act(async () => pending.resolve(respond(local)));
    expect(container.querySelector('[aria-label="Loading sessions"]')).toBeNull();
    expect(pane()).toContain("Wire the retry budget");
    expect(search()).toBe("pane=sessions");
  });

  it("keeps a Commits deep link through the loading state", async () => {
    const pending = deferred();
    sessionsMock.mockReturnValueOnce(pending.promise);
    window.history.replaceState(null, "", `/?pane=commit&commit=${OID_TWO}`);
    await mount();
    expect(container.querySelector('[aria-label="Loading commits"]')).not.toBeNull();

    await act(async () => pending.resolve(respond(local)));
    expect(pane()).toContain("1 of 2 commits");
    expect(pane()).toContain("Tidy the docs");
  });

  it("asks again when the document changes, and keeps the last answer meanwhile", async () => {
    sessionsMock.mockImplementation(async () => respond(local));
    window.history.replaceState(null, "", "/?pane=sessions");
    await mount();
    expect(sessionsMock).toHaveBeenCalledTimes(1);

    const pending = deferred();
    sessionsMock.mockReturnValueOnce(pending.promise);
    await act(async () => source().emit("document"));
    expect(sessionsMock).toHaveBeenCalledTimes(2);
    expect(pane()).toContain("Wire the retry budget");
    expect(container.querySelector('[aria-label="Loading sessions"]')).toBeNull();

    await act(async () =>
      pending.resolve(
        respond({
          available: true,
          sessions: [],
          commits: [{ oid: OID_TWO, subject: "Only", links: [] }],
        }),
      ),
    );
    expect(pane()).toContain("No agent session is linked");
    await openGroup("Commits");
    expect(rows("Only")).toHaveLength(1);
  });

  it("does not ask again when the stream merely opens", async () => {
    await mount();
    expect(sessionsMock).toHaveBeenCalledTimes(1);
    await act(async () => source().emit("open"));
    expect(sessionsMock).toHaveBeenCalledTimes(1);
  });
});

describe("AppLink", () => {
  /** Clicks the link and reports whether the app took the click over. */
  async function click(props: Record<string, unknown>, init: MouseEventInit = {}) {
    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    await act(async () => root!.render(<AppLink href="/?pane=risks" {...props} />));

    let prevented = false;
    const record = (event: Event) => {
      prevented = event.defaultPrevented;
      // Nothing may navigate the test page.
      event.preventDefault();
    };
    document.addEventListener("click", record);
    const event = new MouseEvent("click", { bubbles: true, cancelable: true, ...init });
    await act(async () => void container.querySelector("a")!.dispatchEvent(event));
    document.removeEventListener("click", record);
    return prevented;
  }

  it("moves within the page on a plain click", async () => {
    expect(await click({})).toBe(true);
    expect(window.location.search).toBe("?pane=risks");
  });

  it.each([["metaKey"], ["ctrlKey"], ["shiftKey"], ["altKey"]])(
    "leaves a click with %s to the browser",
    async (modifier) => {
      expect(await click({}, { [modifier]: true })).toBe(false);
      expect(window.location.search).toBe("");
    },
  );

  it("leaves a middle click to the browser", async () => {
    expect(await click({}, { button: 1 })).toBe(false);
    expect(window.location.search).toBe("");
  });

  it('leaves target="_blank" and other origins to the browser', async () => {
    expect(await click({ target: "_blank" })).toBe(false);
    expect(await click({ href: "https://example.com/" })).toBe(false);
    expect(window.location.search).toBe("");
  });
});
