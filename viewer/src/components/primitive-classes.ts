import { cn, FOCUS_RING } from "./classnames";

/** The shared primitives' class recipes, for an element that cannot be the
 *  component itself (a native select, a dialog opened with `showModal()`, the
 *  app's own menu). The components are in `field.tsx`, `dialog-shell.tsx`,
 *  `list-row.tsx`, `menu.tsx` and `section-label.tsx`. */

// ---- Field ----

export type FieldKind = "input" | "select";

/** A text input or select: a card-coloured box with the default border, which
 *  firms on hover, and the shared focus ring. Its font is the surrounding
 *  text's at the body size, so a field inside a `Field` label keeps the
 *  label's weight. Width belongs to the caller. */
const FIELD_BASE =
  "max-w-full rounded-md border border-default bg-surface-card px-[0.65rem] py-[0.4rem] text-base text-primary hover:border-strong";

/** A select draws its own chevron, two tertiary strokes at the right, in
 *  place of the platform's arrow, which differs between systems and ignores
 *  the palette. */
const SELECT =
  "cursor-pointer appearance-none bg-no-repeat pr-8 bg-[linear-gradient(45deg,transparent_50%,var(--text-tertiary)_50%),linear-gradient(135deg,var(--text-tertiary)_50%,transparent_50%)] bg-position-[calc(100%-15px)_55%,calc(100%-10px)_55%] bg-size-[5px_5px]";

export function fieldClassName({ kind = "input" }: { kind?: FieldKind } = {}) {
  return cn(FIELD_BASE, FOCUS_RING, kind === "select" && SELECT);
}

export const FIELD_LABEL = "flex flex-col gap-[0.35rem] text-xs font-medium text-secondary";

// ---- Dialog ----

export type DialogSize = "standard" | "wide";

const dialogSizes: Record<DialogSize, string> = {
  standard: "",
  // A dialog holding a report or a file: wider, and it scrolls inside the
  // window rather than running past it.
  wide: "w-[48rem] max-h-[calc(100vh-4rem)] overflow-y-auto",
};

/** A native `<dialog>` opened with `showModal()`: a raised popup surface,
 *  36rem wide unless the window is narrower, over the scrim. */
export function dialogClassName({ size = "standard" }: { size?: DialogSize } = {}) {
  return cn(
    "m-auto w-[36rem] max-w-[calc(100vw-2rem)] rounded-lg border border-default bg-surface-popup p-6 text-primary shadow-overlay backdrop:bg-surface-scrim",
    dialogSizes[size],
  );
}

// ---- List row ----

/** A row of a full-width list (pull requests, sessions, repositories): ruled
 *  below except the last, washed while pointed at. The row the keys are on
 *  (`data-focused`) is sunken with a bar at its start; that bar marks it, so a
 *  link inside it draws no focus ring of its own. Layout inside the row is the
 *  caller's. */
export const LIST_ROW = cn(
  "min-w-0 border-b border-subtle px-6 py-[0.8rem] last:border-b-0 hover:bg-surface-hover",
  "data-focused:bg-surface-sunken data-focused:shadow-[inset_3px_0_0_var(--text-primary)] data-focused:hover:bg-surface-hover",
  "data-focused:[&_:is(a,[data-row-link]):focus-visible]:shadow-none data-focused:[&_:is(a,[data-row-link]):focus-visible]:outline-none",
);

// ---- Menu ----

/** A menu's surface: warm popup paper with the overlay shadow, its items
 *  stacked two pixels apart, dropping into place as it opens. Position, width
 *  and stacking belong to the caller. */
export const MENU =
  "flex flex-col gap-0.5 rounded-lg border border-default bg-surface-popup p-1.5 shadow-overlay animate-drop-in motion-reduce:animate-none";

export type MenuItemTone = "default" | "danger";

/** A menu item: the pointer, the keys and a press all wash it; one that
 *  stops or removes something is in the critical tone; one that cannot be
 *  chosen now is greyed. */
export function menuItemClassName({ tone = "default" }: { tone?: MenuItemTone } = {}) {
  return cn(
    "flex cursor-pointer items-center gap-2 rounded-sm px-2.5 py-[7px] text-left text-base text-primary",
    "focus-visible:outline-none enabled:hover:bg-surface-hover enabled:focus-visible:bg-surface-hover enabled:active:bg-surface-pressed",
    tone === "danger" && "text-status-critical",
    "disabled:cursor-default disabled:text-disabled",
  );
}

/** What stops or removes something sits apart, below a hairline. */
export const MENU_SEPARATOR = "mx-0.5 my-1 h-px border-0 bg-(--border-subtle)";

/** The parts of an item: its label, which takes the room; a note under the
 *  label saying why it cannot be chosen; a tick's column; its key. */
export const MENU_ITEM_LABEL = "flex-1";
export const MENU_ITEM_NOTE = "mt-0.5 block text-xs text-tertiary";
export const MENU_ITEM_CHECK = "inline-flex w-3.5 flex-none text-primary";
export const MENU_ITEM_KEY = "ml-4 font-mono text-xs text-disabled";

// ---- Section label ----

/** A section's label: small, medium-weight, tracked capitals in the tertiary
 *  text colour ("RUNNING", "CHECKS · 4"). It carries no margin; the caller
 *  spaces it. */
export const SECTION_LABEL = "text-xs font-medium tracking-[0.08em] text-tertiary uppercase";
