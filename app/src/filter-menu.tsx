import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { CLOSED, comboboxKey, type ComboboxState } from "@baum/viewer/combobox";
import { formatCount } from "@baum/viewer/components/format";
import { CheckIcon, ChevronRightIcon, FilterIcon } from "@baum/viewer/components/icons";
import { useDisclosure } from "@baum/viewer/components/use-disclosure";
import type { Entry } from "./api";
import {
  FIELDS,
  hasValue,
  matchesQuery,
  parseQuery,
  suggest,
  tidyQuery,
  toggleValue,
  withText,
  type Option,
} from "./pr-filter";
import "./filter-menu.css";

type Field = (typeof FIELDS)[number];
type Row =
  | { kind: "field"; field: Field }
  | { kind: "value"; field: Field; option: Option }
  | { kind: "add"; text: string }
  | { kind: "write" };

/** How far a menu row's values reach beside the menu. */
const SUBMENU_WIDTH = 256;

/** The filter icon and its menu, as Linear draws one: a search box over the
 *  fields, each opening its values beside the menu. Picking a value adds it
 *  to the query, or takes it out again; the counts are how many of `entries`
 *  (the page before its filter) the query would leave with it. */
export function FilterButton({
  query,
  onChange,
  entries,
  onWrite,
}: {
  query: string;
  onChange: (next: string) => void;
  entries: Entry[];
  /** Opens the filter bar to type a query. */
  onWrite: () => void;
}) {
  const { detailsProps, close, open } = useDisclosure();
  const ids = useId();
  const panel = useRef<HTMLDivElement>(null);
  const [search, setSearch] = useState("");
  const [active, setActive] = useState(0);
  // The field whose values show beside the menu, where they hang, and
  // whether the keys move through them.
  const [sub, setSub] = useState<{ field: Field; top: number; side: string } | null>(null);
  const [inSub, setInSub] = useState(-1);
  useEffect(() => {
    if (open) return;
    setSearch("");
    setActive(0);
    setSub(null);
    setInSub(-1);
  }, [open]);

  const words = search.trim().toLowerCase();
  const fits = (option: Option) =>
    option.value.toLowerCase().includes(words) || option.label.toLowerCase().includes(words);
  const rows: Row[] = !words
    ? [...FIELDS.map((field) => ({ kind: "field" as const, field })), { kind: "write" }]
    : [
        ...FIELDS.filter(
          (field) => field.label.toLowerCase().includes(words) || field.key.startsWith(words),
        ).map((field) => ({ kind: "field" as const, field })),
        ...FIELDS.flatMap((field) =>
          field
            .values(entries)
            .filter(fits)
            .map((option) => ({ kind: "value" as const, field, option })),
        ).slice(0, 30),
        { kind: "add", text: search.trim() },
      ];
  const values = sub ? sub.field.values(entries) : [];
  const live = entries.filter((entry) => matchesQuery(query, entry));
  // What the page would show with the value picked: a value already in is
  // counted as it stands, another as one more choice of its field's term.
  const count = (field: Field, option: Option) =>
    formatCount(
      hasValue(query, field.key, option.value)
        ? live.filter((entry) => field.test(option.value, entry)).length
        : entries.filter((entry) =>
            matchesQuery(toggleValue(query, field.key, option.value), entry),
          ).length,
    );
  // Added only when the query still reads with it.
  const addable = (text: string) => !parseQuery(withText(query, text)).error;

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
    } else {
      close();
      onWrite();
    }
  };
  const rowElement = (index: number) =>
    panel.current?.querySelector<HTMLElement>(`[data-filter-row="${index}"]`) ?? null;

  const onKeyDown = (event: React.KeyboardEvent) => {
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
    } else if (event.key === "Escape" && sub) {
      // Steps out of the values first; the next Escape closes the menu.
      event.preventDefault();
      setSub(null);
      setInSub(-1);
    } else if (event.key === "Enter") {
      event.preventDefault();
      const option = inValues ? values[inSub] : undefined;
      if (sub && option) onChange(toggleValue(query, sub.field.key, option.value));
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
      {values.map((option, index) => {
        const on = hasValue(query, sub.field.key, option.value);
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
            onClick={() => onChange(toggleValue(query, sub.field.key, option.value))}
          >
            <span className="filter-menu-tick">{on ? <CheckIcon size={13} /> : null}</span>
            <span className="filter-menu-label">{option.label}</span>
            {option.label !== option.value ? (
              <code className="filter-menu-key">{option.value}</code>
            ) : null}
            <span className="filter-menu-count">{count(sub.field, option)}</span>
          </div>
        );
      })}
      {values.length === 0 ? <p className="filter-menu-empty">Nothing to pick yet.</p> : null}
    </div>
  ) : null;

  return (
    <details {...detailsProps} className="filter-menu">
      <summary
        aria-label={query ? "Filter, in force" : "Filter"}
        title="Filter"
        className={
          query ? "local-icon-button filter-menu-button on" : "local-icon-button filter-menu-button"
        }
      >
        <FilterIcon size={16} />
      </summary>
      {/* Drawn only while open: a closed menu's controls are no Tab stops. */}
      {open ? (
        <div ref={panel} className="filter-menu-panel">
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
                        <code className="filter-menu-key">{row.field.key}:</code>
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
          <p className="filter-menu-help">
            <code>key:a,b</code> either · <code>-key:a</code> not · <code>OR</code> ·{" "}
            <code>( )</code>
          </p>
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
  focus = 0,
  onClose,
  actions,
}: {
  query: string;
  onChange: (next: string) => void;
  /** The page's pull requests before its filter, whose values are offered. */
  entries: Entry[];
  /** Takes the focus each time this grows, as the menu asks to type. */
  focus?: number;
  /** Clear leaves an empty bar; this hides it. */
  onClose?: () => void;
  /** Saving the filter, at the end of the bar. */
  actions?: ReactNode;
}) {
  const id = useId();
  const input = useRef<HTMLInputElement>(null);
  const [text, setText] = useState(query);
  const [caret, setCaret] = useState(query.length);
  const [state, setState] = useState<ComboboxState>(CLOSED);
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  // The filter can change from outside (the menu, Clear, another window); the
  // text follows unless it already says the same.
  useEffect(() => {
    if (tidyQuery(text) !== query) setText(query);
  }, [query]);
  useEffect(() => {
    if (focus) input.current?.focus();
  }, [focus]);
  // An edit still waiting for its moment goes out when the bar goes.
  const pending = useRef<(() => void) | null>(null);
  useEffect(
    () => () => {
      clearTimeout(timer.current);
      pending.current?.();
    },
    [],
  );

  const { error } = parseQuery(text);
  const options = state.open ? suggest(text, caret, entries).slice(0, 12) : [];
  const apply = (next: string, now = false) => {
    clearTimeout(timer.current);
    pending.current = null;
    const tidy = tidyQuery(next);
    if (tidy === query || parseQuery(tidy).error) return;
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
    state.active >= 0
      ? state.active + 1
      : options.length > 0 && /[^\s(]$/.test(text.slice(0, caret))
        ? 1
        : 0;

  return (
    <div className="filter-bar">
      <div role="search" className="filter-bar-field">
        <FilterIcon size={14} className="filter-bar-icon" />
        <label htmlFor={id} className="sr-only">
          Filter pull requests
        </label>
        <input
          ref={input}
          id={id}
          type="text"
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
          placeholder="repo:acme/app -is:draft review:approved OR risk:>=4"
          value={text}
          onChange={(event) =>
            edit(event.target.value, event.target.selectionStart ?? event.target.value.length)
          }
          onSelect={(event) => setCaret(event.currentTarget.selectionStart ?? 0)}
          onFocus={() => setState({ open: true, active: -1 })}
          onBlur={() => {
            setState(CLOSED);
            apply(text, true);
          }}
          onKeyDown={(event) => {
            if (event.key === "Tab" && !event.shiftKey && tabTakes) {
              event.preventDefault();
              take(tabTakes - 1);
              return;
            }
            const result = comboboxKey(state, event.key, options.length);
            if (result.handled) event.preventDefault();
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
          onClick={() => {
            edit("", 0, false);
            apply("", true);
            onClose?.();
          }}
        >
          Clear
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
