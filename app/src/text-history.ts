/** Undo and redo for every text field, kept by the app rather than the
 *  browser. The desktop app's web view (WebKitGTK in a Tauri window) never
 *  undoes on Ctrl+Z, in any field, so the app keeps each field's history and
 *  answers the keys itself everywhere, browsers included, so a field behaves
 *  the same wherever the app runs:
 *
 *  - ⌘Z / Ctrl+Z undoes, ⇧⌘Z / Ctrl+Shift+Z and Ctrl+Y (not on a Mac) redo,
 *    as does an undo or redo the platform asks for (`historyUndo`).
 *  - Typing is one step until a pause (`GROUP_MS`), a move of the caret, or a
 *    switch between typing and deleting; a paste, a cut, a line break or any
 *    other edit is a step of its own.
 *  - A step restores the text and the selection it had, then announces the
 *    change as typing does (an `input` event), so a React field's `onChange`
 *    follows it.
 *  - A field whose text the page changed (a reset, a sent message) starts its
 *    history again, as a browser's does. */

type Field = HTMLInputElement | HTMLTextAreaElement;
type Snapshot = { value: string; start: number; end: number };
type History = {
  undo: Snapshot[];
  redo: Snapshot[];
  /** The text and selection after the last change this history knows. */
  last: Snapshot;
  /** The field as it was just before the change under way. */
  before: Snapshot | null;
  /** The kind of the last step's edits, and when and where it ended. */
  kind: string;
  at: number;
};

/** Typing within this long of the last keystroke joins its step. */
export const GROUP_MS = 1000;
/** The steps kept per field. */
const LIMIT = 200;

/** The input types a text field takes: not checkboxes, dates or ranges. */
const TEXT_TYPES = new Set(["text", "search", "url", "tel", "email", "password", ""]);

function textField(target: EventTarget | null): Field | null {
  if (target instanceof HTMLTextAreaElement) return target;
  if (target instanceof HTMLInputElement && TEXT_TYPES.has(target.type)) return target;
  return null;
}

function snapshot(field: Field): Snapshot {
  const end = field.value.length;
  return { value: field.value, start: field.selectionStart ?? end, end: field.selectionEnd ?? end };
}

/** What an edit is, for grouping: consecutive typing or deleting joins. */
function kindOf(inputType: string, data: string | null) {
  if (inputType === "insertText" || inputType === "insertCompositionText")
    return data === "\n" ? "" : "type";
  if (inputType.startsWith("delete") && !inputType.includes("ByCut")) return "delete";
  // Paste, cut, drop, line breaks and anything else: a step each.
  return "";
}

/** Sets a field's text as a person's edit would arrive, so React's
 *  `onChange` sees it: through the native setter, then an `input` event. */
function write(field: Field, next: Snapshot, direction: "undo" | "redo") {
  const proto = field instanceof HTMLTextAreaElement ? HTMLTextAreaElement : HTMLInputElement;
  Object.getOwnPropertyDescriptor(proto.prototype, "value")!.set!.call(field, next.value);
  field.setSelectionRange(next.start, next.end);
  const inputType = direction === "undo" ? "historyUndo" : "historyRedo";
  field.dispatchEvent(new InputEvent("input", { bubbles: true, inputType }));
}

/** Keeps the history of every text field under `root`; returns what removes
 *  it again. Installed once, at the root of each page. */
export function installTextHistory(root: Document | HTMLElement = document, mac = isMac()) {
  const histories = new WeakMap<Field, History>();
  // The `input` event a step sends, which must not be recorded as an edit.
  let stepping = false;

  const historyOf = (field: Field) => {
    let history = histories.get(field);
    const now = snapshot(field);
    // The page changed the text since the last change this history knows:
    // its steps no longer lead anywhere sensible.
    if (!history || history.last.value !== now.value) {
      history = { undo: [], redo: [], last: now, before: null, kind: "", at: 0 };
      histories.set(field, history);
    }
    return history;
  };

  const step = (field: Field, direction: "undo" | "redo") => {
    const history = historyOf(field);
    const [from, to] =
      direction === "undo" ? [history.undo, history.redo] : [history.redo, history.undo];
    const next = from.pop();
    if (!next) return;
    to.push(snapshot(field));
    history.last = next;
    history.kind = "";
    stepping = true;
    try {
      write(field, next, direction);
    } finally {
      stepping = false;
    }
  };

  const onBeforeInput = (event: Event) => {
    const field = textField(event.target);
    if (!field) return;
    const { inputType } = event as InputEvent;
    if (inputType === "historyUndo" || inputType === "historyRedo") {
      event.preventDefault();
      step(field, inputType === "historyUndo" ? "undo" : "redo");
      return;
    }
    historyOf(field).before = snapshot(field);
  };

  const onInput = (event: Event) => {
    const field = textField(event.target);
    if (!field || stepping) return;
    const history = histories.get(field);
    if (!history) return;
    const { inputType = "", data = null } = event as InputEvent;
    const before = history.before ?? history.last;
    history.before = null;
    const after = snapshot(field);
    if (after.value === before.value) {
      history.last = after;
      return;
    }
    const kind = kindOf(inputType, data);
    const at = Date.now();
    const joins =
      kind !== "" &&
      kind === history.kind &&
      at - history.at <= GROUP_MS &&
      before.start === history.last.start &&
      before.end === history.last.end;
    if (!joins) {
      history.undo.push(before);
      if (history.undo.length > LIMIT) history.undo.shift();
    }
    history.redo = [];
    history.kind = kind;
    history.at = at;
    history.last = after;
  };

  const onKeyDown = (event: Event) => {
    const key = event as KeyboardEvent;
    const field = textField(key.target);
    if (!field || key.isComposing || key.altKey || field.readOnly || field.disabled) return;
    const command = mac ? key.metaKey && !key.ctrlKey : key.ctrlKey && !key.metaKey;
    if (!command) return;
    // By the letter, or by its place on a layout without Latin letters.
    const letter = /^[a-z]$/i.test(key.key)
      ? key.key.toLowerCase()
      : /^Key([A-Z])$/.exec(key.code)?.[1]?.toLowerCase();
    const direction =
      letter === "z"
        ? key.shiftKey
          ? "redo"
          : "undo"
        : letter === "y" && !mac && !key.shiftKey
          ? "redo"
          : null;
    if (!direction) return;
    // The browser's own undo would act on a history this one replaced.
    key.preventDefault();
    step(field, direction);
  };

  // Captured, so a field's history is up to date before its own handlers run.
  root.addEventListener("beforeinput", onBeforeInput, true);
  root.addEventListener("input", onInput, true);
  root.addEventListener("keydown", onKeyDown, true);
  return () => {
    root.removeEventListener("beforeinput", onBeforeInput, true);
    root.removeEventListener("input", onInput, true);
    root.removeEventListener("keydown", onKeyDown, true);
  };
}

function isMac() {
  return /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
}
