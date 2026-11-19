import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";

import composition from "../../schema/composition-2.json" with { type: "json" };
import review2 from "../../schema/review-2.json" with { type: "json" };
import { REVIEW_1303 } from "../src/review-view/fixture-1303";
import { NATIVE } from "../src/review-view/native-tiles";
import { Gallery, verdict } from "./gallery";
import { SAMPLES } from "./samples";

// ---------- the schemas' bounds ----------

type Schema = {
  $ref?: string;
  type?: string;
  const?: unknown;
  enum?: unknown[];
  pattern?: string;
  properties?: Record<string, Schema>;
  required?: string[];
  additionalProperties?: boolean;
  items?: Schema;
  minItems?: number;
  maxItems?: number;
  minLength?: number;
  maxLength?: number;
  minimum?: number;
  maximum?: number;
  oneOf?: Schema[];
  allOf?: Schema[];
  if?: Schema;
  then?: Schema;
  else?: Schema;
};
type Defs = Record<string, Schema>;

const COMPOSITION = (composition as unknown as { $defs: Defs }).$defs;
const REVIEW_2 = (review2 as unknown as { $defs: Defs }).$defs;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const TYPE_CHECK: Record<string, (value: unknown) => boolean> = {
  string: (value) => typeof value === "string",
  integer: Number.isInteger,
  number: (value) => typeof value === "number",
  boolean: (value) => typeof value === "boolean",
  null: (value) => value === null,
  array: Array.isArray,
  object: isRecord,
};

/** The keywords `problems` checks, and the annotations it may pass over. */
const HANDLED = new Set([
  "$ref",
  "type",
  "const",
  "enum",
  "pattern",
  "properties",
  "required",
  "additionalProperties",
  "items",
  "minItems",
  "maxItems",
  "minLength",
  "maxLength",
  "minimum",
  "maximum",
  "oneOf",
  "allOf",
  "if",
  "then",
  "else",
  "title",
  "description",
  "$comment",
  "examples",
  "default",
]);

/** What `value` breaks of `schema`, its `$ref`s resolved in `defs`: the
 *  JSON Schema keywords the native diagrams' definitions use. A keyword it
 *  doesn't check throws, so a schema that grows one fails here rather than
 *  passing unchecked. */
function problems(defs: Defs, schema: Schema, value: unknown, at = "data"): string[] {
  const unknown = Object.keys(schema).filter((keyword) => !HANDLED.has(keyword));
  if (unknown.length > 0)
    throw new Error(`${at}: the checker doesn't handle ${unknown.join(", ")}`);
  if (schema.additionalProperties !== undefined && typeof schema.additionalProperties !== "boolean")
    throw new Error(`${at}: the checker handles only a boolean additionalProperties`);
  const out: string[] = [];
  const check = (each: Schema, inner: unknown, where: string) => problems(defs, each, inner, where);
  if (schema.$ref) {
    const name = schema.$ref.split("/").pop() ?? "";
    const target = defs[name];
    if (!target) return [`${at}: ${schema.$ref} isn't defined`];
    out.push(...check(target, value, at));
  }
  for (const each of schema.allOf ?? []) out.push(...check(each, value, at));
  if (schema.if) {
    const branch = check(schema.if, value, at).length === 0 ? schema.then : schema.else;
    if (branch) out.push(...check(branch, value, at));
  }
  if (schema.oneOf) {
    const passing = schema.oneOf.filter((each) => check(each, value, at).length === 0);
    if (passing.length !== 1) out.push(`${at} matches ${passing.length} of oneOf`);
  }
  if (schema.const !== undefined && value !== schema.const) out.push(`${at} isn't the const`);
  if (schema.enum && !schema.enum.includes(value)) out.push(`${at} isn't in the enum`);
  if (schema.type && !TYPE_CHECK[schema.type]?.(value))
    return [...out, `${at} isn't of type ${schema.type}`];
  if (typeof value === "string") {
    if (value.length > (schema.maxLength ?? Infinity)) out.push(`${at} is too long`);
    if (value.length < (schema.minLength ?? 0)) out.push(`${at} is too short`);
    if (schema.pattern && !new RegExp(schema.pattern, "u").test(value))
      out.push(`${at} doesn't match ${schema.pattern}`);
  }
  if (typeof value === "number") {
    if (value > (schema.maximum ?? Infinity)) out.push(`${at} is too large`);
    if (value < (schema.minimum ?? -Infinity)) out.push(`${at} is too small`);
  }
  if (Array.isArray(value)) {
    if (value.length > (schema.maxItems ?? Infinity)) out.push(`${at} has too many`);
    if (value.length < (schema.minItems ?? 0)) out.push(`${at} has too few`);
    if (schema.items)
      for (const [index, item] of value.entries())
        out.push(...check(schema.items, item, `${at}[${index}]`));
  }
  if (isRecord(value)) {
    for (const key of schema.required ?? [])
      if (!(key in value)) out.push(`${at}.${key} is missing`);
    for (const [key, field] of Object.entries(value)) {
      const property = schema.properties?.[key];
      if (property) out.push(...check(property, field, `${at}.${key}`));
      else if (schema.additionalProperties === false) out.push(`${at}.${key} isn't allowed`);
    }
  }
  return out;
}

/** A sample's problems: its data against the composition's native data of
 *  its type, the tile around it against `nativeTile`, its data without
 *  caption and citations (which a baum.review/2 tile keeps beside its data)
 *  against review-2.json's, and the host's checks no schema can say
 *  (oss/tiles/view/native.go). */
function boundsProblems(sample: (typeof SAMPLES)[number]): string[] {
  const { id, type, title, data } = sample;
  const { caption: _caption, source: _source, ...v2Data } = data as Record<string, unknown>;
  const out = [
    ...problems(COMPOSITION, { $ref: `#/$defs/nativeData_${type}` }, data),
    ...problems(COMPOSITION, { $ref: "#/$defs/nativeTile" }, { id, type, title, data }, "tile"),
    ...problems(REVIEW_2, { $ref: `#/$defs/v2TileData_${type}` }, v2Data, "v2 data"),
  ];
  if (type === "flow") {
    const lanes = (data as { lanes: { from?: number; steps: unknown[] }[] }).lanes;
    for (const [at, lane] of lanes.entries())
      if (lane.from !== undefined && (at === 0 || lane.from > (lanes[0]?.steps.length ?? 0)))
        out.push(`lane ${at} branches off a step that isn't there`);
  }
  if (type === "table") {
    const { columns, rows } = data as { columns: unknown[]; rows: unknown[][] };
    for (const [at, row] of rows.entries())
      if (row.length !== columns.length) out.push(`row ${at} isn't as long as the columns`);
  }
  return out;
}

describe("the samples", () => {
  const valid = SAMPLES.filter((sample) => sample.expect === "valid");
  const invalid = SAMPLES.filter((sample) => sample.expect === "invalid");

  it("cover each type with a valid and an invalid one", () => {
    for (const type of ["flow", "columns", "sequence", "table"]) {
      expect(valid.some((sample) => sample.type === type)).toBe(true);
      expect(invalid.some((sample) => sample.type === type)).toBe(true);
    }
  });

  it("are checked against the schemas, which catch a sample past its limits", () => {
    const flow = SAMPLES.find((sample) => sample.id === "dg_flow_max")!;
    const data = flow.data as { lanes: { steps: { title: string }[] }[] };
    const over = {
      ...flow,
      id: "dg_Upper",
      data: {
        ...data,
        lanes: [{ steps: [...data.lanes[0]!.steps, { title: "x".repeat(61) }] }],
      },
    };
    expect(boundsProblems(over)).toEqual(
      expect.arrayContaining([
        "data.lanes[0].steps has too many",
        "data.lanes[0].steps[6].title is too long",
        "tile.id doesn't match ^dg_[a-z0-9_]{1,60}$",
        "v2 data.lanes[0].steps has too many",
      ]),
    );
  });

  it("are checked by a checker that refuses keywords it doesn't know", () => {
    expect(() => problems({}, { type: "string", format: "uri" } as Schema, "x")).toThrow(
      "doesn't handle format",
    );
  });

  it("never end a text inside a code span", () => {
    const texts = JSON.stringify(SAMPLES).match(/"(?:[^"\\]|\\.)*"/g) ?? [];
    for (const text of texts) expect((text.match(/`/g) ?? []).length % 2, text).toBe(0);
  });

  it.each(valid.map((sample) => [sample.id, sample] as const))(
    "%s passes the page's check and the schemas' bounds",
    (_, sample) => {
      expect(NATIVE[sample.type]?.valid(sample.data)).toBe(true);
      expect(verdict(sample.type, sample.data)).toEqual({ valid: true });
      expect(boundsProblems(sample)).toEqual([]);
    },
  );

  it.each(invalid.map((sample) => [sample.id, sample] as const))(
    "%s fails, with a reason",
    (_, sample) => {
      expect(NATIVE[sample.type]?.valid(sample.data) ?? false).toBe(false);
      expect(verdict(sample.type, sample.data).reason).toBeTruthy();
    },
  );
});

// ---------- the page ----------

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}
globalThis.IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | null = null;
let container: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  container?.remove();
  root = null;
  container = null;
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
});

async function page(search = "") {
  window.history.replaceState(null, "", `/${search}`);
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => root!.render(<Gallery />));
  // The first poll fails and settles.
  await act(async () => {});
  return container;
}

describe("the gallery", () => {
  it("draws the built-in diagrams with the server unreachable", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.reject(new TypeError("Failed to fetch"))),
    );
    const page_ = await page("?theme=light");
    expect(page_.querySelector('[role="alert"]')?.textContent).toContain(
      "couldn't be read (Failed to fetch)",
    );
    const entries = [...page_.querySelectorAll<HTMLElement>('[data-testid="gallery-entry"]')];
    const fixture = page_.querySelector('[aria-label="Fixture acme/demo#7"]');
    expect(fixture?.querySelectorAll('[data-testid="gallery-entry"]').length).toBeGreaterThan(0);
    expect(entries.length).toBeGreaterThanOrEqual(SAMPLES.length);
    // The page's own drawing: a flow's lanes, a sequence's actors, a table.
    expect(page_.querySelector('[data-tile="dg_flow_checkout"] [data-lane]')).not.toBeNull();
    expect(page_.querySelector('[data-tile="dg_table_limits"] table')).not.toBeNull();
    expect(page_.textContent).toContain("Show all 12 rows");
    // An invalid one as the page draws it, with the reason beside it.
    const broken = page_.querySelector<HTMLElement>('[data-tile="dg_flow_invalid"]');
    expect(broken?.textContent).toContain("This tile's content couldn't be read.");
    const unknown = page_.querySelector<HTMLElement>('[data-tile="dg_timeline"]');
    expect(unknown?.textContent).toContain("This version can't show this kind of tile.");
    const samples = page_.querySelector('[aria-label="Samples"]')!;
    expect(samples.querySelectorAll('[data-testid="gallery-reason"]').length).toBe(
      SAMPLES.filter((sample) => sample.expect === "invalid").length,
    );
    // The fixture's failed tile says so rather than judging its data.
    expect(page_.querySelector('[data-tile="dg_stop"]')?.textContent).toContain(
      "This tile couldn't be written.",
    );
  });

  it("draws each diagram in both themes by default and filters by type", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.reject(new TypeError("Failed to fetch"))),
    );
    const page_ = await page("?type=sequence");
    const entries = [...page_.querySelectorAll<HTMLElement>('[data-testid="gallery-entry"]')];
    expect(entries.length).toBeGreaterThan(0);
    expect(entries.every((entry) => entry.dataset.type === "sequence")).toBe(true);
    for (const entry of entries)
      expect(
        [...entry.querySelectorAll<HTMLElement>("[data-gallery-theme]")].map(
          (each) => each.dataset.galleryTheme,
        ),
      ).toEqual(["light", "dark"]);
  });

  it("adds the server's diagrams", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(
            JSON.stringify({
              version: "1",
              problems: ["runs/x.json: not JSON"],
              documents: [],
              entries: [
                {
                  key: "file:a.json:dg_from_file",
                  source: "file",
                  origin: "a.json",
                  id: "dg_from_file",
                  type: "columns",
                  title: "From a file",
                  data: SAMPLES.find((sample) => sample.id === "dg_columns_access")!.data,
                },
              ],
            }),
          ),
        ),
      ),
    );
    const page_ = await page("?source=file");
    expect(page_.querySelectorAll('[data-testid="gallery-entry"]')).toHaveLength(1);
    expect(page_.querySelector('[data-tile="dg_from_file"]')?.textContent).toContain("Owners");
    expect(page_.textContent).toContain("runs/x.json: not JSON");
    // Its citations are another review's: left out, and said so.
    expect(page_.textContent).toContain("a.json · citations not drawn");
  });

  it("draws a server's baum.review/2 document's native tiles as the page does", async () => {
    // #1303 with one flow on the overview and one on a section, a failed one
    // and a code tile, which isn't native.
    const keep = new Set(["dg_shape", "dg_requests", "dg_stop"]);
    const document = {
      ...REVIEW_1303,
      tiles: REVIEW_1303.tiles.filter((tile) => keep.has(tile.id) || tile.type === "code"),
    };
    stubServer({
      version: "1",
      entries: [],
      problems: [],
      documents: [
        { key: "store:a", source: "store", origin: "acme/demo#7 v1", document },
        { key: "store:b", source: "store", origin: "acme/demo#8 v2", document: { schema: "x" } },
      ],
    });
    const page_ = await page("?source=store&theme=light");
    const entries = [...page_.querySelectorAll<HTMLElement>('[data-testid="gallery-entry"]')];
    expect(entries.map((entry) => entry.dataset.type)).toEqual(["flow", "flow", "flow"]);
    // The overview's in a card, a section's bare under its label.
    const overview = page_.querySelector<HTMLElement>('[data-tile="dg_shape"]')!;
    expect(overview.querySelector("h3")?.className).toContain("font-semibold");
    expect(overview.textContent).toContain("Nothing on the computer listens for the phone.");
    // Cited against the document itself.
    expect(overview.textContent).toContain("1 file");
    const section = page_.querySelector<HTMLElement>('[data-tile="dg_requests"]')!;
    expect(section.querySelector("h3")?.className).toContain("uppercase");
    expect(page_.querySelector('[data-tile="dg_stop"]')?.textContent).toContain(
      "This tile couldn't be written.",
    );
    expect(page_.textContent).toContain("acme/demo#7 v1 · dg_requests");
    expect(page_.textContent).toContain("acme/demo#8 v2: not a baum.review/2 document");
  });

  it("places a document's tiles where the page draws them, and says which it leaves out", async () => {
    const pick = (id: string) => REVIEW_1303.tiles.find((tile) => tile.id === id)!;
    const document = {
      ...REVIEW_1303,
      // A section's tile as the main diagram: the page draws it on the
      // overview, not in its section.
      overview: { ...REVIEW_1303.overview, diagram: "dg_requests" },
      tiles: [
        pick("dg_requests"),
        // The overview's, but not its main diagram, and a section's that
        // isn't listed: the page draws neither.
        pick("dg_shape"),
        { ...pick("dg_pairing"), id: "dg_gone", owner: "sec_gone" },
        pick("dg_encryption"),
      ],
    };
    stubServer({
      version: "1",
      entries: [],
      problems: [],
      documents: [{ key: "store:a", source: "store", origin: "acme/demo#7 v1", document }],
    });
    const page_ = await page("?source=store&theme=light");
    const entry = (tile: string) =>
      page_
        .querySelector(`[data-tile="${tile}"]`)!
        .closest<HTMLElement>('[data-testid="gallery-entry"]')!;
    const heading = (tile: string) =>
      page_.querySelector(`[data-tile="${tile}"] h3`)?.className ?? "";
    expect(entry("dg_requests").textContent).toContain("on overview");
    expect(heading("dg_requests")).toContain("font-semibold");
    expect(entry("dg_encryption").textContent).toContain("on sec_encryption");
    expect(heading("dg_encryption")).toContain("uppercase");
    for (const tile of ["dg_shape", "dg_gone"]) {
      expect(entry(tile).querySelector('[data-testid="gallery-off-page"]')).not.toBeNull();
      expect(heading(tile)).toContain("uppercase");
    }
  });

  it("says a composition's diagram placed nowhere isn't on the page", async () => {
    stubServer({
      version: "1",
      problems: [],
      documents: [],
      entries: [
        {
          key: "file:a.json:dg_nowhere",
          source: "file",
          origin: "a.json",
          id: "dg_nowhere",
          type: "flow",
          title: "Placed nowhere",
          data: SAMPLES[0]!.data,
        },
      ],
    });
    const page_ = await page("?source=file");
    expect(page_.querySelector('[data-testid="gallery-off-page"]')).not.toBeNull();
    expect(page_.querySelector('[data-tile="dg_nowhere"] h3')?.className).toContain("uppercase");
  });

  it("shows every type for one nothing has", async () => {
    stubServer({ version: "1", entries: [], documents: [], problems: [] });
    const page_ = await page("?type=foo&source=sample");
    expect(page_.querySelectorAll('[data-testid="gallery-entry"]')).toHaveLength(SAMPLES.length);
    expect(page_.querySelector<HTMLSelectElement>("select")?.value).toBe("");
  });

  it("shows every source for one it doesn't know", async () => {
    stubServer({ version: "1", entries: [], documents: [], problems: [] });
    const page_ = await page("?source=nonsense");
    expect(page_.querySelector('[aria-label="Fixture acme/demo#7"]')).not.toBeNull();
    expect(page_.querySelector('[aria-label="Samples"]')).not.toBeNull();
  });
});

function stubServer(body: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve(new Response(JSON.stringify(body)))),
  );
}
