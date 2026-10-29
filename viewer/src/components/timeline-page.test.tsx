// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import type { DecisionDetail, DecisionSummary, FeatureSummary } from "../decision-log";
import { type UrlState, UrlStateProvider } from "../navigation";
import type { DetailState } from "./decision-log";
import { type TimelineList, TimelinePage, type TimelinePageProps } from "./timeline-page";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

const decision = (id: string, decided_at: string, summary: string): DecisionSummary => ({
  id,
  session_id: "s1",
  turn: 3,
  decided_at,
  who: "human",
  person: null,
  vendor: "claude_code",
  category: "data_model",
  summary,
  superseded: false,
  file_count: 0,
});

const list = <T,>(rows: T[], more: Partial<TimelineList<T>> = {}): TimelineList<T> => ({
  rows,
  first: "ready",
  cursor: null,
  status: "idle",
  loadMore: () => {},
  ...more,
});

let root: Root | null = null;
let container: HTMLElement;
let search: URLSearchParams;
let writes: { search: string; replace?: boolean }[];

beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  // The page brings an opened row into view, which jsdom does not draw.
  Element.prototype.scrollIntoView ??= () => {};
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  search = new URLSearchParams();
  writes = [];
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  container.remove();
});

function draw(props: Partial<TimelinePageProps<string>> = {}) {
  const url: UrlState = {
    search,
    update: (update, options) => {
      search = update(new URLSearchParams(search));
      writes.push({ search: search.toString(), replace: options?.replace });
      draw(props);
    },
  };
  const all: TimelinePageProps<string> = {
    withFeatures: false,
    features: list<FeatureSummary>([]),
    decisions: list([]),
    readDecision: async () => ({ status: "missing" }),
    readFeature: async () => ({ status: "missing" }),
    readPeople: async () => [],
    files: async () => [],
    openSession: { load: async (id) => id, render: (id) => <p>{id}</p> },
    pullRequestHref: (repository, number) => `/${repository}/pull/${number}`,
    repository: "acme/widgets",
    filterBar: (controls) => <div data-testid="bar">{controls}</div>,
    tabsClassName: "tabs",
    bodyClassName: "body",
    noFeatures: (showAll) => (
      <button type="button" onClick={showAll}>
        Show all
      </button>
    ),
    noDecisions: "Nothing read yet.",
    ...props,
  };
  act(() =>
    root!.render(
      <UrlStateProvider value={url}>
        <TimelinePage {...all} />
      </UrlStateProvider>,
    ),
  );
}

it("draws a log without features as the timeline alone, by day, and opens a row in the address", async () => {
  const readDecision = vi.fn(async () => ({ status: "missing" as const }));
  draw({
    readDecision,
    decisions: list([
      decision("d2", "2026-10-06T15:00:00", "Page with a cursor"),
      decision("d1", "2026-10-05T15:00:00", "Store per session"),
    ]),
  });
  expect(container.querySelector('[role="tablist"]')).toBeNull();
  expect([...container.querySelectorAll("section")].map((each) => each.ariaLabel)).toEqual([
    "Oct 6, 2026",
    "Oct 5, 2026",
  ]);
  expect(container.textContent).toContain("No older decisions.");

  const row = container.querySelector<HTMLButtonElement>("li[data-decision] > button")!;
  await act(async () => row.click());
  expect(writes).toEqual([{ search: "open=d2", replace: true }]);
  // Read once, and kept when it closes and opens again.
  await act(async () => row.click());
  await act(async () => row.click());
  expect(readDecision).toHaveBeenCalledTimes(1);
});

it("says what an empty list says, and switches to All decisions from an empty Features tab", () => {
  draw({ withFeatures: true });
  expect(container.querySelector('[role="tablist"]')).not.toBeNull();
  expect(container.textContent).toContain("No features yet");
  const showAll = [...container.querySelectorAll("button")].find(
    (each) => each.textContent === "Show all",
  );
  act(() => showAll?.click());
  expect(search.get("tab")).toBe("all");
  expect(container.textContent).toContain("No decisions yetNothing read yet.");
});

it("shows a list still being read, one that failed, and a failed older page with its button", () => {
  draw({ decisions: list([], { first: "loading" }), loading: (label) => <p>{label}</p> });
  expect(container.textContent).toContain("Loading decisions…");
  draw({ decisions: list([], { first: "failed" }) });
  expect(container.textContent).toContain("The decision log could not be loaded.");
  const loadMore = vi.fn();
  draw({
    decisions: list([decision("d1", "2026-10-05T15:00:00", "Store per session")], {
      cursor: "c1",
      status: "failed",
      loadMore,
    }),
  });
  expect(container.querySelector('[role="alert"]')?.textContent).toBe(
    "Older decisions could not be loaded.",
  );
  const button = [...container.querySelectorAll("button")].find(
    (each) => each.textContent === "Load older decisions",
  );
  act(() => button?.click());
  expect(loadMore).toHaveBeenCalledOnce();
});

/** The detail of `d1`, its rejected alternative saying which read it came
 *  from. */
function detailOf(alternative: string): DetailState {
  const detail: DecisionDetail = {
    session: {
      id: "s1",
      person: null,
      vendor: "claude_code",
      branch: "main",
      started_at: null,
      ended_at: null,
    },
    decisions: [
      {
        id: "d1",
        turn: 3,
        turns: [3],
        decided_at: "2026-10-05T15:00:00",
        who: "human",
        category: "data_model",
        summary: "Store per session",
        rejected_alternative: alternative,
        superseded: false,
        files: [],
      },
    ],
    turns: [],
    prompts_withheld: false,
    pull_requests: [],
  };
  return { status: "ready", detail };
}

it("keeps an open decision's detail shown while a newer revision reads it again", async () => {
  search = new URLSearchParams("open=d1");
  const pending: { answer?: (state: DetailState) => void } = {};
  const readDecision = vi
    .fn<TimelinePageProps<string>["readDecision"]>()
    .mockResolvedValueOnce(detailOf("One row per pull request"))
    .mockImplementationOnce(() => new Promise((resolve) => (pending.answer = resolve)));
  const decisions = list([decision("d1", "2026-10-05T15:00:00", "Store per session")]);
  await act(async () => draw({ readDecision, decisions, revision: 0 }));
  expect(container.textContent).toContain("One row per pull request");

  await act(async () => draw({ readDecision, decisions, revision: 1 }));
  expect(readDecision).toHaveBeenCalledTimes(2);
  expect(container.textContent).toContain("One row per pull request");
  expect(container.textContent).not.toContain("Loading this decision");

  await act(async () => pending.answer?.(detailOf("One row per pull request, then per session")));
  expect(container.textContent).toContain("then per session");
});
