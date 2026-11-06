/** The element that scrolls `element`: its nearest ancestor that scrolls on
 *  its own, as the local app's panel does, or else the page, as on hosted. */
export function scrollerOf(element: Element | null | undefined): HTMLElement {
  for (let up = element?.parentElement; up; up = up.parentElement)
    if (/auto|scroll/.test(getComputedStyle(up).overflowY)) return up;
  return (document.scrollingElement ?? document.documentElement) as HTMLElement;
}
