import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { resetSessionPanel, sessionPanel } from "./session-panel-state";
import { pinnedLabel, TileLibrarySection, usedLabel } from "./settings-tiles";
import type { TileTypeDetail, TileTypeSummary } from "./tile-library";
import { NOT_IN_THIS_SHELL } from "./tiles/custom/environment";
import { ToastProvider } from "./toast";

let root: Root;
let node: HTMLDivElement;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/?page=settings&section=tiles");
  localStorage.clear();
  resetSessionPanel();
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
});
afterEach(() => {
  act(() => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__;
});

const summary = (over: Partial<TileTypeSummary> = {}): TileTypeSummary => ({
  name: "migration_steps",
  title: "Migration steps",
  use_when: "A pull request changes a database schema; not for data-only changes.",
  glance: "The tables changed, with the risky ones first.",
  origin: "chat",
  created: "2026-11-01T09:00:00Z",
  updated: "2026-11-02T09:00:00Z",
  sizes: ["half", "full"],
  kit: 1,
  digest: `sha256:${"a".repeat(64)}`,
  used_in: 0,
  samples: ["add_column", "drop_table"],
  versions: 2,
  pinned: ["acme/demo"],
  checked: true,
  ...over,
});
const detail = (over: Partial<TileTypeDetail> = {}): TileTypeDetail => ({
  ...summary(),
  fill: "List every table the pull request changes.",
  review_checks: "Check each destructive change.",
  paths: ["**/*.sql"],
  applies_with: ["tables"],
  version_list: [
    { digest: `sha256:${"a".repeat(64)}`, at: "2026-11-02T09:00:00Z", current: true },
    { digest: `sha256:${"b".repeat(64)}`, at: "2026-11-01T09:00:00Z" },
  ],
  ...over,
});

type Call = { method: string; path: string; body: unknown };

/** Answers the library's routes from `routes` (by method and path after the
 *  namespace) and records every call. */
function serve(routes: Record<string, (body: unknown) => unknown>) {
  const calls: Call[] = [];
  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), location.href);
    const path = url.pathname.replace(/^\/s\/test-ns\//, "") + url.search;
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ method, path, body });
    const route = routes[`${method} ${path}`];
    if (!route) return new Response("not found", { status: 404 });
    const answer = route(body);
    if (answer === undefined) return new Response(null, { status: 204 });
    return new Response(JSON.stringify(answer), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  return calls;
}

async function draw(type: string | null, open = vi.fn()) {
  await act(async () => {
    root.render(
      <ToastProvider>
        <TileLibrarySection
          type={type}
          open={open}
          head={(status) => (
            <header>
              <h2 id="settings-tiles-heading">Tile library</h2>
              {status}
            </header>
          )}
        />
      </ToastProvider>,
    );
  });
  await act(async () => {});
  return open;
}

const button = (name: string) =>
  [...node.querySelectorAll("button")].find((b) => b.textContent?.trim() === name);

it("says how the person makes a type when the library is empty", async () => {
  serve({ "GET api/tile-types": () => ({ types: [] }) });
  await draw(null);
  const empty = node.querySelector(".local-empty");
  expect(empty?.querySelector("h2")?.textContent).toBe("No tile types yet");
  expect(empty?.textContent).toContain("ask the session panel to make a tile");
});

it("lists each type with where it came from, when to use it, its pins and its use", async () => {
  serve({
    "GET api/tile-types": () => ({
      types: [
        summary(),
        summary({
          name: "interface_changes",
          title: "Interface changes",
          origin: "import",
          pinned: [],
          used_at: new Date(Date.now() - 2 * 86400000).toISOString(),
          used_in: 3,
          checked: false,
        }),
      ],
    }),
  });
  const open = await draw(null);
  const rows = [...node.querySelectorAll<HTMLElement>("[data-tile-type]")];
  expect(rows.map((row) => row.dataset.tileType)).toEqual(["migration_steps", "interface_changes"]);
  expect(rows[0]!.textContent).toContain("Migration steps");
  expect(rows[0]!.textContent).toContain("Custom");
  expect(rows[0]!.textContent).toContain("Made in chat · Pinned to acme/demo");
  expect(rows[0]!.textContent).toContain("A pull request changes a database schema");
  expect(rows[0]!.textContent).toContain("Not used in a review yet");
  expect(rows[1]!.textContent).toContain("Imported · Not pinned");
  expect(rows[1]!.textContent).toContain("in 3 repositories");
  expect(rows[0]!.textContent).not.toContain("Not checked yet");
  expect(rows[1]!.textContent).toContain("Not checked yet");
  // A row carries no button but its menu; its title opens the type.
  expect([...rows[0]!.querySelectorAll("button")].map((b) => b.getAttribute("aria-label"))).toEqual(
    ["Actions for Migration steps"],
  );
  await act(async () => rows[0]!.querySelector<HTMLAnchorElement>("a")!.click());
  expect(open).toHaveBeenCalledWith("migration_steps");
});

it("names its pins and use in words", () => {
  expect(pinnedLabel([])).toBe("Not pinned");
  expect(pinnedLabel(["acme/a", "acme/b"])).toBe("Pinned to 2 repositories");
  expect(usedLabel({ used_in: 0 })).toBe("Not used in a review yet");
  expect(usedLabel({ used_at: new Date().toISOString(), used_in: 1 })).toMatch(/in 1 repository$/);
});

function paneRoutes(current: { detail: TileTypeDetail }) {
  return {
    "GET api/tile-types": () => ({ types: [summary()] }),
    "GET api/tile-types/migration_steps": () => current.detail,
    "GET api/repositories": () => [
      { repository: "acme/demo", settings: { automatic: null }, clones: [] },
      { repository: "acme/other", settings: { automatic: null }, clones: [] },
    ],
    "GET api/tile-types/migration_steps/preview?sample=add_column": () => ({
      name: "migration_steps",
      title: "Migration steps",
      sizes: ["half", "full"],
      kit: 1,
      digest: `sha256:${"a".repeat(64)}`,
      samples: ["add_column", "drop_table"],
      sample: "add_column",
      data: { tables: [] },
      refs: { glance: {}, detail: {} },
    }),
  };
}

it("shows a type's texts, its previews at each size and its versions", async () => {
  const current = { detail: detail() };
  serve(paneRoutes(current));
  await draw("migration_steps");
  const pane = node.querySelector("[data-tile-type-pane]")!;
  expect(pane.querySelector("h3")?.textContent).toBe("Migration steps");
  expect(pane.textContent).toContain("A pull request changes a database schema");
  expect(pane.textContent).toContain("**/*.sql");
  expect(pane.textContent).toContain("Given to the review agent when this type is filled.");
  expect(pane.textContent).toContain("List every table the pull request changes.");
  expect([...pane.querySelectorAll("figcaption")].map((c) => c.textContent)).toEqual([
    "Half width",
    "Full width",
    "Detail",
  ]);
  const frames = [...pane.querySelectorAll("iframe")];
  expect(frames).toHaveLength(3);
  for (const frame of frames) {
    expect(frame.getAttribute("sandbox")).toBe("allow-scripts");
    expect(frame.getAttribute("src")).toBe(`/tile-frames/${"a".repeat(64)}`);
  }
  expect(pane.querySelector("[role=group][aria-label=Sample]")?.textContent).toContain(
    "drop_table",
  );
  const versions = [...pane.querySelectorAll("[data-version]")];
  expect(versions[0]!.textContent).toContain("Current");
  expect(versions[1]!.textContent).toContain("First saved");
});

it("renames, pins, restores and asks the session panel about the open type", async () => {
  const current = { detail: detail() };
  const calls = serve({
    ...paneRoutes(current),
    "PUT api/tile-types/migration_steps/title": (body) => {
      current.detail = detail({ title: (body as { title: string }).title });
      return current.detail;
    },
    "PUT api/tile-types/migration_steps/pins": (body) => {
      current.detail = detail({ pinned: (body as { repositories: string[] }).repositories });
      return current.detail;
    },
    "POST api/tile-types/migration_steps/restore": () => current.detail,
  });
  const open = await draw("migration_steps");
  const pane = () => node.querySelector("[data-tile-type-pane]")!;

  await act(async () => button("Rename")!.click());
  const field = node.querySelector<HTMLInputElement>("input[aria-label=Title]")!;
  await act(async () => {
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    set.call(field, "Schema migrations");
    field.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () => button("Save")!.click());
  expect(calls).toContainEqual({
    method: "PUT",
    path: "api/tile-types/migration_steps/title",
    body: { title: "Schema migrations" },
  });
  expect(pane().querySelector("h3")?.textContent).toBe("Schema migrations");

  const other = node.querySelector<HTMLInputElement>("#tile-pin-acme-other")!;
  expect(node.querySelector<HTMLInputElement>("#tile-pin-acme-demo")!.checked).toBe(true);
  expect(other.checked).toBe(false);
  await act(async () => other.click());
  expect(calls).toContainEqual({
    method: "PUT",
    path: "api/tile-types/migration_steps/pins",
    body: { repositories: ["acme/demo", "acme/other"] },
  });

  const restore = node.querySelector<HTMLButtonElement>("[data-version] button")!;
  await act(async () => restore.click());
  expect(calls).toContainEqual({
    method: "POST",
    path: "api/tile-types/migration_steps/restore",
    body: { digest: `sha256:${"b".repeat(64)}` },
  });

  const show = vi.spyOn(sessionPanel, "show");
  await act(async () => button("Edit in chat")!.click());
  expect(open).toHaveBeenCalledWith("migration_steps");
  expect(show).toHaveBeenCalled();
});

it("deletes a type with Undo in its toast", async () => {
  const current = { detail: detail() };
  const calls = serve({
    ...paneRoutes(current),
    "DELETE api/tile-types/migration_steps": () => undefined,
    "POST api/tile-types/migration_steps/undelete": () => current.detail,
  });
  const open = await draw("migration_steps");
  await act(async () => button("Delete")!.click());
  expect(calls).toContainEqual({
    method: "DELETE",
    path: "api/tile-types/migration_steps",
    body: {},
  });
  expect(open).toHaveBeenCalledWith(null);
  expect(document.body.textContent).toContain("Deleted Migration steps");
  await act(async () => button("Undo")!.click());
  expect(calls).toContainEqual({
    method: "POST",
    path: "api/tile-types/migration_steps/undelete",
    body: {},
  });
});

it("says custom tiles are unavailable in a desktop shell without frames, and still lists and edits", async () => {
  (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__ = {};
  const current = { detail: detail() };
  const calls = serve(paneRoutes(current));
  await draw("migration_steps");
  const pane = node.querySelector("[data-tile-type-pane]")!;
  expect(pane.textContent).toContain(NOT_IN_THIS_SHELL);
  expect(pane.querySelector("iframe")).toBeNull();
  expect(calls.some((c) => c.path.includes("/preview"))).toBe(false);
  expect(button("Rename")).toBeDefined();
  expect(button("Edit in chat")).toBeDefined();
});

it("exports a type as a download in a browser", async () => {
  const current = { detail: detail() };
  const calls = serve({
    ...paneRoutes(current),
    "GET api/tile-types/migration_steps/export": () => ({ schema: "baum.tile-type/1" }),
  });
  const clicked: string[] = [];
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
    this: HTMLAnchorElement,
  ) {
    clicked.push(this.download);
  });
  Object.assign(URL, { createObjectURL: () => "blob:x", revokeObjectURL: () => {} });
  await draw("migration_steps");
  await act(async () => button("Export…")!.click());
  expect(calls.some((c) => c.path === "api/tile-types/migration_steps/export")).toBe(true);
  expect(clicked).toEqual(["migration_steps.baumtile.json"]);
});

it("shows what an imported file would add, says it was renamed, and imports it", async () => {
  HTMLDialogElement.prototype.showModal ??= function (this: HTMLDialogElement) {
    this.open = true;
  };
  HTMLDialogElement.prototype.close ??= function (this: HTMLDialogElement) {
    this.open = false;
  };
  const check = {
    name: "migration_steps_2",
    renamed_from: "migration_steps",
    title: "Shared migrations",
    use_when: "A schema changes.",
    fill: "Fill every table, and nothing else.",
    glance: "Tables",
    review_checks: "",
    paths: ["db/**"],
    sizes: ["half"],
    kit: 1,
    digest: `sha256:${"c".repeat(64)}`,
    samples: ["one"],
    sample: "one",
    data: {},
    refs: { glance: {}, detail: {} },
  };
  const calls = serve({
    "GET api/tile-types": () => ({ types: [summary()] }),
    "POST api/tile-type-files/check": () => check,
    "POST api/tile-type-files": () => ({
      name: "migration_steps_2",
      renamed_from: "migration_steps",
      checked: false,
    }),
  });
  const open = await draw(null);
  const input = node.querySelector<HTMLInputElement>("input[data-tile-type-file]")!;
  const file = new File(['{"schema":"baum.tile-type/1"}'], "shared.baumtile.json", {
    type: "application/json",
  });
  await act(async () => {
    Object.defineProperty(input, "files", { configurable: true, value: [file] });
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await act(async () => {});
  const dialog = node.querySelector("dialog.tile-import-dialog")!;
  expect(dialog.textContent).toContain("Import Shared migrations");
  expect(dialog.textContent).toContain("saved as migration_steps_2");
  expect(dialog.textContent).toContain("Given to the review agent when this type is filled.");
  expect(dialog.querySelector("[data-import-fill]")?.textContent).toBe(
    "Fill every table, and nothing else.",
  );
  expect(dialog.textContent).toContain("db/**");
  expect(dialog.querySelectorAll("iframe")).toHaveLength(2);
  expect(calls).toContainEqual({
    method: "POST",
    path: "api/tile-type-files/check",
    body: { file: '{"schema":"baum.tile-type/1"}' },
  });
  await act(async () => button("Import")!.click());
  expect(calls.filter((c) => c.path === "api/tile-type-files")).toHaveLength(1);
  expect(open).toHaveBeenCalledWith("migration_steps_2");
  expect(node.querySelector("dialog.tile-import-dialog")).toBeNull();
});

it("says why a file can't be imported", async () => {
  serve({ "GET api/tile-types": () => ({ types: [] }) });
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) =>
    String(input).includes("tile-type-files")
      ? new Response(
          JSON.stringify({ message: "The file is not a tile type file this baum reads" }),
          {
            status: 400,
            headers: { "Content-Type": "application/json" },
          },
        )
      : new Response(JSON.stringify({ types: [] }), {
          headers: { "Content-Type": "application/json" },
        }),
  );
  await draw(null);
  const input = node.querySelector<HTMLInputElement>("input[data-tile-type-file]")!;
  await act(async () => {
    Object.defineProperty(input, "files", {
      configurable: true,
      value: [new File(["{}"], "bad.json")],
    });
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await act(async () => {});
  expect(node.querySelector("[role=alert]")?.textContent ?? node.textContent).toContain(
    "bad.json can't be imported. The file is not a tile type file this baum reads",
  );
  expect(node.querySelector("dialog")).toBeNull();
});
