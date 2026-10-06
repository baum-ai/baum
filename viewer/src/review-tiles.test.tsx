import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { ReviewTiles } from "./components/review-tiles/review-tiles";
import { ReviewSection } from "./components/review-section";
import { ReviewRisk } from "./components/review-risk";
import { AccessModelTile } from "./components/review-tiles/access-model";
import {
  DataMigrationTile,
  ExternalIntegrationTile,
} from "./components/review-tiles/specialist-tiles";
import { reviewDocumentFor } from "../test/review-document";
import { legacyTileKinds, tileInput, tileKinds } from "../test/review-tile-inputs";
import { tileCodeUrl, validReviewTile } from "./review-tiles";
import { tileTitles } from "./components/review-tiles/catalogue";
import type { ReviewDocument } from "./review-document";
import type { ReviewTile, TileCodeFile } from "../generated/review-tile-types.js";

const document = () =>
  structuredClone(reviewDocumentFor("acme/widgets", 42)) as unknown as ReviewDocument;
const render = (tiles: unknown, doc = document()) =>
  renderToStaticMarkup(<ReviewTiles document={doc} tiles={tiles} />);
function code(doc = document()): TileCodeFile {
  return {
    path: "src/test.ts",
    revision: doc.review.head,
    review_version: doc.review.version,
    why: "Supplied source",
    diff: {
      hunks: [
        {
          header: "@@ -1 +1 @@",
          lines: [
            { kind: "remove", old_line: 1, text: "before" },
            { kind: "add", new_line: 1, text: "after" },
          ],
        },
      ],
    },
  };
}

function schemaTile(tableName: string, id = "tile_test") {
  const tile = tileInput("database-schema", id);
  tile.data.tables = [{ name: tableName, columns: [{ name: "column", type: "text" }] }];
  return tile;
}

describe("JSON review tiles", () => {
  it.each(tileKinds)("validates and renders %s beside the behaviour steps", (kind) => {
    const doc = document();
    const tile = tileInput(kind);
    expect(validReviewTile(tile, doc)).toBe(true);
    doc.sections[0]!.tiles = [tile];
    const section = renderToStaticMarkup(
      <ReviewSection
        document={doc}
        section={doc.sections[0]!}
        onSelectSection={() => {}}
        showTiles
      />,
    );
    // Every kind validates and reaches the page. Database and API tiles are
    // the ones framed beside the behaviour steps; nothing else is dropped.
    expect(render([tile], doc)).not.toContain("Tile unavailable");
    expect(section).toContain('aria-label="Review tiles"');
    expect(section.indexOf('aria-label="Behavior"')).toBeLessThan(
      section.indexOf('aria-label="Review tiles"'),
    );
    expect(section.indexOf('aria-label="Review tiles"')).toBeLessThan(
      section.indexOf('aria-label="Risks"'),
    );
    const missing = { ...tile, data: {} };
    expect(validReviewTile(missing, doc)).toBe(false);
    expect(render([missing, tile], doc)).toContain("Tile unavailable");
  });

  it.each(legacyTileKinds)("still validates and renders a stored %s tile", (kind) => {
    // Generation no longer offers these kinds, but older documents keep them.
    expect(tileKinds).not.toContain(kind);
    const doc = document();
    const tile = tileInput(kind);
    expect(validReviewTile(tile, doc)).toBe(true);
    const html = render([tile], doc);
    expect(html).not.toContain("Tile unavailable");
    expect(html).toContain(tileTitles[kind]);
    expect(validReviewTile({ ...tile, data: {} }, doc)).toBe(false);
  });

  it("ignores retired architecture context and deployment in stored tiles", () => {
    const doc = document();
    const tile = tileInput("architecture");
    const node = { id: "outside", label: "Outside world", kind: "external" };
    const stored = { ...tile, data: { ...tile.data, context: [node], deployment: [node] } };
    expect(validReviewTile(stored, doc)).toBe(false);
    const html = render([stored], doc);
    expect(html).not.toContain("Tile unavailable");
    expect(html).toContain(tileTitles.architecture);
    expect(html).not.toContain("Wider context");
    expect(html).not.toContain("Outside world");
  });

  it.each(tileKinds)("keeps a risk's own %s tile", (kind) => {
    // Main keeps a risk's tiles on the risk itself, beside the section link.
    const doc = document();
    const risk = {
      ...doc.sections[0]!.risks[0]!,
      tiles: [tileInput(kind)],
      section: doc.sections[0]!.id,
      sectionTitle: doc.sections[0]!.title,
      sectionState: doc.sections[0]!.derived.state,
    };
    const pane = renderToStaticMarkup(
      <ReviewRisk document={doc} risk={risk} onSelectSection={() => {}} showTiles />,
    );
    expect(pane).not.toContain("Tile unavailable");
    expect(pane).toContain('aria-label="Review tiles"');
    expect(pane).toContain("Section");
  });

  it("holds a section's and a risk's tiles back unless asked to show them", () => {
    const doc = document();
    doc.sections[0]!.tiles = [tileInput("bug-fix")];
    const risk = {
      ...doc.sections[0]!.risks[0]!,
      tiles: [tileInput("bug-fix")],
      section: doc.sections[0]!.id,
      sectionTitle: doc.sections[0]!.title,
      sectionState: doc.sections[0]!.derived.state,
    };
    doc.sections[0]!.risks[0]!.tiles = risk.tiles;
    const section = renderToStaticMarkup(
      <ReviewSection document={doc} section={doc.sections[0]!} onSelectSection={() => {}} />,
    );
    const pane = renderToStaticMarkup(
      <ReviewRisk document={doc} risk={risk} onSelectSection={() => {}} />,
    );
    expect(section).toContain('aria-label="Behavior"');
    expect(section).not.toContain('aria-label="Review tiles"');
    expect(pane).not.toContain('aria-label="Review tiles"');
  });

  it("preserves ordering and repeated kinds but rejects duplicate owner IDs", () => {
    const one = schemaTile("first defect");
    const two = schemaTile("second defect", "tile_second");
    const html = render([one, two]);
    expect(html.indexOf("first defect")).toBeLessThan(html.indexOf("second defect"));
    expect(html).not.toContain("Tile unavailable");
    expect(render([one, one]).match(/Tile unavailable/g)).toHaveLength(2);
  });

  it("keeps legacy content when tiles are absent or empty", () => {
    expect(render(undefined)).toBe("");
    expect(render([])).toBe("");
    const doc = document();
    const before = renderToStaticMarkup(
      <ReviewSection
        document={doc}
        section={doc.sections[0]!}
        onSelectSection={() => {}}
        showTiles
      />,
    );
    doc.sections[0]!.tiles = [];
    expect(
      renderToStaticMarkup(
        <ReviewSection
          document={doc}
          section={doc.sections[0]!}
          onSelectSection={() => {}}
          showTiles
        />,
      ),
    ).toBe(before);
    doc.sections[0]!.tiles = [tileInput("bug-fix")];
    const after = renderToStaticMarkup(
      <ReviewSection
        document={doc}
        section={doc.sections[0]!}
        onSelectSection={() => {}}
        showTiles
      />,
    );
    expect(after).toContain(doc.sections[0]!.summary.text);
    expect(after).toContain("Risks");
    expect(after).not.toContain('aria-label="Evidence"');
  });

  it("isolates malformed data and retains usable anchors without executing text", () => {
    const unsafe = {
      ...schemaTile("<script>alert(1)</script>"),
      // oxlint-disable-next-line no-script-url -- exercise untrusted source URLs
      anchors: [{ type: "external_url", url: "javascript:alert(1)", label: "Unsafe source" }],
    };
    const html = render([
      null,
      42,
      { kind: "user-interface", anchors: [{ type: "file", path: "useful.ts" }] },
      unsafe,
    ]);
    expect(html).toContain("useful.ts");
    expect(html).toContain("&lt;script&gt;");
    expect(html).not.toContain('href="javascript:');
    expect(html).not.toContain("<script>");
    expect(render({})).toContain("Tile unavailable");
    expect(
      render([
        { ...tileInput("database-schema"), anchors: [{ type: "hunk", id: "h_000000000000" }] },
      ]),
    ).toContain("Some tile sources could not be resolved");
  });

  it("uses supplied migration fields and fallback destination", () => {
    const migration = tileInput("data-migration");
    if (migration.kind !== "data-migration") throw Error();
    migration.data.before_fields = ["original_field"];
    migration.data.transform = ["replacement_field"];
    const integration = tileInput("external-integration");
    if (integration.kind !== "external-integration") throw Error();
    integration.data.fallback_target = "Failure ledger";
    const html = renderToStaticMarkup(
      <>
        <DataMigrationTile
          change={{
            ...migration.data,
            code: [],
            codeDiff: [],
            beforeFields: migration.data.before_fields,
            facts: migration.data.facts.map((fact) => ({ ...fact, tone: "neutral" as const })),
          }}
        />
        <ExternalIntegrationTile
          change={{
            ...integration.data,
            code: [],
            codeDiff: [],
            fallbackTarget: integration.data.fallback_target,
            facts: integration.data.facts.map((fact) => ({ ...fact, tone: "neutral" as const })),
          }}
        />
      </>,
    );
    expect(html).toContain("original_field");
    expect(html).toContain("replacement_field");
    expect(html).toContain("Failure ledger");
    expect(html).not.toContain("full_name");
    expect(html).not.toContain("Retry queue");
  });

  it("shows pinned code and missing excerpts without implying review is unnecessary", () => {
    const tile = tileInput("database-schema");
    tile.code = [code()];
    const html = render([tile]);
    expect(html).toContain("before");
    expect(html).toContain("after");
    expect(html).toContain(`/blob/${document().review.head}/src/test.ts`);
    delete tile.code[0]!.diff;
    expect(render([tile])).toContain("Code excerpt not supplied");
    expect(render([tile])).not.toContain("Not necessary to review");
    expect(tileCodeUrl("acme/widgets", { ...code(), path: "src/a b#c.ts" })).toContain(
      "src/a%20b%23c.ts",
    );
  });

  it("preserves previous source versions and rejects future or mismatched current sources", () => {
    const doc = document();
    const tile = tileInput("database-schema");
    tile.code = [code(doc)];
    const historical = structuredClone(doc);
    historical.review.version++;
    historical.review.head = "b".repeat(40);
    historical.review.merge_base = "c".repeat(40);
    expect(validReviewTile(tile, historical)).toBe(true);
    expect(render([tile], historical)).toContain("Older source");
    tile.code[0]!.review_version = historical.review.version;
    expect(validReviewTile(tile, historical)).toBe(false);
    tile.code[0]!.review_version = historical.review.version + 1;
    expect(validReviewTile(tile, historical)).toBe(false);
  });

  it("rejects invalid diff coordinates and unsafe source paths", () => {
    const tile: ReviewTile = tileInput("refactor");
    tile.code = [code()];
    tile.code[0]!.diff!.hunks[0]!.header = "@@ -5 +5 @@";
    expect(validReviewTile(tile, document())).toBe(false);
    tile.code = [{ ...code(), path: "../outside.ts" }];
    expect(validReviewTile(tile, document())).toBe(false);
  });
  it("labels every API field change in requests and responses without hiding requirements", () => {
    const tile = tileInput("api-contract");
    const changes = ["add", "remove", "alter", "rename"] as const;
    const labels = ["Added", "Removed", "Changed", "Renamed"];
    for (const side of ["request", "response"] as const) {
      tile.data[side] = changes.map((change) => ({
        name: `${side}_${change}`,
        type: "string",
        requirement: "required",
        change,
      }));
    }
    const html = render([tile]);
    expect(html).not.toContain("Tile unavailable");
    const rows = html.match(/<li\b[^>]*>[\s\S]*?<\/li>/g)!;
    for (const side of ["request", "response"] as const) {
      changes.forEach((change, index) => {
        const row = rows.find((value) => value.includes(`${side}_${change}`))!;
        expect(row).toContain(labels[index]);
        expect(row).toContain("required");
        expect(row).toContain("bg-(--status-");
        if (change === "remove") expect(row).toContain("line-through");
      });
    }
  });

  it("shows all database column changes without presenting removed columns as current schema", () => {
    const tile = tileInput("database-schema");
    tile.data.tables = [
      {
        name: "records",
        columns: [
          { name: "existing", type: "text" },
          { name: "added", type: "text", change: "add" },
          { name: "removed", type: "text", change: "remove" },
          { name: "altered", type: "text", change: "alter" },
          { name: "renamed", type: "text", change: "rename" },
        ],
      },
    ];
    const html = render([tile]);
    expect(html).not.toContain("Tile unavailable");
    expect(html).toContain('aria-label="Schema changes"');
    expect(html).not.toContain("Schema after this pull request");
    const rows = html.match(/<li\b[^>]*>[\s\S]*?<\/li>/g)!;
    for (const [name, label] of [
      ["added", "New column"],
      ["removed", "Column removed"],
      ["altered", "Column changed"],
      ["renamed", "Column renamed"],
    ]) {
      const row = rows.find((value) => value.includes(`>${name}</span>`))!;
      expect(row).toContain(label);
      if (name === "removed") expect(row).toContain("line-through");
    }
    expect(rows.find((value) => value.includes(">existing</span>"))).not.toContain("bg-(--status-");
  });

  it("draws foreign keys in crow's-foot notation, with optionality from the column's nullability", () => {
    const tile = tileInput("database-schema");
    tile.data.tables = [
      {
        name: "events",
        change: "add",
        columns: [
          { name: "id", type: "uuid", key: "primary", change: "add" },
          { name: "owner_id", type: "uuid", key: "foreign", change: "add" },
          { name: "reviewer_id", type: "uuid nullable", key: "foreign", change: "add" },
        ],
      },
      { name: "users", columns: [{ name: "id", type: "uuid", key: "primary" }] },
    ];
    tile.data.relationships = [
      { from: "events.owner_id", to: "users.id", cardinality: "many-to-one" },
      { from: "events.reviewer_id", to: "users.id", cardinality: "many-to-one" },
    ];
    const html = render([tile]);
    expect(html).not.toContain("Tile unavailable");
    expect(html).toContain("events: zero or many · users: exactly one");
    expect(html).toContain("events: zero or many · users: zero or one");
  });

  it("leads the architecture tile with what changed and draws components, connections and sequences", () => {
    const tile = tileInput("architecture");
    tile.data.containers = [
      { id: "api", label: "API", kind: "service" },
      { id: "queue", label: "Job queue", kind: "runtime", change: "add" },
      { id: "db", label: "Database", kind: "store" },
    ];
    tile.data.edges = [
      { from: "api", to: "queue", label: "Enqueue the job", change: "add" },
      { from: "queue", to: "db", label: "Write the result" },
      { from: "api", to: "db", label: "Direct write", change: "remove" },
    ];
    tile.data.sequences = [
      {
        title: "Saving a result",
        steps: [
          { from: "api", to: "queue", message: "Enqueue", change: "add" },
          { from: "queue", to: "db", message: "Write" },
        ],
      },
    ];
    const html = render([tile]);
    expect(html).not.toContain("Tile unavailable");
    expect(html).toContain(
      "Adds 1 component, 1 connection and 1 runtime step; removes 1 connection",
    );
    expect(html).toContain('aria-label="Components and connections"');
    expect(html).toContain('aria-label="Sequence: Saving a result"');
    expect(html).toContain("Scope of the change");
    expect(html).not.toContain("No parts marked as changed");
  });

  it("says when the review marks no architecture change and names the shared component", () => {
    const tile = tileInput("architecture");
    tile.data.containers = [
      { id: "a", label: "Writer A", kind: "service" },
      { id: "b", label: "Writer B", kind: "service" },
      { id: "lock", label: "Lock", kind: "runtime" },
    ];
    tile.data.edges = [
      { from: "a", to: "lock", label: "Acquire" },
      { from: "b", to: "lock", label: "Acquire" },
    ];
    delete tile.data.sequences;
    const html = render([tile]);
    expect(html).toContain("Writer A and Writer B both go through Lock");
    expect(html).toContain("No parts marked as changed");
  });

  it.each([
    ["Not a breaking change.", "Not breaking"],
    ["Breaking: none.", "Not breaking"],
    ["Nothing breaks for existing clients.", "Not breaking"],
    ["Existing clients are unaffected.", "Not breaking"],
    ["Breaks v1 clients that send legacy ids.", "Breaking"],
    ["Callers should retry on conflict.", null],
  ])("reads the breaking-change wording %j as %s", (phrase, badge) => {
    const tile = tileInput("api-contract");
    tile.data.breaking_change = phrase;
    const html = render([tile]);
    const chips = html.match(/>(Not breaking|Breaking)</g) ?? [];
    expect(chips).toEqual(badge ? [`>${badge}<`] : []);
  });

  it("marks only HTTP statuses, not other leading numbers, as status codes", () => {
    const tile = tileInput("api-contract");
    tile.data.operations = "200 rows per page; 404 for missing pull requests";
    const html = render([tile]);
    expect(html).toMatch(/<code[^>]*>404<\/code>/);
    expect(html).not.toMatch(/<code[^>]*>200<\/code>/);
  });

  it("resolves schema-qualified table names in relationships and merges repeated tables", () => {
    const tile = tileInput("database-schema");
    tile.data.tables = [
      {
        name: "public.orders",
        change: "add",
        columns: [{ name: "user_id", type: "uuid", key: "foreign" }],
      },
      { name: "public.users", columns: [{ name: "id", type: "uuid", key: "primary" }] },
      { name: "public.users", columns: [{ name: "email", type: "text", change: "add" }] },
    ];
    tile.data.relationships = [
      { from: "public.orders.user_id", to: "public.users.id", cardinality: "many-to-one" },
    ];
    const html = render([tile]);
    expect(html).toContain("public.orders: zero or many · public.users: exactly one");
    expect(html.match(/aria-label="public.users table/g)).toHaveLength(1);
    expect(html).toContain("email");
  });

  it("lists architecture connections to unlisted components and labels unknown participants", () => {
    const tile = tileInput("architecture");
    tile.data.containers = [
      { id: "a", label: "A", kind: "service" },
      { id: "a", label: "A again", kind: "service" },
    ];
    tile.data.edges = [{ from: "a", to: "ghost", label: "Calls ghost", change: "add" }];
    tile.data.sequences = [
      { title: "Flow", steps: [{ from: "a", to: "ghost", message: "Hello" }] },
    ];
    const html = render([tile]);
    expect(html).toContain('aria-label="Connections not drawn"');
    expect(html).toContain("Calls ghost");
    expect(html).toContain("Adds 1 connection");
    expect(html).not.toContain("No parts marked as changed");
    expect(html).not.toContain("A again");
    expect(html).toContain(">ghost<");
  });

  it("hides an empty workflow failure path", () => {
    const tile = tileInput("workflow-state-machine");
    tile.data.failure_path = [];
    expect(render([tile])).not.toContain('aria-label="Failure path"');
  });

  it("drops access entries that contradict the model instead of showing contradictions", () => {
    const tile = tileInput("access-model");
    tile.data.actors = [
      { id: "a", label: "Alice" },
      { id: "b", label: "Bob" },
    ];
    tile.data.capabilities = [
      {
        id: "deploy",
        label: "Deploy",
        allowed: ["a", "zed"],
        granted: ["b", "zed"],
        revoked: ["a"],
      },
    ];
    const html = render([tile]);
    expect(html).toContain("Access is unchanged by this pull request");
    expect(html).toContain("contradicted its own");
    expect(html).not.toContain("Gains access:");
    expect(html).not.toContain("Loses access:");
  });

  it("shows the attention box as a neutral card listing what each reference points at", () => {
    const doc = document();
    const section = doc.sections.find((item) => item.risks.length > 0)!;
    const risk = section.risks[0]!;
    risk.text = "Two writers can take the locks in opposite orders";
    section.attention = { text: "Check the lock order.", refs: [risk.id, "rsk_missing"] };
    const html = renderToStaticMarkup(
      <ReviewSection document={doc} section={section} onSelectSection={() => {}} showTiles />,
    );
    const box = html.slice(
      html.indexOf('aria-label="Attention"'),
      html.indexOf("</section>", html.indexOf('aria-label="Attention"')),
    );
    expect(box).toContain("Check the lock order.");
    expect(box).toContain(risk.text);
    expect(box).not.toContain("status-critical-bg");
    // One jump for the risk; the reference that names nothing is text, not a link.
    expect(box.match(/View ↓/g)).toHaveLength(1);
    expect(box).toContain("rsk_missing");
  });

  it("shows a bug fix as before and after, stating the fix is part of the pull request", () => {
    const tile = tileInput("bug-fix");
    tile.data.bug = "Saving twice dropped the second edit";
    tile.data.fix = "Saves are merged in one transaction";
    const html = render([tile]);
    expect(html).toContain("Fixed by this PR");
    const bug = html.indexOf("Saving twice dropped the second edit");
    const fix = html.indexOf("Saves are merged in one transaction");
    expect(html.lastIndexOf("Before this PR", bug)).toBeGreaterThan(-1);
    expect(html.lastIndexOf("In this PR", fix)).toBeGreaterThan(bug);
  });

  it("lets the review's explicit wording outrank a removed field when judging breakage", () => {
    const tile = tileInput("api-contract");
    tile.data.request = [
      { name: "legacy_id", type: "string", requirement: "optional", change: "remove" },
    ];
    tile.data.breaking_change = "Not a breaking change: the server already ignored legacy_id.";
    expect(render([tile]).match(/>(Not breaking|Breaking)</g)).toEqual([">Not breaking<"]);
    tile.data.breaking_change = "Callers should drop the field.";
    expect(render([tile]).match(/>(Not breaking|Breaking)</g)).toEqual([">Breaking<"]);
  });

  it("counts a column operation as changing its table, and only changed tables as updated", () => {
    const tile = tileInput("database-schema");
    tile.data.tables = [
      { name: "orders", columns: [{ name: "status", type: "text" }] },
      { name: "users", columns: [{ name: "id", type: "uuid", key: "primary" }] },
    ];
    tile.data.relationships = [];
    tile.data.operations = [
      { kind: "alter", object: "column", subject: "orders.status", detail: "Now an enum" },
    ];
    delete tile.data.overview;
    const html = render([tile]);
    expect(html).toContain("Updates 1 table");
    const changed = html.slice(html.indexOf('aria-label="Changed tables"'));
    expect(changed.indexOf('aria-label="orders table')).toBeLessThan(
      changed.indexOf('aria-label="Existing tables referenced"'),
    );
    expect(html).toContain("EXISTING TABLE · UNCHANGED");
    expect(html.match(/UNCHANGED/g)).toHaveLength(1);
  });

  it("draws a wide architecture diagram at its own width so it scrolls instead of shrinking", () => {
    const tile = tileInput("architecture");
    tile.data.containers = ["a", "b", "c", "d", "e"].map((id) => ({
      id,
      label: `Component ${id}`,
      kind: "service" as const,
    }));
    tile.data.edges = [
      { from: "a", to: "b", label: "1" },
      { from: "b", to: "c", label: "2" },
      { from: "c", to: "d", label: "3" },
      { from: "d", to: "e", label: "4" },
    ];
    delete tile.data.sequences;
    const html = render([tile]);
    const width = Number(/viewBox="0 0 (\d+) /.exec(html)?.[1]);
    expect(width).toBeGreaterThan(1000);
    expect(html).toContain(`min-width:${width}px`);
    expect(html).not.toMatch(/<svg[^>]*max-w-full/);
  });

  it("wraps a long changed-entity name inside its column", () => {
    const doc = document();
    const section = doc.sections[0]!;
    section.blocks = [
      {
        kind: "entities",
        items: [
          {
            kind: "function",
            name: "Store.DeleteSelfAttributedEntireCheckpointIngests",
            change: "added",
            hunks: [],
          },
        ],
      },
    ];
    const html = renderToStaticMarkup(
      <ReviewSection document={doc} section={section} onSelectSection={() => {}} showTiles />,
    );
    expect(html).toContain("Store.<wbr/>");
    // Camel-case words are break points too, so a word is never split.
    expect(html).toMatch(/Checkpoint(<!-- -->)?<wbr\/><\/span><span>Ingests/);
    expect(html).toMatch(/<code class="[^"]*overflow-wrap:anywhere[^"]*"><span>Store\./);
  });

  it("keeps each excerpt with its own source entry even when URLs repeat across versions", () => {
    const tile = tileInput("database-schema");
    const missing = { ...code(), why: "First source without excerpt", diff: undefined };
    const second = { ...code(), why: "Second source excerpt" };
    second.diff!.hunks[0]!.lines[1]!.text = "second_only";
    const third = { ...code(), why: "Third source excerpt", review_version: 1 };
    third.diff!.hunks[0]!.lines[1]!.text = "third_only";
    tile.code = [missing, second, third];
    const html = render([tile]);
    expect(html).not.toContain("Tile unavailable");
    const firstPart = html.slice(html.indexOf(missing.why), html.indexOf(second.why));
    const secondPart = html.slice(html.indexOf(second.why), html.indexOf(third.why));
    const thirdPart = html.slice(html.indexOf(third.why));
    expect(firstPart).toContain("Code excerpt not supplied");
    expect(firstPart).not.toContain("second_only");
    expect(secondPart).toContain("second_only");
    expect(secondPart).not.toContain("third_only");
    expect(thirdPart).toContain("third_only");
    expect(thirdPart).not.toContain("second_only");
    expect(thirdPart).toContain("Older source");
  });

  it("describes actor assignments as grants and revocations, not new capabilities", () => {
    const tile = tileInput("access-model");
    tile.data.actors = ["a", "b", "c"].map((id) => ({ id, label: id }));
    tile.data.capabilities = [
      { id: "read", label: "Read", allowed: ["a", "b", "c"], granted: ["a", "b", "c"] },
    ];
    const access = () =>
      renderToStaticMarkup(<AccessModelTile change={{ ...tile.data, code: [], codeDiff: [] }} />);
    let html = access();
    expect(html).toContain("3 new grants");
    expect(html).toContain("1 capability");
    expect(html).not.toContain("3 new capabilities");
    tile.data.capabilities[0]!.allowed = [];
    tile.data.capabilities[0]!.granted = [];
    tile.data.capabilities[0]!.revoked = ["a", "b", "c"];
    html = access();
    expect(html).toContain("3 revocations");
    expect(html).not.toContain("Removes 3 capabilities");
    tile.data.capabilities[0]!.revoked = [];
    html = render([tile]);
    expect(html).toContain("Access is unchanged by this pull request");
    expect(html).toContain("No access change");
    expect(html).not.toContain("Current access");
    expect(html).not.toContain("Effective access by actor");
    // Every tile keeps its code and source anchors, so "unchanged" can be checked.
    expect(html).toContain('aria-label="Code"');
    expect(html).toContain('aria-label="Tile sources"');
  });

  it("shows exactly who held a changed capability before and after the pull request", () => {
    const tile = tileInput("access-model");
    tile.data.actors = [
      { id: "admin", label: "Admin" },
      { id: "writer", label: "Writer" },
      { id: "reader", label: "Reader" },
    ];
    tile.data.capabilities = [
      {
        id: "approve",
        label: "Approve",
        allowed: ["admin", "writer"],
        granted: ["writer"],
        revoked: ["reader"],
      },
    ];
    const html = render([tile]);
    expect(html).toContain('aria-label="Access changes"');
    expect(html).toContain("Effective access by actor");
    const before = html.slice(html.indexOf("Current access"), html.indexOf("New access"));
    const after = html.slice(html.indexOf("New access"), html.indexOf("Gains access:"));
    expect(before).toContain("Admin");
    expect(before).toContain("Reader");
    expect(before).not.toContain("Writer");
    expect(after).toContain("Admin");
    expect(after).toContain("Writer");
    expect(after).not.toContain("Reader");
    expect(html).toMatch(/Gains access:.*?Writer/);
    expect(html).toMatch(/Loses access:.*?Reader/);
  });

  it("draws database and API tiles beside every other supplied kind", () => {
    const bug = {
      ...tileInput("bug-fix"),
      data: { bug: "Stated defect", fix: "Stated correction" },
    };
    const framed = render([
      bug,
      tileInput("api-contract", "tile_api"),
      tileInput("database-schema", "tile_schema"),
    ]);
    expect(framed).toContain("Stated defect");
    expect(framed).toContain("Stated correction");
    expect(framed).toContain('aria-label="Schema changes"');
    expect(framed).toContain("bg-(--surface-sunken)");
    // A database tile attached to a risk still renders there. It is not
    // dropped because the behaviour section is where section tiles sit.
    const doc = document();
    const risk = {
      ...doc.sections[0]!.risks[0]!,
      tiles: [schemaTile("risk table", "tile_risk_schema")],
      section: doc.sections[0]!.id,
      sectionTitle: doc.sections[0]!.title,
      sectionState: doc.sections[0]!.derived.state,
    };
    const pane = renderToStaticMarkup(
      <ReviewRisk document={doc} risk={risk} onSelectSection={() => {}} showTiles />,
    );
    expect(pane).toContain("Database schema");
    expect(pane).toContain("risk table");
  });
});
