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
  | { kind: "key"; key: ForwardedKey; shift: boolean };

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
      const height = count(metrics.height);
      if (height === null) return null;
      return {
        kind: "ready",
        metrics: {
          height,
          text_chars: count(metrics.text_chars) ?? 0,
          elements: count(metrics.elements) ?? 0,
          overflow_x: metrics.overflow_x === true,
          overflow_y: metrics.overflow_y === true,
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
};

export function initMessage(fields: Omit<InitMessage, "protocol" | "kind">): InitMessage {
  return { protocol: PROTOCOL, kind: "init", ...fields };
}

export function updateMessage(data: unknown, refs: Record<string, unknown>) {
  return { protocol: PROTOCOL, kind: "update", data, refs } as const;
}

export function themeMessage(theme: "light" | "dark") {
  return { protocol: PROTOCOL, kind: "theme", theme } as const;
}

/** How many messages a frame may send. Past `PER_SECOND` in a second the
 *  rest of that second's messages are dropped unread; a frame over it for
 *  `SUSTAINED` seconds in a row, or past `BURST` in any one second, is
 *  flooding and is unmounted. An honest frame sends a handful: `hello`,
 *  `ready`, a resize per animation frame while it settles, and an error or
 *  two. */
export const RATE = { PER_SECOND: 30, SUSTAINED: 3, BURST: 300 } as const;

export type Admission = "ok" | "drop" | "flood";

/** Counts one frame's messages by the second, as `RATE` says. */
export class RateGuard {
  private second = Number.NaN;
  private seen = 0;
  private overStreak = 0;
  private lastOver = Number.NaN;
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
      // The first message over the limit in this second extends the streak
      // when the second before was over too.
      this.overStreak = this.lastOver === second - 1 ? this.overStreak + 1 : 1;
      this.lastOver = second;
      if (this.overStreak >= RATE.SUSTAINED) {
        this.flooded = true;
        return "flood";
      }
    }
    return "drop";
  }
}

/** Ten height increases by the same amount within a second are a feedback
 *  loop with the frame's own height (`vh` units, say): the height stops
 *  following, as the kit's own guard would stop reporting. */
export class HeightGuard {
  private last = -1;
  private delta = 0;
  private since = 0;
  private grows = 0;
  frozen = false;

  /** The height to apply, or null to keep the current one. */
  next(height: number, now: number): number | null {
    if (this.frozen) return null;
    if (this.last >= 0 && height > this.last) {
      const delta = height - this.last;
      if (delta === this.delta && now - this.since <= 1000) this.grows++;
      else {
        this.delta = delta;
        this.since = now;
        this.grows = 1;
      }
      if (this.grows >= 10) {
        this.frozen = true;
        return null;
      }
    } else this.grows = 0;
    this.last = height;
    return height;
  }
}
