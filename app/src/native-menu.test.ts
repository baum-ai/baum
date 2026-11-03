import { afterEach, beforeEach, expect, it } from "vitest";
import { blockBrowserMenu } from "./native-menu";

let stop: () => void;
beforeEach(() => {
  stop = blockBrowserMenu();
});
afterEach(() => {
  stop();
  document.body.replaceChildren();
  window.getSelection()?.removeAllRanges();
});

function rightClick(element: Element) {
  const event = new MouseEvent("contextmenu", { bubbles: true, cancelable: true });
  element.dispatchEvent(event);
  return event.defaultPrevented;
}

it("turns off the browser's menu where the app offers none", () => {
  document.body.innerHTML = `<main><p>Text</p><a href="#x">Link</a></main>`;
  expect(rightClick(document.querySelector("p")!)).toBe(true);
  expect(rightClick(document.querySelector("a")!)).toBe(true);
});

it("keeps the browser's menu in a field", () => {
  document.body.innerHTML = `<input /><textarea></textarea><div contenteditable><span>x</span></div>`;
  expect(rightClick(document.querySelector("input")!)).toBe(false);
  expect(rightClick(document.querySelector("textarea")!)).toBe(false);
  expect(rightClick(document.querySelector("span")!)).toBe(false);
});

it("keeps the browser's menu over selected text", () => {
  document.body.innerHTML = `<p>Selected words</p>`;
  const paragraph = document.querySelector("p")!;
  window.getSelection()!.selectAllChildren(paragraph);
  expect(rightClick(paragraph)).toBe(false);
});
