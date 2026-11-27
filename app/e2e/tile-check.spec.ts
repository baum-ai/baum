import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

// The render check, end to end: the fixture daemon's check queue and the
// app's runner drawing inside the page, as it does in a browser. Candidates
// are submitted through a route only the fixture has (the agent tools that
// will submit them come later). Each file in e2e/tile-check is a tile that
// exercises the browser runner. The daemon watchdog is covered by
// TestHangBlocksTheDigest in internal/tilechecks.

const CORPUS = "e2e/tile-check";
const SCHEMA = {
  type: "object",
  additionalProperties: false,
  required: ["items"],
  properties: {
    items: { type: "array", maxItems: 5, items: { type: "string", maxLength: 60 } },
  },
};

type Problem = {
  code: string;
  message: string;
  hint: string;
  sample: string;
  face: string;
  line?: number;
};
type Result = {
  check: string;
  digest: string;
  verdict: string;
  reason?: string;
  errors: Problem[];
  warnings: Problem[];
  runs: { sample: string; face: string; size: string; theme: string }[];
  engine: string;
};

function candidate(file: string, html = readFileSync(join(CORPUS, `${file}.html`), "utf8")) {
  return {
    definition: {
      name: `check_${file.replace(/-/g, "_")}`,
      title: `Check ${file}`,
      use_when: "When the render check is tested. Not otherwise.",
      fill: "List the items.",
      data_schema: SCHEMA,
      kit: 1,
      sizes: ["half", "third"],
      source: { format: "html", html },
    },
    samples: [{ name: "example", data: { items: ["alpha", "beta"] } }],
  };
}

/** A tile type directory in the library's layout, as a candidate. */
function typeDirectory(dir: string) {
  const read = (name: string) => readFileSync(join(dir, name), "utf8");
  const meta = JSON.parse(read("type.json"));
  const samples = readdirSync(join(dir, "samples"))
    .filter((name) => name.endsWith(".json"))
    .sort()
    .slice(0, 3)
    .map((name) => JSON.parse(read(join("samples", name))));
  return {
    definition: {
      name: meta.name,
      title: meta.title,
      use_when: meta.use_when,
      fill: read("fill.md"),
      data_schema: JSON.parse(read("schema.json")),
      kit: meta.kit,
      sizes: meta.sizes,
      source: { format: "html", html: read("tile.html") },
    },
    samples,
  };
}

/** Submits a candidate from the signed-in page and waits for its result. */
function submit(page: Page, body: unknown): Promise<Result> {
  return page.evaluate(async (body) => {
    const response = await fetch(new URL("api/tile-checks/submit", location.href), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!response.ok) throw new Error(`${response.status} ${await response.text()}`);
    return response.json();
  }, body);
}

const codes = (problems: Problem[]) => [...new Set(problems.map((p) => p.code))].sort();

test.describe.configure({ mode: "serial" });
test.setTimeout(90_000);

let page: Page;
test.beforeAll(async ({ browser }) => {
  page = await browser.newPage();
  await page.goto(await launchURL());
  await page.waitForURL(/\/s\//);
  // The app's event stream is what hears a check is queued.
  await expect(page.getByRole("navigation").first()).toBeVisible();
});
test.afterAll(async () => {
  await page.close();
});

test("a good tile passes", async () => {
  const result = await submit(page, candidate("good"));
  expect(result.errors).toEqual([]);
  expect(result.verdict).toBe("pass");
  expect(result.check).toMatch(/^chk_[0-9a-f]+$/);
  expect(result.engine).toContain("Mozilla");
  // Each sample at both sizes and widths: the example, minimal and maximal.
  const runs = result.runs.map((r) => `${r.sample} ${r.face} ${r.size}`);
  for (const sample of ["example", "minimal", "maximal"])
    for (const run of ["glance half", "glance third", "detail full"])
      expect(runs).toContain(`${sample} ${run}`);
  // The frames are gone once the check is drawn.
  await expect(page.locator("[data-tile-check-stage]")).toHaveCount(0);
});

for (const [file, verdict, errors, warnings] of [
  ["empty", "fail", ["empty"], []],
  ["overflowing", "fail", ["overflow_glance", "overflow_x"], ["overflow_glance"]],
  ["throwing", "fail", ["console_error", "script_error", "unhandled_rejection"], []],
  ["network", "fail", ["csp_violation"], []],
  ["height-loop", "fail", ["layout_loop"], []],
  ["slow", "pass", [], ["slow"]],
  ["failing", "fail", ["render_failed"], []],
  ["never", "fail", ["timeout"], []],
] as const)
  test(`${file} is reported`, async () => {
    const result = await submit(page, candidate(file));
    expect(codes(result.errors)).toEqual(errors);
    for (const code of warnings) expect(codes(result.warnings)).toContain(code);
    expect(result.verdict).toBe(verdict);
    for (const problem of [...result.errors, ...result.warnings]) expect(problem.hint).not.toBe("");
    if (file === "throwing") {
      const thrown = result.errors.find(
        (p) => p.code === "script_error" && p.sample === "example",
      )!;
      // The kit's own message and line, not "Script error.".
      expect(thrown.message).toContain("cannot draw 2 items");
      expect(thrown.line).toBeGreaterThan(0);
    }
    if (file === "overflowing") {
      // The maximal sample's glance only warns: its card's fade covers it.
      expect(
        result.errors.some((p) => p.code === "overflow_glance" && p.sample === "maximal"),
      ).toBe(false);
      expect(
        result.warnings.some((p) => p.code === "overflow_glance" && p.sample === "maximal"),
      ).toBe(true);
    }
  });

test("a network call in the source fails lint without drawing", async () => {
  const html = readFileSync(join(CORPUS, "good.html"), "utf8").replace(
    "<script>",
    "<script>\n  fetch('/api/repos');",
  );
  const result = await submit(page, candidate("network_lint", html));
  expect(result.verdict).toBe("fail");
  expect(codes(result.errors)).toEqual(["lint"]);
  expect(result.runs).toEqual([]);
});

for (const [name, dir] of [
  ["the schema change library tile", "../tiles/library/schema_change"],
  ["the interface change library tile", "../tiles/library/api_change"],
  ["the kit's starter", "../tiles/kit/starter"],
] as const)
  test(`${name} passes with its samples`, async () => {
    const result = await submit(page, typeDirectory(dir));
    expect(result.errors).toEqual([]);
    expect(result.verdict).toBe("pass");
  });
