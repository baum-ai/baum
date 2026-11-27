import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { draftLine, draftsOn, parseDrafts, type DraftCard } from "./drafts";
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
        { tile: "draft_1", title: "Env", phase: "checking", round: 2, section: "sec_retry" },
        { tile: "draft_2", title: "Bad", phase: "weather" },
        "nonsense",
        { tile: "draft_3", title: "Edge", phase: "unfinished", section: 7, size: "half" },
      ]),
    ).toEqual([
      { tile: "draft_1", title: "Env", phase: "checking", round: 2, section: "sec_retry" },
      { tile: "draft_3", title: "Edge", phase: "unfinished" },
    ]);
    expect(parseDrafts(undefined)).toEqual([]);
  });

  it("stands each card on its section, the Overview, or the first listed one", () => {
    const drafts = [
      card({ tile: "draft_1", section: "sec_b" }),
      card({ tile: "draft_2", section: "context" }),
      card({ tile: "draft_3", section: "sec_gone" }),
      card({ tile: "draft_4" }),
      card({ tile: "draft_5", section: "overview" }),
    ];
    const listed = ["context", "sec_a", "sec_b"];
    const on = (section: string) => draftsOn(drafts, section, listed).map((d) => d.tile);
    expect(on("context")).toEqual(["draft_2", "draft_3"]);
    expect(on("sec_a")).toEqual([]);
    expect(on("sec_b")).toEqual(["draft_1"]);
    // A card naming no section stands on the Overview, as its tile will.
    expect(on("overview")).toEqual(["draft_4", "draft_5"]);
    // Without Where this fits listed, every card for a section not listed
    // stands on the first section.
    expect(draftsOn(drafts, "sec_a", ["sec_a"]).map((d) => d.tile)).toEqual([
      "draft_1",
      "draft_2",
      "draft_3",
    ]);
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

  it("goes from being written through its check rounds, under the tile's title", () => {
    let article = render(card());
    expect(article.dataset.draftPhase).toBe("writing");
    expect(article.querySelector("h3")?.textContent).toBe("New environment variables");
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
