import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { MenuButton, useContextMenu, type MenuItem } from "./context-menu";
import { useReadlineArrows } from "./shortcuts";

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
  useReadlineArrows();
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
async function key(key: string, target: Element = document.activeElement!, ctrlKey = false) {
  await act(async () => {
    target.dispatchEvent(
      new KeyboardEvent("keydown", { key, ctrlKey, bubbles: true, cancelable: true }),
    );
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
  const separator = menu()!.querySelector("hr")!;
  expect(separator.nextElementSibling?.textContent).toBe("Remove");
  expect(choices().every((choice) => choice.tabIndex === -1)).toBe(true);
  expect(choices()[0]!.querySelector("kbd")?.getAttribute("aria-hidden")).toBe("true");
});

it("shows a disabled choice with its note, which the keys pass over", async () => {
  const blocked = vi.fn();
  await render([
    ITEMS[0]!,
    { label: "End", danger: true, disabled: true, note: "Still running", onSelect: blocked },
  ]);
  await act(async () => button().click());
  const end = choices()[1] as HTMLButtonElement;
  expect(end.disabled).toBe(true);
  expect(end.querySelector(".context-menu-note")!.textContent).toBe("Still running");
  await key("ArrowDown");
  expect(focused()).toBe("Peek");
  await act(async () => end.click());
  expect(blocked).not.toHaveBeenCalled();
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

it("moves between choices with j/k and Control+n/p without reaching the page", async () => {
  await render();
  await act(async () => button().click());
  const pageKey = vi.fn();
  window.addEventListener("keydown", pageKey);
  try {
    await key("j");
    expect(focused()).toBe("Archive");
    await key("n", document.activeElement!, true);
    expect(focused()).toBe("Count in the badge");
    await key("k");
    expect(focused()).toBe("Archive");
    await key("p", document.activeElement!, true);
    expect(focused()).toBe("Peek");
    expect(pageKey).not.toHaveBeenCalled();
  } finally {
    window.removeEventListener("keydown", pageKey);
  }
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

it("stays open while a box beside its button scrolls, and closes when its button moves", async () => {
  const beside = document.createElement("div");
  document.body.append(beside);
  await render();
  await act(async () => button().click());
  // Another box scrolling, as the session panel's transcript does while its
  // agent writes, leaves it where it is.
  await act(async () => {
    beside.dispatchEvent(new Event("scroll"));
  });
  expect(menu()).not.toBeNull();
  // A box holding its button scrolls it away: the menu closes.
  await act(async () => {
    node.dispatchEvent(new Event("scroll"));
  });
  expect(menu()).toBeNull();
  await act(async () => button().click());
  await act(async () => {
    document.dispatchEvent(new Event("scroll"));
  });
  expect(menu()).toBeNull();
  beside.remove();
});

it("opens a choice's actions over its menu on the actions key or a right click", async () => {
  const end = vi.fn();
  const sessions: MenuItem[] = [
    { label: "New session", onSelect: () => undefined },
    {
      label: "Old session",
      onSelect: () => undefined,
      actions: () => [{ label: "Archive session", danger: true, onSelect: end }],
    },
  ];
  const menus = () => [...document.querySelectorAll<HTMLElement>('[role="menu"]')];
  const labels = (menu: HTMLElement) =>
    [...menu.querySelectorAll(".context-menu-label")].map((label) => label.textContent);
  await render(sessions);
  await act(async () => button().click());
  await key("ArrowDown");
  expect(focused()).toBe("Old session");
  // The menu keeps its keys from the page, so it handles the actions key
  // itself.
  await key(".", document.activeElement!, true);
  // The list stays open under the actions.
  expect(menus().map(labels)).toEqual([["New session", "Old session"], ["Archive session"]]);
  expect(menus()[1]!.getAttribute("aria-label")).toBe("Actions for Old session");
  expect(focused()).toBe("Archive session");
  // Escape closes only the actions, giving the keys back to their choice.
  await key("Escape");
  expect(menus().map(labels)).toEqual([["New session", "Old session"]]);
  expect(focused()).toBe("Old session");

  // A right click on the choice opens them too; a click on the list under
  // them closes only them. The press gives the choice the keys first, as a
  // browser does.
  const right = async () =>
    act(async () => {
      choices()[1]!.focus();
      choices()[1]!.dispatchEvent(
        new MouseEvent("contextmenu", {
          bubbles: true,
          cancelable: true,
          clientX: 50,
          clientY: 60,
        }),
      );
    });
  await right();
  expect(menus()).toHaveLength(2);
  await act(async () => {
    choices()[0]!.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    choices()[0]!.focus();
  });
  expect(menus().map(labels)).toEqual([["New session", "Old session"]]);

  // Picking an action closes both and gives the keys to the list's button.
  await right();
  await act(async () => menus()[1]!.querySelector<HTMLElement>("[role^=menuitem]")!.click());
  expect(end).toHaveBeenCalledOnce();
  expect(menus()).toHaveLength(0);
  expect(document.activeElement).toBe(button());

  // A click outside both closes both.
  await act(async () => button().click());
  await right();
  await act(async () => {
    document.body.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
  });
  expect(menus()).toHaveLength(0);

  // A choice with no actions keeps the menu.
  await act(async () => button().click());
  await key(".", document.activeElement!, true);
  expect(focused()).toBe("New session");
  expect(menus()).toHaveLength(1);
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
