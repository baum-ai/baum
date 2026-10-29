import { useEffect, useId, useMemo, useRef, useState } from "react";

/** A place Go to can open: a page, a pull request, a review group or an
 *  agent session. */
export type GoToItem = {
  id: string;
  kind: "Page" | "Pull request" | "Review group" | "Session";
  label: string;
  /** What tells it apart from others like it: a repository and number. */
  detail?: string;
  /** More words it is found by, not drawn. */
  keywords?: string;
  href: string;
};

/** How many places Go to draws at once; typing finds the rest. */
const SHOWN = 50;

/** The places that match `query`, best first: every word of it must be in
 *  the item's label, detail, kind or keywords, ignoring case and a leading
 *  `#`, so "baum 42" finds pull request 42 of baum. A label that starts with
 *  the query comes first, then one with a word that does, then the rest in
 *  the order given. With no query, the order given. */
export function goToMatches(items: GoToItem[], query: string): GoToItem[] {
  const words = query.toLowerCase().replace(/#/g, " ").split(/\s+/).filter(Boolean);
  if (words.length === 0) return items.slice(0, SHOWN);
  const whole = words.join(" ");
  const ranked: { item: GoToItem; rank: number; index: number }[] = [];
  items.forEach((item, index) => {
    const label = item.label.toLowerCase();
    const text = `${label} ${item.detail ?? ""} ${item.kind} ${item.keywords ?? ""}`.toLowerCase();
    if (!words.every((word) => text.includes(word))) return;
    const rank = label.startsWith(whole)
      ? 0
      : label.split(/[^a-z0-9]+/).some((part) => part.startsWith(words[0]!))
        ? 1
        : 2;
    ranked.push({ item, rank, index });
  });
  return ranked
    .sort((a, b) => a.rank - b.rank || a.index - b.index)
    .slice(0, SHOWN)
    .map(({ item }) => item);
}

/** Go to, from ⌘K or Ctrl+K anywhere: a search over `items` as a small
 *  native dialog. ↑ and ↓ move through the matches, Enter opens the one
 *  marked, Escape closes. */
export function GoTo({
  items,
  go,
  close,
}: {
  items: GoToItem[];
  go: (href: string) => void;
  close: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const list = useRef<HTMLUListElement>(null);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const matches = useMemo(() => goToMatches(items, query), [items, query]);
  const marked = Math.min(active, matches.length - 1);
  const id = useId();
  useEffect(() => {
    dialog.current?.showModal();
  }, []);
  useEffect(() => {
    list.current
      ?.querySelector<HTMLElement>(`[data-index="${marked}"]`)
      ?.scrollIntoView?.({ block: "nearest" });
  }, [marked]);
  const open = (item: GoToItem | undefined) => {
    if (!item) return;
    close();
    go(item.href);
  };
  return (
    <dialog ref={dialog} data-go-to="" aria-label="Go to" onCancel={close} className="go-to">
      <input
        autoFocus
        type="text"
        role="combobox"
        aria-label="Go to"
        aria-expanded="true"
        aria-controls={`${id}-list`}
        aria-activedescendant={matches[marked] ? `${id}-${marked}` : undefined}
        aria-autocomplete="list"
        placeholder="Go to a page, pull request or session…"
        spellCheck={false}
        value={query}
        onChange={(event) => {
          setQuery(event.target.value);
          setActive(0);
        }}
        onKeyDown={(event) => {
          if (event.key === "ArrowDown" || event.key === "ArrowUp") {
            event.preventDefault();
            const step = event.key === "ArrowDown" ? 1 : -1;
            setActive(Math.min(matches.length - 1, Math.max(0, marked + step)));
          } else if (event.key === "Enter") {
            event.preventDefault();
            open(matches[marked]);
          }
        }}
        className="go-to-input"
      />
      {matches.length === 0 ? (
        <p className="go-to-empty">Nothing matches “{query}”.</p>
      ) : (
        <ul ref={list} id={`${id}-list`} role="listbox" aria-label="Places" className="go-to-list">
          {matches.map((item, index) => (
            // The input keeps the focus and names the marked option, so the
            // options take the pointer only.
            <li
              key={item.id}
              id={`${id}-${index}`}
              data-index={index}
              role="option"
              aria-selected={index === marked}
              onMouseMove={() => setActive(index)}
              onClick={() => open(item)}
              className="go-to-option"
            >
              <span className="go-to-label">{item.label}</span>
              {item.detail ? <span className="go-to-detail">{item.detail}</span> : null}
              <span className="go-to-kind">{item.kind}</span>
            </li>
          ))}
        </ul>
      )}
    </dialog>
  );
}
