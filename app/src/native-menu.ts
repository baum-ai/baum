/** Turns off the browser's right-click menu wherever the app offers none of
 *  its own (`useContextMenu` takes the event first and opens the app's). It
 *  stays where it is useful: in a field, and over selected text, for cut,
 *  copy and paste. Installed once per window, before the first render. */
export function blockBrowserMenu(target: Window = window) {
  const block = (event: MouseEvent) => {
    if (event.defaultPrevented) return;
    const element = event.target instanceof Element ? event.target : null;
    if (element?.closest("input, textarea, select, [contenteditable]")) return;
    if (target.getSelection()?.toString()) return;
    event.preventDefault();
  };
  target.addEventListener("contextmenu", block);
  return () => target.removeEventListener("contextmenu", block);
}
