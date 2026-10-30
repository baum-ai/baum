import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ComponentType,
  type ReactNode,
  type RefObject,
} from "react";
import { Wordmark } from "@baum/viewer/components/editorial";
import { RefreshIcon } from "@baum/viewer/components/icons";
import { Stumpy } from "@baum/viewer/components/stumpy";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import { appURL } from "./api";
import type { Inbox } from "./inboxes";
import { PAGES, type Page } from "./pages";
import { PlanUsageRail } from "./plan-usage";
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
  /** A page under Inbox that is not a queue (New inbox, Manage inboxes) is
   *  the current one, so Inbox is only the section. */
  elsewhere?: boolean;
  toggleBadge: (inbox: Inbox) => void;
  remove: (inbox: Inbox) => void;
  /** Opens the inbox's edit page. */
  edit: (inbox: Inbox) => void;
  /** More entries at the foot of the nested list: where "New inbox" and
   *  "Manage inboxes" go. */
  footer?: ReactNode;
};

const FOLD_KEY = "baum.inbox.folded.v1";
function folded() {
  try {
    return localStorage.getItem(FOLD_KEY) === "1";
  } catch {
    return false;
  }
}

/** The application chrome, after the hosted sidebar: the mark, the sections
 *  and where the work happens, standing on the sand canvas beside the page's
 *  panel, with what is left of the agents' plans above the foot. Below `lg`
 *  it folds to its glyphs. Each section's key shows beside it while Alt is
 *  held, like every key hint. */
export function Sidebar({
  page,
  inbox,
  activeJobs,
  runningSessions,
  needsYou,
  problems,
  inboxes,
  Link,
}: {
  page: Page;
  /** What waits on the person in the inbox, not counting snoozed. */
  inbox: number;
  activeJobs: number;
  runningSessions: number;
  /** Agent sessions waiting on the person; the Sessions badge counts them,
   *  in the caution tone, while there are any. */
  needsYou: number;
  problems: number;
  inboxes?: RailInboxes;
  Link: ComponentType<ViewerLinkProps>;
}) {
  const [fold, setFold] = useState(folded);
  const nested = inboxes && (inboxes.list.length > 0 || inboxes.footer);
  const badges: Partial<Record<Page, { badge: ReactNode; tone?: "warn" | "caution" }>> = {
    inbox: { badge: inbox > 0 ? inbox : null },
    sessions: {
      badge: needsYou > 0 ? needsYou : runningSessions > 0 ? runningSessions : null,
      tone: needsYou > 0 ? "caution" : undefined,
    },
    jobs: { badge: activeJobs > 0 ? activeJobs : null },
    settings: { badge: problems > 0 ? problems : null, tone: "warn" },
  };
  const link = ({ page: target, label, href, icon: Icon }: (typeof PAGES)[number]) => {
    const { badge, tone } = badges[target] ?? {};
    return (
      <Link
        key={target}
        href={href()}
        title={`${label} (${pageKeyLabel(target)})`}
        aria-current={
          page === target && !(target === "inbox" && (inboxes?.selected || inboxes?.elsewhere))
            ? "page"
            : undefined
        }
        aria-keyshortcuts={pageKeyShortcut(target)}
        className="local-rail-link"
      >
        <Icon size={18} className="local-rail-glyph" />
        <span className="local-rail-label">{label}</span>
        {badge ? (
          <span aria-hidden="true" className={`local-rail-badge ${tone ?? ""}`.trim()}>
            {badge}
          </span>
        ) : null}
        <kbd aria-hidden="true" className="key-hint local-rail-key">
          {pageKeyLabel(target)}
        </kbd>
      </Link>
    );
  };
  return (
    <aside className="local-rail">
      <Link href={appURL()} className="local-rail-mark" title="Inbox">
        <Stumpy size={28} className="flex-none" />
        <Wordmark size="sidebar" className="local-rail-label" />
      </Link>
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
                  ▾
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
    // whose Esc would clear the focus just given back to `⋯`.
    const escape = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
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
  // to Inbox when it was the last.
  const afterDelete = () => {
    const row = menu.current;
    const inboxRow = (element: Element | null | undefined) =>
      element?.matches(".local-rail-inbox") ? (element as HTMLElement) : null;
    const next = inboxRow(row?.nextElementSibling) ?? inboxRow(row?.previousElementSibling);
    const target = next
      ? next.querySelector<HTMLElement>("a.local-rail-sub")
      : row?.closest("nav")?.querySelector<HTMLElement>("a.local-rail-link");
    target?.focus();
  };
  return (
    <div className="local-rail-inboxes">
      {inboxes.list.map((inbox) => {
        const count = inboxes.counts[inbox.id] ?? 0;
        return (
          <div
            key={inbox.id}
            ref={open === inbox.id ? menu : undefined}
            className={`local-rail-inbox inbox-${inbox.color}`}
          >
            <Link
              href={appURL(`?inbox=${encodeURIComponent(inbox.id)}`)}
              aria-current={inboxes.selected === inbox.id ? "page" : undefined}
              className="local-rail-sub"
            >
              <span aria-hidden="true" className="inbox-dot" />
              <span className="local-rail-label">{inbox.name}</span>
              {count > 0 ? (
                <span
                  className={`local-rail-sub-count${inbox.badge ? "" : " muted"}`}
                  title={inbox.badge ? undefined : "Not counted in the Inbox badge"}
                >
                  {count}
                </span>
              ) : null}
              {count > 0 && !inbox.badge ? (
                <span className="sr-only"> (not counted in the Inbox badge)</span>
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
                  onClick={() => {
                    close(true);
                    inboxes.toggleBadge(inbox);
                  }}
                >
                  {inbox.badge ? "Leave out of the Inbox badge" : "Count in the Inbox badge"}
                </button>
                <button
                  type="button"
                  onClick={() => {
                    afterDelete();
                    setOpen(null);
                    inboxes.remove(inbox);
                  }}
                >
                  Delete inbox
                </button>
              </div>
            ) : null}
          </div>
        );
      })}
      {inboxes.footer}
    </div>
  );
}

/** The bar along the top of the page's panel, as the hosted `PageHeader`:
 *  the page's name with a short line beside it, its controls on the right. */
export function PageBar({
  title,
  detail,
  actions,
}: {
  title: ReactNode;
  detail?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <header className="local-page-bar">
      <div className="local-page-title">
        <h1>{title}</h1>
        {detail ? <p>{detail}</p> : null}
      </div>
      {actions ? <div className="local-page-actions">{actions}</div> : null}
    </header>
  );
}

/** What a page's refresh button says refreshing the page does. */
type Refresh = { refresh: () => Promise<unknown>; busy: boolean };
type PageRefresh = {
  /** Makes a button the page's refresh until the cleanup it returns runs;
   *  of several, the one drawn last. */
  register: (button: RefObject<Refresh>) => () => void;
  /** Refreshes the page with `button`, else the page's registered one,
   *  unless a refresh is under way or the page is busy; false when the page
   *  has no refresh. */
  press: (button?: RefObject<Refresh>) => boolean;
  /** A refresh of this page is under way. */
  pending: boolean;
};
const PageRefreshContext = createContext<PageRefresh | null>(null);
export const PageRefreshProvider = PageRefreshContext.Provider;

/** The page's refresh, kept above the page so that its key (⌘R) has one
 *  place to go and a refresh under way outlives the button, as when a pull
 *  request's walkthrough gives way to its full review. `scope` names the
 *  page shown: a refresh started on another page is not this one's. */
export function usePageRefresh(scope: string): PageRefresh {
  const buttons = useRef<RefObject<Refresh>[]>([]);
  const [pending, setPending] = useState<string | null>(null);
  const running = useRef<string | null>(null);
  const shown = useRef(scope);
  shown.current = scope;
  const register = useCallback((ref: RefObject<Refresh>) => {
    buttons.current.push(ref);
    return () => {
      buttons.current = buttons.current.filter((each) => each !== ref);
    };
  }, []);
  const press = useCallback((pressed?: RefObject<Refresh>) => {
    const page = (pressed ?? buttons.current.at(-1))?.current;
    if (!page) return false;
    const at = shown.current;
    if (running.current === at || page.busy) return true;
    running.current = at;
    setPending(at);
    const done = () => {
      if (running.current !== at) return;
      running.current = null;
      setPending(null);
    };
    // The page reports its own failures.
    void Promise.resolve().then(page.refresh).then(done, done);
    return true;
  }, []);
  return useMemo(
    () => ({ register, press, pending: pending === scope }),
    [register, press, pending, scope],
  );
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
 *  (⌘R, Ctrl+R elsewhere) presses it, so a page with one is never reloaded
 *  whole. */
export function RefreshButton({
  refresh,
  busy = false,
  label = "Refresh",
}: {
  refresh: () => Promise<unknown>;
  busy?: boolean;
  label?: string;
}) {
  const page = useContext(PageRefreshContext);
  const self = useRef<Refresh>({ refresh, busy });
  useEffect(() => {
    self.current = { refresh, busy };
  });
  const register = page?.register;
  useEffect(() => register?.(self), [register]);
  const shown = busy || Boolean(page?.pending);
  return (
    <button
      type="button"
      aria-label={label}
      title={`${label} (${REFRESH_KEY_LABEL})`}
      aria-keyshortcuts={REFRESH_KEY_SHORTCUT}
      aria-busy={shown || undefined}
      aria-disabled={shown || undefined}
      onClick={() => {
        if (page) page.press(self);
        else if (!busy) void refresh();
      }}
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
        <span className="flex h-10 w-10 flex-none items-center justify-center rounded-(--radius-md) bg-(--surface-wash) text-(--text-primary)">
          {icon}
        </span>
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <h2 id={id} className="text-base font-semibold">
              {title}
            </h2>
            {badge}
          </div>
          {detail ? <p className="mt-0.5 text-xs text-(--text-secondary)">{detail}</p> : null}
        </div>
      </div>
      {actions ? (
        <div className="flex flex-none flex-wrap items-center gap-2">{actions}</div>
      ) : null}
    </header>
  );
}
