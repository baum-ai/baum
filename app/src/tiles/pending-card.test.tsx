import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { draftLine, parseDrafts, placeDrafts, type DraftCard } from "./drafts";
import { PendingCard } from "./pending-card";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

beforeAll(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
});

const card = (patch: Partial<DraftCard> = {}): DraftCard => ({
  tile: "draft_1",
  title: "New environment variables",
  phase: "writing",
  ...patch,
});

describe("a pending card's line", () => {
  it("says the step the tile is at, then that it was left unfinished", () => {
    expect(draftLine(card())).toBe("Being made in the session panel · Writing the component");
    expect(draftLine(card({ phase: "checking", round: 1 }))).toBe(
      "Being made in the session panel · Checking",
    );
    expect(draftLine(card({ phase: "checking", round: 2 }))).toBe(
      "Being made in the session panel · Checking (round 2)",
    );
    expect(draftLine(card({ phase: "failed", reason: "Open the baum app to check tiles" }))).toBe(
      "Being made in the session panel · Open the baum app to check tiles",
    );
    expect(draftLine(card({ phase: "unfinished", round: 3 }))).toBe("Not finished");
  });
});

describe("the tile route's pending cards", () => {
  it("keeps the well-formed ones", () => {
    expect(
      parseDrafts([
        {
          tile: "draft_1",
          title: "Env",
          phase: "checking",
          round: 2,
          size: "half",
          to: { after: "overview" },
        },
        { tile: "draft_2", title: "Bad", phase: "weather" },
        "nonsense",
        {
          tile: "draft_3",
          title: "Edge",
          phase: "unfinished",
          size: "huge",
          to: { edge: "first" },
        },
      ]),
    ).toEqual([
      {
        tile: "draft_1",
        title: "Env",
        phase: "checking",
        round: 2,
        size: "half",
        to: { after: "overview" },
      },
      { tile: "draft_3", title: "Edge", phase: "unfinished", to: { edge: "first" } },
    ]);
    expect(parseDrafts(undefined)).toEqual([]);
  });

  it("places each card next to its tile, or at an edge of the grid", () => {
    const shown = new Set(["overview", "risks"]);
    const placed = placeDrafts(
      [
        card({ tile: "draft_1", to: { after: "overview" } }),
        card({ tile: "draft_2", to: { before: "risks" } }),
        card({ tile: "draft_3", to: { edge: "first" } }),
        card({ tile: "draft_4", to: { after: "gone" } }),
        card({ tile: "draft_5" }),
      ],
      shown,
    );
    expect(placed.after.get("overview")?.map((d) => d.tile)).toEqual(["draft_1"]);
    expect(placed.before.get("risks")?.map((d) => d.tile)).toEqual(["draft_2"]);
    expect(placed.first.map((d) => d.tile)).toEqual(["draft_3"]);
    expect(placed.last.map((d) => d.tile)).toEqual(["draft_4", "draft_5"]);
  });
});

describe("PendingCard", () => {
  let root: Root | null = null;
  let host: HTMLDivElement | null = null;
  afterEach(() => {
    act(() => root?.unmount());
    host?.remove();
    root = host = null;
  });
  const render = (draft: DraftCard, remove?: (tile: string) => void) => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    act(() => root!.render(<PendingCard draft={draft} remove={remove} />));
    return host.querySelector("article")!;
  };

  it("goes from being written through its check rounds, labelled as a custom tile", () => {
    let article = render(card());
    expect(article.dataset.draftPhase).toBe("writing");
    expect(article.textContent).toContain("Custom · New environment variables");
    expect(article.querySelector("[data-draft-line]")?.textContent).toBe(
      "Being made in the session panel · Writing the component",
    );
    act(() => root!.render(<PendingCard draft={card({ phase: "checking", round: 2 })} />));
    article = host!.querySelector("article")!;
    expect(article.dataset.draftPhase).toBe("checking");
    expect(article.textContent).toContain("Checking (round 2)");
  });

  it("offers Remove, which an unfinished card keeps", () => {
    const remove = vi.fn();
    const article = render(card({ phase: "unfinished" }), remove);
    expect(article.querySelector("[data-draft-line]")?.textContent).toBe("Not finished");
    const button = article.querySelector("button")!;
    expect(button.getAttribute("aria-label")).toBe("Remove New environment variables");
    act(() => button.click());
    expect(remove).toHaveBeenCalledWith("draft_1");
  });
});
