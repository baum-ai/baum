import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from "react";
import { formatRelative } from "@baum/viewer/components/format";
import { ChevronDownIcon, TerminalIcon } from "@baum/viewer/components/icons";
import { Markdown } from "@baum/viewer/components/markdown";
import { Badge, Button, CloseButton, LiveDot, SegmentedControl } from "@baum/viewer/components/ui";
import { cn } from "@baum/viewer/components/classnames";
import { agentName, homePath, LocalRequestError } from "./api";
import {
  closeConversation,
  confirmProposal,
  dismissProposal,
  listConversations,
  listProposals,
  readConversation,
  readPanelSettings,
  reportPage,
  saveProposal,
  sendTurn,
  sessionOf,
  startConversation,
  stopTurn,
  type Conversation,
  type ConversationSummary,
  type OperationSummary,
  type PanelMode,
  type PanelPosition,
  type PanelSettings,
  type Progress,
  type Proposal,
  type SummaryField,
  type ThemeSave,
} from "./console-api";
import { useContextMenu, type MenuItem } from "./context-menu";
import { ConfirmDialog } from "./group-dialogs";
import { modelName } from "./desktop/setup-model";
import { useNow } from "./live";
import { LoadingStatus, NineDots } from "./nine-dots";
import { hrefOf, onPull, pageLabel, pickedFocus, samePage, type PageContext } from "./page-context";
import { pendingReviewState } from "./pending-review-model";
import { usePreferences } from "./preferences";
import { Diff } from "./prompt-editor";
import { AgentMark } from "./sessions";
import { MESSAGE, POLL_RUNNING, TranscriptReader, useLiveTranscript } from "./session-transcript";
import { isPanelKey, KeyBar, PANEL_KEY, PANEL_KEY_LABEL } from "./shortcuts";
import { useToast } from "./toast";
import { useVisibleInterval } from "./visible-interval";
import {
  MIN_HEIGHT,
  MIN_WIDTH,
  sessionPanel,
  useSessionPanel,
  type PanelState,
} from "./session-panel-state";
import "./session-panel.css";

export { PANEL_KEY, PANEL_KEY_LABEL } from "./shortcuts";
export {
  DEFAULT_HEIGHT,
  DEFAULT_WIDTH,
  MIN_HEIGHT,
  MIN_WIDTH,
  resetSessionPanel,
  sessionPanel,
  useSessionPanel,
} from "./session-panel-state";

/** A change to a conversation, as the daemon's `console` event carries it:
 *  ids and state, never text. */
export type ConsoleEvent = { conversation: string; kind: string; state: string };

const bus = new EventTarget();
/** Hands the panel a `console` event's data, from the app's event stream. */
export function publishConsoleEvent(data: string) {
  let event: ConsoleEvent;
  try {
    event = JSON.parse(data) as ConsoleEvent;
  } catch {
    return;
  }
  if (typeof event?.conversation !== "string") return;
  bus.dispatchEvent(new CustomEvent("console", { detail: event }));
}
/** Tells the panel the event stream (re)connected, so it may have missed
 *  events: everything it shows is read again. */
export function publishConsoleResync() {
  bus.dispatchEvent(new CustomEvent("console", { detail: null }));
}
/** Tells the panel its setting changed: Settings saved it, or the fix or
 *  review agent it follows. */
export function panelSettingsChanged() {
  bus.dispatchEvent(new Event("settings"));
}
/** Calls `listener` whenever the panel's setting may have changed; returns
 *  the unsubscribe. */
export function onPanelSettingsChanged(listener: () => void) {
  bus.addEventListener("settings", listener);
  return () => bus.removeEventListener("settings", listener);
}

function useConsoleEvents(handler: (event: ConsoleEvent | null) => void) {
  const latest = useRef(handler);
  latest.current = handler;
  useEffect(() => {
    const listener = (event: Event) => latest.current((event as CustomEvent).detail ?? null);
    bus.addEventListener("console", listener);
    return () => bus.removeEventListener("console", listener);
  }, []);
}

/** A read that never overlaps itself: one asked for during another follows
 *  it, once. */
function useCoalesced(read: () => Promise<void>) {
  const latest = useRef(read);
  latest.current = read;
  const busy = useRef(false);
  const again = useRef(false);
  return useCallback(() => {
    if (busy.current) {
      again.current = true;
      return;
    }
    busy.current = true;
    void (async () => {
      try {
        do {
          again.current = false;
          await latest.current().catch(() => {});
        } while (again.current);
      } finally {
        busy.current = false;
      }
    })();
  }, []);
}

/** How long the page reported waits for the address to settle. */
export const REPORT_DEBOUNCE = 250;

/** Reports the page the person has open to the daemon (`PUT
 *  api/console/page`) on every navigation, a moment after it settles, and
 *  again after the event stream reconnects (a restarted daemon knows no
 *  page), whether or not the panel is open: a conversation's agent always
 *  knows the current page. Not while the daemon has no session panel. */
export function usePageReport(context: PageContext, enabled: boolean) {
  const key = JSON.stringify(context);
  const latest = useRef(context);
  latest.current = context;
  const [epoch, setEpoch] = useState(0);
  useConsoleEvents((event) => {
    if (event === null) setEpoch((n) => n + 1);
  });
  // biome-ignore lint/correctness/useExhaustiveDependencies: `key` stands for `context`
  useEffect(() => {
    if (!enabled) return;
    const timer = setTimeout(
      () => void reportPage(latest.current).catch(() => {}),
      REPORT_DEBOUNCE,
    );
    return () => clearTimeout(timer);
  }, [key, enabled, epoch]);
}

/** The daemon's conversations, newest first, and whether it runs the panel
 *  at all; read when the app starts and again on each change. */
function useConversations() {
  const [list, setList] = useState<ConversationSummary[]>([]);
  const [error, setError] = useState("");
  // Whether a read answered, or failed, at least once.
  const [loaded, setLoaded] = useState(false);
  const read = useCoalesced(async () => {
    try {
      const answer = await listConversations();
      sessionPanel.setAvailable(answer !== null);
      setList(answer ?? []);
      setError("");
    } catch (e) {
      // A daemon too old for the panel answers 404: it has none.
      if (e instanceof LocalRequestError && e.status === 404) sessionPanel.setAvailable(false);
      setError(e instanceof Error ? e.message : "The sessions could not be read.");
    } finally {
      setLoaded(true);
    }
  });
  useEffect(() => read(), [read]);
  useConsoleEvents((event) => {
    if (!event || !["text", "proposals"].includes(event.kind)) read();
  });
  return { list, error, read, loaded };
}

/** The panel's setting: the agent, model and mode a new conversation opens
 *  with, and where the panel opens. */
function usePanelSettings() {
  const [settings, setSettings] = useState<PanelSettings | null>(null);
  const read = useCoalesced(async () => {
    try {
      setSettings(await readPanelSettings());
    } catch {
      // A new conversation still opens with the daemon's defaults.
    }
  });
  useEffect(() => {
    read();
    return onPanelSettingsChanged(() => read());
  }, [read]);
  return settings;
}

/** One conversation, read when shown and again on each of its events, and
 *  every few seconds while it runs in case an event was missed. */
function useConversation(id: string | null) {
  const [conversation, setConversation] = useState<Conversation | null>(null);
  const [error, setError] = useState<{ message: string; code?: string } | null>(null);
  const current = useRef(id);
  current.current = id;
  const read = useCoalesced(async () => {
    const asked = current.current;
    if (!asked) return;
    try {
      const answer = await readConversation(asked);
      if (current.current !== asked) return;
      setConversation(answer);
      setError(null);
    } catch (e) {
      if (current.current !== asked) return;
      setError({
        message: e instanceof Error ? e.message : "The session could not be read.",
        code: e instanceof LocalRequestError ? e.code : undefined,
      });
    }
  });
  useEffect(() => {
    setConversation(null);
    setError(null);
    if (id) read();
  }, [id, read]);
  useConsoleEvents((event) => {
    if (!id) return;
    if (event === null || (event.conversation === id && event.kind !== "proposals")) read();
  });
  useVisibleInterval(read, conversation?.state === "running" ? 4000 : null);
  const update = useCallback(
    (next: Conversation) => {
      if (next.id === current.current) setConversation(next);
    },
    [setConversation],
  );
  return { conversation, error, update };
}

/** A conversation's open proposals, read when shown and on its `proposals`
 *  events. */
function useProposals(id: string | null) {
  const [proposals, setProposals] = useState<Proposal[]>([]);
  const current = useRef(id);
  current.current = id;
  const read = useCoalesced(async () => {
    const asked = current.current;
    if (!asked) return;
    const answer = await listProposals(asked);
    if (current.current === asked) setProposals(answer);
  });
  useEffect(() => {
    setProposals([]);
    if (id) read();
  }, [id, read]);
  useConsoleEvents((event) => {
    if (!id) return;
    if (event === null || (event.conversation === id && event.kind === "proposals")) read();
  });
  return { proposals, read };
}

/** The conversation an Ask on a pull request's page opens: the one shown when
 *  it is that pull request's and still open, else that pull request's most
 *  recent open one, which works in its checkout and keeps what its agent
 *  read, else a new one (null), started on the pull request's page by the
 *  first message. A conversation started on another page, or another pull
 *  request's, is never reused. `list` is newest first, as the daemon lists. */
export function conversationFor(
  list: ConversationSummary[],
  current: string | null,
  pull: { repository: string; pull_request: number },
): string | null {
  const ofPull = (c: ConversationSummary) =>
    c.state !== "closed" &&
    c.pull_request === pull.pull_request &&
    (c.repository ?? "").toLowerCase() === pull.repository.toLowerCase();
  const shown = list.find((c) => c.id === current);
  if (shown && ofPull(shown)) return shown.id;
  return list.find(ofPull)?.id ?? null;
}

/** The panel's button in the sidebar: shows or hides it, and says when the
 *  daemon runs no session panel. */
export function SessionPanelButton() {
  const { open, available } = useSessionPanel();
  const unavailable = available === false;
  const title = unavailable
    ? "Session panel: unavailable on this computer"
    : `${open ? "Hide" : "Show"} the session panel (${PANEL_KEY_LABEL})`;
  return (
    <button
      type="button"
      className={cn("local-rail-link local-rail-panel", unavailable && "unavailable")}
      aria-pressed={open}
      aria-keyshortcuts={PANEL_KEY}
      aria-description={unavailable ? "Unavailable" : undefined}
      title={title}
      onClick={() => sessionPanel.toggle()}
    >
      <TerminalIcon size={18} className="local-rail-glyph" />
      <span className="local-rail-label">Session</span>
      {unavailable ? (
        <span aria-hidden="true" className="local-rail-badge caution">
          !
        </span>
      ) : null}
      <kbd aria-hidden="true" className="key-hint local-rail-key">
        {PANEL_KEY_LABEL}
      </kbd>
    </button>
  );
}

/** The session panel's host, mounted once in the app's frame: it reports the
 *  page, knows whether the daemon runs the panel, takes the panel's key and
 *  draws the panel while it is open. `page` is what the person has open and
 *  `title` its subject's name, where the app knows it; `liveTick` counts the
 *  app's `live` events, which bring a session's transcript up to date. */
export function SessionPanelHost({
  page,
  title,
  liveTick,
  go,
}: {
  page: PageContext;
  title?: string;
  liveTick: number;
  /** Goes to a page of the app, as its links do. */
  go: (href: string) => void;
}) {
  const panel = useSessionPanel();
  const conversations = useConversations();
  const settings = usePanelSettings();
  usePageReport(page, panel.available !== false);
  // The panel's key works anywhere, a text field included, so it is not one
  // of the page's keys (`useShortcuts`): from the composer it hides the
  // panel, from anywhere else it shows the panel, or puts the keys in the
  // composer of the one already open. A dialog keeps its keys.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.isComposing || !isPanelKey(event)) return;
      if (document.querySelector("dialog[open]")) return;
      event.preventDefault();
      if (document.activeElement?.matches(COMPOSER)) sessionPanel.hide();
      else sessionPanel.show();
    };
    addEventListener("keydown", onKeyDown);
    return () => removeEventListener("keydown", onKeyDown);
  }, []);
  // An Ask on a pull request's page opens the panel on that pull request's
  // conversation, once the conversations are known; the part of the review it
  // picked rides in the page's focus until the person leaves the page.
  const asked = useRef(0);
  const ask = panel.ask;
  useEffect(() => {
    if (!ask || ask.seq === asked.current || !conversations.loaded) return;
    asked.current = ask.seq;
    sessionPanel.show(conversationFor(conversations.list, panel.conversation, ask));
  }, [ask, conversations.loaded, conversations.list, panel.conversation]);
  useEffect(() => {
    if (ask && !onPull(page, ask)) sessionPanel.dropAsk();
  }, [ask, page]);
  if (!panel.open) return null;
  return (
    <SessionPanel
      panel={panel}
      page={page}
      title={title}
      liveTick={liveTick}
      conversations={conversations.list}
      settings={settings}
      go={go}
    />
  );
}

/** The panel's composer, where its key hides the panel. */
const COMPOSER = ".session-panel-compose textarea";

const MODES: { value: PanelMode; label: string }[] = [
  { value: "auto", label: "Auto" },
  { value: "full", label: "Full access" },
  { value: "workspace", label: "Workspace" },
];
/** How the empty panel says the agent will work, by mode. */
const modeWorks: Record<PanelMode, string> = {
  auto: "with its own reviewer approving each action",
  full: "with your full access",
  workspace: "in its folder",
};
const modeLabel = (mode: string) => MODES.find((m) => m.value === mode)?.label ?? mode;
const modeDetail: Record<PanelMode, string> = {
  auto: "Your agent's own reviewer approves each action or blocks the risky ones.",
  full: "Runs your agent as you would with every prompt approved.",
  workspace: "Edits files in its directory; what it would have to ask for is refused.",
};

/** What a conversation's model is called in the header: its name without
 *  its maker's, which the agent's mark beside it already says. */
export function shortModelName(model: string) {
  return model ? modelName(model).replace(/^Claude /, "") : "default model";
}

/** The overlay, over the page like a drop-down terminal: the bar naming what
 *  the agent sees and where it works, the session as its transcript, the
 *  proposals to save or confirm, and the composer. */
function SessionPanel({
  panel,
  page,
  title,
  liveTick,
  conversations,
  settings,
  go,
}: {
  panel: PanelState;
  page: PageContext;
  title?: string;
  liveTick: number;
  conversations: ConversationSummary[];
  settings: PanelSettings | null;
  go: (href: string) => void;
}) {
  const sideFits = useSideFits();
  const chosen = settings?.position ?? "bottom";
  // A window too narrow for the panel beside the page opens it from the foot.
  const position = (chosen === "left" || chosen === "right") && !sideFits ? "bottom" : chosen;
  const side = position === "left" || position === "right";
  const id = panel.conversation;
  const { conversation, error: readError, update } = useConversation(id);
  const { proposals, read: readProposals } = useProposals(id);
  const [mode, setMode] = useState<PanelMode | null>(null);
  const chosenMode = mode ?? settings?.mode ?? "auto";
  // The turn sent from this window: its text and how many prompts the
  // transcript had then, so the prompt shows at once and until the
  // transcript has it.
  const [turn, setTurn] = useState<{ text: string; prompts: number } | null>(null);
  const [sending, setSending] = useState<"starting" | "sending" | null>(null);
  const [stopping, setStopping] = useState(false);
  const [sendError, setSendError] = useState("");
  const prompts = useRef(0);
  const root = useRef<HTMLElement>(null);
  const composer = useRef<HTMLTextAreaElement>(null);
  const menu = useContextMenu();
  const picker = useRef<HTMLButtonElement>(null);
  const toast = useToast();
  const now = useNow();
  // The conversation a message just started: choosing it keeps the turn.
  const started = useRef<string | null>(null);
  useEffect(() => {
    if (id !== null && started.current === id) {
      started.current = null;
      return;
    }
    setTurn(null);
    setSendError("");
    setStopping(false);
    prompts.current = 0;
  }, [id]);
  // Opened by its key, its button or a new choice of conversation, the
  // composer takes the keys.
  useLayoutEffect(() => {
    composer.current?.focus({ preventScroll: true });
  }, [panel.focus]);
  // Hidden while the keys were in it, they go back to what had them before
  // it opened, or to the page.
  // Measured as it unmounts, before its elements leave the page and the focus
  // with them.
  useLayoutEffect(() => {
    const element = root.current;
    return () => {
      const focused = document.activeElement;
      if (!(focused instanceof HTMLElement) || !element?.contains(focused)) return;
      focused.blur();
      const back = sessionPanel.takeReturnTo();
      if (back instanceof HTMLElement && back.isConnected && !element.contains(back))
        back.focus({ preventScroll: true });
    };
  }, []);
  // The page under it scrolls clear of it: its frame knows how much of it the
  // panel covers, and from which edge (`--session-panel-inset`). From a side,
  // that is the panel's width, and the page's panel narrows beside it.
  useLayoutEffect(() => {
    const element = root.current;
    const frame = element?.closest<HTMLElement>(".local-frame");
    if (!element || !frame) return;
    const measure = () => {
      const box = element.getBoundingClientRect();
      const edge = frame.getBoundingClientRect();
      const covered =
        position === "bottom"
          ? edge.bottom - box.top
          : position === "top"
            ? box.bottom - edge.top
            : box.width;
      frame.style.setProperty("--session-panel-inset", `${Math.max(0, Math.round(covered))}px`);
    };
    frame.dataset.sessionPanel = position;
    measure();
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    observer?.observe(element);
    addEventListener("resize", measure);
    return () => {
      observer?.disconnect();
      removeEventListener("resize", measure);
      delete frame.dataset.sessionPanel;
      frame.style.removeProperty("--session-panel-inset");
    };
  }, [position]);
  const available = panel.available !== false;
  const running = conversation?.state === "running";
  const closed = conversation?.state === "closed";
  // The newest open theme proposal is previewed on the whole app until it is
  // saved, dismissed or the conversation closes; unmounting restores too.
  const { setPreview } = usePreferences();
  const previewed = closed ? undefined : newestTheme(proposals);
  const previewKey = previewed ? JSON.stringify([previewed.id, previewed.after]) : "";
  // A save holds the preview until the preferences reflect it: the proposals
  // change before the save answers, and the preview must not flicker back to
  // the old theme meanwhile.
  const [holding, setHolding] = useState(0);
  const holdPreview = useCallback(async (until: Promise<unknown>) => {
    setHolding((n) => n + 1);
    try {
      await until;
    } finally {
      setHolding((n) => n - 1);
    }
  }, []);
  useEffect(() => {
    if (!previewKey) {
      if (holding === 0) setPreview(null);
      return;
    }
    const [id, after] = JSON.parse(previewKey) as [string, Proposal["after"]];
    const theme = proposedTheme({ after } as Proposal);
    if (!theme) return;
    setPreview({
      id: `proposal:${id}`,
      name: theme.name,
      base: theme.base,
      tokens: theme.tokens,
    });
  }, [previewKey, holding, setPreview]);
  useEffect(() => () => setPreview(null), [setPreview]);
  // An id from an earlier window that the daemon no longer has opens a new
  // conversation instead.
  useEffect(() => {
    if (id && readError?.code === "conversation_not_found") sessionPanel.select(null);
  }, [id, readError]);
  // The model the session runs, once its transcript names it.
  const [usedModel, setUsedModel] = useState("");
  useEffect(() => setUsedModel(""), [id]);
  const [closing, setClosing] = useState(false);
  const latestPage = useRef(page);
  latestPage.current = page;

  const send = async (event?: FormEvent) => {
    event?.preventDefault();
    const text = panel.draft.trim();
    if (!text || sending || !available) return;
    // A message while the agent answers waits in the composer.
    if (running) {
      setSendError("The agent is still answering: wait for the turn to finish, or Stop it.");
      return;
    }
    // Shown at once: the prompt, the composer emptied and the button working.
    setSending(id && !closed ? "sending" : "starting");
    setSendError("");
    setTurn({ text, prompts: prompts.current });
    sessionPanel.setDraft("");
    try {
      let target = id && !closed ? id : null;
      if (!target) {
        const created = await startConversation({ mode: chosenMode, page });
        target = created.id;
        prompts.current = 0;
        setTurn({ text, prompts: 0 });
        started.current = target;
        sessionPanel.select(target);
        update(created);
      }
      setSending("sending");
      // The page now, not the one sent before creating: a navigation while
      // the conversation started was reported, and creating overwrote it.
      update(await sendTurn(target, text, latestPage.current));
      setMode(null);
    } catch (e) {
      setTurn(null);
      // What was typed comes back, unless something new was typed meanwhile.
      if (!sessionPanel.draft()) sessionPanel.setDraft(text);
      setSendError(sendErrorText(e));
    } finally {
      setSending(null);
    }
  };
  const stop = async () => {
    if (!id || stopping) return;
    setStopping(true);
    try {
      update(await stopTurn(id));
    } catch (e) {
      setSendError(e instanceof Error ? e.message : "The turn could not be stopped.");
    } finally {
      setStopping(false);
    }
  };
  const pickerItems = (): MenuItem[] => {
    const items: MenuItem[] = [
      { label: "New session", checked: id === null, onSelect: () => sessionPanel.select(null) },
    ];
    for (const c of conversations)
      items.push({
        label: c.title || "Untitled session",
        checked: c.id === id,
        note: [
          agentName(c.agent),
          c.state === "running" ? "working" : c.state === "closed" ? "closed" : null,
          formatRelative(c.updated_at, now),
        ]
          .filter(Boolean)
          .join(" · "),
        onSelect: () => sessionPanel.select(c.id),
      });
    if (id && conversation && !closed)
      items.push({
        label: "Close this session",
        danger: true,
        note: running ? "Stops the turn running; its directory stays" : "Its directory stays",
        // While the agent works, closing it stops the turn: asked first.
        onSelect: () => (running ? setClosing(true) : close()),
      });
    return items;
  };
  const close = () => {
    if (!id) return;
    void closeConversation(id).then(update, (e: unknown) =>
      toast({
        title: "Could not close the session",
        detail: e instanceof Error ? e.message : undefined,
        error: true,
      }),
    );
  };
  // The message the panel's keys are on, marked as the transcript page marks
  // its own.
  const marked = useRef<HTMLElement | null>(null);
  const mark = (target: HTMLElement | null) => {
    marked.current?.classList.remove("local-message-current");
    target?.classList.add("local-message-current");
    marked.current = target;
  };
  const panelBody = () => root.current?.querySelector<HTMLElement>(".session-panel-body") ?? null;
  /** The last message in view, where reading starts. */
  const latestInView = (body: HTMLElement) => {
    const foot = body.getBoundingClientRect().bottom;
    return [...body.querySelectorAll<HTMLElement>(MESSAGE)].findLast(
      (m) => m.getBoundingClientRect().top < foot - 2,
    );
  };
  /** Gives the keys to the panel, out of its fields, on the latest message
   *  in view: the reading starts from what is being read. */
  const read = () => {
    root.current?.focus({ preventScroll: true });
    const body = panelBody();
    if (body && !marked.current?.isConnected) mark(latestInView(body) ?? null);
  };
  const write = () => composer.current?.focus({ preventScroll: true });
  /** The panel's keys while it has them and no field does: j and k go a
   *  message at a time, g and G to the session's start and end, the arrows
   *  and Page Up and Down scroll, Space shows all of the marked message or
   *  less again, a opens its turn's agent activity, i or Enter go back to
   *  the composer, and q hides the panel. The composer is the session's
   *  last line: j past the last message, or ↓ at the session's end, goes to
   *  it, and ↑ at its start goes back to reading. In a field only that ↑ and
   *  Page Up and Down are taken, so the session scrolls while a message is
   *  written. True when the key was one
   *  of them. */
  const readKey = (event: ReactKeyboardEvent, inField: boolean): boolean => {
    const body = panelBody();
    if (!body) return false;
    const reduced = matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
    const scroll = (top: number) =>
      body.scrollTo({ top, behavior: reduced ? "instant" : "smooth" });
    const view = body.getBoundingClientRect();
    const { key } = event;
    if (key === "PageDown" || key === "PageUp") {
      scroll(body.scrollTop + (key === "PageDown" ? 1 : -1) * body.clientHeight * 0.8);
      return true;
    }
    if (inField) {
      // ↑ with the caret at the composer's very start, where it has nowhere
      // to go, leaves it for the session.
      const field = event.target;
      if (
        key !== "ArrowUp" ||
        event.shiftKey ||
        field !== composer.current ||
        composer.current.selectionStart !== 0 ||
        composer.current.selectionEnd !== 0
      )
        return false;
      read();
      return true;
    }
    if (key === "ArrowDown" || key === "ArrowUp") {
      const atEnd = body.scrollTop + body.clientHeight >= body.scrollHeight - 1;
      if (key === "ArrowDown" && atEnd) write();
      else body.scrollTo({ top: body.scrollTop + (key === "ArrowDown" ? 48 : -48) });
      return true;
    }
    if (key === "i" || key === "Enter") {
      write();
      return true;
    }
    if (key === "q") {
      sessionPanel.hide();
      return true;
    }
    if (key === "g" || key === "G") {
      mark(null);
      scroll(key === "g" ? 0 : body.scrollHeight);
      return true;
    }
    const messages = [...body.querySelectorAll<HTMLElement>(MESSAGE)];
    const current = marked.current?.isConnected ? marked.current : null;
    if (key === "j" || key === "k") {
      const at = current ? messages.indexOf(current) : -1;
      // From no message, the first or the last of those in view.
      const target =
        at >= 0
          ? messages[at + (key === "j" ? 1 : -1)]
          : key === "j"
            ? messages.find((m) => m.getBoundingClientRect().bottom > view.top + 2)
            : messages.findLast((m) => m.getBoundingClientRect().top < view.bottom - 2);
      if (target) {
        mark(target);
        scroll(body.scrollTop + target.getBoundingClientRect().top - view.top - 12);
      } else if (key === "j") write();
      return true;
    }
    if (key === " " || key === "a") {
      const target = current ?? latestInView(body);
      const toggle =
        key === " "
          ? target?.querySelector<HTMLButtonElement>("button[aria-controls][aria-expanded]")
          : target
              ?.closest("li")
              ?.querySelector<HTMLButtonElement>('button[aria-label^="Agent activity"]');
      if (target && toggle) {
        mark(target);
        toggle.click();
      }
      return true;
    }
    return false;
  };
  const onKeyDown = (event: ReactKeyboardEvent) => {
    if (event.defaultPrevented || event.nativeEvent.isComposing) return;
    if (event.key !== "Escape") {
      const target = event.target as HTMLElement;
      // A menu or a dialog of the panel keeps its own keys, as a modified
      // key stays the app's or the browser's.
      if (menu.anchor !== null || target.closest("dialog, [role='menu']")) return;
      if (event.ctrlKey || event.metaKey || event.altKey) return;
      const inField =
        target instanceof HTMLTextAreaElement ||
        target instanceof HTMLInputElement ||
        target instanceof HTMLSelectElement;
      // Space and Enter on a button, a link or a fold press it.
      const presses =
        (event.key === " " || event.key === "Enter") &&
        target.closest("button, a[href], summary, [role='radio']") !== null;
      if (!presses && readKey(event, inField)) {
        event.preventDefault();
        event.stopPropagation();
        return;
      }
      // While the panel has the keys the page's are not the page's: a key
      // that types a character goes no further, but for the help's.
      if (!inField && event.key.length === 1 && event.key !== "?") event.stopPropagation();
      return;
    }
    const target = event.target as HTMLElement;
    event.preventDefault();
    // A field gives up the keys to the panel, and the panel, on the next
    // Escape, to the page, staying open: to what had them before it took
    // them, or to the page itself. The page under it never sees either.
    if (target instanceof HTMLTextAreaElement || target instanceof HTMLInputElement) {
      read();
      return;
    }
    const back = sessionPanel.takeReturnTo();
    if (back instanceof HTMLElement && back.isConnected && !root.current?.contains(back))
      back.focus({ preventScroll: true });
    // Nothing took them (the page's body had them): they are left to it.
    const focused = document.activeElement;
    if (focused instanceof HTMLElement && root.current?.contains(focused)) focused.blur();
  };
  const heading = conversation?.title || (id ? "Session" : "New session");
  // The part of the review an Ask picked, which the agent reads as the page's
  // focus.
  const picked = pickedFocus(page);
  const agent = conversation?.agent ?? settings?.agent ?? "claude-code";
  const model = conversation ? conversation.model || usedModel : (settings?.model ?? "");
  const directory = conversation?.directory;
  const looking = pageLabel(page, title);
  const headId = useId();
  return (
    <section
      ref={root}
      aria-labelledby={headId}
      className="session-panel"
      data-position={position}
      tabIndex={-1}
      style={
        side
          ? {
              width: `min(${panel.width}px, calc(100vw - var(--rail-width) - ${MIN_PAGE_WIDTH + SIDE_GAPS}px))`,
            }
          : { height: `min(${panel.height}px, calc(100dvh - 4rem))` }
      }
      onKeyDown={onKeyDown}
      // The message marked is the keys' own: given up once they leave the
      // panel, or go to one of its fields.
      onFocus={(event) => {
        const target = event.target;
        if (target instanceof HTMLTextAreaElement || target instanceof HTMLInputElement) mark(null);
      }}
      onBlur={(event) => {
        if (!(event.relatedTarget instanceof Node) || !root.current?.contains(event.relatedTarget))
          mark(null);
      }}
    >
      {menu.menu}
      {closing ? (
        <ConfirmDialog
          title="Close this session while the agent works?"
          action="Close session"
          danger
          pending={false}
          close={() => setClosing(false)}
          confirm={() => {
            setClosing(false);
            close();
          }}
        >
          Closing it stops the turn running now. Its directory and what the agent changed there
          stay.
        </ConfirmDialog>
      ) : null}
      {position !== "top" ? (
        <Grip position={position} size={side ? panel.width : panel.height} />
      ) : null}
      <header className="session-panel-head">
        <div className="session-panel-title">
          <TerminalIcon size={16} className="flex-none text-tertiary" />
          <h2 id={headId} title={heading}>
            {heading}
          </h2>
          {running ? (
            <span className="session-panel-state">
              <LiveDot className="text-status-positive" /> Working
            </span>
          ) : null}
          <Button
            ref={picker}
            variant="secondary"
            size="tiny"
            className="gap-1"
            aria-haspopup="menu"
            aria-expanded={menu.anchor !== null && menu.anchor === picker.current}
            onClick={() => {
              if (picker.current) menu.openFrom(picker.current, pickerItems(), "Sessions");
            }}
          >
            Sessions
            <ChevronDownIcon size={12} />
          </Button>
          <CloseButton label="Hide the session panel (Esc)" onClick={() => sessionPanel.hide()} />
        </div>
        <dl className="session-panel-facts">
          <div className="session-panel-looking">
            <dt>Looking at</dt>
            <dd data-testid="session-panel-page" title={looking}>
              {looking}
            </dd>
          </div>
          <div className="session-panel-directory">
            <dt>Directory</dt>
            <dd title={directory}>
              {directory ? (
                <code>{homePath(directory)}</code>
              ) : page.page === "pull_request" ? (
                `A checkout of ${page.repository} #${page.pull_request}, made when it starts`
              ) : (
                "A new empty folder, made when it starts"
              )}
            </dd>
          </div>
          <div>
            <dt>Agent</dt>
            <dd className="session-panel-agent">
              <AgentMark agent={agent} size={14} />
              <span>{shortModelName(model)}</span>
            </dd>
          </div>
          <div>
            <dt>Mode</dt>
            <dd>
              {conversation ? (
                <span title={modeDetail[conversation.mode]}>{modeLabel(conversation.mode)}</span>
              ) : (
                <SegmentedControl
                  label="Mode"
                  value={chosenMode}
                  items={MODES}
                  onChange={(next) => setMode(next)}
                />
              )}
            </dd>
          </div>
        </dl>
        {conversation?.directory_note ? (
          <p className="session-panel-note">{conversation.directory_note}</p>
        ) : null}
      </header>
      <FollowingBody>
        {!available ? (
          <div className="local-empty session-panel-empty">
            <h3>The session panel is unavailable</h3>
            <p>
              baum couldn't open its sessions on this computer, so the panel can't start one. The
              daemon's log says why; restarting the desktop app tries again.
            </p>
          </div>
        ) : id && !conversation && !readError && !turn ? (
          <LoadingStatus block>Reading the session…</LoadingStatus>
        ) : readError && !conversation ? (
          <p role="alert" className="session-panel-error">
            {readError.message}
          </p>
        ) : !conversation && !turn ? (
          <div className="local-empty session-panel-empty">
            <h3>Start a session</h3>
            <p>
              {agentName(agent)} works {modeWorks[chosenMode]}{" "}
              {page.page === "pull_request"
                ? "in a checkout of this pull request"
                : "in a new empty folder"}
              . It sees the page you have open, and asks you to save or confirm any change to baum.
            </p>
          </div>
        ) : (
          <ConversationView
            conversation={conversation}
            turn={turn}
            sending={sending}
            liveTick={liveTick}
            onTranscript={(count, used) => {
              prompts.current = count;
              if (used) setUsedModel(used);
            }}
          />
        )}
        {conversation?.error && !running ? (
          <p role="status" className="session-panel-error">
            {conversation.error}
          </p>
        ) : null}
        {id && proposals.length > 0 ? (
          <ProposalList
            conversation={id}
            proposals={proposals}
            page={page}
            go={go}
            changed={readProposals}
            holdPreview={holdPreview}
          />
        ) : null}
      </FollowingBody>
      {available ? (
        <form className="session-panel-composer" onSubmit={(event) => void send(event)}>
          {sendError ? (
            <p role="alert" className="session-panel-error">
              {sendError}
            </p>
          ) : null}
          {closed ? (
            <p className="session-panel-note">
              This session is closed. Choose New session in the panel's Sessions menu to go on.
            </p>
          ) : null}
          {picked ? (
            <p className="session-panel-focus">
              <span>
                About <strong>{picked.title}</strong>
              </span>
              <Button
                variant="secondary"
                size="compact"
                aria-label={`Stop asking about ${picked.title}`}
                onClick={() => {
                  sessionPanel.clearFocus();
                  write();
                }}
              >
                Remove
              </Button>
            </p>
          ) : null}
          <div className="session-panel-compose">
            <textarea
              ref={composer}
              aria-label="Message to the agent"
              placeholder={
                running
                  ? "The agent is working; write your next message…"
                  : conversation
                    ? "Reply to the agent…"
                    : "Ask the agent to do something…"
              }
              rows={1}
              value={panel.draft}
              disabled={closed}
              onChange={(event) => sessionPanel.setDraft(event.target.value)}
              onKeyDown={(event) => {
                if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing)
                  return;
                event.preventDefault();
                void send();
              }}
            />
            {running ? (
              <Button
                variant="secondary"
                size="compact"
                disabled={stopping}
                onClick={() => void stop()}
              >
                {stopping ? <NineDots size={12} /> : null}
                Stop
              </Button>
            ) : (
              <Button
                type="submit"
                size="compact"
                disabled={!!sending || closed || !panel.draft.trim()}
              >
                {sending ? <NineDots size={12} /> : null}
                {sending === "starting" ? "Starting" : sending ? "Sending" : "Send"}
                <kbd aria-hidden="true" className="key-hint">
                  ↵
                </kbd>
              </Button>
            )}
          </div>
        </form>
      ) : null}
      {/* Its keys, with the app's hints while Alt is held: the reading ones
          while the panel has the keys, the composer's while a field does. */}
      <KeyBar place="panel" className="session-panel-keys session-panel-keys-reading" />
      <footer aria-hidden="true" className="key-bar key-hint session-panel-keys">
        <span>
          <kbd>Esc</kbd>
          <kbd>↑</kbd> read the session
        </span>
        <span>
          <kbd>PgUp</kbd>
          <kbd>PgDn</kbd> scroll
        </span>
        <span>
          <kbd>{PANEL_KEY_LABEL}</kbd> hide
        </span>
      </footer>
      {position === "top" ? <Grip position={position} size={panel.height} /> : null}
    </section>
  );
}

/** Why a message was not sent, in words. */
function sendErrorText(error: unknown) {
  if (error instanceof LocalRequestError) {
    if (error.code === "busy") return "The agent is still answering the last message.";
    if (error.code === "closed") return "This session is closed: start a new one to go on.";
    if (error.code === "shutting_down") return "baum is shutting down, so the message wasn't sent.";
    if (error.code === "console_unavailable")
      return "The session panel is unavailable on this computer.";
  }
  return error instanceof Error ? error.message : "The message could not be sent.";
}

/** The narrowest the page beside a panel opened from a side is left. */
const MIN_PAGE_WIDTH = 320;

/** The edge the panel is resized by: dragged, or with the arrow keys across
 *  it while focused (↑ and ↓ from the top or the foot, ← and → from a side).
 *  `size` is the panel's height, or its width from a side. */
function Grip({ position, size }: { position: PanelPosition; size: number }) {
  const drag = useRef<{ start: number; size: number } | null>(null);
  const side = position === "left" || position === "right";
  // Which way along the pointer's axis the panel grows: away from its edge.
  const sign = position === "bottom" || position === "right" ? -1 : 1;
  const max = () =>
    side
      ? Math.round(window.innerWidth - railWidth() - MIN_PAGE_WIDTH - SIDE_GAPS)
      : Math.round(window.innerHeight - 64);
  const min = side ? MIN_WIDTH : MIN_HEIGHT;
  const set = (next: number) => {
    const bounded = Math.min(next, max());
    if (side) sessionPanel.setWidth(bounded);
    else sessionPanel.setHeight(bounded);
  };
  const along = (event: { clientX: number; clientY: number }) =>
    side ? event.clientX : event.clientY;
  return (
    // biome-ignore lint/a11y/useSemanticElements: a focusable separator is the resize handle's role
    <div
      role="separator"
      aria-orientation={side ? "vertical" : "horizontal"}
      aria-label="Resize the session panel"
      aria-valuenow={size}
      aria-valuemin={min}
      aria-valuemax={Math.max(min, max())}
      tabIndex={0}
      className="session-panel-grip"
      onPointerDown={(event: ReactPointerEvent<HTMLDivElement>) => {
        event.currentTarget.setPointerCapture?.(event.pointerId);
        drag.current = { start: along(event), size };
      }}
      onPointerMove={(event) => {
        if (!drag.current) return;
        set(drag.current.size + sign * (along(event) - drag.current.start));
      }}
      onPointerUp={() => {
        drag.current = null;
      }}
      onPointerCancel={() => {
        drag.current = null;
      }}
      onKeyDown={(event) => {
        const [less, more] = side ? ["ArrowLeft", "ArrowRight"] : ["ArrowUp", "ArrowDown"];
        if (event.key !== less && event.key !== more) return;
        event.preventDefault();
        // The key towards the page grows it.
        const grow = (event.key === less) === sign < 0;
        set(size + (grow ? 40 : -40));
      }}
    />
  );
}

/** The sidebar's room beside the page, in pixels. */
function railWidth() {
  return document.querySelector(".local-rail-lane")?.getBoundingClientRect().width ?? 0;
}

/** The room a side panel takes beyond its own width and the page's: the
 *  window's edge and the gap between them. */
const SIDE_GAPS = 24;

/** Whether the window holds the panel at its narrowest beside the page at
 *  its narrowest, again as the window is resized. */
function useSideFits() {
  const fits = () => window.innerWidth - railWidth() >= MIN_WIDTH + MIN_PAGE_WIDTH + SIDE_GAPS;
  const [value, setValue] = useState(fits);
  // biome-ignore lint/correctness/useExhaustiveDependencies: `fits` reads the window alone
  useEffect(() => {
    const measure = () => setValue(fits());
    measure();
    addEventListener("resize", measure);
    return () => removeEventListener("resize", measure);
  }, []);
  return value;
}

/** The panel's scrolling body, which stays at its end while the reader is
 *  there, as a terminal does, and keeps their place once they scroll up. */
function FollowingBody({ children }: { children: ReactNode }) {
  const body = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  useEffect(() => {
    const element = body.current;
    if (!element) return;
    element.scrollTop = element.scrollHeight;
    const follow = () => {
      if (pinned.current) element.scrollTop = element.scrollHeight;
    };
    const observer = new MutationObserver(follow);
    observer.observe(element, { subtree: true, childList: true, characterData: true });
    return () => observer.disconnect();
  }, []);
  return (
    <div
      ref={body}
      className="session-panel-body"
      onScroll={(event) => {
        const element = event.currentTarget;
        pinned.current = element.scrollTop + element.clientHeight >= element.scrollHeight - 48;
      }}
    >
      {children}
    </div>
  );
}

/** A conversation as its session's transcript, with what the transcript does
 *  not have yet: the prompt just sent, and while the agent works the answer
 *  streaming and the tool it runs. */
function ConversationView({
  conversation,
  turn,
  sending,
  liveTick,
  onTranscript,
}: {
  conversation: Conversation | null;
  turn: { text: string; prompts: number } | null;
  sending: "starting" | "sending" | null;
  liveTick: number;
  onTranscript: (prompts: number, model: string) => void;
}) {
  const session = sessionOf(conversation?.session_key);
  const running = conversation?.state === "running";
  if (conversation && session)
    return (
      <SessionTranscriptView
        key={`${conversation.id}:${session.agent}:${session.id}`}
        conversation={conversation.id}
        agent={session.agent}
        id={session.id}
        title={conversation.title}
        running={running}
        progress={conversation.progress}
        turn={turn}
        sending={sending}
        liveTick={liveTick}
        onTranscript={onTranscript}
      />
    );
  return (
    <Tail
      progress={conversation?.progress ?? null}
      running={running}
      turn={turn}
      sending={sending}
      showPrompt
      text={conversation?.progress?.text ?? ""}
    />
  );
}

/** Text as compared for being in a transcript: its words, one space apart. */
const words = (text: string) => text.replace(/\s+/g, " ").trim();

/** Whether a transcript's replies hold `message`, or the start of it: a reply
 *  may be shown shortened, and a message still streaming is the start of
 *  the reply it becomes. */
export function holdsMessage(replies: { text: string }[], message: string) {
  const start = words(message).slice(0, 200);
  return start !== "" && replies.some((reply) => words(reply.text).includes(start));
}

/** The conversation's session as its transcript, read through the
 *  conversation (`api/console/conversations/{id}/transcript`), so it is
 *  served whether or not the session is still listed, with what the
 *  transcript does not hold yet under it. */
function SessionTranscriptView({
  conversation,
  agent,
  id,
  title,
  running,
  progress,
  turn,
  sending,
  liveTick,
  onTranscript,
}: {
  conversation: string;
  agent: string;
  id: string;
  title?: string;
  running: boolean;
  progress: Progress | null;
  turn: { text: string; prompts: number } | null;
  sending: "starting" | "sending" | null;
  liveTick: number;
  onTranscript: (prompts: number, model: string) => void;
}) {
  const { state } = useLiveTranscript(agent, id, {
    tick: liveTick,
    poll: running ? POLL_RUNNING : null,
    route: `api/console/conversations/${encodeURIComponent(conversation)}/transcript`,
  });
  const read = state.kind === "ready" ? state.transcript : null;
  const count = read?.session.prompts.length ?? 0;
  const model = read?.session.models[0] ?? "";
  useEffect(() => onTranscript(count, model), [count, model, onTranscript]);
  // A transcript of nothing but the first prompt has no trace to draw yet:
  // the prompt shows as just sent until the agent's first step is in it.
  const transcript = read?.session.has_inner_trace ? read : null;
  // The transcript has caught up once it holds the prompt sent (any prompt,
  // for a turn sent elsewhere). Then only the turn's latest message, while
  // the transcript does not hold it yet, shows under it; before, the whole
  // answer so far does.
  const last = transcript?.session.prompts.at(-1);
  const holdsPrompt = transcript !== null && count > (turn ? turn.prompts : count - 1);
  const latest = progress?.message ?? "";
  const text = !holdsPrompt
    ? (progress?.text ?? "")
    : latest && !holdsMessage(last?.replies ?? [], latest)
      ? latest
      : "";
  return (
    <>
      {transcript && state.kind === "ready" ? (
        <TranscriptReader
          transcript={transcript}
          version={state.version}
          summary={state.summary}
          live={running}
          title={title}
          openLastReply
        />
      ) : state.kind === "loading" && !turn && !running ? (
        <LoadingStatus block>Reading the session's transcript…</LoadingStatus>
      ) : (state.kind === "unavailable" || state.kind === "failed") && !running ? (
        <p className="local-transcript-note">
          {progress?.text
            ? "Only the last answer is shown: the session's transcript can't be read. "
            : "The session's transcript can't be read. "}
          {state.message}
        </p>
      ) : null}
      <Tail
        progress={progress}
        running={running}
        turn={turn}
        sending={sending}
        showPrompt={!holdsPrompt}
        text={text}
      />
    </>
  );
}

/** What the transcript does not show yet: the prompt sent, the answer so far
 *  and the tool running, in the transcript's own log style. */
function Tail({
  progress,
  running,
  turn,
  sending,
  showPrompt,
  text: shownText,
}: {
  progress: Progress | null;
  running: boolean;
  turn: { text: string; prompts: number } | null;
  sending: "starting" | "sending" | null;
  showPrompt: boolean;
  /** The answer, or the part of it, the transcript does not hold yet. */
  text: string;
}) {
  const text = shownText.trim();
  if (!(turn && showPrompt) && !text && !running && !sending) return null;
  return (
    <div className="session-panel-tail" aria-live="polite">
      {turn && showPrompt ? (
        <div className="session-panel-prompt" data-message="human">
          {turn.text}
        </div>
      ) : null}
      {text ? (
        <Markdown html={false} className="session-panel-answer">
          {text}
        </Markdown>
      ) : null}
      {sending === "starting" ? (
        <p className="session-panel-working">
          <NineDots size={12} /> Starting the session…
        </p>
      ) : running || sending ? (
        <p className="session-panel-working">
          <LiveDot className="text-status-positive" />
          {progress?.tool ? (
            <span className="session-panel-tool">{progress.tool}</span>
          ) : (
            <span>Working…</span>
          )}
        </p>
      ) : null}
    </div>
  );
}

/** The conversation's open proposals, each to save or confirm, or dismiss. */
function ProposalList({
  conversation,
  proposals,
  page,
  go,
  changed,
  holdPreview,
}: {
  conversation: string;
  proposals: Proposal[];
  page: PageContext;
  go: (href: string) => void;
  changed: () => void;
  holdPreview: (until: Promise<unknown>) => Promise<unknown>;
}) {
  return (
    <section aria-label="Proposals" className="session-panel-proposals">
      {proposals.map((proposal) => (
        <ProposalCard
          key={proposal.id}
          conversation={conversation}
          proposal={proposal}
          page={page}
          go={go}
          changed={changed}
          holdPreview={holdPreview}
        />
      ))}
    </section>
  );
}

/** A proposal's value as text: a string as it is, anything else as JSON. */
export function shown(value: unknown): string {
  if (value === undefined || value === null) return "";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  if (typeof value === "object" && !Array.isArray(value)) {
    const entries = Object.entries(value);
    // One text (a prompt's `{text}`) is that text, its lines its own.
    if (entries.length === 1 && typeof entries[0]![1] === "string") return entries[0]![1];
    // Plain fields are read as lines, a text of several lines under its name.
    if (entries.every(([, v]) => v === null || ["string", "number", "boolean"].includes(typeof v)))
      return entries
        .map(([key, v]) =>
          typeof v === "string" && v.includes("\n") ? `${key}:\n${v}` : `${key}: ${v ?? "none"}`,
        )
        .join("\n");
  }
  return JSON.stringify(value, null, 2);
}

/** A prompt's change (`{text}`, null for the default): what the default
 *  stands for, said in words beside the diff. */
function textChange(value: unknown): { text: string | null } | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const keys = Object.keys(value);
  const text = (value as { text?: unknown }).text;
  return keys.length === 1 && keys[0] === "text" && (text === null || typeof text === "string")
    ? { text: text as string | null }
    : null;
}

/** A colour theme proposal: the one proposal that is for no page in
 *  particular (the "app" sentinel), which the app previews as it is proposed. */
const THEME_PAGE = "app";
const isThemeProposal = (proposal: Proposal) =>
  proposal.kind === "diff" && proposal.page === THEME_PAGE && proposal.target === "theme";

type ThemeProposed = {
  /** The custom theme this edits, when it does. */
  theme_id?: string;
  name: string;
  base: "light" | "dark";
  tokens: Record<string, string>;
  contrast_warnings: { foreground: string; background: string; ratio: number }[];
};

/** The theme a proposal's After holds, or null when it is not one. */
function proposedTheme(proposal: Proposal): ThemeProposed | null {
  const after = proposal.after as Partial<ThemeProposed> | null | undefined;
  if (!after || typeof after !== "object" || typeof after.name !== "string") return null;
  if (after.base !== "light" && after.base !== "dark") return null;
  const tokens: Record<string, string> = {};
  if (after.tokens && typeof after.tokens === "object")
    for (const [token, value] of Object.entries(after.tokens))
      if (typeof value === "string") tokens[token] = value;
  return {
    ...(typeof after.theme_id === "string" && after.theme_id ? { theme_id: after.theme_id } : {}),
    name: after.name,
    base: after.base,
    tokens,
    contrast_warnings: Array.isArray(after.contrast_warnings) ? after.contrast_warnings : [],
  };
}

/** The newest open theme proposal, which is the one previewed: a newer one
 *  replaces the preview. The list holds them oldest first. */
function newestTheme(proposals: Proposal[]): Proposal | undefined {
  return proposals.findLast((proposal) => isThemeProposal(proposal) && proposedTheme(proposal));
}

/** The slots a custom theme is in now, as words ("light", "light and dark"). */
function slotsOf(prefs: { light_theme: string; dark_theme: string }, id: string): string[] {
  return [
    ...(prefs.light_theme === id ? ["light"] : []),
    ...(prefs.dark_theme === id ? ["dark"] : []),
  ];
}

/** A theme proposal's colours and warnings, and what the slots hold now. */
function ThemeView({ proposal }: { proposal: Proposal }) {
  const { preferences } = usePreferences();
  const theme = proposedTheme(proposal);
  const before = proposal.before as { light_theme?: string; dark_theme?: string } | undefined;
  if (!theme) return <Diff before={shown(proposal.before)} after={shown(proposal.after)} />;
  const entries = Object.entries(theme.tokens);
  const edited = theme.theme_id
    ? preferences.themes.find((t) => t.id === theme.theme_id && !t.builtin)
    : undefined;
  const inSlots = theme.theme_id ? slotsOf(preferences, theme.theme_id) : [];
  return (
    <div className="session-panel-theme">
      <p className="session-panel-note">
        {theme.theme_id ? (
          edited ? (
            <>
              A {theme.base} theme, shown on the app now. Saving updates {edited.name}
              {inSlots.length ? ` (your ${inSlots.join(" and ")} theme)` : ""} in place; Save as new
              theme adds it as another theme.
            </>
          ) : (
            <>
              A {theme.base} theme, shown on the app now. The theme it edits is gone: Save as new
              theme adds it to your themes.
            </>
          )
        ) : (
          <>
            A {theme.base} theme, shown on the app now. Saving adds it to your themes and puts it in
            the slot you choose.
          </>
        )}
        {!theme.theme_id && before?.light_theme && before?.dark_theme
          ? ` The slots hold ${before.light_theme} (light) and ${before.dark_theme} (dark).`
          : ""}
      </p>
      {entries.length ? (
        <ul className="session-panel-swatches" aria-label="Colours">
          {entries.map(([token, value]) => (
            <li key={token}>
              <span
                className="session-panel-swatch"
                style={{ backgroundColor: value }}
                aria-hidden="true"
              />
              <code>{token}</code> <span>{value}</span>
            </li>
          ))}
        </ul>
      ) : (
        <p className="session-panel-note">It sets no colours.</p>
      )}
      {theme.contrast_warnings.length ? (
        <div role="status" className="session-panel-warnings" aria-label="Contrast warnings">
          <p className="session-panel-note">These pairs are hard to read (under 4.5:1):</p>
          <ul>
            {theme.contrast_warnings.map((warning) => (
              <li key={`${warning.foreground}/${warning.background}`}>
                <code>{warning.foreground}</code> on <code>{warning.background}</code>:{" "}
                {warning.ratio.toFixed(1)}:1
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  );
}

/** What a change saves against what is saved now, as text. */
function ChangeView({ before, after }: { before: unknown; after: unknown }) {
  const from = textChange(before);
  const to = textChange(after);
  if (from || to) {
    const note =
      to?.text === null || to?.text === ""
        ? "Resets the prompt to baum's default."
        : !from || from.text === null
          ? "Replaces baum's default prompt with this text."
          : null;
    return (
      <>
        {note ? <p className="session-panel-note">{note}</p> : null}
        <Diff before={from?.text ?? ""} after={to?.text ?? ""} />
      </>
    );
  }
  return <Diff before={shown(before)} after={shown(after)} />;
}

/** A change proposed by the agent (`diff`): what it would save against what
 *  is saved now, with Save and Dismiss; or an operation (`operation`): baum's
 *  summary of what it would run, with Confirm and Dismiss. Nothing is saved
 *  or run until the person presses Save or Confirm, on the page it was made
 *  on. */
export function ProposalCard({
  conversation,
  proposal: given,
  page,
  go,
  changed,
  holdPreview,
}: {
  conversation: string;
  proposal: Proposal;
  page: PageContext;
  go: (href: string) => void;
  changed: () => void;
  /** Keeps the theme preview until the promise settles. */
  holdPreview?: (until: Promise<unknown>) => Promise<unknown>;
}) {
  // A confirm answered `operation_changed` replaces the summary shown.
  const [proposal, setProposal] = useState(given);
  useEffect(() => setProposal(given), [given]);
  const [busy, setBusy] = useState<"save" | "new" | "dismiss" | null>(null);
  const [error, setError] = useState<{ text: string; elsewhere?: boolean } | null>(null);
  const [changedNote, setChangedNote] = useState("");
  const [value, setValue] = useState("");
  const toast = useToast();
  const { refresh: refreshPreferences, preferences } = usePreferences();
  const after = proposal.after as { needs_value?: boolean; name?: string } | undefined;
  const needsValue = proposal.kind === "diff" && after?.needs_value === true;
  // A colour theme is for no page: it is saved wherever the person is.
  const themed = isThemeProposal(proposal);
  const proposedColours = themed ? proposedTheme(proposal) : null;
  const [themeName, setThemeName] = useState(proposedColours?.name ?? "");
  const [slot, setSlot] = useState<"light" | "dark">(proposedColours?.base ?? "light");
  // A proposal that edits a custom theme updates it; Save as new theme adds a
  // copy. The theme it names may be gone by now: Update then fails with the
  // daemon's reason, and Save as new theme stays.
  const editedId = proposedColours?.theme_id;
  const edited = editedId
    ? preferences.themes.find((t) => t.id === editedId && !t.builtin)
    : undefined;
  const editedSlots = editedId ? slotsOf(preferences, editedId) : [];
  const elsewhere = !themed && !samePage(proposal.context, page);
  const refused = (e: unknown) => {
    if (e instanceof LocalRequestError && e.code === "not_current_page")
      setError({
        text: `This was proposed on ${pageLabel(proposal.context)}, which isn't the page you have open. Open that page to save or run it.`,
        elsewhere: true,
      });
    else if (e instanceof LocalRequestError && e.code === "stale")
      setError({ text: "What it changes was changed since: ask the agent to propose it again." });
    else setError({ text: e instanceof Error ? e.message : "The request failed." });
  };
  const run = async (action: "save" | "dismiss", mode?: "update" | "new") => {
    setBusy(action === "save" && mode === "new" ? "new" : action);
    setError(null);
    try {
      if (action === "dismiss") await dismissProposal(conversation, proposal.id);
      else if (proposal.kind === "diff") {
        // A new theme always sends its slot: the daemon reads an empty one
        // differently. An update keeps the theme's slots and sends none.
        const name = themeName.trim();
        const themeSave: ThemeSave | undefined = !themed
          ? undefined
          : mode === "update"
            ? { name, mode }
            : editedId
              ? { name, slot: proposedColours?.base ?? slot, mode: "new" }
              : { name, slot };
        const saved = saveProposal(
          conversation,
          proposal.id,
          needsValue ? value : undefined,
          themeSave,
        );
        // The saved theme is in the preferences before the preview ends: the
        // proposals change as soon as the daemon records the save, before it
        // answers.
        const done = themed ? saved.then(() => refreshPreferences()) : saved;
        if (themed && holdPreview) await holdPreview(done.catch(() => undefined));
        await done;
      } else {
        const outcome = await confirmProposal(
          conversation,
          proposal.id,
          proposal.operation?.revision ?? "",
        );
        if (outcome.kind === "changed") {
          setProposal(outcome.proposal);
          setChangedNote(
            "What it would do changed since you read it, so nothing ran. Read it again and confirm.",
          );
          return;
        }
        if (outcome.status >= 300) {
          setError({ text: outcome.message || `It was refused (${outcome.status}).` });
          return;
        }
        toast({ title: proposal.operation?.summary.action ?? "Done", detail: outcome.message });
      }
      if (action === "save" && proposal.kind === "diff") toast({ title: "Saved the change" });
      // A comment saved to the pending review (propose_review_comment) shows
      // in the pull request's review at once.
      const { repository, pull_request } = proposal.context;
      if (
        action === "save" &&
        proposal.kind === "diff" &&
        repository &&
        pull_request &&
        proposal.target.endsWith(":review_comment")
      )
        void pendingReviewState({ repository, number: pull_request }).load();
      changed();
    } catch (e) {
      refused(e);
    } finally {
      setBusy(null);
    }
  };
  const summary = proposal.operation?.summary;
  const valueId = useId();
  return (
    <article className="session-panel-proposal" aria-label="Proposal">
      <header className="session-panel-proposal-head">
        <span className="session-panel-proposal-kind">
          {proposal.kind === "operation" ? "Asks to run" : "Proposes a change"}
        </span>
        <span className="session-panel-proposal-where">
          {pageLabel(proposal.context)} · <code>{proposal.target}</code>
        </span>
      </header>
      {summary ? (
        <OperationView summary={summary} />
      ) : themed ? (
        <ThemeView proposal={proposal} />
      ) : (
        <ChangeView before={proposal.before} after={proposal.after} />
      )}
      {themed && proposedColours ? (
        <div className="session-panel-value session-panel-theme-save">
          <label htmlFor={`${valueId}-name`}>Name</label>
          <input
            id={`${valueId}-name`}
            className="local-field"
            type="text"
            maxLength={60}
            value={themeName}
            onChange={(event) => setThemeName(event.target.value)}
          />
          {editedId ? (
            editedSlots.length ? (
              <p className="session-panel-note">
                Updating keeps it as your {editedSlots.join(" and ")} theme.
              </p>
            ) : null
          ) : (
            <>
              <label htmlFor={`${valueId}-slot`}>Use it as</label>
              <select
                id={`${valueId}-slot`}
                className="local-field"
                value={slot}
                onChange={(event) => setSlot(event.target.value as "light" | "dark")}
              >
                <option value="light">Light theme</option>
                <option value="dark">Dark theme</option>
              </select>
            </>
          )}
        </div>
      ) : null}
      {needsValue ? (
        <div className="session-panel-value">
          <label htmlFor={valueId}>Value of {after?.name ?? "the variable"}</label>
          <input
            id={valueId}
            className="local-field"
            type="password"
            autoComplete="off"
            spellCheck={false}
            value={value}
            onChange={(event) => setValue(event.target.value)}
          />
          <p className="session-panel-note">You type the value; it never goes to the agent.</p>
        </div>
      ) : null}
      {changedNote ? (
        <p role="status" className="session-panel-note">
          {changedNote}
        </p>
      ) : null}
      {error ? (
        <p role="alert" className="session-panel-error">
          {error.text}{" "}
          {error.elsewhere ? (
            <button
              type="button"
              className="session-panel-link"
              onClick={() => go(hrefOf(proposal.context))}
            >
              Open {pageLabel(proposal.context)}
            </button>
          ) : null}
        </p>
      ) : elsewhere ? (
        <p className="session-panel-note">
          Proposed on {pageLabel(proposal.context)}: open it to save or run this.{" "}
          <button
            type="button"
            className="session-panel-link"
            onClick={() => go(hrefOf(proposal.context))}
          >
            Open it
          </button>
        </p>
      ) : null}
      <div className="session-panel-actions">
        <Button
          size="compact"
          disabled={!!busy || (needsValue && !value) || (themed && !themeName.trim())}
          onClick={() => void run("save", editedId ? "update" : undefined)}
        >
          {busy === "save" ? <NineDots size={12} /> : null}
          {proposal.kind === "operation"
            ? "Confirm"
            : editedId
              ? `Update ${edited?.name ?? proposedColours?.name ?? "theme"}`
              : "Save"}
        </Button>
        {editedId ? (
          <Button
            variant="secondary"
            size="compact"
            disabled={!!busy || !themeName.trim()}
            onClick={() => void run("save", "new")}
          >
            {busy === "new" ? <NineDots size={12} /> : null}
            Save as new theme
          </Button>
        ) : null}
        <Button
          variant="secondary"
          size="compact"
          disabled={!!busy}
          onClick={() => void run("dismiss")}
        >
          {busy === "dismiss" ? <NineDots size={12} /> : null}
          Dismiss
        </Button>
      </div>
    </article>
  );
}

/** baum's summary of an operation: its action, then each detail. Text that
 *  is not baum's own (`quoted`) is set apart as quoted data, and the
 *  agent's words (`by_agent`) and long text (`multiline`) are shown whole,
 *  scrolling rather than cut. */
function OperationView({ summary }: { summary: OperationSummary }) {
  return (
    <div className="session-panel-operation">
      <p className="session-panel-action">{summary.action}</p>
      <div className="session-panel-flags">
        {summary.paid ? <Badge tone="caution">Costs money</Badge> : null}
        {summary.irreversible ? <Badge tone="critical">Can't be undone</Badge> : null}
      </div>
      {summary.details?.length || summary.model || summary.effort ? (
        <dl className="session-panel-details">
          {summary.details?.map((field, index) => (
            <Detail key={`${field.label}:${index}`} field={field} />
          ))}
          {summary.model ? (
            <div>
              <dt>Model</dt>
              <dd>{modelName(summary.model)}</dd>
            </div>
          ) : null}
          {summary.effort ? (
            <div>
              <dt>Reasoning effort</dt>
              <dd>{summary.effort}</dd>
            </div>
          ) : null}
        </dl>
      ) : null}
    </div>
  );
}

function Detail({ field }: { field: SummaryField }) {
  const whole = field.multiline || field.by_agent;
  return (
    <div>
      <dt>
        {field.label}
        {field.by_agent ? (
          <span className="session-panel-by-agent">written by the agent</span>
        ) : null}
      </dt>
      <dd>
        {field.quoted ? (
          <blockquote
            className={cn("session-panel-quoted", whole && "whole")}
            data-quoted=""
            aria-label={`Quoted: ${field.label}`}
            // A box that scrolls is reached and scrolled from the keyboard.
            tabIndex={whole ? 0 : undefined}
          >
            {field.value}
          </blockquote>
        ) : (
          <span className={cn(whole && "session-panel-whole")} tabIndex={whole ? 0 : undefined}>
            {field.value}
          </span>
        )}
      </dd>
    </div>
  );
}
