import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { cn } from "@baum/viewer/components/classnames";
import { useReviewFiles } from "@baum/viewer/components/review-contexts";
import { Button } from "@baum/viewer/components/ui";
import { indexedHunkFromFilePatch } from "@baum/viewer/patch";
import type { IndexedHunk } from "@baum/viewer/review-document";
import { useTileHost } from "../host";
import { Quiet } from "../parts";
import { itemTarget } from "../place";
import type { Tile, TileSize } from "../tile-document";
import type { TileContext } from "../types";
import { framesAllowedHere, useAppTheme, useFrameSlot } from "./environment";
import {
  HeightGuard,
  HELLO_TIMEOUT,
  HEIGHT_MIN,
  initMessage,
  KIT_SUPPORTED,
  RateGuard,
  READY_TIMEOUT,
  readMessage,
  themeMessage,
  updateMessage,
  type FrameMessage,
} from "./protocol";
import { citedHunks, MAX_EXCERPTS, resolveRefs, type CitationTarget } from "./refs";
import { listenToFrame } from "./router";

/** A custom tile's frame, as its card's glance or its detail draws it. */

export type Face = "glance" | "detail";

/** Why a frame was taken down. Each one is about this frame alone: nothing a
 *  frame does marks its type, or any other tile, as broken. */
export type FrameFailure =
  | { why: "silent" }
  | { why: "slow" }
  | { why: "error"; message: string }
  | { why: "navigated" }
  | { why: "flood" }
  | { why: "focus" };

const FAILURE_WORDS: Record<FrameFailure["why"], string> = {
  silent: "It didn't start.",
  slow: "It didn't finish drawing in time.",
  error: "It reported an error while drawing.",
  navigated: "It tried to leave its frame.",
  focus: "It kept taking the keyboard from the page.",
  flood: "It sent the page too many messages.",
};

/** What happened to each frame instance on this page, by instance key, so a
 *  frame that failed is not mounted again and again as it scrolls in and out
 *  of view; Retry forgets it. */
const failures = new Map<string, FrameFailure>();
/** Instances whose silent frame was already remounted once after the page
 *  read its tile route again. */
const retried = new Set<string>();
/** A detail's last height, so opening it again reserves the room it takes. */
const heights = new Map<string, number>();

/** The digest's 64 hex digits, from a definition the daemon served; null for
 *  anything else. */
export function frameDigest(definition: unknown): string | null {
  const digest = (definition as { digest?: unknown } | null)?.digest;
  return typeof digest === "string" && /^sha256:[0-9a-f]{64}$/.test(digest)
    ? digest.slice("sha256:".length)
    : null;
}

export function definitionKit(definition: unknown): number | null {
  const kit = (definition as { kit?: unknown } | null)?.kit;
  return typeof kit === "number" && Number.isInteger(kit) && kit >= 1 ? kit : null;
}

/** The layout size the tile is placed at; a detail is drawn full. */
function sizeOf(ctx: TileContext, tile: Tile, face: Face): TileSize {
  if (face === "detail") return "full";
  return ctx.doc.layout.entries.find((entry) => entry.tile === tile.id)?.size ?? "full";
}

/** The glance box's reserved height by size: the frame fills the room its
 *  card's row leaves, and never less, so nothing moves as it loads. */
const GLANCE_MIN: Record<TileSize, string> = {
  full: "min-h-28",
  half: "min-h-36",
  third: "min-h-36",
};

export function TileFrame({ tile, ctx, face }: { tile: Tile; ctx: TileContext; face: Face }) {
  const digest = frameDigest(tile.definition);
  const instance = `${ctx.idPrefix}${tile.id}:${digest}:${face}`;
  const [attempt, setAttempt] = useState(0);
  const [failure, setFailure] = useState<FrameFailure | null>(() => failures.get(instance) ?? null);
  const [ready, setReady] = useState(false);
  const [height, setHeight] = useState(() => heights.get(instance) ?? 240);
  const box = useRef<HTMLDivElement>(null);
  const allowed = useMemo(() => framesAllowedHere(), []);
  const kit = definitionKit(tile.definition);
  const newer = kit !== null && kit > KIT_SUPPORTED;
  const slot = useFrameSlot(box, face === "detail");
  // A frame mounted again (scrolled back to, or remounted after silence)
  // is hidden until it draws again.
  useEffect(() => setReady(false), [slot, attempt]);
  const host = useTileHost();
  const files = useReviewFiles();
  const theme = useAppTheme();
  const size = sizeOf(ctx, tile, face);
  const patches = useCitedPatches(tile, ctx, face);
  const resolved = useMemo(
    () => resolveRefs(tile.data, patches.doc, face, ctx.labels),
    [tile.data, patches.doc, face, ctx.labels],
  );

  const go = (target: CitationTarget) => {
    switch (target.kind) {
      case "hunk":
      case "file":
        if (!files?.open(target.path, target.kind === "hunk" ? target.line : undefined))
          ctx.openPane("files");
        return;
      case "tile":
        ctx.open(target.id);
        return;
      case "change":
        ctx.openChange(target.id);
        return;
      case "item": {
        const place = itemTarget(ctx.doc, target.change, target.id);
        if (place) ctx.open(place.tile.id, place.item);
      }
    }
  };

  const fail = (next: FrameFailure) => {
    if (next.why === "silent" && !retried.has(instance)) {
      // Its document may have left the daemon's registry: reading the tile
      // route again registers it, then the frame is mounted afresh, once.
      retried.add(instance);
      void Promise.resolve(host.reread?.())
        .catch(() => undefined)
        .then(() => setAttempt((count) => count + 1));
      return;
    }
    failures.set(instance, next);
    setFailure(next);
    setReady(false);
  };

  const retry = () => {
    failures.delete(instance);
    retried.delete(instance);
    setFailure(null);
    setReady(false);
    setAttempt((count) => count + 1);
  };

  const label = tile.title;
  const quiet = (words: string) =>
    face === "glance" ? (
      <Quiet>{words}</Quiet>
    ) : (
      <div className="rounded-md border border-dashed border-default px-4 py-3">
        <p className="text-base text-secondary">{words}</p>
      </div>
    );

  if (!digest) return quiet("This tile couldn't be read.");
  if (newer) return quiet(NEWER_KIT);
  if (!allowed) return quiet("Custom tiles aren't available in the desktop app on Windows yet.");
  if (failure)
    return face === "glance" ? (
      <div data-frame-failure={failure.why}>
        <Quiet>This tile couldn't load.</Quiet>
      </div>
    ) : (
      <div
        role="alert"
        data-frame-failure={failure.why}
        className="space-y-3 rounded-md border border-dashed border-default px-4 py-3"
      >
        <div className="space-y-1">
          <p className="text-base text-secondary">This tile couldn't load.</p>
          <p className="text-xs text-tertiary">
            {FAILURE_WORDS[failure.why]}
            {failure.why === "error" && failure.message ? ` ${failure.message}` : ""}
          </p>
        </div>
        <Button variant="secondary" size="compact" onClick={retry}>
          Retry
        </Button>
      </div>
    );

  return (
    <div
      ref={box}
      data-custom-tile={tile.id}
      data-frame-state={ready ? "ready" : slot ? "loading" : "waiting"}
      className={cn(
        "relative min-w-0",
        face === "glance"
          ? cn("flex-none grow basis-0", GLANCE_MIN[size])
          : // Set 4px out on every side: the frame keeps that inset for its
            // focus rings, so its text lines up with the pane's heading.
            "-m-1 w-[calc(100%+8px)]",
      )}
      style={face === "detail" ? { height } : undefined}
    >
      {slot ? (
        <FrameView
          key={attempt}
          src={`/tile-frames/${digest}`}
          title={label}
          face={face}
          ready={ready}
          waitFor={patches.pending}
          init={() =>
            initMessage({
              face,
              size,
              theme,
              lang: document.documentElement.lang || "en",
              data: tile.data,
              refs: resolved.refs,
            })
          }
          theme={theme}
          update={resolved.refs}
          data={tile.data}
          onReady={(metrics) => {
            setReady(true);
            if (face === "detail") {
              const next = Math.max(HEIGHT_MIN, metrics.height);
              heights.set(instance, next);
              setHeight(next);
            }
          }}
          onHeight={(next) => {
            heights.set(instance, next);
            setHeight(next);
          }}
          onFail={fail}
          onJump={(key) => {
            const target = resolved.targets.get(key);
            if (target) go(target);
          }}
          onAsk={() => host.ask?.(tile)}
        />
      ) : null}
    </div>
  );
}

export const NEWER_KIT = "This tile was made with a newer version of baum. Update baum to see it.";

/** How long a detail's init waits for the patches of the code it cites. */
const PATCH_WAIT = 1500;

/** The document a detail's citations resolve against: the shown one, with
 *  the excerpts it omitted read from the patches of the version compared
 *  (as the viewer's own cited code reads them), once loaded. A glance gets
 *  no code, and nothing is read while two versions are compared. */
function useCitedPatches(tile: Tile, ctx: TileContext, face: Face) {
  const [loaded, setLoaded] = useState<{
    data: unknown;
    diffs: ReadonlyMap<string, NonNullable<IndexedHunk["diff"]>>;
  } | null>(null);
  const pending = useRef<Promise<unknown> | null>(null);
  const wanted = useMemo(() => {
    if (face !== "detail" || !ctx.patches) return [];
    const ids = new Set(citedHunks(tile.data).map((anchor) => (anchor as { id: string }).id));
    return ctx.doc.index.hunks
      .filter((hunk) => ids.has(hunk.id) && !hunk.diff)
      .slice(0, MAX_EXCERPTS);
  }, [face, ctx.patches, ctx.doc, tile.data]);
  // Started as the frame mounts, well before its hello.
  useEffect(() => {
    if (wanted.length === 0) return;
    const source = ctx.patches!;
    const version = ctx.doc.review.version;
    const data = tile.data;
    let live = true;
    const loading = Promise.all(
      wanted.map((hunk) =>
        source(hunk.path, version, hunk.id).then(
          (patch) => {
            const diff = patch ? indexedHunkFromFilePatch(patch, hunk) : null;
            return diff ? ([hunk.id, diff] as const) : null;
          },
          () => null,
        ),
      ),
    ).then((entries) => {
      if (pending.current === loading) pending.current = null;
      if (live) setLoaded({ data, diffs: new Map(entries.filter((entry) => entry !== null)) });
    });
    pending.current = loading;
    return () => {
      live = false;
    };
  }, [wanted, tile.data, ctx.patches, ctx.doc.review.version]);
  const doc = useMemo(() => {
    if (!loaded || loaded.diffs.size === 0) return ctx.doc;
    return {
      ...ctx.doc,
      index: {
        ...ctx.doc.index,
        hunks: ctx.doc.index.hunks.map((hunk) => {
          const diff = loaded.diffs.get(hunk.id);
          return diff && !hunk.diff ? { ...hunk, diff } : hunk;
        }),
      },
    };
  }, [ctx.doc, loaded]);
  return { doc, pending: () => pending.current };
}

type FrameViewProps = {
  /** What init waits for, at most PATCH_WAIT, when anything. */
  waitFor: () => Promise<unknown> | null;
  src: string;
  title: string;
  face: Face;
  ready: boolean;
  init: () => ReturnType<typeof initMessage>;
  theme: "light" | "dark";
  data: unknown;
  update: Record<string, unknown>;
  onReady: (metrics: Extract<FrameMessage, { kind: "ready" }>["metrics"]) => void;
  onHeight: (height: number) => void;
  onFail: (failure: FrameFailure) => void;
  onJump: (key: string) => void;
  onAsk: () => void;
};

/** When the reader last pressed Tab in the page, which moves the keyboard
 *  into a detail's frame, and the page's own element that last had it. */
let lastTab = Number.NEGATIVE_INFINITY;
let lastFocused: WeakRef<HTMLElement> | null = null;
if (typeof window !== "undefined") {
  window.addEventListener(
    "keydown",
    (event) => {
      if (event.key === "Tab") lastTab = performance.now();
    },
    true,
  );
  document.addEventListener(
    "focusin",
    (event) => {
      if (event.target instanceof HTMLElement && !(event.target instanceof HTMLIFrameElement))
        lastFocused = new WeakRef(event.target);
    },
    true,
  );
}

/** Gives the page back the keyboard a frame holds. `blur()` does in
 *  Chromium; WebKit keeps the focus in the frame, so the page's element that
 *  last had it, or else the body, takes it. */
function release(iframe: HTMLIFrameElement) {
  iframe.blur();
  if (document.activeElement !== iframe) return;
  const previous = lastFocused?.deref();
  if (previous?.isConnected) previous.focus({ preventScroll: true });
  if (document.activeElement !== iframe) return;
  const body = document.body;
  const had = body.hasAttribute("tabindex");
  if (!had) body.tabIndex = -1;
  body.focus({ preventScroll: true });
  if (!had) body.removeAttribute("tabindex");
}

/** Whether the reader is acting in the page now: a click or key press in the
 *  frame activates the page too. Where the browser cannot say, focus alone
 *  gates. */
function userActive(): boolean {
  const activation = (navigator as Navigator & { userActivation?: { isActive: boolean } })
    .userActivation;
  return activation ? activation.isActive : true;
}

/** One mount of a frame: its iframe, its messages and its timers. A remount
 *  is a new FrameView (a new `key`), so nothing carries over. */
function FrameView(props: FrameViewProps) {
  const frame = useRef<HTMLIFrameElement>(null);
  const latest = useRef(props);
  latest.current = props;
  const started = useRef<((message: unknown) => void) | null>(null);

  useLayoutEffect(() => {
    const iframe = frame.current;
    const target = iframe?.contentWindow;
    if (!iframe || !target) return;
    const { face } = latest.current;
    let done = false;
    let hello = false;
    let inited = false;
    let ready = false;
    let takeBack = () => {};
    const rate = new RateGuard();
    const sizes = new HeightGuard();
    let helloTimer = 0;
    let readyTimer = 0;
    const stop = () => {
      done = true;
      window.clearTimeout(helloTimer);
      window.clearTimeout(readyTimer);
      unlisten();
      iframe.removeEventListener("load", loaded);
      window.removeEventListener("blur", left);
      window.removeEventListener("focus", back);
      iframe.removeEventListener("pointerenter", over);
      iframe.removeEventListener("pointerleave", out);
      window.clearTimeout(focusCheck);
      started.current = null;
    };
    const fail = (failure: FrameFailure) => {
      if (done) return;
      stop();
      latest.current.onFail(failure);
    };
    const send = (message: unknown) => {
      try {
        target.postMessage(message, "*");
      } catch {
        // A message that cannot be cloned is not sent.
      }
    };
    const awaitReady = () => {
      window.clearTimeout(readyTimer);
      readyTimer = window.setTimeout(() => fail({ why: "slow" }), READY_TIMEOUT);
    };
    const gated = () =>
      face === "detail" && document.activeElement === iframe && document.hasFocus();
    const heard = (data: unknown) => {
      if (done) return;
      takeBack();
      if (done) return;
      const admission = rate.admit(performance.now());
      if (admission === "flood") return fail({ why: "flood" });
      if (admission === "drop") return;
      const message = readMessage(data);
      if (!message) return;
      switch (message.kind) {
        case "hello":
          // A second hello is the frame's document loading again.
          if (hello) return fail({ why: "navigated" });
          hello = true;
          window.clearTimeout(helloTimer);
          inited = true;
          awaitReady();
          {
            const start = () => {
              if (done) return;
              send(latest.current.init());
              started.current = send;
              awaitReady();
            };
            const waiting = latest.current.waitFor();
            if (!waiting) start();
            // Then a turn for the page to draw what arrived.
            else
              void Promise.race([
                waiting,
                new Promise((resolve) => setTimeout(resolve, PATCH_WAIT)),
              ]).then(() => setTimeout(start, 0));
          }
          return;
        case "ready":
          if (!inited) return;
          window.clearTimeout(readyTimer);
          ready = true;
          latest.current.onReady(message.metrics);
          return;
        case "resize": {
          if (face !== "detail" || !ready) return;
          const height = sizes.next(message.height, performance.now());
          if (height !== null) latest.current.onHeight(height);
          return;
        }
        case "error":
          // A render that threw or ran out of time never draws: the frame
          // fails now rather than at the timeout. Anything else (a logged
          // error, a blocked request, an error after it drew) leaves the
          // tile drawing; it is the author's to read in a render check.
          if (!ready && (message.code === "script_error" || message.code === "timeout"))
            fail({ why: "error", message: message.message });
          return;
        case "jump":
          if (gated() && userActive()) latest.current.onJump(message.key);
          return;
        case "ask":
          if (gated() && userActive()) latest.current.onAsk();
          return;
        case "open":
          // Only a glance would open its own detail, and a glance is inert.
          return;
        case "key":
          if (!gated()) return;
          // The page's own keys act on it as if pressed on the frame.
          iframe.dispatchEvent(
            new KeyboardEvent("keydown", {
              key: message.key,
              shiftKey: message.shift,
              bubbles: true,
              cancelable: true,
            }),
          );
      }
    };
    const loaded = () => {
      // The tile's document has loaded by the time it is ready: a load after
      // that is the frame navigating. One before it never sends ready.
      if (ready) fail({ why: "navigated" });
    };
    // A frame may take the keyboard only when the reader gives it: a glance
    // never (it lies under its card's link), a detail when the reader
    // tabbed or clicked into it. Focus a frame takes by itself goes back to
    // where it was; a frame that keeps taking it is taken down.
    // The page's window loses focus as a frame takes it (the frame's
    // element gets no focus event of its own).
    // A reader clicking into it has the pointer over it; the page sees the
    // pointer enter and leave the frame's element, never what it does inside.
    let stolen = 0;
    let focusCheck = 0;
    let pointed = false;
    const over = () => {
      pointed = true;
    };
    const out = () => {
      pointed = false;
    };
    iframe.addEventListener("pointerenter", over);
    iframe.addEventListener("pointerleave", out);
    let consented = false;
    // Each time it takes the keyboard counts once, however often the page
    // looks before it is given back.
    let holding = false;
    takeBack = () => {
      if (done) return;
      if (document.activeElement !== iframe) {
        consented = false;
        holding = false;
        return;
      }
      if (consented || (face === "detail" && pointed)) return;
      release(iframe);
      if (holding) return;
      holding = document.activeElement === iframe;
      if (++stolen > 3) fail({ why: "focus" });
    };
    const left = () => {
      const given = face === "detail" && (performance.now() - lastTab < 1000 || pointed);
      window.clearTimeout(focusCheck);
      focusCheck = window.setTimeout(() => {
        if (given && document.activeElement === iframe) consented = true;
        takeBack();
      }, 0);
    };
    // A window without focus loses none as the frame takes it: the page also
    // looks when it gets focus back and whenever the frame speaks.
    const back = () => {
      window.clearTimeout(focusCheck);
      focusCheck = window.setTimeout(takeBack, 0);
    };
    window.addEventListener("blur", left);
    window.addEventListener("focus", back);
    const unlisten = listenToFrame(target, heard);
    iframe.addEventListener("load", loaded);
    // Its window is heard before it has an address, so its hello is not missed.
    iframe.src = latest.current.src;
    helloTimer = window.setTimeout(() => fail({ why: "silent" }), HELLO_TIMEOUT);
    return stop;
  }, []);

  const { theme, data, update } = props;
  const first = useRef(true);
  useEffect(() => {
    if (first.current) return;
    started.current?.(themeMessage(theme));
  }, [theme]);
  useEffect(() => {
    if (first.current) return;
    started.current?.(updateMessage(data, update));
  }, [data, update]);
  useEffect(() => {
    first.current = false;
  }, []);

  return (
    <iframe
      ref={frame}
      title={props.title}
      sandbox="allow-scripts"
      referrerPolicy="no-referrer"
      allow=""
      // A glance lies under its card's link and takes neither focus nor
      // clicks; a detail is reached with Tab.
      tabIndex={props.face === "glance" ? -1 : 0}
      className={cn(
        "block border-0 bg-transparent",
        props.face === "glance"
          ? "pointer-events-none absolute inset-0 h-full w-full"
          : "h-full w-full",
        props.ready ? "visible" : "invisible",
      )}
    />
  );
}
