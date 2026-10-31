import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { MenuButton, useContextMenu, type MenuItem } from "./context-menu";

let node: HTMLDivElement;
let root: Root;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
});

const archive = vi.fn();
const remove = vi.fn();
const badge = vi.fn();
function Row({ items }: { items: MenuItem[] }) {
  const menu = useContextMenu();
  return (
    <>
      <ul>
        <li onContextMenu={(event) => menu.open(event, items, "acme/demo #7")}>
          <a href="#7">Change 7</a>
          <MenuButton label="acme/demo #7" items={() => items} menu={menu} />
        </li>
      </ul>
      {menu.menu}
    </>
  );
}
const ITEMS: MenuItem[] = [
  { label: "Peek", hint: "Space", onSelect: () => undefined },
  { label: "Archive", onSelect: archive },
  { label: "Count in the badge", checked: true, onSelect: badge },
  { label: "Remove", danger: true, onSelect: remove },
];
async function render(items = ITEMS) {
  await act(async () => root.render(<Row items={items} />));
}
const button = () => node.querySelector<HTMLButtonElement>(".row-menu-button")!;
const menu = () => document.querySelector<HTMLElement>('[role="menu"]');
const choices = () => [...(menu()?.querySelectorAll<HTMLElement>("[role^=menuitem]") ?? [])];
const focused = () => document.activeElement?.querySelector(".context-menu-label")?.textContent;
async function key(key: string, target: Element = document.activeElement!) {
  await act(async () => {
    target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
  });
}

it("names the row's ⋯ and says it opens a menu, and whether it is open", async () => {
  await render();
  expect(button().getAttribute("aria-label")).toBe("Actions for acme/demo #7");
  expect(button().getAttribute("aria-haspopup")).toBe("menu");
  expect(button().getAttribute("aria-expanded")).toBe("false");
  await act(async () => button().click());
  expect(button().getAttribute("aria-expanded")).toBe("true");
  expect(button().getAttribute("aria-controls")).toBe(menu()!.id);
  expect(menu()!.getAttribute("aria-label")).toBe("Actions for acme/demo #7");
  expect(focused()).toBe("Peek");
});

it("draws on-or-off choices as checkboxes and sets apart what removes", async () => {
  await render();
  await act(async () => button().click());
  expect(choices().map((choice) => choice.getAttribute("role"))).toEqual([
    "menuitem",
    "menuitem",
    "menuitemcheckbox",
    "menuitem",
  ]);
  expect(choices()[2]!.getAttribute("aria-checked")).toBe("true");
  // Every choice keeps the tick's column, so the labels line up.
  expect(choices().every((choice) => choice.querySelector(".context-menu-check"))).toBe(true);
  // The separator stands right before Remove, and is not a choice.
  const separator = menu()!.querySelector('[role="separator"]')!;
  expect(separator.nextElementSibling?.textContent).toBe("Remove");
  expect(choices().every((choice) => choice.tabIndex === -1)).toBe(true);
  expect(choices()[0]!.querySelector("kbd")?.getAttribute("aria-hidden")).toBe("true");
});

it("moves with the arrows, Home, End and a letter, and wraps", async () => {
  await render();
  await act(async () => button().click());
  await key("ArrowUp");
  expect(focused()).toBe("Remove");
  await key("ArrowDown");
  expect(focused()).toBe("Peek");
  await key("End");
  expect(focused()).toBe("Remove");
  await key("Home");
  expect(focused()).toBe("Peek");
  await key("c");
  expect(focused()).toBe("Count in the badge");
  await key("a");
  expect(focused()).toBe("Archive");
});

it("opens on the last choice with Up, and gives the keys back on Escape", async () => {
  await render();
  button().focus();
  await key("ArrowUp", button());
  expect(focused()).toBe("Remove");
  await key("Escape");
  expect(menu()).toBeNull();
  expect(document.activeElement).toBe(button());
  expect(button().getAttribute("aria-expanded")).toBe("false");
});

it("closes on Tab, back on its button", async () => {
  await render();
  await act(async () => button().click());
  await key("Tab");
  expect(menu()).toBeNull();
  expect(document.activeElement).toBe(button());
});

it("runs a choice, closed, with the keys back on the button", async () => {
  await render();
  await act(async () => button().click());
  await act(async () => choices()[1]!.click());
  expect(archive).toHaveBeenCalledOnce();
  expect(menu()).toBeNull();
  expect(document.activeElement).toBe(button());
});

it("closes when its own button is pressed again", async () => {
  await render();
  await act(async () => button().click());
  await act(async () => {
    button().dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    button().click();
  });
  expect(menu()).toBeNull();
  await act(async () => button().click());
  expect(menu()).not.toBeNull();
});

it("opens the same menu on a right click on the row", async () => {
  await render();
  await act(async () => {
    node.querySelector("a")!.dispatchEvent(
      new MouseEvent("contextmenu", {
        bubbles: true,
        cancelable: true,
        clientX: 40,
        clientY: 20,
      }),
    );
  });
  expect(menu()!.getAttribute("aria-label")).toBe("Actions for acme/demo #7");
  expect(
    choices().map((choice) => choice.querySelector(".context-menu-label")?.textContent),
  ).toEqual(["Peek", "Archive", "Count in the badge", "Remove"]);
});
