import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { Inbox } from "./inbox";
import {
  commandAction,
  forgetLastRows,
  INBOX_SHORTCUTS,
  listKeys,
  moveFocus,
  ShortcutsSheet,
  useRowFocus,
  useShortcuts,
} from "./shortcuts";
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
  forgetLastRows();
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
    "A pull request or group",
    "Walkthrough",
    "Sessions",
    "Jobs",
    "Repositories",
    "Settings",
    "A session or job transcript",
  ]);
  // jsdom is not a Mac: Ctrl, and Alt with the arrows for back and forward.
  expect(listed("Anywhere")).toEqual([
    "Ctrl+K",
    "Ctrl+.",
    "Ctrl+N",
    "Ctrl+P",
    "Ctrl+1",
    "Ctrl+2",
    "Ctrl+3",
    "Ctrl+4",
    "Ctrl+5",
    "Ctrl+6",
    "Ctrl+7",
    "Ctrl+8",
    "Alt+←",
    "Alt+→",
    "?",
    "Alt",
    "Esc",
  ]);
  expect(listed("Inbox")).toEqual(["j", "↓", "k", "↑", "Enter", "m", "f", "t", "s", "u"]);
  expect(listed("Pull requests")).toEqual([
    "j",
    "↓",
    "k",
    "↑",
    "Enter",
    "o",
    "x",
    "Space",
    "m",
    "f",
    "g",
    "t",
    "r",
    "Esc",
    "h",
    "←",
    "l",
    "→",
  ]);
  expect(listed("A pull request or group")).toEqual(["j", "k", "m", "f", "g", "t", "u", "Esc"]);
  expect(listed("Walkthrough")).toEqual(["→", "j", "←", "k", "c", "o", "Enter", "t", "e"]);
  const list = ["j", "↓", "k", "↑", "Enter", "o", "Esc"];
  expect(listed("Sessions")).toEqual([...list, "p", "r", "Space", "1–9"]);
  expect(listed("Jobs")).toEqual([...list, "t"]);
  expect(listed("Repositories")).toEqual([...list, "a", "/"]);
  expect(listed("A session or job transcript")).toEqual(["u", "Esc"]);
  expect(INBOX_SHORTCUTS.flatMap((s) => s.keys)).toEqual([
    "j",
    "ArrowDown",
    "k",
    "ArrowUp",
    "Enter",
    "o",
    "x",
    " ",
    "m",
    "f",
    "g",
    "t",
    "r",
  ]);
});

it("lists the keys of where the sheet was opened first", async () => {
  await act(async () => root.render(<ShortcutsSheet current="pulls" close={() => {}} />));
  expect(Array.from(node.querySelectorAll("dialog h3")).map((h) => h.textContent)).toEqual([
    "Pull requests",
    "Anywhere",
    "Inbox",
    "A pull request or group",
    "Walkthrough",
    "Sessions",
    "Jobs",
    "Repositories",
    "Settings",
    "A session or job transcript",
  ]);
});

it("reads the app-wide keys with the platform's command key", () => {
  const key = (patch: Partial<KeyboardEvent>) => ({
    key: "",
    code: "",
    ctrlKey: false,
    metaKey: false,
    altKey: false,
    shiftKey: false,
    ...patch,
  });
  // The sidebar's pages in order, by the digit's place on the keyboard.
  expect(commandAction(key({ ctrlKey: true, key: "2", code: "Digit2" }), false)).toEqual({
    kind: "page",
    page: "pulls",
  });
  expect(commandAction(key({ ctrlKey: true, key: "&", code: "Digit1" }), false)).toEqual({
    kind: "page",
    page: "inbox",
  });
  expect(commandAction(key({ metaKey: true, key: "8", code: "Digit8" }), true)).toEqual({
    kind: "page",
    page: "settings",
  });
  expect(commandAction(key({ ctrlKey: true, key: "9", code: "Digit9" }), false)).toBeNull();
  expect(commandAction(key({ metaKey: true, key: "," }), true)).toEqual({
    kind: "page",
    page: "settings",
  });
  expect(commandAction(key({ ctrlKey: true, key: "k" }), false)).toEqual({ kind: "go-to" });
  expect(commandAction(key({ metaKey: true, key: "K" }), true)).toEqual({ kind: "go-to" });
  // Back and forward: ⌘[ and ⌘] on a Mac, Alt with the arrows elsewhere.
  expect(commandAction(key({ metaKey: true, key: "[" }), true)).toEqual({ kind: "back" });
  expect(commandAction(key({ metaKey: true, key: "]" }), true)).toEqual({ kind: "forward" });
  expect(commandAction(key({ altKey: true, key: "ArrowLeft" }), false)).toEqual({
    kind: "back",
  });
  expect(commandAction(key({ altKey: true, key: "ArrowRight" }), false)).toEqual({
    kind: "forward",
  });
  // The other platform's modifier, extra modifiers and bare keys are not the app's.
  expect(commandAction(key({ metaKey: true, key: "1", code: "Digit1" }), false)).toBeNull();
  expect(commandAction(key({ ctrlKey: true, key: "1", code: "Digit1" }), true)).toBeNull();
  expect(
    commandAction(key({ ctrlKey: true, shiftKey: true, key: "!", code: "Digit1" }), false),
  ).toBeNull();
  expect(commandAction(key({ metaKey: true, key: "[" }), false)).toBeNull();
  expect(commandAction(key({ key: "k" }), false)).toBeNull();
});

const opened = vi.fn();
function List({ ids }: { ids: string[] }) {
  const rows = useRowFocus<HTMLUListElement>("list", ids);
  useShortcuts((event) => void listKeys(event, rows));
  return (
    <ul ref={rows.list}>
      {ids.map((id) => (
        <li key={id} data-row-id={id} data-focused={rows.focused === id || undefined}>
          <a href={`#${id}`} data-row-link="" onClick={() => opened(id)}>
            {id}
          </a>
        </li>
      ))}
    </ul>
  );
}

it("moves through a list's rows and opens the focused one", async () => {
  await act(async () => root.render(<List ids={["a", "b", "c"]} />));
  const focusedRow = () => node.querySelector<HTMLElement>("[data-focused]")?.dataset.rowId;
  await press("j");
  expect(focusedRow()).toBe("a");
  expect(document.activeElement?.textContent).toBe("a");
  await press("ArrowDown", {}, document.activeElement!);
  await press("j", {}, document.activeElement!);
  await press("j", {}, document.activeElement!);
  expect(focusedRow()).toBe("c");
  await press("k", {}, document.activeElement!);
  expect(focusedRow()).toBe("b");
  await press("o", {}, document.activeElement!);
  expect(opened).toHaveBeenCalledWith("b");
  // A row that leaves the list takes the focus with it.
  await act(async () => root.render(<List ids={["a", "c"]} />));
  expect(focusedRow()).toBeUndefined();
  await press("k");
  expect(focusedRow()).toBe("c");
  await press("Escape", {}, document.activeElement!);
  expect(focusedRow()).toBeUndefined();
  expect(document.activeElement).toBe(document.body);
});

it("comes back to a list on the row it left from", async () => {
  const focusedRow = () => node.querySelector<HTMLElement>("[data-focused]")?.dataset.rowId;
  await act(async () => root.render(<List ids={["a", "b", "c"]} />));
  await press("j");
  await press("j", {}, document.activeElement!);
  expect(focusedRow()).toBe("b");
  // Opening the row draws its page in the list's place; back, the row is kept.
  await act(async () => root.render(<p>Page</p>));
  await act(async () => root.render(<List ids={["a", "b", "c"]} />));
  expect(focusedRow()).toBe("b");
  expect(document.activeElement?.textContent).toBe("b");
  // A row opened by the pointer is kept as well.
  await act(async () => node.querySelector<HTMLElement>('[data-row-id="c"] a')!.click());
  await act(async () => root.render(<p>Page</p>));
  await act(async () => root.render(<List ids={["a", "b", "c"]} />));
  expect(focusedRow()).toBe("c");
  // Escape gives the row up for good.
  await press("Escape", {}, document.activeElement!);
  await act(async () => root.render(<p>Page</p>));
  await act(async () => root.render(<List ids={["a", "b", "c"]} />));
  expect(focusedRow()).toBeUndefined();
});

it("moves within a list", () => {
  expect(moveFocus([], null, 1)).toBeNull();
  expect(moveFocus(["a", "b"], "gone", 1)).toBe("a");
  expect(moveFocus(["a", "b"], "b", 1)).toBe("b");
  expect(moveFocus(["a", "b"], "b", -1)).toBe("a");
});
