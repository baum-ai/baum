import { type ReactNode, useEffect, useId, useState } from "react";

import { CLOSED, type ComboboxState, comboboxKey } from "../combobox";
import { cn, FOCUS_RING } from "./classnames";
import { CloseIcon } from "./icons";

export type ComboboxOption = { value: string; label: ReactNode; detail?: ReactNode };

/** A text box that suggests values as the reader types: the ARIA list
 *  autocomplete pattern. Focus stays in the box; the arrows move a highlight
 *  through the list, Enter takes it (or applies the text as typed), and Escape
 *  closes the list, then clears.
 *
 *  `onQuery` reports every edit, so the caller can fetch suggestions for it;
 *  `onChange` reports what was applied. */
export function Combobox({
  label,
  value,
  options,
  onQuery,
  onChange,
  placeholder,
  loading = false,
  empty = "No suggestions",
  className,
}: {
  label: string;
  value: string;
  options: ComboboxOption[];
  onQuery: (text: string) => void;
  onChange: (value: string) => void;
  placeholder?: string;
  loading?: boolean;
  empty?: string;
  className?: string;
}) {
  const id = useId();
  const listId = `${id}-list`;
  const [text, setText] = useState(value);
  const [state, setState] = useState<ComboboxState>(CLOSED);

  // The applied value can change from outside (a file chip, clearing the
  // filters, Back), and the box follows it.
  useEffect(() => setText(value), [value]);

  const optionId = (index: number) => `${id}-option-${index}`;
  const active = state.open && state.active >= 0 ? options[state.active] : undefined;
  const activeId = active ? optionId(state.active) : undefined;

  // The highlight moves with the arrows past the list's own scroll.
  useEffect(() => {
    if (activeId) document.getElementById(activeId)?.scrollIntoView({ block: "nearest" });
  }, [activeId]);

  const apply = (next: string) => {
    setText(next);
    setState(CLOSED);
    if (next !== value) onChange(next);
  };

  return (
    <div className={cn("relative", className)}>
      <label htmlFor={id} className="sr-only">
        {label}
      </label>
      <input
        id={id}
        type="text"
        role="combobox"
        aria-autocomplete="list"
        aria-expanded={state.open}
        aria-controls={listId}
        aria-activedescendant={activeId}
        autoComplete="off"
        spellCheck={false}
        value={text}
        placeholder={placeholder}
        onChange={(event) => {
          setText(event.target.value);
          setState({ open: true, active: -1 });
          onQuery(event.target.value);
        }}
        onFocus={() => onQuery(text)}
        onBlur={() => {
          // Leaving the box applies nothing, so it shows what is applied
          // again rather than text that filters nothing.
          setState(CLOSED);
          setText(value);
        }}
        onKeyDown={(event) => {
          const result = comboboxKey(state, event.key, options.length);
          if (result.handled) event.preventDefault();
          setState(result.state);
          if (result.action === "select" && options[state.active]) {
            apply(options[state.active]!.value);
          } else if (result.action === "commit") {
            apply(text.trim());
          } else if (result.action === "clear") {
            apply("");
            onQuery("");
          }
        }}
        className={cn(
          "w-full rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card) py-1 pr-7 pl-2 text-base text-(--text-primary) placeholder:text-(--text-tertiary)",
          FOCUS_RING,
        )}
      />
      {text ? (
        <button
          type="button"
          aria-label={`Clear ${label.toLowerCase()}`}
          onClick={() => apply("")}
          className={cn(
            "absolute top-1/2 right-1 flex h-5 w-5 -translate-y-1/2 cursor-pointer items-center justify-center rounded-(--radius-xs) text-(--text-tertiary) hover:text-(--text-primary)",
            FOCUS_RING,
          )}
        >
          <CloseIcon size={12} />
        </button>
      ) : null}
      <div
        id={listId}
        role="listbox"
        aria-label={label}
        hidden={!state.open}
        aria-busy={loading || undefined}
        className="absolute left-0 z-20 mt-1 max-h-72 w-full min-w-64 overflow-auto rounded-(--radius-md) border border-(--border-default) bg-(--surface-card) p-1 shadow-(--shadow-overlay)"
      >
        {options.map((option, index) => (
          <div
            key={option.value}
            id={optionId(index)}
            role="option"
            aria-selected={index === state.active}
            // Not focusable in turn: focus stays in the box, which names the
            // highlighted option. Chosen on press, before the box's blur would
            // close the list; the keyboard chooses through the box.
            tabIndex={-1}
            onMouseDown={(event) => {
              event.preventDefault();
              apply(option.value);
            }}
            className={cn(
              "flex cursor-pointer items-baseline justify-between gap-3 rounded-(--radius-sm) px-2 py-1.5 text-base",
              index === state.active
                ? "bg-(--surface-pressed) text-(--text-primary)"
                : "text-(--text-secondary) hover:bg-(--surface-hover)",
            )}
          >
            <span className="min-w-0 truncate">{option.label}</span>
            {option.detail ? (
              <span className="flex-none text-xs text-(--text-tertiary) tabular-nums">
                {option.detail}
              </span>
            ) : null}
          </div>
        ))}
        {options.length === 0 ? (
          <p className="px-2 py-1.5 text-base text-(--text-tertiary)">
            {loading ? "Searching…" : empty}
          </p>
        ) : null}
      </div>
    </div>
  );
}
