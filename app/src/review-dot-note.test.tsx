import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ReviewDot } from "./review-dot";

let root: Root;
let node: HTMLDivElement;
let shown = false;

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  shown = false;
  // jsdom has no popover API: keep its open state by hand.
  HTMLElement.prototype.showPopover = function () {
    shown = true;
    this.dispatchEvent(Object.assign(new Event("toggle"), { newState: "open" }));
  };
  HTMLElement.prototype.hidePopover = function () {
    shown = false;
    this.dispatchEvent(Object.assign(new Event("toggle"), { newState: "closed" }));
  };
  const matches = Element.prototype.matches;
  vi.spyOn(Element.prototype, "matches").mockImplementation(function (
    this: Element,
    selector: string,
  ) {
    return selector === ":popover-open" ? shown : matches.call(this, selector);
  });
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.restoreAllMocks();
});

it("closes an open note on Escape and keeps that Escape from the page", async () => {
  await act(async () => root.render(<ReviewDot state="failed" detail="interrupted" />));
  const note = node.querySelector<HTMLElement>("[popover]")!;
  await act(async () => note.showPopover());
  expect(shown).toBe(true);
  const page = vi.fn();
  window.addEventListener("keydown", page);
  await act(async () => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", cancelable: true }));
  });
  window.removeEventListener("keydown", page);
  expect(shown).toBe(false);
  expect(page).not.toHaveBeenCalled();
  // Closed, it leaves Escape to the page again.
  window.addEventListener("keydown", page);
  window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
  window.removeEventListener("keydown", page);
  expect(page).toHaveBeenCalledOnce();
});
