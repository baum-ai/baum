/** Number, duration and date formatting shared by every analytics widget.
 *
 *  All of it goes through Intl with an explicit `undefined` locale, so a
 *  server-rendered string and its client re-render agree. Hard-coding "en-US"
 *  would lie to non-US readers; passing a browser locale during SSR is not
 *  possible, so the two must resolve the same way.
 *
 *  Every formatter takes null/undefined and returns the em dash, because "no
 *  data yet" and "zero" have to look different on screen. */

import { SERIES } from "../series";

const EM_DASH = "—";

const integer = new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 });
const oneDecimal = new Intl.NumberFormat(undefined, {
  minimumFractionDigits: 1,
  maximumFractionDigits: 1,
});
const compact = new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 });
const integerEnUS = new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 });
const compactEnUS = new Intl.NumberFormat("en-US", {
  notation: "compact",
  maximumFractionDigits: 1,
});

export function formatCount(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return EM_DASH;
  return integer.format(value);
}

/** Below 1000 a compact formatter would still abbreviate to the same digits it
 *  started with, so both compact formatters below share this one threshold
 *  rather than each deciding it independently. */
function compactWith(exact: Intl.NumberFormat, abbreviated: Intl.NumberFormat) {
  return (value: number | null | undefined): string => {
    if (value == null || !Number.isFinite(value)) return EM_DASH;
    return Math.abs(value) < 1000 ? exact.format(value) : abbreviated.format(value);
  };
}

/** Compact form for axis ticks and tiles where four digits will not fit. */
export const formatCompact = compactWith(integer, compact);

/** The abbreviated diff-stat idiom: fixed to en-US punctuation like
 *  {@link formatLines} in `review-document.ts`, so a tile that abbreviates a
 *  line count and a panel that spells out the same figure in full never show
 *  the reader two different decimal conventions for one number. */
export const formatLinesCompact = compactWith(integerEnUS, compactEnUS);

/** Takes a share in 0..1, not a pre-multiplied percentage. */
export function formatShare(value: number | null | undefined, digits = 0): string {
  if (value == null || !Number.isFinite(value)) return EM_DASH;
  const percent = value * 100;
  if (digits === 0) return `${integer.format(percent)}%`;
  return `${oneDecimal.format(percent)}%`;
}

export function formatDelta(value: number | null | undefined, digits = 0): string {
  if (value == null || !Number.isFinite(value)) return EM_DASH;
  const sign = value > 0 ? "+" : "";
  return `${sign}${formatShare(value, digits)}`;
}

/** Money is never abbreviated below a dollar: "$0.4k" hides the interesting part
 *  of a cost figure, which is usually its order of magnitude near zero.
 *
 *  The currency part is always a plain "$". The default symbol is "US$"
 *  wherever the dollar is not the local currency, and even the narrow symbol
 *  stays "US$" or "USD" in some locales (Arabic, Albanian), so the formatted
 *  currency part is replaced outright. Digits, separators and symbol placement
 *  still follow the locale. `locale` defaults to the runtime's, like every
 *  formatter here; tests pass one to check other locales. */
export function formatUSD(value: number | null | undefined, locale?: string): string {
  if (value == null || !Number.isFinite(value)) return EM_DASH;
  const absolute = Math.abs(value);
  const digits = absolute >= 100 ? 0 : absolute >= 1 ? 2 : 4;
  return new Intl.NumberFormat(locale, {
    style: "currency",
    currency: "USD",
    currencyDisplay: "narrowSymbol",
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  })
    .formatToParts(value)
    .map((part) => (part.type === "currency" ? "$" : part.value))
    .join("");
}

/** Seconds to the coarsest unit that still says something useful. */
export function formatDuration(seconds: number | null | undefined): string {
  if (seconds == null || !Number.isFinite(seconds)) return EM_DASH;
  if (seconds < 60) return `${integer.format(seconds)}s`;
  const minutes = seconds / 60;
  if (minutes < 60) return `${integer.format(minutes)}m`;
  const hours = minutes / 60;
  if (hours < 24) return `${oneDecimal.format(hours)}h`;
  const days = hours / 24;
  return `${oneDecimal.format(days)}d`;
}

const dayLabel = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" });
const monthLabel = new Intl.DateTimeFormat(undefined, { month: "short", year: "numeric" });
const hourLabel = new Intl.DateTimeFormat(undefined, { hour: "numeric" });
const fullLabel = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  year: "numeric",
});

export type Granularity = "hour" | "day" | "week" | "month";

const BUCKET_OPTIONS: Record<Granularity, Intl.DateTimeFormatOptions> = {
  hour: { hour: "numeric" },
  day: { month: "short", day: "numeric" },
  week: { month: "short", day: "numeric" },
  month: { month: "short", year: "numeric" },
};

const bucketLabels = new Map<string, Intl.DateTimeFormat>();

function bucketLabel(granularity: Granularity, timeZone: string): Intl.DateTimeFormat {
  const key = `${granularity}|${timeZone}`;
  let label = bucketLabels.get(key);
  if (!label) {
    try {
      label = new Intl.DateTimeFormat(undefined, { ...BUCKET_OPTIONS[granularity], timeZone });
    } catch {
      // An unknown zone name falls back to the reader's own rather than failing
      // the whole chart.
      label = new Intl.DateTimeFormat(undefined, BUCKET_OPTIONS[granularity]);
    }
    bucketLabels.set(key, label);
  }
  return label;
}

/** The axis tick for one series bucket. Weeks read as their start day, which is
 *  what the bucket actually is.
 *
 *  `timeZone` is the zone the series was bucketed in, from its scope.
 *  A bucket is the instant a local day began there, so reading it in any other
 *  zone — the browser's, when a shared link pinned UTC — labels a day with its
 *  neighbour's date. */
export function formatBucket(iso: string, granularity: Granularity, timeZone?: string): string {
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return EM_DASH;
  if (timeZone) return bucketLabel(granularity, timeZone).format(at);
  switch (granularity) {
    case "hour":
      return hourLabel.format(at);
    case "month":
      return monthLabel.format(at);
    default:
      return dayLabel.format(at);
  }
}

export function formatDate(iso: string | null | undefined): string {
  if (!iso) return EM_DASH;
  const at = new Date(iso);
  return Number.isNaN(at.getTime()) ? EM_DASH : fullLabel.format(at);
}

const timeLabel = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" });

/** The time of day an instant falls at, in the reader's own zone, for a row
 *  that already sits under its day. */
export function formatTime(iso: string | null | undefined): string {
  if (!iso) return EM_DASH;
  const at = new Date(iso);
  return Number.isNaN(at.getTime()) ? EM_DASH : timeLabel.format(at);
}

/** Relative time for "synced 4 minutes ago" style captions. */
export function formatRelative(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return EM_DASH;
  const at = new Date(iso).getTime();
  if (Number.isNaN(at)) return EM_DASH;
  const seconds = Math.round((at - now) / 1000);
  const relative = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  const units: [Intl.RelativeTimeFormatUnit, number][] = [
    ["second", 60],
    ["minute", 60],
    ["hour", 24],
    ["day", 30],
    ["month", 12],
  ];
  let value = seconds;
  for (const [unit, limit] of units) {
    if (Math.abs(value) < limit) return relative.format(value, unit);
    value = Math.round(value / limit);
  }
  return relative.format(value, "year");
}

/** Whole hours, where 0 means "within the hour" rather than "instantly".
 *  formatDuration(0) renders "0s", which reads as a measurement error on a
 *  latency that was only ever recorded to the hour. */
export function formatHours(hours: number | null | undefined): string {
  if (hours == null || !Number.isFinite(hours)) return EM_DASH;
  if (hours < 1) return "<1h";
  return formatDuration(hours * 3600);
}

/** Two letters from a name, for an avatar with no image behind it.
 *
 *  Splits on slashes, underscores and hyphens as well as whitespace: the names
 *  this is given are as often `acme-corp` or `acme/backend` as "Ada Kim". */
export function initialsFor(name: string): string {
  const parts = name.split(/[\s/_-]+/).filter(Boolean);
  if (parts.length === 0) return "?";
  return parts
    .map((part) => part[0])
    .join("")
    .slice(0, 2)
    .toUpperCase();
}

/** A short commit sha, the way every git tool shows one. */
export function formatSHA(sha: string): string {
  return sha.slice(0, 7);
}

/** A stable colour per person, drawn from the chart palette so a participant
 *  never collides with a status tone. Hashing the name means every reader of
 *  a thread sees the same person in the same colour with nothing to keep in
 *  sync. */
export function participantColor(name: string): string {
  let hash = 0;
  for (let i = 0; i < name.length; i++) hash = (hash * 31 + name.charCodeAt(i)) | 0;
  return SERIES[Math.abs(hash) % SERIES.length]!;
}

/** Seconds between two instants, or null when either end is missing.
 *
 *  Null rather than 0: a session with no recorded end did not last no time. */
export function durationSeconds(
  from: string | null | undefined,
  to: string | null | undefined,
): number | null {
  if (!from || !to) return null;
  const start = Date.parse(from);
  const end = Date.parse(to);
  if (Number.isNaN(start) || Number.isNaN(end) || end < start) return null;
  return Math.round((end - start) / 1000);
}

export { EM_DASH };

/** Evidence comparisons need time of day, with an explicit common timezone. */
export function formatDateTime(iso: string): string {
  return `${new Intl.DateTimeFormat("en-GB", { dateStyle: "medium", timeStyle: "short", timeZone: "UTC" }).format(new Date(iso))} UTC`;
}

const shortDateTimeLabel = new Intl.DateTimeFormat("en-GB", {
  day: "numeric",
  month: "short",
  hour: "2-digit",
  minute: "2-digit",
  timeZone: "UTC",
});

/** An instant as a day and a time without the year, for a run of events close
 *  together; `formatDateTime` gives the whole of it, for a tooltip. */
export function formatShortDateTime(iso: string): string {
  return shortDateTimeLabel.format(new Date(iso));
}

/** GitHub's picture for a login, from the public redirect every profile has.
 *  A GitHub App's login ends in "[bot]" and has no such page, so it gets no
 *  URL and the caller falls back to initials. */
export function githubAvatarURL(login: string | null | undefined, size = 64): string | null {
  if (!login || login.endsWith("[bot]")) return null;
  return `https://github.com/${encodeURIComponent(login)}.png?size=${size}`;
}

/** How many code points `text` holds: a surrogate pair counts once. */
export function codePoints(text: string): number {
  let count = 0;
  for (let i = 0; i < text.length; i++) {
    const unit = text.charCodeAt(i);
    if (unit < 0xdc00 || unit > 0xdfff || i === 0 || !isHighSurrogate(text.charCodeAt(i - 1))) {
      count++;
    }
  }
  return count;
}

function isHighSurrogate(unit: number): boolean {
  return unit >= 0xd800 && unit <= 0xdbff;
}
