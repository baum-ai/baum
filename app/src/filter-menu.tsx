import { useEffect, useId, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { CLOSED, comboboxKey, type ComboboxState } from "@baum/viewer/combobox";
import { formatCount } from "@baum/viewer/components/format";
import {
  CheckIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  FilterIcon,
} from "@baum/viewer/components/icons";
import { SMALL_PRIMARY } from "./action-classes";
import { useDisclosure } from "@baum/viewer/components/use-disclosure";
import type { Entry } from "./api";
import {
  fieldsFor,
  hasValue,
  OP_LABELS,
  matchesQuery,
  queryProblem,
  suggest,
  tidyQuery,
  toggleValue,
  withText,
  type Op,
  type Option,
} from "./pr-filter";
import { COMMAND, MAC, useShortcuts } from "./shortcuts";
import "./filter-menu.css";

type Field = ReturnType<typeof fieldsFor>[number];
type Row =
  | { kind: "field"; field: Field }
  | { kind: "value"; field: Field; option: Option }
  | { kind: "add"; text: string }
  | { kind: "write" };

/** How far a menu row's values reach beside the menu. */
const SUBMENU_WIDTH = 256;

/** The filter icon and its menu, as Linear draws one: a search box over the
 *  fields, each opening its values beside the menu, and the query itself at
 *  the foot (`footer`, the page's `FilterBar` with its saves). Picking a value
 *  adds it to the query, or takes it out again; the counts are how many of
 *  `entries` (the page before its filter) the query would leave with it. The
 *  icon is lit while `active`, a filter is in force. */
export function FilterButton({
  query,
  onChange,
  entries,
  active: lit = query !== "",
  kindOf,
  inbox = false,
  footer,
  openRequest = false,
  onOpened,
  onClosed,
}: {
  query: string;
  onChange: (next: string) => void;
  entries: Entry[];
  active?: boolean;
  /** What a pull request waits for in an inbox's queue, for `waiting`. */
  kindOf?: (entry: Entry) => string | undefined;
  /** An inbox's page: `waiting` is offered too. */
  inbox?: boolean;
  footer?: ReactNode;
  /** Opens the menu with Add filter focused; `onOpened` clears it. */
  openRequest?: boolean;
  onOpened?: () => void;
  onClosed?: () => void;
}) {
  const { detailsProps, close, open } = useDisclosure();
  const focusQuery = () =>
    panel.current?.querySelector<HTMLTextAreaElement>(".filter-menu-query textarea")?.focus();
  const [focusOnOpen, setFocusOnOpen] = useState(false);
  useShortcuts((event) => {
    if (event.key !== "/") return;
    event.preventDefault();
    if (detailsProps.ref.current) detailsProps.ref.current.open = true;
    detailsProps.onToggle();
    panel.current?.querySelector<HTMLInputElement>(".filter-menu-search")?.focus();
  });
  useEffect(() => {
    if (!openRequest) return;
    if (detailsProps.ref.current) detailsProps.ref.current.open = true;
    setFocusOnOpen(true);
    onOpened?.();
  }, [openRequest]);
  // The panel sits on the window, not in the page, so no scrolling column
  // clips it: under the icon, its right edge on the icon's, kept on screen.
  const [place, setPlace] = useState<{ top: number; left: number } | null>(null);
  useLayoutEffect(() => {
    if (!open) return;
    const measure = () => {
      const icon = detailsProps.ref.current?.getBoundingClientRect();
      const width = panel.current?.offsetWidth ?? 0;
      if (!icon) return;
      setPlace({
        top: icon.bottom + 4,
        left: Math.max(8, Math.min(icon.right - width, window.innerWidth - width - 8)),
      });
    };
    measure();
    window.addEventListener("resize", measure);
    window.addEventListener("scroll", measure, true);
    return () => {
      window.removeEventListener("resize", measure);
      window.removeEventListener("scroll", measure, true);
    };
  }, [open]);
  const wasOpen = useRef(false);
  const positioned = place !== null;
  useLayoutEffect(() => {
    if (open && positioned)
      panel.current?.querySelector<HTMLInputElement>(".filter-menu-search")?.focus();
  }, [open, positioned]);
  useEffect(() => {
    if (open && focusOnOpen) {
      panel.current?.querySelector<HTMLInputElement>(".filter-menu-search")?.focus();
      setFocusOnOpen(false);
    }
    if (!open && wasOpen.current) {
      detailsProps.ref.current?.querySelector("summary")?.blur();
      onClosed?.();
    }
    wasOpen.current = open;
  }, [open, focusOnOpen]);
  const ids = useId();
  const panel = useRef<HTMLDivElement>(null);
  const [search, setSearch] = useState("");
  const [active, setActive] = useState(0);
  // The field whose values show beside the menu, where they hang, and
  // whether the keys move through them.
  const [sub, setSub] = useState<{ field: Field; top: number; side: string } | null>(null);
  const [inSub, setInSub] = useState(-1);
  // The operator the open field's values are picked with.
  const [op, setOp] = useState<Op>("=");
  useEffect(() => {
    if (open) return;
    setSearch("");
    setActive(0);
    setSub(null);
    setInSub(-1);
    setOp("=");
  }, [open]);

  const fields = fieldsFor(inbox);
  const passes = (text: string, entry: Entry) => matchesQuery(text, entry, kindOf?.(entry));
  const words = search.trim().toLowerCase();
  const fits = (option: Option) =>
    option.value.toLowerCase().includes(words) || option.label.toLowerCase().includes(words);
  const rows: Row[] = !words
    ? [...fields.map((field) => ({ kind: "field" as const, field })), { kind: "write" }]
    : [
        ...fields
          .filter(
            (field) => field.label.toLowerCase().includes(words) || field.key.startsWith(words),
          )
          .map((field) => ({ kind: "field" as const, field })),
        ...fields
          .flatMap((field) =>
            field
              .values(entries)
              .filter(fits)
              .map((option) => ({ kind: "value" as const, field, option })),
          )
          .slice(0, 30),
        { kind: "add", text: search.trim() },
      ];
  // The menu offers the plain comparisons; the bar takes `>` and `<` too.
  const ops: Op[] = sub
    ? (sub.field.ops ?? (["=", "!="] as Op[])).filter((one) => one !== ">" && one !== "<")
    : [];
  const values = sub
    ? sub.field.values(entries).filter((one) => !sub.field.check?.(one.value, op))
    : [];
  const live = entries.filter((entry) => passes(query, entry));
  // Count matches belonging to this option, not the union of every selected
  // value of its field. Other fields still narrow the count.
  const count = (field: Field, option: Option, with_: Op = "=") =>
    formatCount(
      hasValue(query, field.key, option.value, with_)
        ? live.filter((entry) => passes(toggleValue("", field.key, option.value, with_), entry))
            .length
        : entries.filter(
            (entry) =>
              passes(toggleValue(query, field.key, option.value, with_), entry) &&
              passes(toggleValue("", field.key, option.value, with_), entry),
          ).length,
    );
  // Added only when the query still reads with it.
  const addable = (text: string) => !queryProblem(withText(query, text), inbox);

  const openSub = (field: Field, row: HTMLElement | null, keys: boolean, hover = false) => {
    const box = panel.current?.getBoundingClientRect();
    const side = !box
      ? "inside"
      : box.right + SUBMENU_WIDTH <= window.innerWidth
        ? "right"
        : box.left >= SUBMENU_WIDTH
          ? "left"
          : "inside";
    // In the list's place the values open only when asked for, not as the
    // pointer passes.
    if (hover && side === "inside") return;
    if (sub?.field !== field) setOp("=");
    setSub({ field, top: side === "inside" ? 0 : (row?.offsetTop ?? 0), side });
    setInSub(keys ? 0 : -1);
  };
  const pick = (row: Row, element: HTMLElement | null, keys: boolean) => {
    if (row.kind === "field") openSub(row.field, element, keys);
    else if (row.kind === "value") onChange(toggleValue(query, row.field.key, row.option.value));
    else if (row.kind === "add") {
      if (!addable(row.text)) return;
      onChange(withText(query, row.text));
      close();
    } else focusQuery();
  };
  const rowElement = (index: number) =>
    panel.current?.querySelector<HTMLElement>(`[data-filter-row="${index}"]`) ?? null;

  const onKeyDown = (event: React.KeyboardEvent) => {
    if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) return;
    const inValues = sub && inSub >= 0;
    const length = inValues ? values.length : rows.length;
    const move = (step: number) => {
      event.preventDefault();
      if (!length) return;
      if (inValues) setInSub((inSub + step + length) % length);
      else {
        setActive((active + step + length) % length);
        setSub(null);
      }
    };
    if (event.key === "ArrowDown") move(1);
    else if (event.key === "ArrowUp") move(-1);
    else if (event.key === "ArrowRight" && !inValues && rows[active]?.kind === "field") {
      event.preventDefault();
      pick(rows[active]!, rowElement(active), true);
    } else if (event.key === "ArrowLeft" && sub) {
      event.preventDefault();
      setSub(null);
      setInSub(-1);
    } else if (event.key === "Tab" && sub && ops.length > 1) {
      // Tab moves the open field's operator along: is, is not, at least…
      event.preventDefault();
      setOp(ops[(ops.indexOf(op) + (event.shiftKey ? ops.length - 1 : 1)) % ops.length]!);
    } else if (event.key === "Escape" && sub) {
      // Steps out of the values first; the next Escape closes the menu.
      event.preventDefault();
      setSub(null);
      setInSub(-1);
    } else if (event.key === "Enter") {
      event.preventDefault();
      const option = inValues ? values[inSub] : undefined;
      if (sub && option) onChange(toggleValue(query, sub.field.key, option.value, op));
      else if (rows[active]) pick(rows[active]!, rowElement(active), true);
    }
  };

  const valueList = sub ? (
    <div
      role="listbox"
      aria-label={sub.field.label}
      aria-multiselectable="true"
      className={`filter-menu-sub ${sub.side}`}
      style={sub.side === "inside" ? undefined : { top: sub.top }}
    >
      {sub.side === "inside" ? (
        <button
          type="button"
          className="filter-menu-row filter-menu-back"
          onClick={() => setSub(null)}
        >
          ← {sub.field.label}
        </button>
      ) : null}
      <div
        role="radiogroup"
        aria-label={`How ${sub.field.label} compares`}
        className="filter-menu-ops"
      >
        {ops.map((one) => (
          <button
            key={one}
            type="button"
            role="radio"
            aria-checked={one === op}
            className={one === op ? "filter-menu-op on" : "filter-menu-op"}
            onMouseDown={(event) => event.preventDefault()}
            onClick={() => setOp(one)}
          >
            {OP_LABELS[one]}
          </button>
        ))}
      </div>
      {values.map((option, index) => {
        const on = hasValue(query, sub.field.key, option.value, op);
        return (
          <div
            key={option.value}
            id={`${ids}-value-${index}`}
            role="option"
            aria-selected={on}
            tabIndex={-1}
            className={index === inSub ? "filter-menu-row active" : "filter-menu-row"}
            onMouseEnter={() => setInSub(index)}
            onMouseDown={(event) => event.preventDefault()}
            onClick={() => onChange(toggleValue(query, sub.field.key, option.value, op))}
          >
            <span className="filter-menu-tick">{on ? <CheckIcon size={13} /> : null}</span>
            <span className="filter-menu-label">{option.label}</span>
            {option.label !== option.value ? (
              <code className="filter-menu-key">{option.value}</code>
            ) : null}
            <span className="filter-menu-count">{count(sub.field, option, op)}</span>
          </div>
        );
      })}
      {values.length === 0 ? <p className="filter-menu-empty">Nothing to pick yet.</p> : null}
    </div>
  ) : null;

  return (
    <details
      {...detailsProps}
      className="filter-menu"
      onKeyDownCapture={(event) => {
        if (
          !open ||
          event.key.toLowerCase() !== "c" ||
          !(event.metaKey || event.ctrlKey) ||
          event.altKey ||
          event.shiftKey ||
          event.nativeEvent.isComposing
        )
          return;
        event.preventDefault();
        event.stopPropagation();
        const clear = panel.current?.querySelector<HTMLButtonElement>(".filter-bar-text-button");
        if (clear) clear.click();
        else onChange("");
        setSearch("");
        setActive(0);
        setSub(null);
        setInSub(-1);
        panel.current?.querySelector<HTMLInputElement>(".filter-menu-search")?.focus();
      }}
      // Tab past its last control (or before its first) leaves the menu,
      // which then closes, as a pointer elsewhere does.
      onBlur={(event) => {
        const next = event.relatedTarget as Node | null;
        if (next && !event.currentTarget.contains(next)) close();
      }}
    >
      <summary
        aria-label={lit ? "Filter, in force" : "Filter"}
        title={lit ? `Filter: ${query} (/)` : "Filter (/)"}
        aria-keyshortcuts="/"
        className={
          lit ? "local-icon-button filter-menu-button on" : "local-icon-button filter-menu-button"
        }
        onClick={(event) => {
          event.preventDefault();
          if (detailsProps.ref.current) detailsProps.ref.current.open = !open;
          detailsProps.onToggle();
        }}
      >
        <FilterIcon size={16} />
        <kbd aria-hidden="true" className="key-hint">
          /
        </kbd>
        {lit ? <span aria-hidden="true" className="filter-menu-dot" /> : null}
      </summary>
      {/* Drawn only while open: a closed menu's controls are no Tab stops. */}
      {open ? (
        <div
          ref={panel}
          className="filter-menu-panel"
          style={place ? { top: place.top, left: place.left } : { visibility: "hidden" }}
        >
          <input
            type="text"
            role="combobox"
            aria-label="Add filter"
            aria-expanded="true"
            aria-controls={`${ids}-rows`}
            aria-activedescendant={
              sub && inSub >= 0 ? `${ids}-value-${inSub}` : `${ids}-row-${active}`
            }
            autoComplete="off"
            spellCheck={false}
            // biome-ignore lint/a11y/noAutofocus: the menu opens to be typed in, as Linear's does
            autoFocus
            placeholder="Add filter…"
            value={search}
            onChange={(event) => {
              setSearch(event.target.value);
              setActive(0);
              setSub(null);
            }}
            onKeyDown={onKeyDown}
            className="filter-menu-search"
          />
          {sub?.side === "inside" ? (
            valueList
          ) : (
            <div
              id={`${ids}-rows`}
              role="listbox"
              aria-label="Filters"
              className="filter-menu-rows"
            >
              {rows.map((row, index) => {
                const key =
                  row.kind === "field"
                    ? `field:${row.field.key}`
                    : row.kind === "value"
                      ? `value:${row.field.key}:${row.option.value}`
                      : row.kind;
                const className = [
                  "filter-menu-row",
                  index === active ? "active" : "",
                  row.kind === "write" ? "filter-menu-write" : "",
                  row.kind === "add" && !addable(row.text) ? "disabled" : "",
                ]
                  .filter(Boolean)
                  .join(" ");
                return (
                  <div
                    key={key}
                    id={`${ids}-row-${index}`}
                    data-filter-row={index}
                    role="option"
                    aria-selected={index === active}
                    aria-haspopup={row.kind === "field" ? "listbox" : undefined}
                    aria-expanded={row.kind === "field" ? sub?.field === row.field : undefined}
                    tabIndex={-1}
                    className={className}
                    onMouseEnter={(event) => {
                      setActive(index);
                      if (row.kind === "field")
                        openSub(row.field, event.currentTarget, false, true);
                      else setSub(null);
                    }}
                    onMouseDown={(event) => event.preventDefault()}
                    onClick={(event) => pick(row, event.currentTarget, false)}
                  >
                    {row.kind === "field" ? (
                      <>
                        <span className="filter-menu-label">{row.field.label}</span>
                        <code className="filter-menu-key">{row.field.key}</code>
                        <ChevronRightIcon size={12} className="filter-menu-chevron" />
                      </>
                    ) : row.kind === "value" ? (
                      <>
                        <span className="filter-menu-tick">
                          {hasValue(query, row.field.key, row.option.value) ? (
                            <CheckIcon size={13} />
                          ) : null}
                        </span>
                        <span className="filter-menu-label">
                          {row.field.label}: {row.option.label}
                        </span>
                        <span className="filter-menu-count">{count(row.field, row.option)}</span>
                      </>
                    ) : row.kind === "add" ? (
                      <span className="filter-menu-label">
                        {row.text.includes(":") ? "Add " : "Titles containing "}
                        <q>{row.text}</q>
                      </span>
                    ) : (
                      <span className="filter-menu-label">Write a query…</span>
                    )}
                  </div>
                );
              })}
            </div>
          )}
          {sub && sub.side !== "inside" ? valueList : null}
          {footer ? <div className="filter-menu-query">{footer}</div> : null}
        </div>
      ) : null}
    </details>
  );
}

/** The query as text, GitHub's way: it suggests fields, their values and
 *  the operators as it is typed, and applies each edit that reads (a moment
 *  after the last key). An edit that does not read says why and leaves the
 *  last filter in force. */
export function FilterBar({
  query,
  onChange,
  entries,
  inbox = false,
  actions,
}: {
  query: string;
  onChange: (next: string) => void;
  /** The page's pull requests before its filter, whose values are offered. */
  entries: Entry[];
  /** An inbox's page: `waiting` is suggested too. */
  inbox?: boolean;
  /** Saving the filter, at the end of the bar. */
  actions?: ReactNode;
}) {
  const id = useId();
  const input = useRef<HTMLTextAreaElement>(null);
  // The box grows with the query, which wraps rather than scrolling sideways.
  useLayoutEffect(() => {
    const box = input.current;
    if (!box) return;
    box.style.height = "auto";
    box.style.height = `${box.scrollHeight}px`;
  });
  const [text, setText] = useState(query);
  const [caret, setCaret] = useState(query.length);
  const [state, setState] = useState<ComboboxState>(CLOSED);
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  // The filter can change from outside (the menu, Clear, another window); the
  // text follows unless it already says the same.
  useEffect(() => {
    if (tidyQuery(text) !== query) setText(query);
  }, [query]);
  // An edit still waiting for its moment goes out when the bar goes.
  const pending = useRef<(() => void) | null>(null);
  useEffect(
    () => () => {
      clearTimeout(timer.current);
      pending.current?.();
    },
    [],
  );

  const error = queryProblem(text, inbox);
  const options = state.open ? suggest(text, caret, entries, inbox).slice(0, 12) : [];
  const apply = (next: string, now = false) => {
    clearTimeout(timer.current);
    pending.current = null;
    const tidy = tidyQuery(next);
    if (tidy === query || queryProblem(tidy, inbox)) return;
    const send = () => {
      pending.current = null;
      onChange(tidy);
    };
    if (now) send();
    else {
      pending.current = send;
      timer.current = setTimeout(send, 250);
    }
  };
  const edit = (next: string, at: number, open = true) => {
    setText(next);
    setCaret(at);
    setState(open ? { open: true, active: -1 } : CLOSED);
    apply(next);
  };
  const take = (index: number) => {
    const option = options[index];
    if (!option) return;
    edit(option.query, option.caret, true);
    requestAnimationFrame(() => input.current?.setSelectionRange(option.caret, option.caret));
  };
  // Tab takes the highlighted suggestion, or the first while a word is being
  // typed (its index here, 0 for none); otherwise it moves on.
  const tabTakes =
    state.active >= 0 && options[state.active]
      ? state.active + 1
      : options.length > 0 && /[^\s()]$/.test(text.slice(0, caret))
        ? 1
        : 0;

  if (!query.trim() && !text.trim())
    return actions ? <div className="filter-bar-actions">{actions}</div> : null;

  return (
    <div className="filter-bar">
      <div role="search" className="filter-bar-field">
        <label htmlFor={id} className="sr-only">
          Filter pull requests
        </label>
        <textarea
          ref={input}
          rows={1}
          id={id}
          role="combobox"
          aria-autocomplete="list"
          aria-expanded={options.length > 0}
          aria-controls={`${id}-list`}
          aria-activedescendant={state.active >= 0 ? `${id}-option-${state.active}` : undefined}
          aria-invalid={error ? true : undefined}
          data-own-tab={tabTakes ? "" : undefined}
          aria-describedby={error ? `${id}-error` : undefined}
          autoComplete="off"
          spellCheck={false}
          value={text}
          onChange={(event) =>
            edit(event.target.value, event.target.selectionStart ?? event.target.value.length)
          }
          onSelect={(event) => {
            const at = event.currentTarget.selectionStart ?? 0;
            if (at === caret) return;
            // The highlight belonged to the suggestions at the old caret.
            setCaret(at);
            setState((now) => ({ ...now, active: -1 }));
          }}
          onFocus={() => setState({ open: true, active: -1 })}
          onBlur={() => {
            setState(CLOSED);
            apply(text, true);
          }}
          onKeyDown={(event) => {
            if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) return;
            if (event.key === "Tab" && !event.shiftKey && tabTakes) {
              event.preventDefault();
              take(tabTakes - 1);
              return;
            }
            const result = comboboxKey(state, event.key, options.length);
            // Escape with the list closed is not the box's: a menu around it closes.
            if (result.handled && result.action !== "clear") event.preventDefault();
            setState(result.state);
            if (result.action === "select") take(state.active);
            else if (result.action === "commit") apply(text, true);
            else if (result.action === "clear") input.current?.blur();
          }}
          className="filter-bar-input"
        />
        <div
          id={`${id}-list`}
          role="listbox"
          aria-label="Suggestions"
          hidden={options.length === 0}
          className="filter-bar-suggestions"
        >
          {options.map((option, index) => (
            <div
              key={`${option.label}:${index}`}
              id={`${id}-option-${index}`}
              role="option"
              aria-selected={index === state.active}
              tabIndex={-1}
              // Taken on press, before the box's blur would close the list.
              onMouseDown={(event) => {
                event.preventDefault();
                take(index);
              }}
              className={index === state.active ? "filter-menu-row active" : "filter-menu-row"}
            >
              <code className="filter-menu-label">{option.label}</code>
              {option.detail ? <span className="filter-menu-count">{option.detail}</span> : null}
            </div>
          ))}
        </div>
      </div>
      <div className="filter-bar-actions">
        <button
          type="button"
          className="filter-bar-text-button"
          aria-keyshortcuts={`${MAC ? "Meta" : "Control"}+c`}
          onClick={() => {
            edit("", 0, false);
            apply("", true);
          }}
        >
          Clear
          <kbd aria-hidden="true" className="key-hint">
            {COMMAND}C
          </kbd>
        </button>
        {actions}
      </div>
      {error && text.trim() ? (
        <p id={`${id}-error`} className="filter-bar-error" role="status">
          Not applied: {error}
        </p>
      ) : null}
    </div>
  );
}

export type SaveOption = { label: string; onSelect: () => void };

/** Saving the filter, as GitHub's merge button: the first option is the
 *  button, and the arrow beside it opens the rest. Nothing shows without an
 *  option, and a single one is a plain button. */
export function SaveButton({ options }: { options: SaveOption[] }) {
  const { detailsProps, close, open } = useDisclosure();
  const [first, ...rest] = options;
  if (!first) return null;
  return (
    <div className="filter-save">
      <button
        type="button"
        className={`${SMALL_PRIMARY} filter-save-main`}
        // Taken on click, after the query's box has given up its last edit.
        onClick={first.onSelect}
      >
        {first.label}
        {first.label === "Create inbox" ? (
          <kbd aria-hidden="true" className="key-hint">
            {COMMAND}↵
          </kbd>
        ) : null}
      </button>
      {rest.length ? (
        <details {...detailsProps} className="filter-save-more">
          <summary aria-label="More ways to save" className={`${SMALL_PRIMARY} filter-save-arrow`}>
            <ChevronDownIcon size={12} />
          </summary>
          {open ? (
            <div role="menu" className="filter-save-menu">
              {rest.map((option) => (
                <button
                  key={option.label}
                  type="button"
                  role="menuitem"
                  className="filter-menu-row"
                  onClick={() => {
                    close();
                    option.onSelect();
                  }}
                >
                  {option.label}
                  {option.label === "Create inbox" ? (
                    <span aria-hidden="true" className="key-hint filter-menu-key">
                      {COMMAND}↵
                    </span>
                  ) : null}
                </button>
              ))}
            </div>
          ) : null}
        </details>
      ) : null}
    </div>
  );
}

/** Why the page's filter could not be saved, with Retry. */
export function FilterSaveError({ error, retry }: { error: string; retry: () => Promise<void> }) {
  return error ? (
    <p role="alert" className="upnext-filter-error">
      {error}{" "}
      <button type="button" onClick={() => void retry()}>
        Retry
      </button>
    </p>
  ) : null;
}
