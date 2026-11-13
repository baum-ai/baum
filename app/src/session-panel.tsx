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
import { formatRelative, formatUSD } from "@baum/viewer/components/format";
import { ChevronDownIcon, TerminalIcon } from "@baum/viewer/components/icons";
import { Markdown } from "@baum/viewer/components/markdown";
import { Badge, Button, CloseButton, LiveDot, SegmentedControl } from "@baum/viewer/components/ui";
import { cn } from "@baum/viewer/components/classnames";
import { agentName, LocalRequestError } from "./api";
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
  type PanelSettings,
  type Progress,
  type Proposal,
  type SummaryField,
} from "./console-api";
import { useContextMenu, type MenuItem } from "./context-menu";
import { ConfirmDialog } from "./group-dialogs";
import { modelName } from "./desktop/setup-model";
import { useNow } from "./live";
import { LoadingStatus, NineDots } from "./nine-dots";
import { hrefOf, onPull, pageLabel, pickedFocus, samePage, type PageContext } from "./page-context";
import { pendingReviewState } from "./pending-review-model";
import { Diff } from "./prompt-editor";
import { POLL_RUNNING, TranscriptReader, useLiveTranscript } from "./session-transcript";
import { isPanelKey, PANEL_KEY, PANEL_KEY_LABEL, useShortcuts } from "./shortcuts";
import { useToast } from "./toast";
import { useVisibleInterval } from "./visible-interval";
import { MIN_HEIGHT, sessionPanel, useSessionPanel, type PanelState } from "./session-panel-state";
import "./session-panel.css";

export { PANEL_KEY, PANEL_KEY_LABEL } from "./shortcuts";
export {
  DEFAULT_HEIGHT,
  MIN_HEIGHT,
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
  useShortcuts((event) => {
    if (!isPanelKey(event)) return;
    event.preventDefault();
    sessionPanel.toggle();
  });
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

const MODES: { value: PanelMode; label: string }[] = [
  { value: "full", label: "Full access" },
  { value: "workspace", label: "Workspace" },
];
const modeLabel = (mode: string) => MODES.find((m) => m.value === mode)?.label ?? mode;
const modeDetail: Record<PanelMode, string> = {
  full: "Runs your agent as you would with every prompt approved.",
  workspace: "Edits files in its directory; what it would have to ask for is refused.",
};

/** What a conversation's agent and model are called in the header. */
function agentLine(agent: string, model: string) {
  return `${agentName(agent)} · ${model ? modelName(model) : "default model"}`;
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
  const position = settings?.position ?? "bottom";
  const id = panel.conversation;
  const { conversation, error: readError, update } = useConversation(id);
  const { proposals, read: readProposals } = useProposals(id);
  const [mode, setMode] = useState<PanelMode | null>(null);
  const chosenMode = mode ?? settings?.mode ?? "full";
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
  // panel covers, and from which edge (`--session-panel-inset`).
  useLayoutEffect(() => {
    const element = root.current;
    const frame = element?.closest<HTMLElement>(".local-frame");
    if (!element || !frame) return;
    const measure = () => {
      const box = element.getBoundingClientRect();
      const edge = frame.getBoundingClientRect();
      const covered = position === "bottom" ? edge.bottom - box.top : box.bottom - edge.top;
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
  const onKeyDown = (event: ReactKeyboardEvent) => {
    if (event.key !== "Escape" || event.defaultPrevented || event.nativeEvent.isComposing) return;
    const target = event.target as HTMLElement;
    event.preventDefault();
    // A field with something in it gives up the keys to the panel, which the
    // next Escape hides; the page under it never sees either.
    if (
      (target instanceof HTMLTextAreaElement || target instanceof HTMLInputElement) &&
      target.value
    ) {
      root.current?.focus({ preventScroll: true });
      return;
    }
    sessionPanel.hide();
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
      style={{ height: `min(${panel.height}px, calc(100dvh - 4rem))` }}
      onKeyDown={onKeyDown}
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
      {position === "bottom" ? <Grip position={position} height={panel.height} /> : null}
      <header className="session-panel-head">
        <div className="session-panel-title">
          <TerminalIcon size={16} className="flex-none text-(--text-tertiary)" />
          <h2 id={headId} title={heading}>
            {heading}
          </h2>
          {running ? (
            <span className="session-panel-state">
              <LiveDot className="text-(--status-positive)" /> Working
            </span>
          ) : null}
          <Button
            ref={picker}
            variant="secondary"
            size="compact"
            className="gap-1"
            aria-haspopup="menu"
            aria-expanded={menu.anchor !== null && menu.anchor === picker.current}
            onClick={() => {
              if (picker.current) menu.openFrom(picker.current, pickerItems(), "Sessions");
            }}
          >
            Sessions
            <ChevronDownIcon size={14} />
          </Button>
          <CloseButton
            label={`Hide the session panel (${PANEL_KEY_LABEL})`}
            onClick={() => sessionPanel.hide()}
          />
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
                <code>{directory}</code>
              ) : page.page === "pull_request" ? (
                `A checkout of ${page.repository} #${page.pull_request}, made when it starts`
              ) : (
                "A new empty folder, made when it starts"
              )}
            </dd>
          </div>
          <div>
            <dt>Agent</dt>
            <dd>{agentLine(agent, model)}</dd>
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
          {conversation?.cost_usd !== undefined ? (
            <div>
              <dt>Cost</dt>
              <dd>
                {conversation.cost_partial ? "≥ " : ""}
                {formatUSD(conversation.cost_usd)}
              </dd>
            </div>
          ) : null}
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
              {agentName(agent)} works{" "}
              {chosenMode === "full" ? "with your full access" : "in its folder"}{" "}
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
                  composer.current?.focus({ preventScroll: true });
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
              rows={2}
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
      {position === "top" ? <Grip position={position} height={panel.height} /> : null}
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

/** The edge the panel is resized by: dragged, or ↑ and ↓ while focused. */
function Grip({ position, height }: { position: "bottom" | "top"; height: number }) {
  const drag = useRef<{ start: number; height: number } | null>(null);
  const sign = position === "bottom" ? -1 : 1;
  return (
    // biome-ignore lint/a11y/useSemanticElements: a focusable separator is the resize handle's role
    <div
      role="separator"
      aria-orientation="horizontal"
      aria-label="Resize the session panel"
      aria-valuenow={height}
      aria-valuemin={MIN_HEIGHT}
      aria-valuemax={Math.max(MIN_HEIGHT, Math.round(window.innerHeight - 64))}
      tabIndex={0}
      className="session-panel-grip"
      onPointerDown={(event: ReactPointerEvent<HTMLDivElement>) => {
        event.currentTarget.setPointerCapture?.(event.pointerId);
        drag.current = { start: event.clientY, height };
      }}
      onPointerMove={(event) => {
        if (!drag.current) return;
        const next = drag.current.height + sign * (event.clientY - drag.current.start);
        sessionPanel.setHeight(Math.min(next, window.innerHeight - 64));
      }}
      onPointerUp={() => {
        drag.current = null;
      }}
      onPointerCancel={() => {
        drag.current = null;
      }}
      onKeyDown={(event) => {
        if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return;
        event.preventDefault();
        const grow = (event.key === "ArrowUp") === (position === "bottom");
        sessionPanel.setHeight(Math.min(height + (grow ? 40 : -40), window.innerHeight - 64));
      }}
    />
  );
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
          <LiveDot className="text-(--status-positive)" />
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
}: {
  conversation: string;
  proposals: Proposal[];
  page: PageContext;
  go: (href: string) => void;
  changed: () => void;
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
}: {
  conversation: string;
  proposal: Proposal;
  page: PageContext;
  go: (href: string) => void;
  changed: () => void;
}) {
  // A confirm answered `operation_changed` replaces the summary shown.
  const [proposal, setProposal] = useState(given);
  useEffect(() => setProposal(given), [given]);
  const [busy, setBusy] = useState<"save" | "dismiss" | null>(null);
  const [error, setError] = useState<{ text: string; elsewhere?: boolean } | null>(null);
  const [changedNote, setChangedNote] = useState("");
  const [value, setValue] = useState("");
  const toast = useToast();
  const after = proposal.after as { needs_value?: boolean; name?: string } | undefined;
  const needsValue = proposal.kind === "diff" && after?.needs_value === true;
  const elsewhere = !samePage(proposal.context, page);
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
  const run = async (action: "save" | "dismiss") => {
    setBusy(action);
    setError(null);
    try {
      if (action === "dismiss") await dismissProposal(conversation, proposal.id);
      else if (proposal.kind === "diff")
        await saveProposal(conversation, proposal.id, needsValue ? value : undefined);
      else {
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
      ) : (
        <ChangeView before={proposal.before} after={proposal.after} />
      )}
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
          disabled={!!busy || (needsValue && !value)}
          onClick={() => void run("save")}
        >
          {busy === "save" ? <NineDots size={12} /> : null}
          {proposal.kind === "operation" ? "Confirm" : "Save"}
        </Button>
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
