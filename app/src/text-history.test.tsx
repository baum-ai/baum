import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { GROUP_MS, installTextHistory } from "./text-history";

let field: HTMLTextAreaElement;
let uninstall: () => void;
beforeEach(() => {
  vi.useFakeTimers();
  field = document.createElement("textarea");
  document.body.append(field);
  uninstall = installTextHistory(document, false);
});
afterEach(() => {
  uninstall();
  document.body.replaceChildren();
  vi.useRealTimers();
});

const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;

/** One edit as a browser makes it: beforeinput, the change, input. */
function edit(target: HTMLTextAreaElement, inputType: string, data: string | null) {
  target.dispatchEvent(
    new InputEvent("beforeinput", { bubbles: true, cancelable: true, inputType, data }),
  );
  const { selectionStart: start, selectionEnd: end, value } = target;
  let next = value;
  let caret = start;
  if (inputType === "deleteContentBackward") {
    const from = start === end ? Math.max(0, start - 1) : start;
    next = value.slice(0, from) + value.slice(end);
    caret = from;
  } else {
    next = value.slice(0, start) + (data ?? "") + value.slice(end);
    caret = start + (data ?? "").length;
  }
  setter.call(target, next);
  target.setSelectionRange(caret, caret);
  target.dispatchEvent(new InputEvent("input", { bubbles: true, inputType, data }));
}
const type = (text: string, target = field) => {
  for (const char of text) edit(target, "insertText", char);
};
const press = (key: string, mods: Partial<KeyboardEventInit> = {}, target = field) => {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...mods });
  target.dispatchEvent(event);
  return event;
};
const undo = () => press("z", { ctrlKey: true });
const caret = () => [field.selectionStart, field.selectionEnd];

it("undoes a run of typing in one step, restoring the caret, and redoes it", () => {
  type("hello world");
  expect(undo().defaultPrevented).toBe(true);
  expect(field.value).toBe("");
  expect(caret()).toEqual([0, 0]);
  press("z", { ctrlKey: true, shiftKey: true });
  expect(field.value).toBe("hello world");
  undo();
  press("y", { ctrlKey: true });
  expect(field.value).toBe("hello world");
  expect(caret()).toEqual([11, 11]);
});

it("starts a new step after a pause, a move of the caret, or a switch to deleting", () => {
  type("one");
  vi.advanceTimersByTime(GROUP_MS + 1);
  type(" two");
  edit(field, "deleteContentBackward", null);
  edit(field, "deleteContentBackward", null);
  field.setSelectionRange(0, 0);
  type(">");
  expect(field.value).toBe(">one t");
  undo();
  expect(field.value).toBe("one t");
  undo();
  expect(field.value).toBe("one two");
  undo();
  expect(field.value).toBe("one");
  undo();
  expect(field.value).toBe("");
  // Nothing more to undo.
  undo();
  expect(field.value).toBe("");
});

it("makes a paste or a line break a step of its own", () => {
  type("a");
  edit(field, "insertFromPaste", "PASTED");
  edit(field, "insertLineBreak", "\n");
  type("b");
  undo();
  expect(field.value).toBe("aPASTED\n");
  undo();
  expect(field.value).toBe("aPASTED");
  undo();
  expect(field.value).toBe("a");
});

it("drops the redo steps once something new is typed", () => {
  type("abc");
  undo();
  type("x");
  press("z", { ctrlKey: true, shiftKey: true });
  expect(field.value).toBe("x");
});

it("starts over when the page changes the text", () => {
  type("draft");
  setter.call(field, "reset by the page");
  type("!");
  undo();
  expect(field.value).toBe("reset by the page");
  undo();
  expect(field.value).toBe("reset by the page");
});

it("answers the platform's own undo and redo", () => {
  type("abc");
  const asked = new InputEvent("beforeinput", {
    bubbles: true,
    cancelable: true,
    inputType: "historyUndo",
  });
  field.dispatchEvent(asked);
  expect(asked.defaultPrevented).toBe(true);
  expect(field.value).toBe("");
});

it("uses ⌘ on a Mac, where Ctrl+Z and Ctrl+Y are left alone", () => {
  uninstall();
  uninstall = installTextHistory(document, true);
  type("mac");
  expect(press("z", { ctrlKey: true }).defaultPrevented).toBe(false);
  expect(press("y", { ctrlKey: true }).defaultPrevented).toBe(false);
  expect(field.value).toBe("mac");
  press("z", { metaKey: true });
  expect(field.value).toBe("");
  press("z", { metaKey: true, shiftKey: true });
  expect(field.value).toBe("mac");
});

it("leaves the keys alone while composing, on other fields and by layout", () => {
  type("abc");
  expect(press("z", { ctrlKey: true, isComposing: true }).defaultPrevented).toBe(false);
  expect(field.value).toBe("abc");
  const box = document.createElement("input");
  box.type = "checkbox";
  document.body.append(box);
  expect(press("z", { ctrlKey: true }, box as never).defaultPrevented).toBe(false);
  // A layout without Latin letters: the key in Z's place.
  press("я", { ctrlKey: true, code: "KeyZ" });
  expect(field.value).toBe("");
});

it("keeps a React field's state in step with an undo", async () => {
  vi.useRealTimers();
  const host = document.createElement("div");
  document.body.append(host);
  const root: Root = createRoot(host);
  const seen: string[] = [];
  function Controlled() {
    const [text, setText] = useState("");
    seen.push(text);
    return <textarea value={text} onChange={(e) => setText(e.target.value)} />;
  }
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  await act(async () => root.render(<Controlled />));
  const area = host.querySelector("textarea")!;
  await act(async () => type("typed", area));
  expect(seen.at(-1)).toBe("typed");
  await act(async () => void press("z", { ctrlKey: true }, area));
  expect(area.value).toBe("");
  expect(seen.at(-1)).toBe("");
  await act(async () => root.unmount());
});
