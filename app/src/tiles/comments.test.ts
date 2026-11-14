import { describe, expect, it, vi } from "vitest";
import type { UrlState } from "@baum/viewer/navigation";
import type { ReviewDocument } from "@baum/viewer/review-document";
import baseReview from "../../../tiles/testdata/base.review.json" with { type: "json" };
import { commentTarget, pendingSubmission, type PendingComment } from "../pending-review-model";
import { pageContextOf } from "../page-context";
import { commentCounts } from "./comments";
import { golden } from "./goldens-test-helper";
import { backToLanding, noteLanding } from "./keys";
import { withTiles } from "./tile-document";
import { plainText } from "./parts";
import { reviewPageOf } from "./view";

const review = () =>
  withTiles(structuredClone(baseReview) as unknown as ReviewDocument, golden("base"));

describe("text as a glance shows it", () => {
  it("drops markdown's marks only where they are markup", () => {
    expect(plainText("PR #123")).toBe("PR #123");
    expect(plainText("Written in C#")).toBe("Written in C#");
    expect(plainText("When a > b")).toBe("When a > b");
    expect(plainText("## Title\n> quoted\n- **bold** and *it* and `code` and ~~gone~~")).toBe(
      "Title quoted bold and it and code and gone",
    );
    expect(plainText("Cites rsk_retry_storm", new Map([["rsk_retry_storm", "the storm"]]))).toBe(
      "Cites the storm",
    );
  });
});

describe("a comment on a tile", () => {
  it("on a Change is what the full review makes of its section or risk", () => {
    const doc = review();
    expect(commentTarget(doc, { kind: "tile", id: "sec_retry_policy" })).toEqual(
      commentTarget(doc, { kind: "section", id: "sec_retry_policy" }),
    );
    const risk = commentTarget(doc, {
      kind: "tile",
      id: "sec_retry_policy",
      item: "rsk_retry_storm",
    });
    expect(risk).toEqual(
      commentTarget(doc, { kind: "risk", section: "sec_retry_policy", id: "rsk_retry_storm" }),
    );
    expect(risk.target.kind).toBe("risk");
  });

  it("on a Change is its section's, on the first line its summary cites", () => {
    const { target, place } = commentTarget(review(), { kind: "tile", id: "sec_retry_policy" });
    expect(target).toEqual({
      kind: "section",
      id: "sec_retry_policy",
      title: "Retry failed deliveries",
    });
    expect(place).toEqual({ path: "worker/deliver.go", line: 30, side: "RIGHT", start_line: 10 });
  });

  it("on a risk keeps the risk target, on the risk's own code", () => {
    const { target, place } = commentTarget(review(), {
      kind: "tile",
      id: "risks",
      item: "rsk_retry_storm",
    });
    expect(target.kind).toBe("risk");
    expect(target.id).toBe("rsk_retry_storm");
    expect(place?.path).toBe("worker/deliver.go");
  });

  it("on evidence or a decision names the item and places on its code", () => {
    const evidence = commentTarget(review(), {
      kind: "tile",
      id: "evidence",
      item: "ev_retry_test",
    });
    expect(evidence.target).toEqual({
      kind: "tile",
      id: "evidence",
      title: "Evidence: A test drives five failures and checks the delays",
    });
    expect(evidence.place?.path).toBe("worker/deliver_test.go");
    const decision = commentTarget(review(), {
      kind: "tile",
      id: "decisions",
      item: "dec_doubling_delay",
    });
    expect(decision.target.title).toBe("Decisions: Double the delay rather than use a fixed one");
    expect(decision.place?.path).toBe("worker/deliver.go");
  });

  it("on Everything else lands on its first hunk", () => {
    const { target, place } = commentTarget(review(), { kind: "tile", id: "everything_else" });
    expect(target.kind).toBe("tile");
    expect(place?.path).toBe("client/client.gen.go");
  });

  it("on the Overview, Intent, Context or Media goes in the review's body under its title", () => {
    for (const id of ["overview", "intent", "context", "media"]) {
      const { target, place, head } = commentTarget(review(), { kind: "tile", id });
      expect(target.kind, id).toBe("tile");
      expect(place, id).toBeNull();
      const comment: PendingComment = {
        id: "c1",
        target,
        body: "Looks fine",
        place,
        head,
        created: 0,
      };
      const sent = pendingSubmission(
        { repository: "acme/widgets", number: 42, head_sha: head },
        { comments: [comment], note: "" },
        "COMMENT",
      );
      expect(sent.comments, id).toEqual([]);
      expect(sent.body, id).toBe(`**${target.title}**\nLooks fine`);
    }
  });

  it("is counted on the tile that shows it", () => {
    const doc = review();
    const counts = commentCounts(doc, [
      { target: { kind: "tile", id: "evidence", title: "" } },
      { target: { kind: "section", id: "sec_config_flag", title: "" } },
      { target: { kind: "section", id: "sec_generated_client", title: "" } },
      { target: { kind: "risk", id: "rsk_retry_storm", title: "" } },
      { target: { kind: "general", id: "", title: "" } },
    ]);
    expect(Object.fromEntries(counts)).toEqual({
      evidence: 1,
      sec_config_flag: 1,
      everything_else: 1,
      risks: 1,
    });
  });
});

describe("the review page", () => {
  it("is the setting, unless the address asks for the other", () => {
    expect(reviewPageOf(new URLSearchParams(), "full")).toBe("full");
    expect(reviewPageOf(new URLSearchParams(), "tiles")).toBe("tiles");
    expect(reviewPageOf(new URLSearchParams("view=tiles"), "full")).toBe("tiles");
    expect(reviewPageOf(new URLSearchParams("view=full"), "tiles")).toBe("full");
    expect(reviewPageOf(new URLSearchParams("view=grid"), "full")).toBe("full");
  });

  it("is reported to the chat with the tile and item open", () => {
    const of = (query: string, page: "full" | "tiles" = "full") =>
      pageContextOf(new URLSearchParams(query), () => undefined, "", page);
    expect(of("repository=acme%2Fdemo&pr=7")).toEqual({
      page: "pull_request",
      repository: "acme/demo",
      pull_request: 7,
    });
    expect(of("repository=acme%2Fdemo&pr=7", "tiles").focus).toEqual({ view: "tiles" });
    expect(
      of("repository=acme%2Fdemo&pr=7&view=tiles&pane=tile&tile=risks&tile_item=rsk_a").focus,
    ).toEqual({ view: "tiles", tile: "risks", item: "rsk_a" });
    expect(of("group=g1&pane=tile&tile=overview", "tiles")).toEqual({
      page: "group",
      group: "g1",
      focus: { view: "tiles", tile: "overview" },
    });
    expect(of("repository=acme%2Fdemo&pr=7&view=full&pane=tile&tile=risks", "tiles").focus).toBe(
      undefined,
    );
  });
});

describe("Escape and u in a tile", () => {
  const url = (query: string) => {
    const state = { search: new URLSearchParams(query), update: vi.fn() };
    return state as unknown as UrlState & { update: ReturnType<typeof vi.fn> };
  };
  const key = (name: string, target: EventTarget = document.body) => {
    const event = new KeyboardEvent("keydown", { key: name, cancelable: true });
    Object.defineProperty(event, "target", { value: target });
    return event;
  };

  it("go back to the landing page", () => {
    for (const name of ["Escape", "u"]) {
      const state = url("pr=7&pane=tile&tile=risks&more=1");
      expect(backToLanding(key(name), state)).toBe(true);
      const next = state.update.mock.calls[0]![0](new URLSearchParams(state.search));
      expect(next.toString()).toBe("pr=7&more=1");
    }
  });

  it("go back through history to the landing page shown before, else take the tile's place", () => {
    const back = vi.spyOn(history, "back").mockImplementation(() => {});
    history.replaceState({ depth: 4 }, "");
    noteLanding(new URLSearchParams("pr=7&more=1"));
    history.replaceState({ depth: 5 }, "");
    const opened = url("pr=7&more=1&pane=tile&tile=risks");
    expect(backToLanding(key("Escape"), opened)).toBe(true);
    expect(back).toHaveBeenCalledTimes(1);
    expect(opened.update).not.toHaveBeenCalled();
    // Two tiles on, or arrived by a link: the landing page takes the tile's
    // entry, so Back does not open the tile again.
    history.replaceState({ depth: 6 }, "");
    const stepped = url("pr=7&more=1&pane=tile&tile=media");
    expect(backToLanding(key("u"), stepped)).toBe(true);
    expect(back).toHaveBeenCalledTimes(1);
    expect(stepped.update.mock.calls[0]![1]).toMatchObject({ replace: true });
    back.mockRestore();
  });

  it("leave the landing page, the rail and the side pane alone", () => {
    expect(backToLanding(key("Escape"), url("pr=7"))).toBe(false);
    const rail = document.createElement("nav");
    rail.id = "review-rail";
    const row = document.createElement("button");
    rail.append(row);
    document.body.append(rail);
    expect(backToLanding(key("Escape", row), url("pr=7&pane=tile&tile=risks"))).toBe(false);
    rail.remove();
  });
});
