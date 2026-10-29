import { describe, expect, it } from "vitest";

import { CLOSED, comboboxKey } from "./combobox";

describe("combobox keys", () => {
  it("opens on the first option going down and the last going up", () => {
    expect(comboboxKey(CLOSED, "ArrowDown", 3)).toEqual({
      state: { open: true, active: 0 },
      action: "none",
      handled: true,
    });
    expect(comboboxKey(CLOSED, "ArrowUp", 3).state).toEqual({ open: true, active: 2 });
  });

  it("moves through the options and wraps at either end", () => {
    expect(comboboxKey({ open: true, active: 0 }, "ArrowDown", 3).state.active).toBe(1);
    expect(comboboxKey({ open: true, active: 2 }, "ArrowDown", 3).state.active).toBe(0);
    expect(comboboxKey({ open: true, active: 0 }, "ArrowUp", 3).state.active).toBe(2);
    expect(comboboxKey({ open: true, active: -1 }, "ArrowUp", 3).state.active).toBe(2);
    expect(comboboxKey({ open: true, active: 2 }, "ArrowUp", 3).state.active).toBe(1);
  });

  it("highlights nothing when there are no options", () => {
    expect(comboboxKey(CLOSED, "ArrowDown", 0).state).toEqual({ open: true, active: -1 });
    expect(comboboxKey({ open: true, active: -1 }, "ArrowUp", 0).state.active).toBe(-1);
  });

  it("selects the highlighted option on Enter, else applies the text", () => {
    expect(comboboxKey({ open: true, active: 1 }, "Enter", 3)).toEqual({
      state: CLOSED,
      action: "select",
      handled: true,
    });
    expect(comboboxKey({ open: true, active: -1 }, "Enter", 3).action).toBe("commit");
    expect(comboboxKey(CLOSED, "Enter", 3).action).toBe("commit");
    // An option list that shrank under the highlight applies the text instead.
    expect(comboboxKey({ open: true, active: 4 }, "Enter", 2).action).toBe("commit");
  });

  it("closes on the first Escape and clears on the second", () => {
    expect(comboboxKey({ open: true, active: 1 }, "Escape", 3)).toEqual({
      state: CLOSED,
      action: "none",
      handled: true,
    });
    expect(comboboxKey(CLOSED, "Escape", 3).action).toBe("clear");
  });

  it("closes on Tab without keeping focus, and ignores other keys", () => {
    expect(comboboxKey({ open: true, active: 1 }, "Tab", 3)).toEqual({
      state: CLOSED,
      action: "none",
      handled: false,
    });
    const open = { open: true, active: 1 };
    expect(comboboxKey(open, "a", 3)).toEqual({ state: open, action: "none", handled: false });
  });
});
