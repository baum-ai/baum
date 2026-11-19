import { type ReactNode, useId, useLayoutEffect, useRef, useState } from "react";

import { cn, FOCUS_RING } from "./classnames";
import { MoreIcon } from "./icons";
import { SEGMENT_BASE } from "./segmented";
import { tabIds } from "./tabs";
import { SegmentedControl } from "./ui";

type PaneTab<T extends string> = {
  value: T;
  label: string;
  icon?: ReactNode;
  tone?: "critical" | "caution";
};

const DOT = { critical: "bg-status-critical", caution: "bg-status-caution" } as const;

/** The space between two tabs of the strip (`gap-0.5`). */
const GAP = 2;

/** Tabs that keep to one line in a narrow pane: as many as fit, in order, and
 *  the rest under a ⋯ at the strip's end. The tab on show always stands
 *  in the strip, in the last place that fits when it would be under More. A
 *  tab's width is measured when it is drawn, so the strip is drawn whole
 *  until each has been. */
export function PaneTabs<T extends string>({
  label,
  tablist,
  value,
  items,
  onChange,
  className,
}: {
  label: string;
  tablist: string;
  value: T;
  items: PaneTab<T>[];
  onChange: (value: T) => void;
  className?: string;
}) {
  const strip = useRef<HTMLDivElement>(null);
  const widths = useRef(new Map<string, number>());
  const moreWidth = useRef(0);
  const [room, setRoom] = useState<number | null>(null);
  const [, setMeasured] = useState(0);
  // Each drawn tab's width, by its value, as the strip draws it; nothing is
  // read while the strip is hidden (a folded pane, a closed sheet). True when
  // a width changed.
  const measure = useRef<() => boolean>(() => false);
  measure.current = () => {
    const element = strip.current;
    if (!element || element.clientWidth === 0) return false;
    let changed = false;
    const note = (key: string, size: number) => {
      if (size === 0 || widths.current.get(key) === size) return;
      widths.current.set(key, size);
      changed = true;
    };
    for (const item of items) {
      const tab = element.ownerDocument.getElementById(tabIds(tablist, item.value).tab);
      if (tab && element.contains(tab)) note(item.value, tab.offsetWidth);
    }
    const more = element.querySelector<HTMLElement>("[data-pane-tabs-more]");
    if (more && more.offsetWidth !== moreWidth.current && more.offsetWidth > 0) {
      moreWidth.current = more.offsetWidth;
      changed = true;
    }
    return changed;
  };
  useLayoutEffect(() => {
    const element = strip.current;
    if (!element) return;
    const resized = new ResizeObserver(() => {
      if (measure.current()) setMeasured((count) => count + 1);
      setRoom(element.clientWidth || null);
    });
    resized.observe(element);
    return () => resized.disconnect();
  }, []);
  // Again whenever what the strip draws may have changed.
  useLayoutEffect(() => {
    if (measure.current()) setMeasured((count) => count + 1);
  }, [items, value, room]);
  const shown = fitting(items, value, room, widths.current, moreWidth.current);
  const rest = items.filter((item) => !shown.includes(item));
  return (
    <div ref={strip} className={cn("flex min-w-0 items-end overflow-hidden", className)}>
      <SegmentedControl
        label={label}
        variant="tabs"
        tablist={tablist}
        value={value}
        onChange={onChange}
        items={shown}
        className="flex-none"
      />
      <span className="box-content flex h-9 min-w-0 flex-1 items-center border-b border-subtle">
        {rest.length > 0 ? <MoreTabs items={rest} onChange={onChange} /> : null}
      </span>
    </div>
  );
}

/** The leading tabs that fit in `room` (all of them until every width is
 *  known), with the one on show swapped in for the last when it would not. */
function fitting<T extends string>(
  items: PaneTab<T>[],
  value: T,
  room: number | null,
  widths: Map<string, number>,
  more: number,
): PaneTab<T>[] {
  const sizes = items.map((item) => widths.get(item.value));
  if (room === null || sizes.some((size) => size === undefined)) return items;
  const width = (from: (number | undefined)[]) =>
    from.reduce<number>((total, size) => total + (size ?? 0), 0) + GAP * (from.length - 1);
  if (width(sizes) <= room) return items;
  const left = room - (more || 36);
  let count = 0;
  while (count < items.length && width(sizes.slice(0, count + 1)) <= left) count += 1;
  const shown = items.slice(0, Math.max(1, count));
  const current = items.find((item) => item.value === value);
  if (current && !shown.includes(current)) shown[shown.length - 1] = current;
  return shown;
}

/** The tabs that did not fit, under a button that reads as one of them. */
function MoreTabs<T extends string>({
  items,
  onChange,
}: {
  items: PaneTab<T>[];
  onChange: (value: T) => void;
}) {
  const id = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const popup = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const tone = items.find((item) => item.tone)?.tone;
  const names = items.map((item) => item.label).join(", ");
  return (
    <>
      <button
        ref={trigger}
        type="button"
        data-pane-tabs-more=""
        aria-expanded={open}
        aria-controls={id}
        popoverTarget={id}
        aria-label={`More tabs: ${names}`}
        title={names}
        className={cn(
          SEGMENT_BASE,
          FOCUS_RING,
          "relative h-9 flex-none rounded-sm px-2 text-secondary hover:text-primary active:translate-y-px",
        )}
        onClick={() => {
          const button = trigger.current;
          const menu = popup.current;
          if (!button || !menu) return;
          const rect = button.getBoundingClientRect();
          menu.style.top = `${rect.bottom + 4}px`;
          menu.style.left = `${Math.max(8, Math.min(rect.left, window.innerWidth - 200))}px`;
        }}
      >
        <MoreIcon />
        {tone ? (
          <span
            aria-hidden
            className={cn("absolute top-2 right-1 h-1.5 w-1.5 rounded-full", DOT[tone])}
          />
        ) : null}
      </button>
      <div
        ref={popup}
        id={id}
        popover="auto"
        role="group"
        aria-label="More tabs"
        onToggle={(event) => setOpen(event.newState === "open")}
        className="fixed m-0 w-48 max-w-[calc(100vw-1rem)] rounded-md border border-default bg-surface-card p-1 text-base text-primary shadow-overlay"
      >
        {items.map((item) => (
          <button
            key={item.value}
            type="button"
            className={cn(
              "flex w-full cursor-pointer items-center gap-2 rounded-sm px-2 py-2 text-left hover:bg-surface-hover active:translate-y-px",
              FOCUS_RING,
            )}
            onClick={() => {
              popup.current?.hidePopover();
              onChange(item.value);
            }}
          >
            <span className="flex flex-none text-tertiary">{item.icon}</span>
            <span className="min-w-0 flex-1">{item.label}</span>
            {item.tone ? (
              <span
                aria-hidden
                className={cn("h-1.5 w-1.5 flex-none rounded-full", DOT[item.tone])}
              />
            ) : null}
          </button>
        ))}
      </div>
    </>
  );
}
