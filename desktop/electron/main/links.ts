// Opening links to other sites in the default browser. A page asks in one of
// two ways: a request for a new window (`target="_blank"`, `window.open`),
// which the navigation handler passes to `LinkOpener.open`, or the `open_link`
// command, which the daemon's pages and the setup screen may call. Both take
// the same URLs (navigation.ts's `decideNewWindow`) and share the app's one
// `LinkOpener`, so a page opens the browser at most once a second however it
// asks.
import { registerCommand } from "./commands.ts";
import { errorMessage, log } from "./log.ts";
import { decideNewWindow, OpenLimit, schemeAndHost } from "./navigation.ts";

/** What a request to open a URL did. */
export type Opened =
  /** Handed to the browser. */
  | "browser"
  /** Allowed, but another link opened less than a second ago. */
  | "dropped"
  /** Not a link to another site. */
  | "refused";

export interface LinkDeps {
  /** `shell.openExternal`. */
  openExternal: (url: string) => void | Promise<void>;
  /** Milliseconds on a monotonic clock. */
  now?: () => number;
  /** Log a dropped link, as development builds do. */
  debug?: boolean;
}

export class LinkOpener {
  private readonly limit = new OpenLimit();
  private readonly now: () => number;

  constructor(private readonly deps: LinkDeps) {
    this.now = deps.now ?? (() => performance.now());
  }

  /** Opens `url` in the default browser if it is a link to another site and
   *  no other link opened within the last second. */
  open(link: string | URL): Opened {
    const url = String(link);
    if (decideNewWindow(url) !== "open-in-browser") return "refused";
    if (!this.limit.allow(this.now())) {
      if (this.deps.debug) log(`dropped a second link to ${schemeAndHost(url)} within a second`);
      return "dropped";
    }
    const failed = (e: unknown) => log(`cannot open a link in the browser: ${errorMessage(e)}`);
    try {
      Promise.resolve(this.deps.openExternal(new URL(url).href)).catch(failed);
    } catch (e) {
      failed(e);
    }
    return "browser";
  }
}

/** `open_link`: opens a link to another site in the default browser. A URL
 *  the shell would not open from a new-window request is an error; a second
 *  link within a second is dropped without one. */
export function registerLinkCommands(opener: LinkOpener): void {
  registerCommand("open_link", (args) => {
    if (typeof args.url !== "string" || !URL.canParse(args.url)) {
      throw new Error("That is not a link.");
    }
    if (opener.open(args.url) === "refused") {
      throw new Error("Only links to other web sites open in the browser.");
    }
  });
}
