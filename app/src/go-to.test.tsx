import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { GoTo, goToMatches, type GoToItem } from "./go-to";

const items: GoToItem[] = [
  { id: "page:inbox", kind: "Page", label: "Inbox", href: "/inbox" },
  { id: "page:pulls", kind: "Page", label: "Pull requests", href: "/pulls" },
  {
    id: "pr:1",
    kind: "Pull request",
    label: "Fix the login redirect",
    detail: "acme/web #42",
    keywords: "42 sam fix-login",
    href: "/pr/42",
  },
  {
    id: "pr:2",
    kind: "Pull request",
    label: "Pull the inbox apart",
    detail: "acme/app #7",
    keywords: "7 alex",
    href: "/pr/7",
  },
  {
    id: "session:1",
    kind: "Session",
    label: "Codex on fix-login",
    detail: "Codex · acme/web · fix-login",
    href: "/session/1",
  },
];

it("finds places by every word, best first", () => {
  expect(goToMatches(items, "").map((item) => item.id)).toEqual(items.map((item) => item.id));
  // A label that starts with the query comes first, then a label with a word
  // that does, then the rest (here by its kind) in the order given.
  expect(goToMatches(items, "pull").map((item) => item.id)).toEqual(["page:pulls", "pr:2", "pr:1"]);
  expect(goToMatches(items, "fix").map((item) => item.id)).toEqual(["pr:1", "session:1"]);
  // A repository and number, with or without #, by detail and keywords.
  expect(goToMatches(items, "web #42").map((item) => item.id)).toEqual(["pr:1"]);
  expect(goToMatches(items, "42").map((item) => item.id)).toEqual(["pr:1"]);
  expect(goToMatches(items, "SAM").map((item) => item.id)).toEqual(["pr:1"]);
  expect(goToMatches(items, "session").map((item) => item.id)).toEqual(["session:1"]);
  expect(goToMatches(items, "nothing like this")).toEqual([]);
});

let root: Root;
let node: HTMLDivElement;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  // jsdom has no modal dialogs.
  HTMLDialogElement.prototype.showModal = function () {
    this.open = true;
  };
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
});

it("moves through the matches with the arrows and opens one with Enter", async () => {
  const go = vi.fn();
  const close = vi.fn();
  await act(async () => root.render(<GoTo items={items} go={go} close={close} />));
  const input = node.querySelector<HTMLInputElement>('[role="combobox"]')!;
  const marked = () => node.querySelector('[aria-selected="true"]')?.textContent;
  const key = (key: string) =>
    act(async () => {
      input.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
    });
  expect(marked()).toContain("Inbox");
  await key("ArrowDown");
  await key("ArrowDown");
  expect(marked()).toContain("Fix the login redirect");
  expect(input.getAttribute("aria-activedescendant")).toBe(
    node.querySelector('[aria-selected="true"]')!.id,
  );
  await key("ArrowUp");
  await key("ArrowUp");
  await key("ArrowUp");
  expect(marked()).toContain("Inbox");
  // Typing narrows the list and marks its best match.
  await act(async () => {
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    set.call(input, "inbox");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  expect(node.querySelectorAll('[role="option"]')).toHaveLength(2);
  await key("ArrowDown");
  expect(marked()).toContain("Pull the inbox apart");
  await key("Enter");
  expect(close).toHaveBeenCalled();
  expect(go).toHaveBeenCalledWith("/pr/7");
});
