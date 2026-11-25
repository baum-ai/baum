import { describe, expect, it } from "vitest";
import type { Anchor, IndexedHunk, IndexedHunkLine } from "@baum/viewer/review-document";
import { chromeOf } from "./chrome";
import { parseReviewV2, type ReviewV2, type V2Section } from "./document";
import { REVIEW_1303, review1303 } from "./fixture-1303";
import { bodyOfSteps } from "./from-v1";
import {
  CODE_PLACE,
  commentSelection,
  CONTEXT_ID,
  defaultSection,
  detailsOf,
  groupFiles,
  intentRows,
  listedSections,
  notesByHunk,
  placeOf,
  reviewOf,
  riskSummary,
  sectionViews,
  selectionOf,
  sessionQuotes,
  shownSectionView,
  sideSectionView,
  viewOfPath,
  testNames,
  shownFile,
  tilesOn,
  viewFiles,
  type ViewFile,
} from "./model";

/** #1303 as the page reads it off the tile route. */
const REVIEW = parseReviewV2(JSON.parse(JSON.stringify(REVIEW_1303)))!;
/** A version with nothing beyond its review: no tiles and no overview parts. */
const BARE: ReviewV2 = {
  ...REVIEW,
  summary: { ...REVIEW.summary, verdict: null, risk_why: "" },
  overview: { screenshots: [], videos: [], diagram: null, intent: [] },
  tiles: [],
};

describe("parseReviewV2", () => {
  it("reads a served document as it is", () => {
    expect(REVIEW.sections.map((section) => section.id)).toEqual(
      REVIEW_1303.sections.map((section) => section.id),
    );
    expect(REVIEW.sections[0]).toEqual(REVIEW_1303.sections[0]);
    expect(REVIEW.tiles.map((tile) => [tile.id, tile.owner, tile.state])).toEqual(
      REVIEW_1303.tiles.map((tile) => [tile.id, tile.owner, tile.state]),
    );
    expect(REVIEW.overview).toEqual(REVIEW_1303.overview);
    expect(REVIEW.summary.verdict).toEqual(REVIEW_1303.summary.verdict);
    expect(REVIEW.context).toEqual(REVIEW_1303.context);
  });

  it("is null for anything that is not a baum.review/2 document", () => {
    expect(parseReviewV2(undefined)).toBeNull();
    expect(parseReviewV2({ ...REVIEW_1303, schema: "baum.review/1" })).toBeNull();
    expect(parseReviewV2({ ...REVIEW_1303, review: null })).toBeNull();
  });

  it("drops a malformed item, never the whole document", () => {
    const review = parseReviewV2({
      schema: "baum.review/2",
      review: REVIEW_1303.review,
      summary: { verdict: { decision: "maybe", text: "", points: [{ text: 3 }] } },
      overview: {
        screenshots: ["img_1", 4],
        videos: ["vid_1", null],
        intent: [{ asked: "Make it work", session: "s", prompt: 1 }, { asked: 3 }],
      },
      context: { area: { text: " ", files: [] } },
      sections: [
        {
          id: "sec_a",
          title: "A",
          hunks: ["h_a", 7],
          body: [
            {
              type: "points",
              items: [{ text: "Does it.", cites: [{ type: "hunk", id: "h_a" }, 3] }, {}],
            },
          ],
          risks: [{ id: "r1", tier: "odd", text: "May break." }, { id: "r2" }],
          details: "pending",
          derived: { state: "new" },
        },
        { title: "No id" },
      ],
      tiles: [
        { id: "dg_a", type: "flow", title: "A", state: "ready", owner: "sec_a", data: {} },
        { id: "dg_b", type: "flow", state: "drawn" },
        { type: "flow" },
      ],
    });
    expect(review).not.toBeNull();
    expect(review?.summary.verdict).toBeNull();
    expect(review?.context).toBeNull();
    expect(review?.overview.screenshots).toEqual(["img_1"]);
    expect(review?.overview.videos).toEqual(["vid_1"]);
    expect(review?.overview.intent).toEqual([
      { asked: "Make it work", got: "", session: "s", prompt: 1 },
    ]);
    expect(review?.sections).toHaveLength(1);
    const [section] = review!.sections;
    expect(section?.hunks).toEqual(["h_a"]);
    expect(section?.body).toEqual([
      {
        type: "points",
        items: [{ text: "Does it.", cites: [{ type: "hunk", id: "h_a" }] }],
      },
      // The section's tile, which no body placed.
      { type: "tile", tile: "dg_a" },
    ]);
    // A tier the page does not know reads as low.
    expect(section?.risks.map((risk) => [risk.id, risk.tier])).toEqual([["r1", "low"]]);
    expect(detailsOf(section!)).toBe("pending");
    // A state the page does not know is drawn as unavailable.
    expect(review?.tiles.map((tile) => [tile.id, tile.state, tile.owner])).toEqual([
      ["dg_a", "ready", "sec_a"],
      ["dg_b", "unavailable", ""],
    ]);
    expect(review?.index.hunks).toEqual([]);
  });

  it("gives a native tile its caption and citations in its data", () => {
    const tile = REVIEW.tiles.find((each) => each.id === "dg_encryption")!;
    expect(tile.data).toMatchObject({
      caption: "A message that fails to decrypt ends the session.",
      source: [{ type: "hunk", id: "h_session" }],
    });
  });

  it("draws a code tile from the text the host filled, and leaves one without as it is", () => {
    const filled = REVIEW.tiles.find((each) => each.id === "code_remote_routes")!;
    expect(filled.data).toEqual({
      language: "go",
      content:
        '\t{"POST", "/api/prs/review"},\n\t{"POST", "/api/prs/resolve-thread"},\n\t{"POST", "/api/prs/refresh"},\n\t{"POST", "/api/agent-requests/{id}/answer"},',
      source: [{ type: "hunk", id: "h_routes", lines: [61, 64] }],
    });
    const read = parseReviewV2({
      ...REVIEW_1303,
      tiles: [
        {
          id: "code_x",
          type: "code",
          owner: "sec_allowlist",
          title: "X",
          state: "ready",
          data: { hunk: "h_routes", lines: [61, 64], text: "x := 1" },
        },
        {
          id: "code_y",
          type: "code",
          owner: "sec_allowlist",
          title: "Y",
          state: "unavailable",
          data: { hunk: "h_routes", lines: [61, 64] },
        },
      ],
    })!.tiles;
    expect(read[0]!.data).toMatchObject({ language: "go", content: "x := 1" });
    expect(read[1]!.data).toEqual({ hunk: "h_routes", lines: [61, 64] });
  });
});

describe("the document a review carries", () => {
  it("is the served one, else the review converted on its own", () => {
    expect(reviewOf(review1303())).toEqual(REVIEW);
    const bare = reviewOf({ ...chromeOf(REVIEW), schema: "baum.review/1" });
    expect(bare.tiles).toEqual([]);
    expect(bare.overview).toEqual({ screenshots: [], videos: [], diagram: null, intent: [] });
    expect(bare.sections.map((section) => section.id)).toEqual(
      REVIEW.sections.map((section) => section.id),
    );
  });

  it("places tiles by their owner, never the overview's diagram", () => {
    expect(tilesOn(REVIEW, "sec_allowlist").map((tile) => tile.id)).toEqual([
      "dg_allowlist",
      "dg_always_allow",
      "lib_api_change",
      "code_remote_routes",
    ]);
    const diagramOnSection = { ...REVIEW, overview: { ...REVIEW.overview, diagram: "dg_pairing" } };
    expect(tilesOn(diagramOnSection, "sec_pairing")).toEqual([]);
  });
});

describe("the sections list", () => {
  it("lists Where this fits first and opens on the first real section", () => {
    const list = listedSections(REVIEW);
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
    // A section's line is its gist, else its summary's first sentence.
    expect(list[1]?.line).toBe("A one-time code pairs up to 16 phones.");
    expect(list.find((section) => section.id === "sec_deps")?.line).toBe(
      "Adds the WebSocket and Noise libraries.",
    );
    expect(defaultSection(list)).toBe("sec_pairing");
  });

  it("leaves Where this fits out while no tile stands on it and the review has no context", () => {
    const review = { ...REVIEW, context: null };
    const tiles = review.tiles.filter((tile) => tile.owner !== CONTEXT_ID);
    expect(listedSections({ ...review, tiles })[0]?.id).toBe("sec_pairing");
    // A tile it owns brings it back.
    expect(listedSections(review)[0]?.id).toBe(CONTEXT_ID);
    expect(listedSections({ ...BARE, context: null })[0]?.id).toBe("sec_pairing");
  });

  it("lists sections no longer in the code last", () => {
    const review = {
      ...REVIEW,
      sections: REVIEW.sections.map((section) =>
        section.id === "sec_pairing"
          ? { ...section, derived: { ...section.derived, state: "stale" as const } }
          : section,
      ),
    };
    const list = listedSections(review);
    expect(list.at(-1)).toMatchObject({ id: "sec_pairing", obsolete: true });
    expect(defaultSection(list)).toBe("sec_relay");
  });
});

describe("places", () => {
  const place = (selection: Parameters<typeof placeOf>[0]) => placeOf(selection, REVIEW);

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

  it("keeps a section's view, but not its Overview or a view it doesn't know", () => {
    const tests = selectionOf({ tab: "sections", section: "sec_allowlist", view: "tests" });
    expect(tests).toEqual({ kind: "section", id: "sec_allowlist", view: "tests" });
    expect(place(tests)).toEqual({ tab: "sections", section: "sec_allowlist", view: "tests" });
    expect(selectionOf({ tab: "sections", section: "sec_allowlist", view: "overview" })).toEqual({
      kind: "section",
      id: "sec_allowlist",
    });
    expect(place({ kind: "section", id: "sec_allowlist", view: "later" })).toEqual({
      tab: "sections",
      section: "sec_allowlist",
    });
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
    const files = viewFiles(REVIEW);
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
    const files = viewFiles(REVIEW);
    expect(shownFile(files, "oss/go.sum")?.path).toBe("oss/go.sum");
    expect(shownFile(files, "missing.go")?.path).toBe("oss/internal/uiserver/remote.go");
    expect(shownFile(files, null)?.path).toBe("oss/internal/uiserver/remote.go");
  });

  it("pins each behaviour step and open risk to the hunks it cites", () => {
    const notes = notesByHunk(REVIEW);
    expect(notes.get("h_routes")).toEqual([
      {
        kind: "step",
        id: "sec_allowlist-b1-p0",
        section: "sec_allowlist",
        sectionTitle: "What a phone can do",
        text: "Deny by default.",
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
      ...REVIEW,
      sections: REVIEW.sections.map((section) =>
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
  it("explains the risk with risk_why, else the score's first reason", () => {
    expect(riskSummary(REVIEW)).toBe(REVIEW_1303.summary.risk_why);
    expect(riskSummary(BARE)).toBe(
      "Phones can approve pull requests and answer agents through a third-party relay.",
    );
    expect(
      riskSummary({ ...BARE, summary: { ...BARE.summary, risk_score: undefined } }),
    ).toBeNull();
  });

  it("says what was asked with the overview's intent, else each session's first prompt", () => {
    expect(intentRows(REVIEW)[0]).toEqual({
      asked: "Review on the phone",
      got: "Inbox, reviews, approve, comment, request changes and resolve threads.",
      session: "ses_mobile",
      prompt: 1,
      at: "2026-11-14T03:12:00Z",
    });
    expect(intentRows(BARE)).toEqual([
      {
        asked: "allows to review on mobile",
        session: "ses_mobile",
        prompt: 1,
        at: "2026-11-14T03:12:00Z",
      },
    ]);
    expect(intentRows({ ...BARE, index: { ...BARE.index, sessions: [] } })).toEqual([]);
  });
});

describe("a section's views", () => {
  const sectionOf = (id: string) => REVIEW.sections.find((each) => each.id === id)!;
  const of = (id: string) => sectionViews(REVIEW, sectionOf(id));

  it("counts each view's files, or quotes, and leaves the empty ones at 0", () => {
    expect(of("sec_allowlist").counts).toEqual({ code: 1, tests: 1, session: 0, docs: 1 });
    expect(of("sec_relay").counts).toEqual({ code: 1, tests: 0, session: 2, docs: 0 });
    // Generated files count as the section's code.
    expect(of("sec_deps").counts).toEqual({ code: 1, tests: 0, session: 0, docs: 0 });
  });

  it("groups the section's own hunks by their role, file by file", () => {
    const views = of("sec_allowlist");
    expect(views.code.files.map((file) => [file.path, file.hunks.map((hunk) => hunk.id)])).toEqual([
      ["oss/internal/uiserver/remote.go", ["h_routes", "h_refusals"]],
    ]);
    expect(views.code.generated).toEqual([]);
    expect(views.tests.files.map((file) => file.path)).toEqual([
      "oss/internal/uiserver/remote_test.go",
    ]);
    expect(views.docs.files.map((file) => file.path)).toEqual(["oss/SECURITY.md"]);
    const deps = of("sec_deps");
    expect(deps.code.files).toEqual([]);
    expect(deps.code.generated.map((file) => [file.path, file.group])).toEqual([
      ["oss/go.sum", "Generated"],
    ]);
    // Another section's hunks of the same file stay out.
    expect(of("sec_lifecycle").code.files[0]?.hunks.map((hunk) => hunk.id)).toEqual([
      "h_daemon",
      "h_shutdown",
    ]);
  });

  it("shows the view asked for while it has something, else the Overview", () => {
    const counts = of("sec_allowlist").counts;
    expect(shownSectionView("tests", counts)).toBe("tests");
    expect(shownSectionView("session", counts)).toBe("overview");
    expect(shownSectionView(undefined, counts)).toBe("overview");
  });

  it("puts the view asked for beside the Overview, else the first that has something", () => {
    const counts = of("sec_allowlist").counts;
    expect(sideSectionView("tests", counts)).toBe("tests");
    expect(sideSectionView("session", counts)).toBe("code");
    expect(sideSectionView(undefined, counts)).toBe("code");
    expect(sideSectionView("overview", { code: 0, tests: 0, session: 0, docs: 2 })).toBe("docs");
    expect(sideSectionView(undefined, { code: 0, tests: 0, session: 0, docs: 0 })).toBeNull();
  });

  it("finds the view that draws a file of the section", () => {
    const views = of("sec_allowlist");
    expect(viewOfPath(views, "oss/internal/uiserver/remote.go")).toBe("code");
    expect(viewOfPath(views, "oss/SECURITY.md")).toBe("docs");
    expect(viewOfPath(views, views.tests.files[0]!.path)).toBe("tests");
    expect(viewOfPath(views, "oss/elsewhere.go")).toBeNull();
  });
});

const testHunk = (
  path: string,
  lines: [kind: "add" | "remove" | "context", text: string][],
  header = "@@ -1,1 +1,1 @@",
): IndexedHunk => ({
  id: `h_${path}`,
  path,
  new_range: [1, lines.length],
  additions: lines.filter(([kind]) => kind === "add").length,
  deletions: lines.filter(([kind]) => kind === "remove").length,
  change: "modified",
  role: "test",
  diff: {
    header,
    lines: lines.map(([kind, text], at) => ({
      kind,
      text,
      ...(kind !== "add" ? { old_line: at + 1 } : {}),
      ...(kind !== "remove" ? { new_line: at + 1 } : {}),
    })) as IndexedHunkLine[],
  },
});

describe("the tests a section's hunks add or change", () => {
  it("names Go tests added, and those whose body changes", () => {
    expect(
      testNames([
        testHunk("a/x_test.go", [
          ["context", "func TestOld(t *testing.T) {"],
          ["context", "\tif got := f(); got != 1 {"],
          ["remove", "\t\tt.Fatal(got)"],
          ["add", '\t\tt.Fatalf("got %d", got)'],
          ["context", "\t}"],
          ["context", "}"],
          ["context", ""],
          ["context", "func TestUntouched(t *testing.T) {"],
          ["context", "}"],
          ["add", ""],
          ["add", "// TestNew checks the new path."],
          ["add", "func TestNew(t *testing.T) {"],
          ["add", "\tt.Parallel()"],
          ["add", "}"],
          ["add", "func BenchmarkNew(b *testing.B) {}"],
          ["add", "func (s *suite) TestMethod() {}"],
        ]),
      ]),
    ).toEqual(["TestOld", "TestNew", "BenchmarkNew"]);
  });

  it("reads a Go hunk inside a test from its header", () => {
    expect(
      testNames([
        testHunk(
          "a/x_test.go",
          [
            ["context", "\twant := 2"],
            ["add", "\tcheck(t, want)"],
          ],
          "@@ -10,2 +10,3 @@ func TestHeader(t *testing.T) {",
        ),
      ]),
    ).toEqual(["TestHeader"]);
  });

  it("names it and test calls in JavaScript and TypeScript", () => {
    expect(
      testNames([
        testHunk("app/x.test.tsx", [
          ["context", 'describe("the page", () => {'],
          ["add", '  it("draws the row", () => {'],
          ["add", "    expect(1).toBe(1);"],
          ["add", "  });"],
          ["context", "  it('keeps its place', async () => {"],
          ["remove", "    await old();"],
          ["add", "    await next();"],
          ["context", "  });"],
          ["context", '  it("is left alone", () => {});'],
          ["add", '  test.skip("is skipped for now", () => {});'],
          ["context", "});"],
        ]),
        testHunk("e2e/x.spec.ts", [
          ["add", 'test.skip(!process.env.SHOTS, "pictures on request");'],
          ["add", 'test("a picture", async ({ page }) => {'],
          ["add", "  await page.goto('/');"],
          ["add", "});"],
        ]),
      ]),
    ).toEqual(["draws the row", "keeps its place", "is skipped for now", "a picture"]);
  });

  it("is empty when the hunks change no test, and ignores data files", () => {
    expect(
      testNames([
        testHunk("a/x_test.go", [
          ["add", "package a"],
          ["add", "import ("],
          ["add", '\t"testing"'],
          ["add", ")"],
          ["add", "func helper() int {"],
          ["add", "\treturn 1"],
          ["add", "}"],
        ]),
        testHunk("a/testdata/golden.json", [["add", '  "it": "test(\\"x\\", ...)"']]),
      ]),
    ).toEqual([]);
  });

  it("gives up rather than guess", () => {
    // A change inside a block that opens above the hunk.
    expect(
      testNames([
        testHunk("a/x_test.go", [
          ["context", "\twant := 2"],
          ["add", "\tcheck(t, want)"],
        ]),
      ]),
    ).toBeNull();
    expect(
      testNames([
        testHunk(
          "app/x.test.ts",
          [
            ["context", "    const row = draw();"],
            ["add", "    expect(row).toBeTruthy();"],
          ],
          '@@ -4,2 +4,3 @@ describe("rows", () => {',
        ),
      ]),
    ).toBeNull();
    // Names built at run time.
    expect(
      testNames([testHunk("app/x.test.ts", [["add", 'it.each(cases)("reads %s", (c) => {']])]),
    ).toBeNull();
    expect(
      testNames([testHunk("app/x.test.ts", [["add", "test(`reads ${kind}`, () => {"]])]),
    ).toBeNull();
    // Another language, or a hunk whose code isn't in the review.
    expect(testNames([testHunk("tests/test_x.py", [["add", "def test_x():"]])])).toBeNull();
    expect(testNames([{ ...testHunk("a/x_test.go", []), diff: undefined }])).toBeNull();
  });
});

describe("the session turns a section cites", () => {
  const withCites = (section: V2Section, cites: Anchor[]): V2Section => ({
    ...section,
    body: [...section.body, { type: "points", items: [{ text: "Step.", cites }] }],
  });
  const relay = REVIEW.sections.find((each) => each.id === "sec_relay")!;

  it("quotes the prompts and events cited, once each, in the order they happened", () => {
    const quotes = sessionQuotes(
      REVIEW,
      withCites(relay, [
        { type: "session_event", session: "ses_mobile", event: "ev_standin" },
        { type: "prompt", session: "ses_mobile", index: 1 },
      ]),
    );
    expect(quotes.map((quote) => [quote.kind, quote.who, quote.text, quote.turn])).toEqual([
      ["prompt", "person", "allows to review on mobile", 1],
      ["prompt", "person", "use an encrypted relay that we run", 2],
      [
        "event",
        "agent",
        "Ran the relay tests against an in-process stand-in written from the relay's README; no deployed relay was available.",
        2,
      ],
    ]);
    expect(quotes[0]).toMatchObject({
      session: "ses_mobile",
      vendor: "claude",
      at: "2026-11-14T03:12:00Z",
    });
  });

  it("reads the citations of the section's tiles", () => {
    const tile = {
      id: "dg_x",
      type: "flow",
      owner: "sec_relay",
      title: "X",
      state: "ready" as const,
      cites: [{ type: "session_event", session: "ses_mobile", event: "ev_standin" } as Anchor],
      data: { lanes: [], source: [{ type: "prompt", session: "ses_mobile", index: 1 }] },
    };
    const bare = { ...relay, body: [], risks: [] };
    expect(sessionQuotes(REVIEW, bare)).toEqual([]);
    expect(sessionQuotes(REVIEW, bare, [tile]).map((quote) => quote.kind)).toEqual([
      "prompt",
      "event",
    ]);
  });

  it("leaves out what the index can't resolve to words", () => {
    expect(
      sessionQuotes(
        REVIEW,
        withCites({ ...relay, body: [], risks: [] }, [
          { type: "prompt", session: "ses_mobile", index: 9 },
          { type: "prompt", session: "ses_gone", index: 1, input: "typed elsewhere" },
          { type: "session_event", session: "ses_mobile", event: "ev_gone" },
        ]),
      ).map((quote) => [quote.text, quote.vendor]),
    ).toEqual([["typed elsewhere", undefined]]);
  });
});

describe("a section's body", () => {
  const tile = (id: string, owner: string, extra: Record<string, unknown> = {}) => ({
    id,
    type: "flow",
    title: id,
    state: "ready",
    owner,
    data: {},
    ...extra,
  });
  const read = (section: Record<string, unknown>, tiles: unknown[] = []) =>
    parseReviewV2({
      schema: "baum.review/2",
      review: REVIEW_1303.review,
      sections: [{ id: "sec_a", title: "A", hunks: [], risks: [], ...section }],
      tiles,
    })!.sections[0]!;

  it("reads the blocks served, in order", () => {
    const section = read(
      {
        body: [
          { type: "prose", text: "It **does** it." },
          {
            type: "points",
            title: "Choices",
            items: [
              { text: "One", more: "Why.", cites: [{ type: "hunk", id: "h_a" }] },
              { text: "Two" },
            ],
          },
          { type: "tile", tile: "dg_a" },
          { type: "note", text: "Aside." },
        ],
      },
      [tile("dg_a", "sec_a")],
    );
    expect(section.body).toEqual([
      { type: "prose", text: "It **does** it." },
      {
        type: "points",
        title: "Choices",
        items: [
          { text: "One", more: "Why.", cites: [{ type: "hunk", id: "h_a" }] },
          { text: "Two", cites: [] },
        ],
      },
      { type: "tile", tile: "dg_a" },
      { type: "note", text: "Aside." },
    ]);
  });

  it("drops a malformed block or point", () => {
    const section = read({
      body: [
        { type: "prose", text: "  " },
        { type: "points", items: [{ text: "" }, { more: "no text" }] },
        { type: "chart", text: "unknown" },
        { type: "points", items: [{ text: "Kept" }, 3] },
        "nonsense",
      ],
    });
    expect(section.body).toEqual([{ type: "points", items: [{ text: "Kept", cites: [] }] }]);
  });

  it("places a tile no body placed after the blocks", () => {
    const tiles = [tile("dg_a", "sec_a"), tile("dg_b", "sec_b"), tile("dg_c", "sec_a")];
    expect(read({ body: [{ type: "prose", text: "What it does." }] }, tiles).body).toEqual([
      { type: "prose", text: "What it does." },
      { type: "tile", tile: "dg_a" },
      { type: "tile", tile: "dg_c" },
    ]);
  });

  it("builds a baum.review/1 section's body from its summary and steps", () => {
    expect(
      bodyOfSteps("What it does.", [
        { id: "s1", title: "Reads", text: "It reads.", cites: [{ type: "hunk", id: "h_a" }] },
        { id: "s2", title: "", text: "It writes.", cites: [] },
      ]),
    ).toEqual([
      { type: "prose", text: "What it does." },
      {
        type: "points",
        items: [
          { id: "s1", text: "**Reads.** It reads.", cites: [{ type: "hunk", id: "h_a" }] },
          { id: "s2", text: "It writes.", cites: [] },
        ],
      },
    ]);
  });

  it("ignores a tile block naming a missing tile or another section's, and a repeat", () => {
    const section = read(
      {
        body: [
          { type: "tile", tile: "dg_gone" },
          { type: "tile", tile: "dg_other" },
          { type: "tile", tile: "dg_a" },
          { type: "tile", tile: "dg_a" },
        ],
      },
      [tile("dg_a", "sec_a"), tile("dg_other", "sec_b")],
    );
    expect(section.body).toEqual([{ type: "tile", tile: "dg_a" }]);
  });

  it("appends the section's tiles the body doesn't place, a person's own included", () => {
    const section = read(
      {
        body: [
          { type: "tile", tile: "dg_b" },
          { type: "prose", text: "Text." },
        ],
      },
      [tile("dg_a", "sec_a"), tile("dg_b", "sec_a"), tile("dg_c", "sec_a", { added: true })],
    );
    expect(section.body.map((block) => (block.type === "tile" ? block.tile : block.type))).toEqual([
      "dg_b",
      "prose",
      "dg_a",
      "dg_c",
    ]);
  });

  it("reads the points as the steps the chrome, notes and comments use", () => {
    const section: V2Section = {
      ...REVIEW.sections[0]!,
      id: "sec_a",
      gist: "",
      body: [
        { type: "prose", text: "Lead prose." },
        {
          type: "points",
          items: [
            { text: "**Does** a thing. More.", cites: [{ type: "hunk", id: "h_routes" }] },
            { id: "step_old", text: "Kept id.", cites: [] },
          ],
        },
      ],
    };
    const review: ReviewV2 = { ...REVIEW, sections: [section] };
    const chrome = chromeOf(review);
    expect(chrome.sections[0]?.summary?.text).toBe("Lead prose.");
    expect(
      chrome.sections[0]?.blocks.flatMap((block) =>
        "steps" in block ? (block.steps as { id: string }[]).map((step) => step.id) : [],
      ),
    ).toEqual(["sec_a-b1-p0", "step_old"]);
    expect(notesByHunk(review).get("h_routes")).toMatchObject([
      { kind: "step", id: "sec_a-b1-p0", text: "Does a thing." },
    ]);
    expect(listedSections(review).find((entry) => entry.id === "sec_a")?.line).toBe("Lead prose.");
  });
});

describe("grouping the changed files", () => {
  const file = (path: string, sections: string[] = [], group: ViewFile["group"] = "Code") =>
    ({ path, group, hunks: [], sections }) as ViewFile;
  const sections = [
    { id: CONTEXT_ID, title: "Where this fits" },
    { id: "a", title: "Pairing" },
    { id: "b", title: "Relay" },
    { id: "c", title: "Nothing changed here" },
  ];
  const files = [
    file("oss/internal/jobs/run.go", ["a"]),
    file("oss/internal/jobs/queue.go", ["a", "b"]),
    file("oss/internal/jobs/z_test.go", ["b"], "Tests"),
    file("oss/internal/remote/relay.go", ["b"]),
    file("oss/README.md", [], "Docs"),
    file("go.mod", [], "Config"),
    file("oss/go.sum", [], "Generated"),
  ];
  const shape = (rows: ReturnType<typeof groupFiles>) =>
    rows.map((row) => [row.title, row.depth, row.files.map((each) => each.path)]);

  it("by type: Code, Tests, Docs, Config and Generated, empty groups left out", () => {
    const rows = groupFiles(files, "type", sections);
    expect(rows.map((row) => row.title)).toEqual(["Code", "Tests", "Docs", "Config", "Generated"]);
    expect(rows.find((row) => row.title === "Generated")?.generated).toBe(true);
    expect(groupFiles([file("a.go")], "type", sections).map((row) => row.title)).toEqual(["Code"]);
  });

  it("by folder: compact directories, files by name under their folder", () => {
    expect(shape(groupFiles(files, "folder", sections))).toEqual([
      ["Repository root", 0, ["go.mod"]],
      // oss holds files of its own, so it stands and its folders nest in it.
      ["oss", 0, ["oss/go.sum", "oss/README.md"]],
      ["internal", 1, []],
      [
        "jobs",
        2,
        ["oss/internal/jobs/queue.go", "oss/internal/jobs/run.go", "oss/internal/jobs/z_test.go"],
      ],
      ["remote", 2, ["oss/internal/remote/relay.go"]],
    ]);
  });

  it("by folder: a chain of folders with one folder each is one heading", () => {
    expect(
      shape(
        groupFiles(
          [file("oss/internal/jobs/run.go"), file("oss/internal/jobs/queue.go")],
          "folder",
          sections,
        ),
      ),
    ).toEqual([
      ["oss/internal/jobs", 0, ["oss/internal/jobs/queue.go", "oss/internal/jobs/run.go"]],
    ]);
  });

  it("by feature: the sections in order, a file under each of its sections, the rest last", () => {
    const rows = groupFiles(files, "feature", sections);
    expect(shape(rows)).toEqual([
      ["Pairing", 0, ["oss/internal/jobs/run.go", "oss/internal/jobs/queue.go"]],
      [
        "Relay",
        0,
        [
          "oss/internal/jobs/queue.go",
          "oss/internal/jobs/z_test.go",
          "oss/internal/remote/relay.go",
        ],
      ],
      ["Not in a section", 0, ["oss/README.md", "go.mod", "oss/go.sum"]],
    ]);
    // Sections that explain no file have no heading.
    expect(rows.map((row) => row.title)).not.toContain("Nothing changed here");
  });

  it("by feature, a review with every file in a section has no 'Not in a section'", () => {
    const rows = groupFiles([file("a.go", ["a"])], "feature", sections);
    expect(rows.map((row) => row.title)).toEqual(["Pairing"]);
  });

  it("lists every file of the real review under some feature", () => {
    const real = viewFiles(REVIEW);
    const rows = groupFiles(real, "feature", listedSections(REVIEW));
    const listed = new Set(rows.flatMap((row) => row.files.map((each) => each.path)));
    expect([...listed].toSorted()).toEqual(real.map((each) => each.path).toSorted());
  });
});
