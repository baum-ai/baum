import { describe, expect, it } from "vitest";
import { commentTarget, pendingSubmission, type PendingComment } from "../pending-review-model";
import { pageContextOf } from "../page-context";
import { fromV1 } from "../review-view/from-v1";
import { withReview, type ViewTile } from "../review-view/model";
import { fixtureReview, fixtureTile } from "./fixture-test-helper";

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
  it("is reported to the chat with the tab and the section read", () => {
    const of = (query: string) => pageContextOf(new URLSearchParams(query), () => undefined, "");
    const pr = "repository=acme%2Fdemo&pr=7";
    expect(of(pr).focus).toEqual({ tab: "overview" });
    expect(of(`${pr}&pane=section&section=sec_retry`).focus).toEqual({
      tab: "sections",
      section: "sec_retry",
    });
    expect(of(`${pr}&pane=section&section=context`).focus).toEqual({
      tab: "sections",
      section: "context",
    });
    expect(of(`${pr}&pane=risk&risk_section=sec_retry&risk_id=rsk_storm`).focus).toEqual({
      tab: "sections",
      section: "sec_retry",
      item: "rsk_storm",
    });
    expect(of(`${pr}&pane=tile&tile=code&tile_item=a%2Fb.go`).focus).toEqual({
      tab: "code",
      file: "a/b.go",
    });
    expect(of(`${pr}&pane=tile&tile=dg_flow`).focus).toEqual({
      tab: "sections",
      tile: "dg_flow",
    });
    expect(of("group=g1&pane=section&section=sec_a")).toEqual({ page: "group", group: "g1" });
  });
});
