import { useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent } from "react";
import { CloseIcon, SortIcon } from "@baum/viewer/components/icons";
import { useDisclosure } from "@baum/viewer/components/use-disclosure";
import {
  describeSort,
  dropLastSortKey,
  parseSort,
  SORT_FIELDS,
  sortWords,
  toggleSortField,
  withoutSortField,
} from "./pr-sort";
import { useShortcuts } from "./shortcuts";
import { useTopLayer } from "./top-layer";
import "./filter-menu.css";

/** ⇧S, which opens and closes the sort from anywhere on the page. */
export const SORT_KEY = "S";

/** The sort icon and its menu, beside the filter's: the fields with a digit
 *  each, which adds the field as the next key or turns it round; Backspace
 *  drops the last key. Without a key the list keeps its own order, which
 *  also settles any tie. The icon is lit while a sort is in force. */
export function SortButton({
  sort,
  onChange,
}: {
  /** The sort as written (`pr-sort.ts`); "" for none. */
  sort: string;
  onChange: (next: string) => void;
}) {
  const { detailsProps, close, open } = useDisclosure();
  const keys = parseSort(sort);
  const lit = keys.length > 0;
  const panel = useRef<HTMLDivElement>(null);
  const ids = useId();
  const [active, setActive] = useState(0);
  const toggleOpen = (next: boolean) => {
    if (detailsProps.ref.current) detailsProps.ref.current.open = next;
    detailsProps.onToggle();
  };
  useShortcuts((event) => {
    if (event.key !== SORT_KEY) return;
    event.preventDefault();
    toggleOpen(!detailsProps.ref.current?.open);
  });
  useTopLayer(panel, open);
  // The panel sits on the window under the icon, its right edge on the
  // icon's, kept on screen, as the filter's does.
  const [place, setPlace] = useState<{ top: number; left: number } | null>(null);
  useLayoutEffect(() => {
    if (!open) {
      setPlace(null);
      return;
    }
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
  useLayoutEffect(() => {
    if (open && place) panel.current?.focus({ preventScroll: true });
  }, [open, place !== null]);
  const wasOpen = useRef(false);
  useEffect(() => {
    if (open) setActive(0);
    else if (wasOpen.current) detailsProps.ref.current?.querySelector("summary")?.blur();
    wasOpen.current = open;
  }, [open]);

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.ctrlKey || event.metaKey || event.altKey || event.nativeEvent.isComposing) return;
    const key = event.key;
    const field = SORT_FIELDS[Number(key) - 1];
    let handled = true;
    if (/^[1-9]$/.test(key)) {
      if (field) {
        onChange(toggleSortField(sort, field.field));
        setActive(Number(key) - 1);
      }
    } else if (key === "Backspace") onChange(dropLastSortKey(sort));
    else if (key === "Delete") onChange(withoutSortField(sort, SORT_FIELDS[active]!.field));
    else if (key === "ArrowDown" || key === "j") setActive((at) => (at + 1) % SORT_FIELDS.length);
    else if (key === "ArrowUp" || key === "k")
      setActive((at) => (at - 1 + SORT_FIELDS.length) % SORT_FIELDS.length);
    else if (key === "Home" || key === "g") setActive(0);
    else if (key === "End" || key === "G") setActive(SORT_FIELDS.length - 1);
    else if (key === "Enter" || key === " ")
      onChange(toggleSortField(sort, SORT_FIELDS[active]!.field));
    else if (key === "Escape" || key === SORT_KEY) {
      close();
      detailsProps.ref.current?.querySelector("summary")?.focus();
    } else handled = false;
    // The page's own keys (j, k, digits, Escape) stay out while the menu
    // has them.
    if (handled) {
      event.preventDefault();
      event.stopPropagation();
    }
  };

  const title = lit ? `Sort: ${describeSort(sort)} (⇧S)` : "Sort (⇧S)";
  return (
    <details
      {...detailsProps}
      className="sort-menu"
      // Tab out of the menu closes it, as a pointer elsewhere does.
      onBlur={(event) => {
        const next = event.relatedTarget as Node | null;
        if (next && !event.currentTarget.contains(next)) close();
      }}
    >
      <summary
        aria-label={lit ? `Sort, ${describeSort(sort)}` : "Sort"}
        title={title}
        aria-keyshortcuts="Shift+S"
        className={
          lit ? "local-icon-button sort-menu-button on" : "local-icon-button sort-menu-button"
        }
        onClick={(event) => {
          event.preventDefault();
          toggleOpen(!open);
        }}
      >
        <SortIcon size={16} />
        <kbd aria-hidden="true" className="key-hint">
          ⇧S
        </kbd>
        {lit ? <span aria-hidden="true" className="filter-menu-dot" /> : null}
      </summary>
      {open ? (
        <div
          ref={panel}
          role="menu"
          aria-label="Sort by"
          aria-activedescendant={`${ids}-${active}`}
          tabIndex={-1}
          className="sort-menu-panel"
          style={place ? { top: place.top, left: place.left } : { visibility: "hidden" }}
          onKeyDown={onKeyDown}
        >
          <div className="sort-menu-head">
            <span className="sort-menu-title">Sort by</span>
            {lit ? (
              <span className="sort-menu-current">
                {keys.length} of {SORT_FIELDS.length}
              </span>
            ) : null}
          </div>
          <div className="filter-menu-rows sort-menu-rows">
            {SORT_FIELDS.map((info, index) => {
              const at = keys.findIndex((key) => key.field === info.field);
              const key = keys[at];
              return (
                <div
                  key={info.field}
                  id={`${ids}-${index}`}
                  role="menuitemcheckbox"
                  aria-checked={at >= 0}
                  aria-keyshortcuts={String(index + 1)}
                  tabIndex={-1}
                  className={index === active ? "filter-menu-row active" : "filter-menu-row"}
                  onMouseEnter={() => setActive(index)}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => onChange(toggleSortField(sort, info.field))}
                >
                  <span className="sort-menu-digit">{index + 1}</span>
                  <span className="filter-menu-label">{info.label}</span>
                  {key ? (
                    <>
                      <span className="sort-menu-direction">{sortWords(key)}</span>
                      <span className="sort-menu-place" title={`Sort key ${at + 1}`}>
                        {at + 1}
                      </span>
                      <button
                        type="button"
                        tabIndex={-1}
                        aria-label={`Stop sorting by ${info.label}`}
                        className="sort-menu-remove"
                        onMouseDown={(event) => event.preventDefault()}
                        onClick={(event) => {
                          event.stopPropagation();
                          onChange(withoutSortField(sort, info.field));
                        }}
                      >
                        <CloseIcon size={12} />
                      </button>
                    </>
                  ) : null}
                </div>
              );
            })}
          </div>
          <div className="sort-menu-foot" aria-hidden="true">
            <span>
              <span className="sort-menu-key">1–{SORT_FIELDS.length}</span> add or turn round
            </span>
            <span>
              <span className="sort-menu-key">⌫</span> drop last
            </span>
          </div>
        </div>
      ) : null}
    </details>
  );
}
