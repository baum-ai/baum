import type { Entry } from "./api";

/** What pull requests can be sorted by. */
export type SortField =
  | "opened"
  | "activity"
  | "risk"
  | "time"
  | "size"
  | "repo"
  | "author"
  | "reviewed";

/** One key of a sort: the field, and whether it runs against its first
 *  direction (`reversed`). */
export type SortKey = { field: SortField; reversed: boolean };

type Value = number | string | null;

/** A field, its words and how it reads a pull request. `descending` is the
 *  direction it takes when added: the one most often wanted. A pull request
 *  without a value goes last whichever way the field runs. */
export type SortFieldInfo = {
  field: SortField;
  label: string;
  descending: boolean;
  /** The direction as words: first as added, then reversed. */
  words: [string, string];
  value: (entry: Entry) => Value;
};

const time = (text: string | undefined | null) => (text ? Date.parse(text) || null : null);
const lower = (text: string | undefined | null) => (text ? text.toLowerCase() : null);

/** The fields in the menu's order, which its digit keys follow. */
export const SORT_FIELDS: SortFieldInfo[] = [
  {
    field: "opened",
    label: "Opened",
    descending: false,
    words: ["Oldest first", "Newest first"],
    value: (entry) => time(entry.pull_request.created_at),
  },
  {
    field: "activity",
    label: "Last activity",
    descending: true,
    words: ["Newest first", "Oldest first"],
    value: (entry) => time(entry.pull_request.updated_at),
  },
  {
    field: "risk",
    label: "Risk",
    descending: true,
    words: ["Highest first", "Lowest first"],
    value: (entry) => entry.risk_score?.score ?? null,
  },
  {
    field: "time",
    label: "Review time",
    descending: false,
    words: ["Shortest first", "Longest first"],
    value: (entry) => entry.review_minutes ?? null,
  },
  {
    field: "size",
    label: "Size",
    descending: false,
    words: ["Smallest first", "Largest first"],
    value: ({ pull_request: pr }) =>
      pr.additions === undefined && pr.deletions === undefined
        ? null
        : (pr.additions ?? 0) + (pr.deletions ?? 0),
  },
  {
    field: "repo",
    label: "Repository",
    descending: false,
    words: ["A to Z", "Z to A"],
    value: (entry) => lower(entry.pull_request.repository),
  },
  {
    field: "author",
    label: "Author",
    descending: false,
    words: ["A to Z", "Z to A"],
    value: (entry) => lower(entry.pull_request.author),
  },
  {
    field: "reviewed",
    label: "Reviewed",
    descending: true,
    words: ["Newest first", "Oldest first"],
    value: (entry) => time(entry.latest?.generated_at),
  },
];

const INFO = new Map(SORT_FIELDS.map((info) => [info.field, info]));
export const sortInfo = (field: SortField) => INFO.get(field)!;

/** A sort as written: its fields in turn, comma-separated, each reversed with
 *  `-` before it (`opened,-risk`). Unknown or repeated fields are skipped, so
 *  a sort from a newer app still reads. "" is none: Priority alone. */
export function parseSort(text: string | undefined | null): SortKey[] {
  const keys: SortKey[] = [];
  for (const part of (text ?? "").split(",")) {
    const word = part.trim();
    const reversed = word.startsWith("-");
    const field = (reversed ? word.slice(1) : word) as SortField;
    if (!INFO.has(field) || keys.some((key) => key.field === field)) continue;
    keys.push({ field, reversed });
  }
  return keys;
}

export function formatSort(keys: SortKey[]): string {
  return keys.map((key) => `${key.reversed ? "-" : ""}${key.field}`).join(",");
}

/** Adds the field as the last key, or turns it round if the sort has it. */
export function toggleSortField(text: string, field: SortField): string {
  const keys = parseSort(text);
  const at = keys.findIndex((key) => key.field === field);
  if (at < 0) return formatSort([...keys, { field, reversed: false }]);
  return formatSort(keys.map((key, i) => (i === at ? { ...key, reversed: !key.reversed } : key)));
}

/** The sort without its last key. */
export function dropLastSortKey(text: string): string {
  return formatSort(parseSort(text).slice(0, -1));
}

/** The sort without the field. */
export function withoutSortField(text: string, field: SortField): string {
  return formatSort(parseSort(text).filter((key) => key.field !== field));
}

/** The direction a key runs, in words. */
export function sortWords(key: SortKey): string {
  return sortInfo(key.field).words[key.reversed ? 1 : 0];
}

/** The sort in words, as the button's title says it: "Opened, oldest first,
 *  then Risk, highest first". */
export function describeSort(text: string): string {
  const keys = parseSort(text);
  if (keys.length === 0) return "Priority";
  return (
    keys
      // "Oldest first" reads on as "oldest first"; "A to Z" stays.
      .map(
        (key) =>
          `${sortInfo(key.field).label}, ${sortWords(key).replace(/^[A-Z][a-z]/, (start) => start.toLowerCase())}`,
      )
      .join(", then ")
  );
}

/** Orders two pull requests by the keys in turn; 0 when they tie on all. */
export function compareEntries(keys: SortKey[], a: Entry, b: Entry): number {
  for (const key of keys) {
    const info = sortInfo(key.field);
    const x = info.value(a);
    const y = info.value(b);
    if (x === y) continue;
    // Without a value goes last, whichever way the key runs.
    if (x === null) return 1;
    if (y === null) return -1;
    const order =
      typeof x === "number" && typeof y === "number"
        ? x - y
        : String(x).localeCompare(String(y), undefined, { numeric: true });
    if (order === 0) continue;
    return info.descending !== key.reversed ? -order : order;
  }
  return 0;
}

/** The items sorted by the keys, keeping their order (Priority) where they
 *  tie; the same array when there are no keys. */
export function sortBy<T>(items: T[], keys: SortKey[], entryOf: (item: T) => Entry): T[] {
  if (keys.length === 0) return items;
  return [...items].sort((a, b) => compareEntries(keys, entryOf(a), entryOf(b)));
}
