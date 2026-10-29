import { useCallback, useEffect, useRef, useState } from "react";

/** A `<details>` disclosure with the three behaviours the element does not give
 *  you: close on select, close on an outside pointer, close on Escape.
 *
 *  A menu in a layout that stays mounted outlives the navigation it
 *  triggered, so without `close` it would stay open over the page it just
 *  moved you to. */
export function useDisclosure() {
  const ref = useRef<HTMLDetailsElement>(null);
  const [open, setOpen] = useState(false);

  const close = useCallback(() => {
    if (ref.current) ref.current.open = false;
    setOpen(false);
  }, []);

  // Listening from mount, and reading `open` off the element, rather than from
  // the `open` state: that only follows the toggle event, a task after the
  // element opens, so an Escape in between would leave the menu open.
  useEffect(() => {
    const onPointerDown = (event: PointerEvent) => {
      if (ref.current?.open && !ref.current.contains(event.target as Node)) close();
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || !ref.current?.open) return;
      // A control inside that used the key (a combobox closing its list) has
      // it, and an open disclosure inside this one closes first: Escape steps
      // out one level at a time.
      if (event.defaultPrevented || ref.current.querySelector("details[open]")) return;
      // Claimed only where it was pressed, so an enclosing disclosure whose
      // listener runs later stays open, but an open sibling still closes.
      if (ref.current.contains(event.target as Node)) event.preventDefault();
      close();
      // Escape should leave focus somewhere sensible, not on a hidden element.
      ref.current?.querySelector("summary")?.focus();
    };

    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [close]);

  return {
    open,
    close,
    detailsProps: {
      ref,
      onToggle: () => setOpen(ref.current?.open ?? false),
    },
  };
}
