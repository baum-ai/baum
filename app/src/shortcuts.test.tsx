import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { Inbox } from "./inbox";
import { INBOX_SHORTCUTS, moveFocus, ShortcutsSheet } from "./shortcuts";
import { rowKey, type Entry, type ReviewGroup } from "./api";

const entry = (number: number, patch: Partial<Entry> = {}, mine = true): Entry => ({
  pull_request: {
    repository: "acme/demo",
    number,
    title: `Change ${number}`,
    url: `https://github.com/acme/demo/pull/${number}`,
    head_sha: "head",
    state: "OPEN",
    draft: false,
    fork: false,
    authored_by_me: mine,
    review_requested_from_me: false,
    author_has_write: true,
    team_membership_known: true,
    viewer_can_push: true,
    review: "",
    checks: "passing",
    merge: "ready",
    unresolved: 0,
    unresolved_capped: false,
  },
  clone_id: "clone",
  status: "no_review",
  version_count: 0,
  latest: null,
  risk_score: null,
  historical: false,
  stale: false,
  review: "",
  checks: "passing",
  merge: "ready",
  unresolved: 0,
  unresolved_capped: false,
  review_minutes: null,
  ...patch,
});
const group = {
  id: "g1",
  repository: "acme/demo",
  name: "Auth rework",
  members: [5, 6],
  created_at: "2026-10-26T10:00:00Z",
  updated_at: "2026-10-26T10:00:00Z",
  version_count: 0,
  latest: null,
  risk_score: null,
  reviewed: [],
  outdated: false,
} as ReviewGroup;

// Drawn order: Needs changes (#2); Ready to merge: the group (#5 and #6
// inside it, collapsed), then #1; Other (#3).
const rows = [
  entry(1),
  entry(2, { merge: "conflict", checks: "failing" }),
  entry(3, {}, false),
  entry(5),
  entry(6),
];
let root: Root;
let node: HTMLDivElement;
const handlers = {
  generate: vi.fn(),
  refresh: vi.fn(),
  fix: vi.fn(),
  setSelection: vi.fn(),
};
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
  // jsdom has no modal dialogs.
  HTMLDialogElement.prototype.showModal = function () {
    this.open = true;
  };
  for (const handler of Object.values(handlers)) handler.mockClear();
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "fetch",
    vi.fn(() => new Promise<Response>(() => {})),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
});

const render = (props: Partial<Parameters<typeof Inbox>[0]> = {}) =>
  act(async () =>
    root.render(
      <Inbox
        rows={rows}
        cloneFor={() => "clone"}
        selection={new Set()}
        setSelection={handlers.setSelection}
        Link={({ children, ...props }) => <a {...props}>{children}</a>}
        groups={[group]}
        fix={handlers.fix}
        generate={handlers.generate}
        refresh={handlers.refresh}
        {...props}
      />,
    ),
  );
const press = (key: string, init: KeyboardEventInit = {}, target: Element = document.body) =>
  act(async () => {
    target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, ...init }));
  });
const focused = () => node.querySelector("[data-focused]");
const focusedTitle = () => focused()?.querySelector(".inbox-pr-title")?.textContent;

it("moves the row focus through the drawn rows, group rows included, and stops at the ends", async () => {
  await render();
  expect(focused()).toBeNull();
  await press("j");
  expect(focusedTitle()).toBe("Change 2");
  await press("ArrowDown");
  expect(focusedTitle()).toBe("Auth rework");
  expect(document.activeElement).toBe(focused()?.querySelector("a"));
  // The group's members are collapsed, so the next row is the loose one.
  await press("j");
  expect(focusedTitle()).toBe("Change 1");
  await press("j");
  expect(focusedTitle()).toBe("Change 3");
  await press("j");
  expect(focusedTitle()).toBe("Change 3");
  await press("k");
  await press("ArrowUp");
  expect(focusedTitle()).toBe("Auth rework");
});

it("visits a group's members once it is expanded", async () => {
  await render();
  await act(async () =>
    node.querySelector<HTMLElement>('button[aria-label="Expand Auth rework"]')!.click(),
  );
  for (let i = 0; i < 2; i++) await press("j");
  expect(focusedTitle()).toBe("Auth rework");
  await press("j");
  expect(focusedTitle()).toBe("Change 5");
  await press("j");
  expect(focusedTitle()).toBe("Change 6");
  await press("j");
  expect(focusedTitle()).toBe("Change 1");
});

it("skips the rows of a collapsed bucket and drops a focus that left the list", async () => {
  await render();
  await press("j");
  expect(focusedTitle()).toBe("Change 2");
  await act(async () => node.querySelector<HTMLElement>('button[aria-expanded="true"]')!.click());
  expect(focused()).toBeNull();
  await press("j");
  expect(focusedTitle()).toBe("Auth rework");
});

it("starts from the last row on k", async () => {
  await render();
  await press("k");
  expect(focusedTitle()).toBe("Change 3");
});

it("ignores keys while typing, with a modifier, or under an open dialog", async () => {
  await render();
  const input = document.createElement("input");
  const select = document.createElement("select");
  const area = document.createElement("textarea");
  const editable = document.createElement("div");
  // jsdom does not compute isContentEditable from the attribute.
  Object.defineProperty(editable, "isContentEditable", { value: true });
  node.append(input, select, area, editable);
  for (const field of [input, select, area, editable]) await press("j", {}, field);
  expect(focused()).toBeNull();
  await press("j", { ctrlKey: true });
  await press("j", { metaKey: true });
  await press("j", { altKey: true });
  expect(focused()).toBeNull();
  const dialog = document.createElement("dialog");
  dialog.open = true;
  node.append(dialog);
  await press("j");
  expect(focused()).toBeNull();
  dialog.remove();
  await press("j");
  expect(focusedTitle()).toBe("Change 2");
});

it("still works after a checkbox was clicked", async () => {
  await render();
  const box = node.querySelector<HTMLInputElement>('input[aria-label="Select acme/demo #1"]')!;
  await press("j", {}, box);
  expect(focusedTitle()).toBe("Change 2");
});

it("opens the focused row through its link", async () => {
  await render();
  const clicked = vi.fn((event: Event) => event.preventDefault());
  node.addEventListener("click", clicked);
  await press("Enter");
  expect(clicked).not.toHaveBeenCalled();
  await press("j");
  await press("o");
  expect(clicked).toHaveBeenCalledTimes(1);
  expect((clicked.mock.calls[0]![0].target as HTMLElement).textContent).toBe("Change 2");
});

it("opens with Enter from the page, and leaves Enter on the focused link to the link", async () => {
  await render();
  await press("j");
  const clicked = vi.fn((event: Event) => event.preventDefault());
  node.addEventListener("click", clicked);
  await press("Enter", {}, document.activeElement!);
  expect(clicked).not.toHaveBeenCalled();
  (document.activeElement as HTMLElement).blur();
  await press("Enter");
  expect(clicked).toHaveBeenCalledTimes(1);
});

it("toggles the selection with x", async () => {
  await render();
  await press("x");
  expect(handlers.setSelection).not.toHaveBeenCalled();
  await press("j");
  await press("x");
  const update = handlers.setSelection.mock.calls[0]![0] as (s: Set<string>) => Set<string>;
  expect([...update(new Set())]).toEqual([rowKey(rows[1]!)]);
});

it("opens Merge on a ready row only", async () => {
  await render();
  await press("j");
  await press("m");
  expect(node.querySelector("dialog")).toBeNull();
  await press("j");
  await press("j");
  expect(focusedTitle()).toBe("Change 1");
  await press("m");
  expect(node.querySelector("dialog")?.textContent).toContain("Merge pull request #1?");
});

it("calls fix only on a row that offers it, and generate and refresh", async () => {
  await render();
  await press("j");
  expect(focusedTitle()).toBe("Change 2");
  await press("f");
  expect(handlers.fix).toHaveBeenCalledWith(rows[1]);
  await press("g");
  expect(handlers.generate).toHaveBeenCalledWith(rows[1]);
  await press("j");
  await press("f");
  expect(handlers.fix).toHaveBeenCalledTimes(1);
  await press("r");
  expect(handlers.refresh).toHaveBeenCalledTimes(1);
});

it("does not generate a review already starting, nor act on a group row", async () => {
  await render({ rows: [entry(5, { status: "generating" }), entry(6)] });
  await press("j");
  expect(focusedTitle()).toBe("Auth rework");
  await press("g");
  await press("x");
  await press("m");
  await press("f");
  expect(handlers.generate).not.toHaveBeenCalled();
  expect(handlers.setSelection).not.toHaveBeenCalled();
  expect(handlers.fix).not.toHaveBeenCalled();
  await render({ rows: [entry(1, { status: "generating" })] });
  await press("j");
  await press("g");
  expect(handlers.generate).not.toHaveBeenCalled();
});

it("clears the focus with Escape", async () => {
  await render();
  await press("j");
  await press("Escape", {}, document.activeElement!);
  expect(focused()).toBeNull();
  expect(document.activeElement).toBe(document.body);
});

it("lists exactly the keys that work in the sheet", async () => {
  await act(async () => root.render(<ShortcutsSheet close={() => {}} />));
  const sheet = node.querySelector("dialog")!;
  expect(sheet.open).toBe(true);
  const listed = (group: string) =>
    Array.from(sheet.querySelectorAll(`[aria-label="${group}"] dt kbd`)).map(
      (kbd) => kbd.textContent,
    );
  const headings = () => Array.from(sheet.querySelectorAll("h3")).map((h) => h.textContent);
  expect(headings()).toEqual([
    "Anywhere",
    "Inbox",
    "Pull requests",
    "A pull request, group or session",
  ]);
  expect(listed("Anywhere")).toEqual(["?", "Alt", "Esc"]);
  expect(listed("Inbox")).toEqual(["j", "↓", "k", "↑", "Enter", "m", "s", "u"]);
  expect(listed("Pull requests")).toEqual([
    "j",
    "↓",
    "k",
    "↑",
    "Enter",
    "o",
    "x",
    "m",
    "f",
    "g",
    "r",
    "Esc",
  ]);
  expect(listed("A pull request, group or session")).toEqual(["u", "Esc"]);
  expect(INBOX_SHORTCUTS.flatMap((s) => s.keys)).toEqual([
    "j",
    "ArrowDown",
    "k",
    "ArrowUp",
    "Enter",
    "o",
    "x",
    "m",
    "f",
    "g",
    "r",
  ]);
});

it("lists the keys of where the sheet was opened first", async () => {
  await act(async () => root.render(<ShortcutsSheet current="pulls" close={() => {}} />));
  expect(Array.from(node.querySelectorAll("dialog h3")).map((h) => h.textContent)).toEqual([
    "Pull requests",
    "Anywhere",
    "Inbox",
    "A pull request, group or session",
  ]);
});

it("moves within a list", () => {
  expect(moveFocus([], null, 1)).toBeNull();
  expect(moveFocus(["a", "b"], "gone", 1)).toBe("a");
  expect(moveFocus(["a", "b"], "b", 1)).toBe("b");
  expect(moveFocus(["a", "b"], "b", -1)).toBe("a");
});
