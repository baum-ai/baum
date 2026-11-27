/** The page's side of the custom tile frame protocol (`baum.tile/1`; see
 *  "Custom tile kit" in oss/ARCHITECTURE.md). A frame's author can post
 *  anything, so every message is read here with strict guards: a message
 *  that is not exactly one of the kinds below, with fields of the right
 *  types, is dropped. Nothing a frame sends is ever drawn as markup. */

export const PROTOCOL = "baum.tile/1";

/** The newest kit major this page can host. The daemon serves the frame
 *  documents from the same build, so a tile made for a newer major is
 *  already unavailable; this is the page's own check of the same thing. */
export const KIT_SUPPORTED = 1;

/** The longest string a frame may send in any field. */
export const MAX_STRING = 1024;
/** The longest error message kept, as the kit clips its own. */
export const MAX_ERROR = 500;

/** A detail frame's height, in CSS pixels; past the most, it scrolls inside. */
export const HEIGHT_MIN = 120;
export const HEIGHT_MAX = 20000;

/** From mounting (setting the frame's address) to its `hello`. */
export const HELLO_TIMEOUT = 5000;
/** How long the render check waits for a `pong` from a frame that missed
 *  its `hello`: one that does not answer is stuck in its script, a hang.
 *  Over a second, as a hidden window runs timers about once a second. */
export const PING_TIMEOUT = 2000;
/** From `init` or `update` to the frame's `ready`. The kit gives a render
 *  3 seconds and then waits for its fonts, so this leaves it room. */
export const READY_TIMEOUT = 5000;

/** Keys a detail frame may hand to the page, when the tile did not use them. */
export const FORWARDED_KEYS = [
  "Escape",
  "u",
  "j",
  "k",
  "ArrowLeft",
  "ArrowRight",
  "[",
  "]",
  "?",
] as const;
export type ForwardedKey = (typeof FORWARDED_KEYS)[number];
const KEYS: ReadonlySet<string> = new Set(FORWARDED_KEYS);

export type ReadyMetrics = {
  height: number;
  text_chars: number;
  elements: number;
  overflow_x: boolean;
  overflow_y: boolean;
  /** Images, drawings and canvases of at least 16×16. */
  media: number;
  /** The tile drew `baum.empty`, or `baum.fail`. */
  empty_state: boolean;
  failed: boolean;
  /** Citation chips and diffs drawn with no payload. */
  unresolved: number;
  /** How long the render function took, before the kit settled. */
  render_ms: number;
};

/** What a frame may say, as the page reads it. */
export type FrameMessage =
  | { kind: "hello"; kit: number; registered: boolean }
  | { kind: "ready"; metrics: ReadyMetrics }
  | { kind: "resize"; height: number }
  | {
      kind: "error";
      code: string;
      message: string;
      line?: number;
      column?: number;
      blocked?: string;
    }
  | { kind: "jump"; key: string }
  | { kind: "ask" }
  | { kind: "open" }
  | { kind: "key"; key: ForwardedKey; shift: boolean }
  | { kind: "pong" };

const isPlainRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" &&
  value !== null &&
  !Array.isArray(value) &&
  (Object.getPrototypeOf(value) === Object.prototype || Object.getPrototypeOf(value) === null);

const count = (value: unknown, least = 0): number | null =>
  typeof value === "number" && Number.isSafeInteger(value) && value >= least ? value : null;

const text = (value: unknown, most = MAX_STRING): string | null =>
  typeof value === "string" && value.length <= most ? value : null;

const clip = (value: string, most: number) =>
  value.length > most ? `${value.slice(0, most - 1)}…` : value;

/** A detail's height request held to the bounds; null when it is not a
 *  height at all. */
export function clampHeight(value: unknown): number | null {
  if (typeof value !== "number" || !Number.isFinite(value)) return null;
  return Math.min(HEIGHT_MAX, Math.max(HEIGHT_MIN, Math.ceil(value)));
}

/** Reads one message a frame posted, or null for anything that is not a
 *  well-formed message of a known kind. Unknown fields are ignored; a field
 *  of the wrong type, or a string over its cap where the exact value
 *  matters, drops the whole message. */
export function readMessage(data: unknown): FrameMessage | null {
  if (!isPlainRecord(data) || data.protocol !== PROTOCOL || typeof data.kind !== "string")
    return null;
  switch (data.kind) {
    case "hello": {
      const kit = count(data.kit, 1);
      if (kit === null || typeof data.registered !== "boolean") return null;
      return { kind: "hello", kit, registered: data.registered };
    }
    case "ready": {
      const metrics = data.metrics;
      if (!isPlainRecord(metrics)) return null;
      // Held to a detail's bounds here, as a resize is.
      const height = count(metrics.height) === null ? null : clampHeight(metrics.height);
      if (height === null) return null;
      return {
        kind: "ready",
        metrics: {
          height,
          text_chars: count(metrics.text_chars) ?? 0,
          elements: count(metrics.elements) ?? 0,
          overflow_x: metrics.overflow_x === true,
          overflow_y: metrics.overflow_y === true,
          media: count(metrics.media) ?? 0,
          empty_state: metrics.empty_state === true,
          failed: metrics.failed === true,
          unresolved: count(metrics.unresolved) ?? 0,
          render_ms: count(metrics.render_ms) ?? 0,
        },
      };
    }
    case "resize": {
      const height = clampHeight(data.height);
      return height === null ? null : { kind: "resize", height };
    }
    case "error": {
      const code = text(data.code, 64);
      if (code === null || !/^[a-z_]{1,64}$/.test(code) || typeof data.message !== "string")
        return null;
      // The author writes the message: kept as text, clipped, never markup.
      const message: FrameMessage = {
        kind: "error",
        code,
        message: clip(data.message.slice(0, MAX_STRING), MAX_ERROR),
      };
      const line = count(data.line, 1);
      const column = count(data.column, 0);
      const blocked = text(data.blocked);
      if (line !== null) message.line = line;
      if (column !== null) message.column = column;
      if (blocked !== null) message.blocked = blocked;
      return message;
    }
    case "jump": {
      const key = text(data.key);
      return key === null ? null : { kind: "jump", key };
    }
    case "ask":
    case "open":
    case "pong":
      return { kind: data.kind };
    case "key": {
      if (typeof data.key !== "string" || !KEYS.has(data.key)) return null;
      return { kind: "key", key: data.key as ForwardedKey, shift: data.shift === true };
    }
    default:
      return null;
  }
}

/** Page → frame messages. */
export type InitMessage = {
  protocol: typeof PROTOCOL;
  kind: "init";
  face: "glance" | "detail";
  size: "full" | "half" | "third";
  theme: "light" | "dark";
  lang: string;
  data: unknown;
  refs: Record<string, unknown>;
  /** Set by the render check's runner: the kit settles without animation
   *  frames or timers and answers `measure`. */
  check?: { id: string };
};

export function initMessage(fields: Omit<InitMessage, "protocol" | "kind">): InitMessage {
  return { protocol: PROTOCOL, kind: "init", ...fields };
}

export function updateMessage(data: unknown, refs: Record<string, unknown>) {
  return { protocol: PROTOCOL, kind: "update", data, refs } as const;
}

/** The render check's runner asks a detail for its height now; the kit
 *  answers with a `resize`, changed or not. */
export function measureMessage() {
  return { protocol: PROTOCOL, kind: "measure" } as const;
}

/** The render check's runner asks a frame that has not said hello whether
 *  its script still yields: the kit answers `pong` from its message
 *  listener, which a frame stuck in a loop never runs. */
export function pingMessage() {
  return { protocol: PROTOCOL, kind: "ping" } as const;
}

export function themeMessage(theme: "light" | "dark") {
  return { protocol: PROTOCOL, kind: "theme", theme } as const;
}

/** How many messages a frame may send. Past `PER_SECOND` in a second the
 *  rest of that second's messages are dropped unread; a frame over it in
 *  more than `OVER_SECONDS` of the last `WINDOW` seconds, or past `BURST` in
 *  any one second, is flooding and is unmounted. An honest frame sends a
 *  handful: `hello`, `ready`, a resize per animation frame while it settles,
 *  and an error or two. */
export const RATE = { PER_SECOND: 30, WINDOW: 10, OVER_SECONDS: 3, BURST: 300 } as const;

export type Admission = "ok" | "drop" | "flood";

/** Counts one frame's messages by the second, as `RATE` says. The router
 *  counts a message before anything reads it. */
export class RateGuard {
  private second = Number.NaN;
  private seen = 0;
  /** The seconds that went over the rate, newest last. */
  private over: number[] = [];
  private flooded = false;

  admit(now: number): Admission {
    if (this.flooded) return "flood";
    const second = Math.floor(now / 1000);
    if (second !== this.second) {
      this.second = second;
      this.seen = 0;
    }
    this.seen++;
    if (this.seen > RATE.BURST) {
      this.flooded = true;
      return "flood";
    }
    if (this.seen <= RATE.PER_SECOND) return "ok";
    if (this.seen === RATE.PER_SECOND + 1) {
      this.over = this.over.filter((at) => at > second - RATE.WINDOW);
      this.over.push(second);
      if (this.over.length > RATE.OVER_SECONDS) {
        this.flooded = true;
        return "flood";
      }
    }
    return "drop";
  }
}

/** What a detail's height may do. Ten increases by the same amount within a
 *  second are a feedback loop with the frame's own height (`vh` units, say),
 *  and so are `FLIPS` changes of direction within `FLIP_WINDOW` ms (a height
 *  that bounces between two values): either way the height stops following,
 *  as the kit's own guard would stop reporting. */
export const HEIGHT_LOOP = { GROWS: 10, FLIPS: 6, FLIP_WINDOW: 2000 } as const;

export class HeightGuard {
  private last = -1;
  private delta = 0;
  private since = 0;
  private grows = 0;
  private direction = 0;
  private flips: number[] = [];
  frozen = false;

  /** The height to apply, or null to keep the current one. */
  next(height: number, now: number): number | null {
    if (this.frozen) return null;
    if (this.last >= 0 && height !== this.last) {
      const direction = Math.sign(height - this.last);
      if (this.direction !== 0 && direction !== this.direction) {
        this.flips = this.flips.filter((at) => now - at <= HEIGHT_LOOP.FLIP_WINDOW);
        this.flips.push(now);
        if (this.flips.length >= HEIGHT_LOOP.FLIPS) {
          this.frozen = true;
          return null;
        }
      }
      this.direction = direction;
    }
    if (this.last >= 0 && height > this.last) {
      const delta = height - this.last;
      if (delta === this.delta && now - this.since <= 1000) this.grows++;
      else {
        this.delta = delta;
        this.since = now;
        this.grows = 1;
      }
      if (this.grows >= HEIGHT_LOOP.GROWS) {
        this.frozen = true;
        return null;
      }
    } else this.grows = 0;
    this.last = height;
    return height;
  }
}
