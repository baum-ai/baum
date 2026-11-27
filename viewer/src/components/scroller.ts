/** The element that scrolls `element`: its nearest ancestor that scrolls on
 *  its own, as the local app's panel does, or else the page, as on hosted. */
export function scrollerOf(element: Element | null | undefined): HTMLElement {
  for (let up = element?.parentElement; up; up = up.parentElement)
    if (/auto|scroll/.test(getComputedStyle(up).overflowY)) return up;
  return (document.scrollingElement ?? document.documentElement) as HTMLElement;
}

/** Scrolls everything that scrolls `element`, the page too, back to its top,
 *  so what stands above it (a header, a page's bar) shows again. The keys
 *  call it on reaching the first of something: the innermost scroller alone
 *  may already be at its top, or only scroll sideways, while the page above
 *  it is still scrolled down. */
export function scrollToTop(element: Element | null | undefined) {
  for (let up = element?.parentElement; up; up = up.parentElement)
    if (/auto|scroll/.test(getComputedStyle(up).overflowY)) up.scrollTo?.({ top: 0 });
  (document.scrollingElement ?? document.documentElement).scrollTo?.({ top: 0 });
}

/** Reads on through a block taller than the space that scrolls it, as a
 *  pager does: while the block runs on past that space's bottom (`forward`)
 *  or top, scrolls a part of the space that way and is true. False when the
 *  block fits, or its end is already in view, so the keys move on to the
 *  next block. */
export function readThrough(block: HTMLElement, forward: boolean, smooth: boolean): boolean {
  const root = (document.scrollingElement ?? document.documentElement) as HTMLElement;
  // The nearest space that scrolls up and down: one that only scrolls
  // sideways (a wide table's) is passed over.
  let scroller = root;
  for (let up = block.parentElement; up; up = up.parentElement)
    if (/auto|scroll/.test(getComputedStyle(up).overflowY) && up.scrollHeight > up.clientHeight) {
      scroller = up;
      break;
    }
  const page = scroller === root;
  const frame = page ? { top: 0, bottom: window.innerHeight } : scroller.getBoundingClientRect();
  const top = Math.max(frame.top, 0);
  const bottom = Math.min(frame.bottom, window.innerHeight);
  const box = block.getBoundingClientRect();
  if (box.height <= bottom - top) return false;
  const beyond = forward ? box.bottom - bottom : top - box.top;
  if (beyond <= 1) return false;
  scroller.scrollBy({
    top: (forward ? 1 : -1) * Math.min(beyond, (bottom - top) * 0.6),
    behavior: smooth ? "smooth" : "auto",
  });
  return true;
}
