import { describe, expect, it } from "vitest";
import { REVIEW_1303, review1303, VIEW_1303 } from "./fixture-1303";
import {
  CODE_PLACE,
  commentSelection,
  CONTEXT_ID,
  defaultSection,
  emptyView,
  intentRows,
  listedSections,
  notesByHunk,
  parseReviewView,
  placeOf,
  reviewViewOf,
  riskSummary,
  selectionOf,
  shownFile,
  tilesOn,
  viewFiles,
} from "./model";

describe("parseReviewView", () => {
  it("keeps a served view as it is", () => {
    expect(parseReviewView(JSON.parse(JSON.stringify(VIEW_1303)))).toEqual(VIEW_1303);
  });

  it("is null for anything that is not a view", () => {
    expect(parseReviewView(undefined)).toBeNull();
    expect(parseReviewView({ schema: "baum.composition/2" })).toBeNull();
  });

  it("drops a malformed entry, never the whole view", () => {
    const view = parseReviewView({
      schema: "baum.view/1",
      overview: {
        verdict: { text: " ", points: [] },
        screenshots: ["img_1", 4],
        intent: [{ asked: "Make it work", session: "s", prompt: 1 }, { asked: 3 }],
      },
      sections: [{ id: "sec_a", tiles: ["dg_a", 7] }, { tiles: [] }],
      tiles: [
        { id: "dg_a", type: "flow", title: "A", state: "ready", data: {} },
        { id: "dg_b", type: "flow", state: "drawn" },
        { type: "flow" },
      ],
    });
    expect(view?.overview.verdict).toBeNull();
    expect(view?.overview.screenshots).toEqual(["img_1"]);
    expect(view?.overview.intent).toEqual([
      { asked: "Make it work", got: "", session: "s", prompt: 1 },
    ]);
    expect(view?.sections).toEqual([{ id: "sec_a", tiles: ["dg_a"] }]);
    // A state the page does not know is drawn as unavailable.
    expect(view?.tiles.map((tile) => [tile.id, tile.state])).toEqual([
      ["dg_a", "ready"],
      ["dg_b", "unavailable"],
    ]);
  });
});

describe("the view a document carries", () => {
  it("is the served one, else the empty view with every section listed", () => {
    expect(reviewViewOf(review1303())).toEqual(VIEW_1303);
    const bare = reviewViewOf(review1303(null));
    expect(bare).toEqual(emptyView(REVIEW_1303));
    expect(bare.sections.map((section) => section.id)).toEqual([
      CONTEXT_ID,
      ...REVIEW_1303.sections.map((section) => section.id),
    ]);
    expect(bare.tiles).toEqual([]);
  });

  it("places tiles on their section, never the overview's diagram", () => {
    expect(tilesOn(VIEW_1303, "sec_allowlist").map((tile) => tile.id)).toEqual([
      "dg_allowlist",
      "dg_always_allow",
      "lib_api_change",
      "code_remote_routes",
    ]);
    const placedTwice = {
      ...VIEW_1303,
      sections: [{ id: "sec_pairing", tiles: ["dg_shape", "dg_pairing"] }],
    };
    expect(tilesOn(placedTwice, "sec_pairing").map((tile) => tile.id)).toEqual(["dg_pairing"]);
  });
});

describe("the sections list", () => {
  it("lists Where this fits first and opens on the first real section", () => {
    const list = listedSections(REVIEW_1303, VIEW_1303);
    expect(list.map((section) => section.id)).toEqual([
      CONTEXT_ID,
      "sec_pairing",
      "sec_relay",
      "sec_encryption",
      "sec_requests",
      "sec_allowlist",
      "sec_lifecycle",
      "sec_deps",
    ]);
    expect(list[0]).toMatchObject({ title: "Where this fits" });
    expect(list[1]?.line).toBe(
      "A pairing code holds a one-time secret that lasts 5 minutes and is never logged.",
    );
    expect(defaultSection(list)).toBe("sec_pairing");
  });

  it("leaves Where this fits out while context holds no tiles and the review none", () => {
    const review = { ...REVIEW_1303, summary: { ...REVIEW_1303.summary, context: undefined } };
    // As the daemon serves it: context listed first, empty.
    const view = {
      ...VIEW_1303,
      sections: [{ id: CONTEXT_ID, tiles: [] }, ...VIEW_1303.sections.slice(1)],
    };
    expect(listedSections(review, view)[0]?.id).toBe("sec_pairing");
    // A tile placed there brings it back.
    expect(listedSections(review, VIEW_1303)[0]?.id).toBe(CONTEXT_ID);
  });

  it("leaves Where this fits out of a review with no context and nothing placed on it", () => {
    const review = { ...REVIEW_1303, summary: { ...REVIEW_1303.summary, context: undefined } };
    const list = listedSections(review, emptyView(review));
    expect(list[0]?.id).toBe("sec_pairing");
  });

  it("lists sections no longer in the code last", () => {
    const review = {
      ...REVIEW_1303,
      sections: REVIEW_1303.sections.map((section) =>
        section.id === "sec_pairing"
          ? { ...section, derived: { ...section.derived, state: "stale" as const } }
          : section,
      ),
    };
    const list = listedSections(review, VIEW_1303);
    expect(list.at(-1)).toMatchObject({ id: "sec_pairing", obsolete: true });
    expect(defaultSection(list)).toBe("sec_relay");
  });
});

describe("places", () => {
  const place = (selection: Parameters<typeof placeOf>[0]) =>
    placeOf(selection, REVIEW_1303, VIEW_1303);

  it("reads the Summary as the Overview and the Changes as the first section", () => {
    expect(place({ kind: "summary" })).toEqual({ tab: "overview" });
    expect(place({ kind: "risks" })).toEqual({ tab: "overview" });
    expect(place({ kind: "changes" })).toEqual({ tab: "sections", section: "sec_pairing" });
  });

  it("opens a section, a risk in it, and a tile on the section it is placed on", () => {
    expect(place({ kind: "section", id: CONTEXT_ID })).toEqual({
      tab: "sections",
      section: CONTEXT_ID,
    });
    expect(place({ kind: "section", id: "sec_gone" })).toEqual({
      tab: "sections",
      section: "sec_pairing",
    });
    expect(place({ kind: "risk", section: "sec_allowlist", id: "rsk_always_allow" })).toEqual({
      tab: "sections",
      section: "sec_allowlist",
      item: "rsk_always_allow",
    });
    expect(place({ kind: "tile", id: "dg_encryption" })).toEqual({
      tab: "sections",
      section: "sec_encryption",
      item: "tile-dg_encryption",
    });
  });

  it("keeps the Code tab and its file in the address", () => {
    const selection = selectionOf({ tab: "code", file: "oss/go.sum" });
    expect(selection).toEqual({ kind: "tile", id: CODE_PLACE, item: "oss/go.sum" });
    expect(place(selection)).toEqual({ tab: "code", file: "oss/go.sum" });
    expect(place(selectionOf({ tab: "code", file: null }))).toEqual({ tab: "code", file: null });
    expect(selectionOf({ tab: "sections", section: "sec_relay" })).toEqual({
      kind: "section",
      id: "sec_relay",
    });
    expect(selectionOf({ tab: "overview" })).toEqual({ kind: "summary" });
  });

  it("comments on the file open in Code", () => {
    expect(commentSelection({ kind: "tile", id: CODE_PLACE, item: "a.go" })).toEqual({
      kind: "file",
      id: "a.go",
    });
    expect(commentSelection({ kind: "tile", id: CODE_PLACE })).toEqual({ kind: "summary" });
    expect(commentSelection({ kind: "section", id: "sec_relay" })).toEqual({
      kind: "section",
      id: "sec_relay",
    });
  });
});

describe("the Code tab", () => {
  it("groups the files, generated ones last", () => {
    const files = viewFiles(REVIEW_1303);
    expect(files.map((file) => [file.group, file.path])).toEqual([
      ["Code", "oss/internal/uiserver/remote.go"],
      ["Code", "oss/internal/cli/daemon.go"],
      ["Code", "oss/internal/remote/pairing.go"],
      ["Code", "oss/internal/remote/devices.go"],
      ["Code", "oss/internal/remote/session.go"],
      ["Code", "oss/internal/remote/relay.go"],
      ["Code", "oss/internal/remote/serve.go"],
      ["Tests", "oss/internal/uiserver/remote_test.go"],
      ["Docs", "oss/SECURITY.md"],
      ["Generated", "oss/go.sum"],
    ]);
    expect(files.find((file) => file.path.endsWith("relay.go"))?.sections).toEqual(["sec_relay"]);
  });

  it("opens on the file asked for, else the first that is not generated", () => {
    const files = viewFiles(REVIEW_1303);
    expect(shownFile(files, "oss/go.sum")?.path).toBe("oss/go.sum");
    expect(shownFile(files, "missing.go")?.path).toBe("oss/internal/uiserver/remote.go");
    expect(shownFile(files, null)?.path).toBe("oss/internal/uiserver/remote.go");
  });

  it("pins each behaviour step and open risk to the hunks it cites", () => {
    const notes = notesByHunk(REVIEW_1303);
    expect(notes.get("h_routes")).toEqual([
      {
        kind: "step",
        id: "bhv_deny_default",
        section: "sec_allowlist",
        sectionTitle: "What a phone can do",
        text: "Deny by default",
      },
      {
        kind: "risk",
        id: "rsk_always_allow",
        section: "sec_allowlist",
        sectionTitle: "What a phone can do",
        text: "A phone can answer an agent's permission request with Always allow, which changes that agent's settings for good.",
        tier: "high",
      },
    ]);
    expect(notes.get("h_gosum")).toBeUndefined();
  });

  it("leaves a mitigated risk and an obsolete section's notes out", () => {
    const review = {
      ...REVIEW_1303,
      sections: REVIEW_1303.sections.map((section) =>
        section.id === "sec_allowlist"
          ? {
              ...section,
              risks: section.risks.map((risk) => ({ ...risk, state: "mitigated" as const })),
            }
          : section.id === "sec_pairing"
            ? { ...section, derived: { ...section.derived, state: "stale" as const } }
            : section,
      ),
    };
    const notes = notesByHunk(review);
    expect(notes.get("h_routes")?.map((note) => note.kind)).toEqual(["step"]);
    expect(notes.get("h_pairing")).toBeUndefined();
  });
});

describe("the overview's fallbacks", () => {
  it("explains the risk with the view's sentences, else the score's first reason", () => {
    expect(riskSummary(REVIEW_1303, VIEW_1303)).toBe(VIEW_1303.overview.risk_summary);
    expect(riskSummary(REVIEW_1303, emptyView(REVIEW_1303))).toBe(
      "Phones can approve pull requests and answer agents through a third-party relay.",
    );
    const unscored = { ...REVIEW_1303, summary: { ...REVIEW_1303.summary, risk_score: undefined } };
    expect(riskSummary(unscored, emptyView(unscored))).toBeNull();
  });

  it("says what was asked with the view's rows, else each session's first prompt", () => {
    expect(intentRows(REVIEW_1303, VIEW_1303)[0]).toEqual({
      asked: "Review on the phone",
      got: "Inbox, reviews, approve, comment, request changes and resolve threads.",
      session: "ses_mobile",
      prompt: 1,
      at: "2026-11-14T03:12:00Z",
    });
    expect(intentRows(REVIEW_1303, emptyView(REVIEW_1303))).toEqual([
      {
        asked: "allows to review on mobile",
        session: "ses_mobile",
        prompt: 1,
        at: "2026-11-14T03:12:00Z",
      },
    ]);
    const unlinked = { ...REVIEW_1303, index: { ...REVIEW_1303.index, sessions: [] } };
    expect(intentRows(unlinked, emptyView(unlinked))).toEqual([]);
  });
});
