import { useLayoutEffect, type RefObject } from "react";

/** Raises an open menu into the browser's top layer, as a manual popover, so
 *  it draws over every panel on the page (the session panel, the chat, the
 *  sidebar) whatever their z-index, and a menu opened later draws over one
 *  opened earlier. The panels stay where they are, and stay open.
 *
 *  `open` is truthy while the menu shows; a new value (another row's menu)
 *  raises it again. A menu the page places on the window (`position: fixed`)
 *  keeps its place. One hung from its box (`position: absolute`) is pinned
 *  where its box puts it and follows that box. Call it before any layout
 *  effect that measures the menu. */
export function useTopLayer(ref: RefObject<HTMLElement | null>, open: unknown) {
  useLayoutEffect(() => {
    const menu = ref.current;
    // jsdom has no popover API.
    if (!open || !menu || typeof menu.showPopover !== "function") return;
    const holder =
      getComputedStyle(menu).position === "absolute" ? (menu.offsetParent as HTMLElement) : null;
    // Where the box puts it, before any margin or entrance animation moves
    // it: below the box from the box's foot, above it from the box's top, so
    // a list keeps its edge on the box as either grows or shrinks.
    const top = holder ? holder.clientTop + menu.offsetTop : 0;
    const bottom = top + menu.offsetHeight;
    const above = bottom <= 0;
    const gap = holder ? top - holder.offsetHeight : 0;
    const left = holder ? holder.clientLeft + menu.offsetLeft : 0;
    const width = menu.getBoundingClientRect().width;
    menu.setAttribute("popover", "manual");
    menu.showPopover();
    // Followed every frame while open: the box can move or the list change
    // with no scroll to tell.
    let frame = 0;
    const place = () => {
      if (!holder) return;
      const box = holder.getBoundingClientRect();
      Object.assign(menu.style, {
        position: "fixed",
        top: above ? "auto" : `${box.bottom + gap}px`,
        bottom: above ? `${document.documentElement.clientHeight - box.top - bottom}px` : "auto",
        left: `${box.left + left}px`,
        right: "auto",
        width: `${width}px`,
        margin: "0",
      });
      frame = requestAnimationFrame(place);
    };
    place();
    return () => {
      cancelAnimationFrame(frame);
      if (menu.isConnected && menu.matches(":popover-open")) menu.hidePopover();
      menu.removeAttribute("popover");
      if (holder)
        for (const key of ["position", "top", "left", "right", "bottom", "width", "margin"])
          menu.style.removeProperty(key);
    };
  }, [open]);
}
