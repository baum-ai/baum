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
