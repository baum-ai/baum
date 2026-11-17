import { describe, expect, it } from "vitest";
import type { IndexedHunk, IndexedHunkLine, Section } from "@baum/viewer/review-document";
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
  sectionViews,
  selectionOf,
  sessionQuotes,
  shownSectionView,
  testNames,
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

describe("a section's views", () => {
  const sectionOf = (id: string) => REVIEW_1303.sections.find((each) => each.id === id)!;
  const of = (id: string) => sectionViews(REVIEW_1303, VIEW_1303, sectionOf(id));

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
  const withCites = (section: Section, anchors: Section["risks"][number]["anchors"]) => ({
    ...section,
    summary: { text: "Summary.", anchors },
  });
  const relay = REVIEW_1303.sections.find((each) => each.id === "sec_relay")!;

  it("quotes the prompts and events cited, once each, in the order they happened", () => {
    const quotes = sessionQuotes(
      REVIEW_1303,
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
      title: "X",
      state: "ready" as const,
      data: { lanes: [], source: [{ type: "prompt", session: "ses_mobile", index: 1 }] },
    };
    const bare = { ...relay, blocks: [], risks: [], summary: null };
    expect(sessionQuotes(REVIEW_1303, bare)).toEqual([]);
    expect(sessionQuotes(REVIEW_1303, bare, [tile]).map((quote) => quote.text)).toEqual([
      "allows to review on mobile",
    ]);
  });

  it("leaves out what the index can't resolve to words", () => {
    expect(
      sessionQuotes(
        REVIEW_1303,
        withCites({ ...relay, blocks: [], risks: [] }, [
          { type: "prompt", session: "ses_mobile", index: 9 },
          { type: "prompt", session: "ses_gone", index: 1, input: "typed elsewhere" },
          { type: "session_event", session: "ses_mobile", event: "ev_gone" },
        ]),
      ).map((quote) => [quote.text, quote.vendor]),
    ).toEqual([["typed elsewhere", undefined]]);
  });
});
