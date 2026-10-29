/** The keyboard behaviour of an autocomplete combobox (the ARIA "list
 *  autocomplete" pattern), kept apart from the component so it can be tested
 *  without a DOM.
 *
 *  `active` is the highlighted option, -1 for none: the text box keeps focus
 *  throughout and names the highlighted option through
 *  `aria-activedescendant`. */
export type ComboboxState = { open: boolean; active: number };

export type ComboboxAction =
  /** Nothing to do beyond the new state. */
  | "none"
  /** Choose the highlighted option. */
  | "select"
  /** Apply what is typed, as typed. */
  | "commit"
  /** Empty the box and what it applied. */
  | "clear";

export type ComboboxKeyResult = {
  state: ComboboxState;
  action: ComboboxAction;
  /** Whether the key was the combobox's, so its default is prevented. */
  handled: boolean;
};

export const CLOSED: ComboboxState = { open: false, active: -1 };

export function comboboxKey(state: ComboboxState, key: string, count: number): ComboboxKeyResult {
  switch (key) {
    case "ArrowDown":
      return {
        state: {
          open: true,
          active: count === 0 ? -1 : state.open ? (state.active + 1) % count : 0,
        },
        action: "none",
        handled: true,
      };
    case "ArrowUp":
      return {
        state: {
          open: true,
          active:
            count === 0 ? -1 : !state.open || state.active <= 0 ? count - 1 : state.active - 1,
        },
        action: "none",
        handled: true,
      };
    case "Enter":
      return {
        state: CLOSED,
        action: state.open && state.active >= 0 && state.active < count ? "select" : "commit",
        handled: true,
      };
    case "Escape":
      // The first press closes the list; with it already closed, it clears.
      return state.open
        ? { state: CLOSED, action: "none", handled: true }
        : { state: CLOSED, action: "clear", handled: true };
    case "Tab":
      return { state: CLOSED, action: "none", handled: false };
    default:
      return { state, action: "none", handled: false };
  }
}
