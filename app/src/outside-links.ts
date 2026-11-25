type Shell = Window & {
  __TAURI_INTERNALS__?: {
    invoke(command: string, args?: Record<string, unknown>): Promise<unknown>;
  };
  __BAUM_DESKTOP__?: unknown;
};

/** Whether the desktop shell opens links with its `open_link` command
 *  (`window.__BAUM_DESKTOP__.openLinks === true`). It must on macOS, where
 *  the webview asks the shell about a `target="_blank"` click as a
 *  navigation, which the shell refuses, before it asks for a new window, so
 *  such a link did nothing. An older shell does not declare it and opens
 *  links from new-window requests. */
function shellOpensLinks(win: Shell): boolean {
  if (!win.__TAURI_INTERNALS__) return false;
  const declared = win.__BAUM_DESKTOP__;
  return (
    typeof declared === "object" &&
    declared !== null &&
    Object.hasOwn(declared, "openLinks") &&
    (declared as { openLinks?: unknown }).openLinks === true
  );
}

/** Opens a page outside the app, as a link to it would: in the desktop app
 *  through the shell's command, else by clicking a detached
 *  `target="_blank"` link, which a browser opens in a new tab and an older
 *  desktop shell hands to the browser. */
export function openExternal(url: string, win: Window = window) {
  const shell = win as Shell;
  if (shellOpensLinks(shell)) {
    shell.__TAURI_INTERNALS__!.invoke("open_link", { url }).catch((e: unknown) => {
      console.warn("baum: the desktop app did not open a link", e);
    });
    return;
  }
  Object.assign(win.document.createElement("a"), {
    href: url,
    target: "_blank",
    rel: "noreferrer",
  }).click();
}

/** In the desktop app, the window never navigates away from the app, so a
 *  link to another site opens in the browser only when the app asks for it.
 *  This turns a person's click on such a link into that request, inside the
 *  click: a same-window link always, and a `target="_blank"` one when the
 *  shell opens links itself (`shellOpensLinks`). A browser follows the link
 *  as usual, so this does nothing outside the desktop app.
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
  if (!(target as Shell).__TAURI_INTERNALS__) return () => {};
  const click = (event: MouseEvent) => {
    if (!trusted(event) || event.defaultPrevented || event.button !== 0) return;
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const link = event.target instanceof Element ? event.target.closest("a[href]") : null;
    if (!(link instanceof HTMLAnchorElement) || link.hasAttribute("download")) return;
    const sameWindow = !link.target || link.target.toLowerCase() === "_self";
    if (!sameWindow && !shellOpensLinks(target as Shell)) return;
    let url: URL;
    try {
      url = new URL(link.href, target.location.href);
    } catch {
      return;
    }
    if (url.protocol !== "http:" && url.protocol !== "https:") return;
    if (url.origin === target.location.origin) return;
    event.preventDefault();
    openExternal(url.href, target);
  };
  target.addEventListener("click", click);
  return () => target.removeEventListener("click", click);
}
