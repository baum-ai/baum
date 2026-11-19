import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { cn } from "@baum/viewer/components/classnames";
import { useReviewFiles } from "@baum/viewer/components/review-contexts";
import { Button } from "@baum/viewer/components/ui";
import { indexedHunkFromFilePatch } from "@baum/viewer/patch";
import type { IndexedHunk } from "@baum/viewer/review-document";
import { useTileHost } from "../host";
import { Quiet } from "../parts";
import type { Tile, TileContext, TileSize } from "../types";
import { GridIcon } from "../icons";
import { framesAllowedHere, NOT_IN_THIS_SHELL, useAppTheme, useFrameSlot } from "./environment";
import {
  HeightGuard,
  HELLO_TIMEOUT,
  initMessage,
  KIT_SUPPORTED,
  MAX_ERROR,
  READY_TIMEOUT,
  readMessage,
  themeMessage,
  updateMessage,
} from "./protocol";
import { citedHunks, MAX_EXCERPTS, resolveRefs, type CitationTarget } from "./refs";
import { isBuiltinType } from "./library";
import { listenToFrame } from "./router";
import { HUNG_WORDS, usePreflight } from "./preflight";

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

/** The host's own reason for each failure. The author's words, when there
 *  are any, are shown apart from it, quoted, behind Details. */
const FAILURE_WORDS: Record<FrameFailure["why"], string> = {
  silent: "It didn't start.",
  slow: "It didn't finish drawing in time.",
  error: "It failed while drawing.",
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
/** One reading of the tile routes again, shared by every frame that was
 *  silent while it runs. */
let rereading: Promise<unknown> | null = null;
function rereadOnce(reread: (() => Promise<unknown>) | undefined): Promise<unknown> {
  if (!reread) return Promise.resolve();
  rereading ??= Promise.resolve()
    .then(reread)
    .catch(() => undefined)
    .finally(() => {
      rereading = null;
    });
  return rereading;
}

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

/** The size the tile is drawn at: the review page gives a tile the width of
 *  its section, so a detail is full, as is a glance of a type that declares
 *  it; else the largest size the type declares. */
function sizeOf(tile: Tile, face: Face): TileSize {
  if (face === "detail") return "full";
  const sizes = (tile.definition as { sizes?: unknown } | null | undefined)?.sizes;
  const declared: unknown[] = Array.isArray(sizes) ? sizes : [];
  return SIZES_LARGEST_FIRST.find((size) => declared.includes(size)) ?? "full";
}

const SIZES_LARGEST_FIRST: TileSize[] = ["full", "half", "third"];

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
  const size = sizeOf(tile, face);
  const patches = useCitedPatches(tile, ctx, face);
  const resolved = useMemo(
    () => resolveRefs(tile.data, patches.source, face, ctx.labels),
    [tile.data, patches.source, face, ctx.labels],
  );
  // The glance's size: what the preflight draws it at.
  const preflight = usePreflight(tile, ctx, sizeOf(tile, "glance"), digest);

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
      case "item":
        ctx.openChange(target.change, target.id);
    }
  };

  const fail = (next: FrameFailure) => {
    if (next.why === "silent" && !retried.has(instance)) {
      // Its document may have left the daemon's registry: reading the tile
      // route again registers it, then the frame is mounted afresh, once.
      retried.add(instance);
      void rereadOnce(host.reread).then(() => setAttempt((count) => count + 1));
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
  if (!allowed) return quiet(NOT_IN_THIS_SHELL);
  if (preflight === "hung") return quiet(HUNG_WORDS);
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
          <p className="text-xs text-tertiary">{FAILURE_WORDS[failure.why]}</p>
          {failure.why === "error" && failure.message ? (
            <details className="text-xs text-tertiary">
              <summary className="cursor-pointer">Details</summary>
              <p className="mt-1">The tile reported:</p>
              <blockquote className="mt-1 max-h-32 overflow-auto rounded-sm bg-surface-wash px-2 py-1 font-mono break-all whitespace-pre-wrap">
                {failure.message.slice(0, MAX_ERROR)}
              </blockquote>
            </details>
          ) : null}
        </div>
        <Button variant="secondary" size="compact" onClick={retry}>
          Retry
        </Button>
      </div>
    );

  const frameBox = (
    <div
      ref={box}
      data-custom-tile={tile.id}
      data-frame-state={
        ready ? "ready" : preflight === "checking" ? "checking" : slot ? "loading" : "waiting"
      }
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
      {slot && preflight === "show" ? (
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
          onReady={() => setReady(true)}
          onHeight={(next) => {
            heights.set(instance, next);
            setHeight(next);
          }}
          onFail={fail}
          onJump={(key) => {
            const target = resolved.targets.get(key);
            if (target) go(target);
            return Boolean(target);
          }}
          onAsk={() => host.ask?.(tile)}
        />
      ) : null}
    </div>
  );
  // A built-in library type is baum's own: its detail reads as the page's.
  if (face === "glance" || isBuiltinType(tile)) return frameBox;
  // A detail's body is the tile's own: the host says so, and draws a faint
  // edge of its own around it, so nothing the tile draws reads as the page.
  return (
    <div className="space-y-1.5">
      <p className="flex items-center gap-1.5 text-xs text-tertiary">
        <GridIcon size={12} className="flex-none" />
        Custom tile
      </p>
      <div className="rounded-md border border-subtle px-3 py-2">{frameBox}</div>
    </div>
  );
}

export const NEWER_KIT = "This tile was made with a newer version of baum. Update baum to see it.";

/** A tile type drawn without a review, as the Tile library page previews
 *  it: one face of one sample, its data and the payloads of its citations
 *  as the daemon's preview route resolved them (`library.Sample.Refs`), in
 *  the same sandboxed frame and under the same checks as on the tiles page.
 *  Nothing in a preview jumps or asks: a sample's citations name its own
 *  made-up hunks. `reread` reads the preview route again, which registers
 *  the type's frame document again, before a frame that did not start is
 *  mounted once more. A glance fills its box; a detail takes its height.
 *  The caller keys it by type version and sample: another is a new frame. */
export function PreviewFrame({
  digest,
  kit,
  title,
  face,
  size,
  data,
  refs,
  reread,
}: {
  digest: string;
  kit: number;
  title: string;
  face: Face;
  size: TileSize;
  data: unknown;
  refs: Record<string, unknown>;
  reread?: () => Promise<unknown>;
}) {
  const instance = `preview:${digest}:${face}:${size}`;
  const [attempt, setAttempt] = useState(0);
  const [failure, setFailure] = useState<FrameFailure | null>(null);
  const [ready, setReady] = useState(false);
  const [height, setHeight] = useState(() => heights.get(instance) ?? 240);
  const box = useRef<HTMLDivElement>(null);
  const allowed = useMemo(() => framesAllowedHere(), []);
  const theme = useAppTheme();
  const slot = useFrameSlot(box, face === "detail");
  useEffect(() => setReady(false), [slot, attempt]);
  const retried = useRef(false);
  const valid = /^[0-9a-f]{64}$/.test(digest);
  const quiet = (words: string) =>
    face === "glance" ? (
      <Quiet>{words}</Quiet>
    ) : (
      <div className="rounded-md border border-dashed border-default px-4 py-3">
        <p className="text-base text-secondary">{words}</p>
      </div>
    );
  if (!valid) return quiet("This tile couldn't be read.");
  if (kit > KIT_SUPPORTED) return quiet(NEWER_KIT);
  if (!allowed) return quiet(NOT_IN_THIS_SHELL);
  const fail = (next: FrameFailure) => {
    if (next.why === "silent" && !retried.current && reread) {
      retried.current = true;
      void reread()
        .catch(() => undefined)
        .then(() => setAttempt((count) => count + 1));
      return;
    }
    setFailure(next);
    setReady(false);
  };
  if (failure)
    return (
      <div
        role="alert"
        data-frame-failure={failure.why}
        className="space-y-2 rounded-md border border-dashed border-default px-4 py-3"
      >
        <p className="text-base text-secondary">This tile couldn't load.</p>
        <p className="text-xs text-tertiary">{FAILURE_WORDS[failure.why]}</p>
        <Button
          variant="secondary"
          size="compact"
          onClick={() => {
            retried.current = false;
            setFailure(null);
            setAttempt((count) => count + 1);
          }}
        >
          Retry
        </Button>
      </div>
    );
  return (
    <div
      ref={box}
      data-preview-frame={face}
      data-frame-state={ready ? "ready" : slot ? "loading" : "waiting"}
      className={cn(
        "relative min-w-0",
        face === "glance" ? "h-full w-full" : "-m-1 w-[calc(100%+8px)]",
      )}
      style={face === "detail" ? { height } : undefined}
    >
      {slot ? (
        <FrameView
          key={attempt}
          src={`/tile-frames/${digest}`}
          title={title}
          face={face}
          ready={ready}
          waitFor={() => null}
          init={() =>
            initMessage({
              face,
              size,
              theme,
              lang: document.documentElement.lang || "en",
              data,
              refs,
            })
          }
          theme={theme}
          update={refs}
          data={data}
          onReady={() => setReady(true)}
          onHeight={(next) => {
            heights.set(instance, next);
            setHeight(next);
          }}
          onFail={fail}
          onJump={() => false}
          onAsk={() => {}}
        />
      ) : null}
    </div>
  );
}

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
    return ctx.source.index.hunks
      .filter((hunk) => ids.has(hunk.id) && !hunk.diff)
      .slice(0, MAX_EXCERPTS);
  }, [face, ctx.patches, ctx.source, tile.data]);
  // Started as the frame mounts, well before its hello.
  useEffect(() => {
    if (wanted.length === 0) return;
    const source = ctx.patches!;
    const version = ctx.source.version;
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
  }, [wanted, tile.data, ctx.patches, ctx.source.version]);
  const source = useMemo(() => {
    if (!loaded || loaded.diffs.size === 0) return ctx.source;
    return {
      ...ctx.source,
      index: {
        ...ctx.source.index,
        hunks: ctx.source.index.hunks.map((hunk) => {
          const diff = loaded.diffs.get(hunk.id);
          return diff && !hunk.diff ? { ...hunk, diff } : hunk;
        }),
      },
    };
  }, [ctx.source, loaded]);
  return { source, pending: () => pending.current };
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
  onReady: () => void;
  onHeight: (height: number) => void;
  onFail: (failure: FrameFailure) => void;
  /** True when the key named a place the page went to. */
  onJump: (key: string) => boolean;
  onAsk: () => void;
};

/** What the page saw the reader do with the keyboard, outside the frames
 *  (a key a frame forwards is dispatched on its iframe and not counted):
 *  their last key press, and their last Tab, and the page's own element
 *  that last had the keyboard. */
let lastPageKey = Number.NEGATIVE_INFINITY;
let lastTab = Number.NEGATIVE_INFINITY;
let lastFocused: WeakRef<HTMLElement> | null = null;
if (typeof window !== "undefined") {
  window.addEventListener(
    "keydown",
    (event) => {
      if (event.target instanceof HTMLIFrameElement) return;
      lastPageKey = performance.now();
      if (event.key === "Tab") lastTab = lastPageKey;
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

/** A Tab that lands on a frame's element counts as giving it the keyboard
 *  this soon after the key; a click inside it only when the page saw no key
 *  press of its own this soon before (typing elsewhere is not a click). */
const TAB_LANDS = 300;
const TYPING = 500;
/** At most one jump or question per this many ms, each during a user
 *  activation. */
const ASK_INTERVAL = 1000;

/** Forgets the reader's last keys, for tests whose clocks start again. */
export function forgetInput() {
  lastPageKey = Number.NEGATIVE_INFINITY;
  lastTab = Number.NEGATIVE_INFINITY;
  lastFocused = null;
}

/** Gives the page back the keyboard a frame holds: to the page's element
 *  that last had it (the reader goes on typing where they were), else by
 *  `blur()`, which Chromium honours, else to the body, as WebKit keeps the
 *  focus in the frame through `blur()`. */
function release(iframe: HTMLIFrameElement) {
  const previous = lastFocused?.deref();
  if (previous?.isConnected) previous.focus({ preventScroll: true });
  if (document.activeElement !== iframe) return;
  iframe.blur();
  if (document.activeElement !== iframe) return;
  const body = document.body;
  const had = body.hasAttribute("tabindex");
  if (!had) body.tabIndex = -1;
  body.focus({ preventScroll: true });
  if (!had) body.removeAttribute("tabindex");
}

/** Whether the browser reports a user activation now: a click or key press,
 *  in the page or a frame. A browser that cannot say is taken as no. */
function userActive(): boolean {
  const activation = (navigator as Navigator & { userActivation?: { isActive: boolean } })
    .userActivation;
  return activation?.isActive === true;
}

/** One mount of a frame: its iframe, its messages and its timers. A remount
 *  is a new FrameView (a new `key`), so nothing carries over. */
function FrameView(props: FrameViewProps) {
  const frame = useRef<HTMLIFrameElement>(null);
  const latest = useRef(props);
  latest.current = props;
  const started = useRef<((message: { kind: string }) => void) | null>(null);

  useLayoutEffect(() => {
    const iframe = frame.current;
    const target = iframe?.contentWindow;
    if (!iframe || !target) return;
    const { face } = latest.current;
    let done = false;
    let hello = false;
    let inited = false;
    let ready = false;
    /** Sent init or update and not yet answered with ready. */
    let awaiting = false;
    let takeBack = () => {};
    const sizes = new HeightGuard();
    let helloTimer = 0;
    let readyTimer = 0;
    let heightFrame = 0;
    let pendingHeight = 0;
    let lastAsk = Number.NEGATIVE_INFINITY;
    const stop = () => {
      done = true;
      window.clearTimeout(helloTimer);
      window.clearTimeout(readyTimer);
      cancelAnimationFrame(heightFrame);
      unlisten();
      iframe.removeEventListener("load", loaded);
      iframe.removeEventListener("focus", tabbedIn);
      window.removeEventListener("blur", left);
      window.removeEventListener("focus", back);
      document.removeEventListener("focusin", elsewhere, true);
      iframe.removeEventListener("pointerenter", over);
      iframe.removeEventListener("pointerleave", out);
      window.clearTimeout(focusCheck);
      started.current = null;
    };
    const fail = (failure: FrameFailure) => {
      if (done) return;
      stop();
      // Until the page draws the failure in its place, the frame stays
      // mounted with nothing left to take the keyboard back from it: its
      // document is unloaded at once, so it runs no more, and the reader
      // goes on typing where they were.
      iframe.inert = true;
      iframe.src = "about:blank";
      if (document.activeElement === iframe) release(iframe);
      latest.current.onFail(failure);
    };
    const send = (message: { kind: string }) => {
      try {
        target.postMessage(message, "*");
      } catch {
        // A message that cannot be cloned is not sent.
        return;
      }
      // Init and update are answered with ready; a theme is not.
      if (message.kind === "init" || message.kind === "update") {
        awaiting = true;
        awaitReady();
      }
    };
    const awaitReady = () => {
      window.clearTimeout(readyTimer);
      readyTimer = window.setTimeout(() => fail({ why: "slow" }), READY_TIMEOUT);
    };
    /** A detail's height: through the guard, at most one a frame. */
    const height = (requested: number, now: boolean) => {
      if (face !== "detail") return;
      const next = sizes.next(requested, performance.now());
      if (next === null) return;
      pendingHeight = next;
      if (now) {
        cancelAnimationFrame(heightFrame);
        heightFrame = 0;
        latest.current.onHeight(next);
        return;
      }
      if (heightFrame) return;
      heightFrame = requestAnimationFrame(() => {
        heightFrame = 0;
        if (!done) latest.current.onHeight(pendingHeight);
      });
    };
    /** The reader gave this detail the keyboard: with a Tab that landed on
     *  it, or a click in it. */
    let consented = false;
    const keyboardGiven = () =>
      face === "detail" && document.activeElement === iframe && document.hasFocus() && consented;
    /** A jump or a question: from a detail the reader is in, during a user
     *  activation, one a second at most. */
    const mayAsk = () =>
      keyboardGiven() && userActive() && !(performance.now() - lastAsk < ASK_INTERVAL);
    const heard = (data: unknown) => {
      if (done) return;
      takeBack();
      if (done) return;
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
          if (!awaiting) {
            // Unasked, a ready is no more than a resize.
            if (ready) height(message.metrics.height, false);
            return;
          }
          awaiting = false;
          window.clearTimeout(readyTimer);
          height(message.metrics.height, !ready);
          ready = true;
          latest.current.onReady();
          return;
        case "resize":
          if (ready) height(message.height, false);
          return;
        case "error":
          // A render that threw or ran out of time never draws: the frame
          // fails now rather than at the timeout. Anything else (a logged
          // error, a blocked request, an error after it drew) leaves the
          // tile drawing; it is the author's to read in a render check.
          if (!ready && (message.code === "script_error" || message.code === "timeout"))
            fail({ why: "error", message: message.message });
          return;
        case "jump":
          if (mayAsk() && latest.current.onJump(message.key)) lastAsk = performance.now();
          return;
        case "ask":
          if (!mayAsk()) return;
          lastAsk = performance.now();
          latest.current.onAsk();
          return;
        case "open":
          // Only a glance would open its own detail, and a glance is inert.
          return;
        case "key":
          if (!keyboardGiven()) return;
          // The page's own keys act on it as if pressed on the frame: the
          // event carries the whitelisted key's name and Shift, nothing else.
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
    // never (it lies under its card's link), a detail when a Tab lands on
    // its element or the reader clicks in it. The page sees neither the
    // click nor what happens inside; it sees the pointer over the frame's
    // element, its own window losing focus, and the browser's user
    // activation, and it takes a click to be one only when the page saw no
    // key press of its own just before (the reader typing elsewhere, the
    // pointer resting on the frame, is not a click). Focus a frame takes by
    // itself goes back to where it was; a frame that keeps taking it is
    // taken down.
    let stolen = 0;
    let focusCheck = 0;
    let pointed = false;
    let tabbed = false;
    const over = () => {
      pointed = true;
    };
    const out = () => {
      pointed = false;
    };
    const since = (at: number) => performance.now() - at;
    /** The page's own Tab just now: what moved its focus onto the frame
     *  (a frame cannot press a key in the page). Chromium fires no focus
     *  event on the element; the window's blur follows the key at once. */
    const justTabbed = () => {
      const tab = since(lastTab);
      return tab >= 0 && tab < TAB_LANDS;
    };
    const tabbedIn = () => {
      tabbed = face === "detail" && justTabbed();
    };
    iframe.addEventListener("pointerenter", over);
    iframe.addEventListener("pointerleave", out);
    iframe.addEventListener("focus", tabbedIn);
    // Each time it takes the keyboard counts once, however often the page
    // looks before it is given back.
    let holding = false;
    takeBack = () => {
      if (done) return;
      if (document.activeElement !== iframe) {
        consented = false;
        tabbed = false;
        holding = false;
        return;
      }
      if (consented) return;
      release(iframe);
      if (holding) return;
      holding = document.activeElement === iframe;
      if (++stolen > 3) fail({ why: "focus" });
    };
    const left = () => {
      const clicked = pointed && userActive() && since(lastPageKey) > TYPING;
      const given = face === "detail" && (tabbed || justTabbed() || clicked);
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
    // The keyboard back in the page ends what the reader gave the frame.
    const elsewhere = (event: FocusEvent) => {
      if (event.target === iframe) return;
      consented = false;
      tabbed = false;
    };
    window.addEventListener("blur", left);
    window.addEventListener("focus", back);
    document.addEventListener("focusin", elsewhere, true);
    const unlisten = listenToFrame(target, { heard, flooded: () => fail({ why: "flood" }) });
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
