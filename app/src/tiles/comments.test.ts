import { describe, expect, it } from "vitest";
import { commentTarget, pendingSubmission, type PendingComment } from "../pending-review-model";
import { pageContextOf } from "../page-context";
import { fromV1 } from "../review-view/from-v1";
import { withReview, type ViewTile } from "../review-view/model";
import { fixtureReview, fixtureTile } from "./fixture-test-helper";
import { reviewPageOf } from "./view";

/** The fixture's review served with its custom tile, a native diagram
 *  citing a hunk and a prose note citing nothing. */
const review = () => {
  const document = fixtureReview();
  const tiles: ViewTile[] = [
    { ...fixtureTile("route_changes"), owner: "sec_retry", cites: [] },
    {
      id: "dg_flow",
      type: "flow",
      owner: "sec_retry",
      title: "How a retry runs",
      state: "ready",
      cites: [{ type: "hunk", id: "h_000000000001" }],
      data: {
        lanes: [{ steps: [{ title: "Fail" }, { title: "Wait" }] }],
        source: [{ type: "hunk", id: "h_000000000001" }],
      },
    },
    {
      id: "notes",
      type: "prose",
      owner: "sec_retry",
      title: "Notes",
      state: "ready",
      cites: [],
      data: { text: "Nothing to cite.", anchors: [] },
    },
  ];
  return withReview(document, { ...fromV1(document), tiles });
};

describe("a comment on a tile", () => {
  it("is the tile's, on the first line of the code its data cites", () => {
    const { target, place } = commentTarget(review(), { kind: "tile", id: "route_changes" });
    expect(target).toEqual({ kind: "tile", id: "route_changes", title: "Route changes" });
    expect(place?.path).toBe("config/config.go");
  });

  it("on a native diagram is placed on its source", () => {
    const { target, place } = commentTarget(review(), { kind: "tile", id: "dg_flow" });
    expect(target.title).toBe("How a retry runs");
    expect(place?.path).toBe("worker/deliver.go");
  });

  it("citing no code goes in the review's body under its title", () => {
    const { target, place, head } = commentTarget(review(), { kind: "tile", id: "notes" });
    expect(target.kind).toBe("tile");
    expect(place).toBeNull();
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
    expect(sent.comments).toEqual([]);
    expect(sent.body).toBe("**Notes**\nLooks fine");
  });

  it("on a tile the view does not have is a general comment", () => {
    expect(commentTarget(review(), { kind: "tile", id: "gone" }).target.kind).toBe("general");
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

  it("is reported to the chat with the tab and the section read", () => {
    const of = (query: string, page: "full" | "tiles" = "full") =>
      pageContextOf(new URLSearchParams(query), () => undefined, "", page);
    const pr = "repository=acme%2Fdemo&pr=7";
    expect(of(pr)).toEqual({ page: "pull_request", repository: "acme/demo", pull_request: 7 });
    expect(of(pr, "tiles").focus).toEqual({ view: "tiles", tab: "overview" });
    expect(of(`${pr}&view=tiles&pane=section&section=sec_retry`).focus).toEqual({
      view: "tiles",
      tab: "sections",
      section: "sec_retry",
    });
    expect(of(`${pr}&view=tiles&pane=section&section=context`).focus).toEqual({
      view: "tiles",
      tab: "sections",
      section: "context",
    });
    expect(of(`${pr}&view=tiles&pane=risk&risk_section=sec_retry&risk_id=rsk_storm`).focus).toEqual(
      { view: "tiles", tab: "sections", section: "sec_retry", item: "rsk_storm" },
    );
    expect(of(`${pr}&view=tiles&pane=tile&tile=code&tile_item=a%2Fb.go`).focus).toEqual({
      view: "tiles",
      tab: "code",
      file: "a/b.go",
    });
    expect(of(`${pr}&view=tiles&pane=tile&tile=dg_flow`).focus).toEqual({
      view: "tiles",
      tab: "sections",
      tile: "dg_flow",
    });
    expect(of("group=g1&pane=section&section=sec_a", "tiles")).toEqual({
      page: "group",
      group: "g1",
      focus: { view: "tiles", tab: "sections", section: "sec_a" },
    });
    expect(of(`${pr}&view=full&pane=section&section=sec_a`, "tiles").focus).toBe(undefined);
  });
});
