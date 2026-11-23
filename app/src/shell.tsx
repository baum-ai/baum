import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ComponentType,
  type ReactNode,
  type RefObject,
} from "react";
import { Wordmark } from "@baum/viewer/components/editorial";
import { ChevronDownIcon, HelpIcon, RefreshIcon, SidebarIcon } from "@baum/viewer/components/icons";
import { Stumpy } from "@baum/viewer/components/stumpy";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import { appURL } from "./api";
import { useContextMenu, type MenuItem } from "./context-menu";
import type { Inbox } from "./inboxes";
import { PAGES, type Page } from "./pages";
import { PlanUsageRail } from "./plan-usage";
import { useMedia } from "./media";
import { usePreferences, type SidebarChoice } from "./preferences";
import { NineDots } from "./nine-dots";
import {
  pageKeyLabel,
  pageKeyShortcut,
  REFRESH_KEY_LABEL,
  REFRESH_KEY_SHORTCUT,
} from "./shortcuts";

/** The inboxes nested under Inbox, with what changes them. Absent when the
 *  daemon is older and has none. */
export type RailInboxes = {
  list: Inbox[];
  /** What waits in each, by id, not counting snoozed. */
  counts: Record<string, number>;
  /** The id of the inbox whose queue the page shows. */
  selected: string | null;
  /** Deletes the inbox once confirmed, then runs `after`. */
  remove: (inbox: Inbox, after?: () => void) => void;
  /** Opens the inbox's edit page. */
  edit: (inbox: Inbox) => void;
  /** New inbox and Manage inboxes, in the right-click menu of Inbox. */
  create: () => void;
  manage: () => void;
};

/** What the Inbox heading says to a screen reader besides its name: the
 *  total of what waits, as its visual badge shows it. */
export function inboxWaitingText(count: number) {
  return `, ${count} waiting`;
}

/** Why the last inbox cannot be deleted. */
export const KEEP_ONE = "Keep at least one inbox";
const FOLD_KEY = "baum.inbox.folded.v1";
function folded() {
  try {
    return localStorage.getItem(FOLD_KEY) === "1";
  } catch {
    return false;
  }
}

/** The last sidebar choice this window saw, so a reload draws the sidebar as
 *  it was before the saved preferences arrive rather than folding it after. */
const SIDEBAR_KEY = "baum.sidebar.v1";
function cachedSidebar(): SidebarChoice {
  try {
    return localStorage.getItem(SIDEBAR_KEY) === "folded" ? "folded" : "pinned";
  } catch {
    return "pinned";
  }
}

/** How long a pointer rests on the folded sidebar before it opens, and how
 *  long it may wander off before it folds. Opening at once would flash the
 *  sidebar open under every pointer on its way across the window. */
const OPEN_DELAY_MS = 120;
const CLOSE_DELAY_MS = 200;

/** Whether the folded sidebar is open over the page under a mouse, with the
 *  handlers that open and fold it. The pointer is followed while the sidebar
 *  is pinned too (`folded` false), so it is known on unpinning. */
function usePeek(folded: boolean) {
  const [peeking, setPeeking] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  // Whether a mouse is over the sidebar, and whether the sidebar stays folded
  // under it until it leaves: after a click on a link or the pin button
  // folds it, so it does not open again at the next nudge.
  const inside = useRef(false);
  const held = useRef(false);
  const cancel = () => {
    clearTimeout(timer.current);
    timer.current = undefined;
  };
  const after = (next: boolean, delay: number) => {
    cancel();
    timer.current = setTimeout(() => {
      timer.current = undefined;
      setPeeking(next);
    }, delay);
  };
  useEffect(() => () => clearTimeout(timer.current), []);
  // A mouse only: a touch has no hover to leave, so a tap would open the
  // sidebar over the page and leave it there. Movement opens it as well as
  // entering: a pointer already resting on it when the app loads entered
  // before React was listening.
  const over = (event: React.PointerEvent) => {
    if (event.pointerType !== "mouse") return;
    inside.current = true;
    if (!folded || held.current) return;
    if (peeking) cancel();
    else if (timer.current === undefined) after(true, OPEN_DELAY_MS);
  };
  const leave = (event: React.PointerEvent) => {
    if (event.pointerType !== "mouse") return;
    inside.current = false;
    held.current = false;
    if (peeking) after(false, CLOSE_DELAY_MS);
    else cancel();
  };
  /** Folds it now, and keeps it folded under the pointer until it leaves. */
  const fold = () => {
    cancel();
    setPeeking(false);
    held.current = inside.current;
  };
  return { peeking, over, leave, fold };
}

/** The application chrome, after the hosted sidebar: the mark, the sections
 *  and where the work happens, standing on the sand canvas beside the page's
 *  panel, with what is left of the agents' plans above the foot. Each
 *  section's key shows beside it while Alt is held, like every key hint.
 *
 *  The button beside Help folds it to its glyphs, so the page gets the width,
 *  and pins it open again; the choice is saved with the appearance
 *  preferences. Folded, Help is hidden and `?` shows the shortcuts sheet.
 *  Folded, resting the mouse on it or moving keyboard focus into
 *  it opens it over the page, and a link chosen there folds it again. Below
 *  `lg` it is always folded, and the button is gone. */
export function Sidebar({
  page,
  inbox,
  activeJobs,
  runningSessions,
  needsYou,
  problems,
  settingsWarning,
  update,
  inboxes,
  help,
  Link,
}: {
  page: Page;
  /** What waits on the person: each pull request once, snoozed left out,
   *  whichever saved inbox holds it or none. */
  inbox: number;
  activeJobs: number;
  runningSessions: number;
  /** Agent sessions waiting on the person; the Sessions badge counts them,
   *  in the caution tone, while there are any. */
  needsYou: number;
  problems: number;
  settingsWarning?: string | null;
  /** A newer desktop app version; Settings counts it. */
  update?: string | null;
  inboxes?: RailInboxes;
  /** What Help, beside the baum title, offers besides the docs. */
  help?: RailHelp;
  Link: ComponentType<ViewerLinkProps>;
}) {
  const [fold, setFold] = useState(folded);
  const { preferences, ready, update: savePreferences } = usePreferences();
  const modifier = preferences.page_modifier;
  const narrow = useMedia("(width < 64rem)");
  const choice = ready ? preferences.sidebar : cachedSidebar();
  useEffect(() => {
    if (!ready) return;
    try {
      localStorage.setItem(SIDEBAR_KEY, preferences.sidebar);
    } catch {
      // The next load draws it pinned until the preferences arrive.
    }
  }, [ready, preferences.sidebar]);
  const pinned = choice === "pinned" && !narrow;
  const peek = usePeek(!pinned);
  const nested = inboxes && inboxes.list.length > 0;
  // A right click on Inbox makes or manages inboxes.
  const inboxMenu = useContextMenu();
  const helpMenu = useContextMenu();
  const badges: Partial<Record<Page, { badge: ReactNode; tone?: "warn" | "caution" }>> = {
    inbox: { badge: inbox > 0 ? inbox : null },
    sessions: {
      badge: needsYou > 0 ? needsYou : runningSessions > 0 ? runningSessions : null,
      tone: needsYou > 0 ? "caution" : undefined,
    },
    jobs: { badge: activeJobs > 0 ? activeJobs : null },
    settings: {
      badge:
        problems > 0 || update
          ? problems + (settingsWarning ? 1 : 0) + (update ? 1 : 0)
          : settingsWarning
            ? "!"
            : null,
      tone: problems > 0 ? "warn" : settingsWarning ? "caution" : undefined,
    },
  };
  const settingsNote = [settingsWarning, update ? `baum ${update} is available` : null]
    .filter(Boolean)
    .join("; ");
  const link = ({ page: target, label, href, icon: Icon }: (typeof PAGES)[number]) => {
    const { badge, tone } = badges[target] ?? {};
    const content = (
      <>
        <Icon size={18} className="local-rail-glyph" />
        <span className="local-rail-label">{label}</span>
        {target === "inbox" && inbox > 0 ? (
          <span className="sr-only">{inboxWaitingText(inbox)}</span>
        ) : null}
        {badge ? (
          <span aria-hidden="true" className={`local-rail-badge ${tone ?? ""}`.trim()}>
            {badge}
          </span>
        ) : null}
        <kbd aria-hidden="true" className="key-hint local-rail-key">
          {pageKeyLabel(target, modifier)}
        </kbd>
      </>
    );
    // Inbox is the section the saved inboxes sit under, not a place of its
    // own: a heading with their count, never a link or the current page.
    if (target === "inbox")
      return (
        <div
          key={target}
          title={`${label} (${pageKeyLabel(target, modifier)})`}
          className="local-rail-link local-rail-heading"
          onContextMenu={
            inboxes
              ? (event) =>
                  inboxMenu.open(
                    event,
                    [
                      { label: "New inbox…", onSelect: inboxes.create },
                      { label: "Manage inboxes…", onSelect: inboxes.manage },
                    ],
                    "Inbox",
                  )
              : undefined
          }
        >
          {content}
        </div>
      );
    return (
      <Link
        key={target}
        href={href()}
        title={`${label} (${pageKeyLabel(target, modifier)})${target === "settings" && settingsNote ? ": " + settingsNote : ""}`}
        aria-description={target === "settings" ? settingsNote || undefined : undefined}
        aria-current={page === target ? "page" : undefined}
        aria-keyshortcuts={pageKeyShortcut(target, modifier)}
        className="local-rail-link"
      >
        {content}
      </Link>
    );
  };
  return (
    <div
      className="local-rail-lane"
      data-rail={pinned ? "pinned" : peek.peeking ? "open" : "folded"}
      onPointerEnter={peek.over}
      onPointerMove={peek.over}
      onPointerLeave={peek.leave}
      onClick={(event) => {
        // A place chosen from the open sidebar is where the person wants to
        // look: the sidebar no longer covers it.
        if (!pinned && (event.target as Element).closest("a")) peek.fold();
      }}
    >
      <aside data-tab-region="" className="local-rail">
        {inboxMenu.menu}
        {helpMenu.menu}
        <div className="local-rail-head">
          <Link href={appURL()} className="local-rail-mark" title="Inbox">
            <Stumpy size={28} className="flex-none" />
            <Wordmark size="sidebar" className="local-rail-label" />
          </Link>
          <HelpButton help={help} menu={helpMenu} shown={pinned || peek.peeking} />
          <button
            type="button"
            className="local-rail-pin"
            aria-label="Pin sidebar"
            aria-pressed={choice === "pinned"}
            title={choice === "pinned" ? "Fold the sidebar to its glyphs" : "Pin the sidebar open"}
            disabled={!ready}
            onClick={() => {
              const next = choice === "pinned" ? "folded" : "pinned";
              // Either way it is not open over the page: unpinned under the
              // pointer, it stays folded until the pointer leaves.
              peek.fold();
              savePreferences({ sidebar: next });
            }}
          >
            <SidebarIcon size={16} />
          </button>
        </div>
        <nav aria-label="Main" className="local-rail-nav local-rail-main">
          {PAGES.filter((item) => !item.foot).map((item) =>
            item.page === "inbox" && inboxes && nested ? (
              <div key="inbox">
                <div className="local-rail-row">
                  {link(item)}
                  <button
                    type="button"
                    className="local-rail-fold"
                    aria-expanded={!fold}
                    aria-controls="local-rail-inboxes"
                    aria-label="Inboxes"
                    onClick={() => {
                      setFold(!fold);
                      try {
                        localStorage.setItem(FOLD_KEY, fold ? "0" : "1");
                      } catch {
                        // Not remembered across reloads.
                      }
                    }}
                  >
                    <ChevronDownIcon size={14} />
                  </button>
                </div>
                <div id="local-rail-inboxes" hidden={fold}>
                  {fold ? null : <InboxRows inboxes={inboxes} Link={Link} />}
                </div>
              </div>
            ) : (
              link(item)
            ),
          )}
        </nav>
        <div className="local-rail-foot">
          <PlanUsageRail />
          <nav aria-label="More" className="local-rail-nav">
            {PAGES.filter((item) => item.foot).map(link)}
          </nav>
        </div>
      </aside>
    </div>
  );
}

/** Where "Open the docs" goes. */
export const DOCS_URL = "https://docs.usebaum.com/";

/** What the sidebar's Help menu does beyond opening the docs. */
export type RailHelp = {
  /** Shows the shortcuts sheet. */
  shortcuts: () => void;
  /** Starts a report of a problem. */
  report: () => void;
  /** Set while the button is mounted to what opens its menu, so the `?` key
   *  can open it with the keys on Show shortcuts: `?` then Enter shows the
   *  sheet. */
  opener?: RefObject<(() => void) | null>;
};

/** Help, at the right of the baum title: a menu of the shortcuts, reporting
 *  a problem and the docs. It opens from the keys as a menu button does (Enter, Space, and
 *  Down on the first choice, Up on the last), and from the `?` key. The folded
 *  sidebar hides it; `shown` is false then, so `?` shows the shortcuts sheet
 *  instead of a menu hanging from nothing. */
function HelpButton({
  help,
  menu,
  shown,
}: {
  help?: RailHelp;
  menu: ReturnType<typeof useContextMenu>;
  shown: boolean;
}) {
  const ref = useRef<HTMLButtonElement>(null);
  const open = menu.anchor !== null && menu.anchor === ref.current;
  const items = (): MenuItem[] => [
    ...(help
      ? [
          { label: "Show shortcuts", onSelect: help.shortcuts },
          { label: "Report a problem", onSelect: help.report },
        ]
      : []),
    {
      label: "Open the docs",
      // The desktop app hands a new window to the browser.
      onSelect: () => void window.open(DOCS_URL, "_blank", "noopener,noreferrer"),
    },
  ];
  const show = (last = false, returnTo?: Element | null) => {
    if (ref.current) menu.openFrom(ref.current, items(), "Help", last, returnTo);
  };
  const opener = help?.opener;
  useEffect(() => {
    if (!opener || !shown) return;
    // Opened by `?`, the keys go back to where they were, not to this button,
    // which would then draw its focus ring.
    opener.current = () => show(false, document.activeElement);
    return () => {
      opener.current = null;
    };
  });
  return (
    <button
      ref={ref}
      type="button"
      className="local-rail-help"
      aria-label="Help"
      title="Help (?)"
      aria-haspopup="menu"
      aria-expanded={open}
      aria-controls={open ? menu.id : undefined}
      onClick={(event) => {
        const button = event.currentTarget;
        // The press that closed the menu leaves it closed.
        if (button.dataset.closing !== undefined) delete button.dataset.closing;
        else show();
      }}
      onPointerLeave={(event) => delete event.currentTarget.dataset.closing}
      onKeyDown={(event) => {
        if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
        event.preventDefault();
        show(event.key === "ArrowUp");
      }}
    >
      <HelpIcon size={16} />
    </button>
  );
}

function InboxRows({
  inboxes,
  Link,
}: {
  inboxes: RailInboxes;
  Link: ComponentType<ViewerLinkProps>;
}) {
  const [open, setOpen] = useState<string | null>(null);
  const keepId = useId();
  // The last inbox cannot be deleted: Inbox always has somewhere to open.
  const last = inboxes.list.length <= 1;
  const menu = useRef<HTMLDivElement>(null);
  const popup = useRef<HTMLDivElement>(null);
  // The `⋯` button of the row whose menu is open.
  const more = () => menu.current?.querySelector<HTMLElement>(".local-rail-more") ?? null;
  // The menu is a disclosure of plain buttons: it opens on the first, Esc and
  // a click elsewhere close it, and focus goes back to its `⋯` button, so it
  // never drops to the page.
  const close = (restoreFocus: boolean) => {
    if (restoreFocus) more()?.focus();
    setOpen(null);
  };
  // The menu is placed against the window, under its row or above it when
  // the row is near the bottom, so the sidebar's scrolling list cannot clip it.
  useLayoutEffect(() => {
    const row = menu.current?.getBoundingClientRect();
    const element = popup.current;
    if (!open || !row || !element) return;
    const height = element.offsetHeight;
    const below = row.bottom + 2;
    element.style.top = `${below + height > window.innerHeight - 8 ? Math.max(8, row.top - height - 2) : below}px`;
    element.style.left = `${row.left + 8}px`;
  }, [open]);
  useEffect(() => {
    if (!open) return;
    menu.current?.querySelector<HTMLElement>(".local-rail-menu button")?.focus();
    const outside = (event: MouseEvent) => {
      if (menu.current?.contains(event.target as Node)) return;
      // Focus that was inside the row would otherwise fall to the page.
      close(!!menu.current?.contains(document.activeElement));
    };
    // Esc is the menu's alone while it is open: taken before the page's keys,
    // whose Esc would clear the focus just given back to `⋯`. Tab closes it
    // the same way, as it does a row's menu.
    const escape = (event: KeyboardEvent) => {
      if (event.key !== "Escape" && event.key !== "Tab") return;
      event.preventDefault();
      event.stopPropagation();
      close(true);
    };
    // A placed menu would drift from its row as the sidebar scrolls or the
    // window resizes, so either closes it.
    const away = () => close(!!menu.current?.contains(document.activeElement));
    document.addEventListener("mousedown", outside);
    window.addEventListener("keydown", escape, true);
    window.addEventListener("scroll", away, true);
    window.addEventListener("resize", away);
    return () => {
      document.removeEventListener("mousedown", outside);
      window.removeEventListener("keydown", escape, true);
      window.removeEventListener("scroll", away, true);
      window.removeEventListener("resize", away);
    };
  }, [open]);
  // A deleted row takes its button with it: focus moves to its neighbour, or
  // to nothing, as the last inbox is never deleted.
  const neighbour = () => {
    const row = menu.current;
    const inboxRow = (element: Element | null | undefined) =>
      element?.matches(".local-rail-inbox") ? (element as HTMLElement) : null;
    const next = inboxRow(row?.nextElementSibling) ?? inboxRow(row?.previousElementSibling);
    const target = next ? next.querySelector<HTMLElement>("a.local-rail-sub") : null;
    return target;
  };
  // The first row left, where the page goes once the open inbox is gone.
  const firstLeft = () =>
    Array.from(
      menu.current?.parentElement?.querySelectorAll<HTMLElement>(".local-rail-inbox") ?? [],
    )
      .find((row) => row !== menu.current)
      ?.querySelector<HTMLElement>("a.local-rail-sub") ?? null;
  return (
    <div className="local-rail-inboxes">
      {inboxes.list.map((inbox) => {
        const count = inboxes.counts[inbox.id] ?? 0;
        return (
          <div
            key={inbox.id}
            ref={open === inbox.id ? menu : undefined}
            className={`local-rail-inbox inbox-${inbox.color}`}
            // A right click opens the same menu as its ⋯: Edit and Delete.
            onContextMenu={(event) => {
              event.preventDefault();
              setOpen(inbox.id);
            }}
          >
            <Link
              href={appURL(`?inbox=${encodeURIComponent(inbox.id)}`)}
              aria-current={inboxes.selected === inbox.id ? "page" : undefined}
              className="local-rail-sub"
            >
              <span aria-hidden="true" className="inbox-dot" />
              <span className="local-rail-label">{inbox.name}</span>
              {count > 0 && inbox.badge ? (
                <span className="local-rail-sub-count">{count}</span>
              ) : null}
            </Link>
            <button
              type="button"
              className="local-rail-more"
              aria-label={`More for ${inbox.name}`}
              aria-expanded={open === inbox.id}
              onClick={() => (open === inbox.id ? close(true) : setOpen(inbox.id))}
            >
              ⋯
            </button>
            {open === inbox.id ? (
              <div ref={popup} className="local-rail-menu">
                <button
                  type="button"
                  onClick={() => {
                    setOpen(null);
                    inboxes.edit(inbox);
                  }}
                >
                  Edit inbox
                </button>
                <button
                  type="button"
                  disabled={last}
                  aria-describedby={last ? keepId : undefined}
                  title={last ? KEEP_ONE : undefined}
                  onClick={() => {
                    // The open inbox's deletion opens the first one left: focus its row.
                    const next = inboxes.selected === inbox.id ? firstLeft() : neighbour();
                    setOpen(null);
                    inboxes.remove(inbox, () => next?.focus());
                  }}
                >
                  Delete inbox
                </button>
                {last ? (
                  <span id={keepId} className="local-rail-menu-note">
                    {KEEP_ONE}
                  </span>
                ) : null}
              </div>
            ) : null}
          </div>
        );
      })}
    </div>
  );
}

/** The bar along the top of the page's panel, as the hosted `PageHeader`:
 *  the page's name with a short line beside it, its controls on the right. */
export function PageBar({
  title,
  detail,
  actions,
  back,
}: {
  title: ReactNode;
  detail?: ReactNode;
  actions?: ReactNode;
  /** The way back to where the page was opened from, before its name. */
  back?: ReactNode;
}) {
  // The bar stays at the top of the panel as the page scrolls under it; what
  // else sticks there sits below it (`--page-bar-height`).
  const bar = useRef<HTMLElement>(null);
  useLayoutEffect(() => {
    const element = bar.current;
    const main = element?.closest<HTMLElement>(".local-main");
    if (!element || !main || typeof ResizeObserver !== "function") return;
    const observer = new ResizeObserver(() =>
      main.style.setProperty("--page-bar-height", `${element.offsetHeight}px`),
    );
    observer.observe(element);
    return () => {
      observer.disconnect();
      main.style.removeProperty("--page-bar-height");
    };
  }, []);
  return (
    <header ref={bar} className="local-page-bar">
      {back}
      <div className="local-page-title">
        <h1>{title}</h1>
        {detail ? <p>{detail}</p> : null}
      </div>
      {actions ? <div className="local-page-actions">{actions}</div> : null}
    </header>
  );
}

/** Where the app's pages scroll: the panel under the notices
 *  (`.local-main`), or the window where there is none. */
export function pageScroll() {
  const main = document.querySelector<HTMLElement>(".local-main");
  if (main)
    return {
      top: main.scrollTop,
      height: main.scrollHeight,
      view: main.clientHeight,
      to: (top: number, behavior?: ScrollBehavior) => main.scrollTo?.({ top, behavior }),
    };
  const page = document.scrollingElement ?? document.documentElement;
  return {
    top: window.scrollY,
    height: page.scrollHeight,
    view: window.innerHeight,
    to: (top: number, behavior?: ScrollBehavior) => window.scrollTo({ top, behavior }),
  };
}

/** One press of a page's refresh. `signal` aborts when the page's refresh
 *  is pressed again; `restart` says this press replaces a refresh or a read
 *  of the page's own under way, which may never answer; `current` says
 *  whether it is still the page's latest and the page is still shown. */
export type RefreshRun = { signal: AbortSignal; restart: boolean; current: () => boolean };
/** What a page's refresh button says refreshing the page does. */
type Refresh = { refresh: (run: RefreshRun) => Promise<unknown>; busy: boolean };
type PageRefresh = {
  /** Makes a refresh the page's until the cleanup it returns runs; of
   *  several, the one registered last. */
  register: (button: RefObject<Refresh>) => () => void;
  /** Refreshes the page with its latest registered refresh, else the app's
   *  own; a press while one is under way starts it again. */
  press: () => void;
  /** A refresh of this page is under way. */
  pending: boolean;
};
const PageRefreshContext = createContext<PageRefresh | null>(null);
export const PageRefreshProvider = PageRefreshContext.Provider;

/** The page's refresh, kept above the page so that its key (⌘R) has one
 *  place to go and a refresh under way outlives the button, as when a pull
 *  request's walkthrough gives way to its full review. `scope` names the
 *  page shown: each page's refresh runs and shows on its own. A page that
 *  registers none, as one still loading, refreshes with `fallback`. */
export function usePageRefresh(
  scope: string,
  fallback: (run: RefreshRun) => Promise<unknown>,
): PageRefresh {
  const buttons = useRef<RefObject<Refresh>[]>([]);
  const running = useRef(new Map<string, AbortController>());
  const [pending, setPending] = useState<ReadonlySet<string>>(new Set());
  const shown = useRef(scope);
  shown.current = scope;
  const latestFallback = useRef(fallback);
  latestFallback.current = fallback;
  const register = useCallback((ref: RefObject<Refresh>) => {
    buttons.current.push(ref);
    return () => {
      buttons.current = buttons.current.filter((each) => each !== ref);
    };
  }, []);
  const press = useCallback(() => {
    const page = buttons.current.at(-1)?.current;
    const at = shown.current;
    const before = running.current.get(at);
    before?.abort();
    const controller = new AbortController();
    running.current.set(at, controller);
    setPending((now) => new Set(now).add(at));
    const run: RefreshRun = {
      signal: controller.signal,
      restart: Boolean(before) || Boolean(page?.busy),
      current: () => running.current.get(at) === controller && shown.current === at,
    };
    const done = () => {
      if (running.current.get(at) !== controller) return;
      running.current.delete(at);
      setPending((now) => new Set([...now].filter((each) => each !== at)));
    };
    // The page reports its own failures.
    void Promise.resolve()
      .then(() => (page ? page.refresh(run) : latestFallback.current(run)))
      .then(done, done);
  }, []);
  return useMemo(
    () => ({ register, press, pending: pending.has(scope) }),
    [register, press, pending, scope],
  );
}

/** Makes `refresh` the page's while the caller is drawn, for the refresh key
 *  on a page that does not show the button, as one still loading; a refresh
 *  registered later, such as a button drawn after or an open picker's, takes
 *  over until it goes, for the button too. Gives what presses the page's
 *  refresh and whether one is under way. */
export function usePageRefreshAction(
  refresh: ((run: RefreshRun) => Promise<unknown>) | undefined,
  busy = false,
) {
  const page = useContext(PageRefreshContext);
  const self = useRef<Refresh>({ refresh: refresh ?? (() => Promise.resolve()), busy });
  useEffect(() => {
    if (refresh) self.current = { refresh, busy };
  });
  const register = page?.register;
  const registered = Boolean(refresh);
  useEffect(() => (registered ? register?.(self) : undefined), [register, registered]);
  return { press: () => page?.press(), pending: Boolean(page?.pending) };
}

/** A refresh for a page whose reads an effect makes: `again` changes `count`,
 *  which the effect reads on, and settles once a read made at that count or
 *  later calls `settled` with its count, or when the page goes. */
export function useReread() {
  const [count, setCount] = useState(0);
  const asked = useRef(0);
  const waiting = useRef<{ count: number; resolve: () => void }[]>([]);
  const again = useCallback(
    () =>
      new Promise<void>((resolve) => {
        asked.current += 1;
        waiting.current.push({ count: asked.current, resolve });
        setCount(asked.current);
      }),
    [],
  );
  const settled = useCallback((read: number) => {
    waiting.current = waiting.current.filter((each) => {
      if (each.count > read) return true;
      each.resolve();
      return false;
    });
  }, []);
  useEffect(
    () => () => {
      for (const each of waiting.current.splice(0)) each.resolve();
    },
    [],
  );
  return { count, again, settled };
}

/** The page's refresh, the last of its bar's actions: reads what the page
 *  shows again, with the loader in place of the arrow until `refresh`
 *  settles or while `busy` (a read the page started itself). The refresh key
 *  (⌘R, Ctrl+R elsewhere) presses it; pressing it again starts it again. */
export function RefreshButton({
  refresh,
  busy = false,
  label = "Refresh",
}: {
  refresh: (run: RefreshRun) => Promise<unknown>;
  busy?: boolean;
  label?: string;
}) {
  const page = usePageRefreshAction(refresh, busy);
  const shown = busy || page.pending;
  return (
    <button
      type="button"
      aria-label={label}
      title={`${label} (${REFRESH_KEY_LABEL})`}
      aria-keyshortcuts={REFRESH_KEY_SHORTCUT}
      aria-busy={shown || undefined}
      aria-disabled={shown || undefined}
      onClick={page.press}
      className="local-icon-button local-refresh"
    >
      {shown ? <NineDots size={16} /> : <RefreshIcon size={16} />}
    </button>
  );
}

/** Something worth knowing that is not a failure: a pause, a refresh that
 *  will retry, a problem on the computer. Errors stay `Alert`s. */
export function Notice({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div role="status" className="local-notice">
      <p>{children}</p>
      {action ? <span className="local-notice-action">{action}</span> : null}
    </div>
  );
}

/** The header of a settings panel: a glyph, a title with an optional state
 *  beside it, a line of detail, and the panel's actions on the right. */
export function PanelHeader({
  id,
  icon,
  title,
  badge,
  detail,
  actions,
}: {
  id: string;
  icon: ReactNode;
  title: ReactNode;
  badge?: ReactNode;
  detail?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <header className="flex flex-wrap items-center gap-x-4 gap-y-3 p-5">
      <div className="flex min-w-0 flex-[1_1_18rem] items-center gap-3">
        <span className="flex h-10 w-10 flex-none items-center justify-center rounded-md bg-surface-wash text-primary">
          {icon}
        </span>
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <h2 id={id} className="text-base font-semibold">
              {title}
            </h2>
            {badge}
          </div>
          {detail ? <p className="mt-0.5 text-xs text-secondary">{detail}</p> : null}
        </div>
      </div>
      {actions ? (
        <div className="flex max-w-full flex-none flex-wrap items-center gap-2">{actions}</div>
      ) : null}
    </header>
  );
}
