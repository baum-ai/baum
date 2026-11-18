import { openExternal } from "./api";

/** In the desktop app, a link to another site that would open in the same
 *  window would do nothing: the window never navigates away from the app, and
 *  the app opens other sites in the browser only when a page asks for a new
 *  window. This turns a person's click on such a link into that request,
 *  inside the click, so the webview allows it. A browser follows the link as
 *  usual, so this does nothing outside the desktop app.
 *
 *  It acts only on a real (`isTrusted`), primary, unmodified click in this
 *  document that no handler of the app has taken (`defaultPrevented`), so it
 *  listens last, on the window as the click bubbles. A frame has its own
 *  document and never reaches it. Installed once per window. */
export function openOutsideLinksInBrowser(
  target: Window = window,
  // Tests stand in for a person: a test cannot dispatch a trusted event.
  trusted = (event: Event) => event.isTrusted,
) {
  if (!(target as Window & { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__) return () => {};
  const click = (event: MouseEvent) => {
    if (!trusted(event) || event.defaultPrevented || event.button !== 0) return;
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const link = event.target instanceof Element ? event.target.closest("a[href]") : null;
    if (!(link instanceof HTMLAnchorElement) || link.hasAttribute("download")) return;
    if (link.target && link.target.toLowerCase() !== "_self") return;
    let url: URL;
    try {
      url = new URL(link.href, target.location.href);
    } catch {
      return;
    }
    if (url.protocol !== "http:" && url.protocol !== "https:") return;
    if (url.origin === target.location.origin) return;
    event.preventDefault();
    openExternal(url.href);
  };
  target.addEventListener("click", click);
  return () => target.removeEventListener("click", click);
}
