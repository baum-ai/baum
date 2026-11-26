import { useEffect, useRef, useSyncExternalStore } from "react";
import type { Entry } from "./api";
import type { ConversationSummary } from "./console-api";
import { uiState, useUIState } from "./ui-state";
import { upNextKey } from "./up-next";

/** What happened on a pull request since the person last had its page open,
 *  by kind. */
export type Activity = {
  /** New heads: pushes to its branch, as the app saw them. */
  pushes?: number;
  /** New review versions baum wrote. */
  reviews?: number;
  /** More review threads left open on GitHub. */
  threads?: number;
  /** A reviewer approved, requested changes or commented. */
  decisions?: number;
  /** A session panel conversation about it finished a turn. */
  answers?: number;
  /** Anything else GitHub updated it for, as a comment. */
  github?: number;
};
type Kind = keyof Activity;

/** A pull request as last seen: what the next read is compared with. */
type Observed = {
  head: string;
  versions: number;
  unresolved: number;
  review: string;
  updated: string;
  /** Each finished panel conversation about it, by id: when it last changed. */
  answers: Record<string, string>;
};
type PullRecord = {
  /** When it last changed or was opened, which keeps the newest records. */
  at: number;
  last: Observed;
  unread: Activity;
};
type Stored = { prs: Record<string, PullRecord> };

/** How many pull requests the record keeps, the latest changed first. */
export const KEEP = 500;

const DECISIONS = new Set(["approved", "changes_requested", "commented"]);
const KINDS: Kind[] = ["answers", "reviews", "pushes", "threads", "decisions", "github"];
const WORDS: Record<Kind, [string, string]> = {
  answers: ["agent answer", "agent answers"],
  reviews: ["new review", "new reviews"],
  pushes: ["push", "pushes"],
  threads: ["new thread", "new threads"],
  decisions: ["review on GitHub", "reviews on GitHub"],
  github: ["update on GitHub", "updates on GitHub"],
};

export function activityCount(activity: Activity | undefined): number {
  return activity ? KINDS.reduce((sum, kind) => sum + (activity[kind] ?? 0), 0) : 0;
}

/** The activity in words, the largest kinds first: "2 pushes and 1 agent answer". */
export function activityText(activity: Activity): string {
  const parts = KINDS.filter((kind) => (activity[kind] ?? 0) > 0)
    .sort((a, b) => (activity[b] ?? 0) - (activity[a] ?? 0))
    .map((kind) => {
      const n = activity[kind] ?? 0;
      return `${n} ${WORDS[kind][n === 1 ? 0 : 1]}`;
    });
  if (parts.length < 2) return parts[0] ?? "";
  return `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}`;
}

/** Several pull requests' activity added up, as a group's row counts its members'. */
export function sumActivity(list: (Activity | undefined)[]): Activity | undefined {
  const sum = list.reduce<Activity>((total, one) => (one ? add(total, one) : total), {});
  return activityCount(sum) > 0 ? sum : undefined;
}

const isOpen = (entry: Entry) =>
  !entry.historical &&
  (!entry.pull_request.state || entry.pull_request.state.toUpperCase() === "OPEN");

/** How many panel conversations of one pull request the record follows, the
 *  newest first. */
const ANSWERS = 100;

/** A pull request as the latest read shows it. Each panel conversation about
 *  it is kept as `running` while a turn runs, else as when it last changed;
 *  a closed one is left out. `conversations` is null until the panel's list is
 *  read, which keeps what was seen of them. */
export function observe(
  entry: Entry,
  conversations: ConversationSummary[] | null,
  previous?: Observed,
): Observed {
  const pr = entry.pull_request;
  let answers = previous?.answers ?? {};
  if (conversations) {
    const found = conversations
      .filter(
        (c) =>
          c.state !== "closed" &&
          c.pull_request === pr.number &&
          c.repository?.toLowerCase() === pr.repository.toLowerCase(),
      )
      .slice(0, ANSWERS)
      .map((c): [string, string] => [c.id, c.state === "running" ? "running" : c.updated_at]);
    // In id order, so a list reordered alone changes nothing.
    answers = Object.fromEntries(found.sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)));
  }
  // A head discovery saw before GitHub's details for it has no review or
  // threads yet: keep what was seen, so their return counts nothing.
  const known = entry.unresolved !== null || !previous;
  return {
    head: pr.head_sha,
    versions: entry.version_count,
    unresolved: known ? (entry.unresolved ?? 0) : previous.unresolved,
    review: known ? entry.review : previous.review,
    updated: pr.updated_at ?? "",
    answers,
  };
}

/** What changed from one read of a pull request to the next. An update on
 *  GitHub counts only when no push, thread or review explains it. */
export function changes(before: Observed, after: Observed): Activity {
  const found: Activity = {};
  if (before.head && after.head && before.head !== after.head) found.pushes = 1;
  if (after.versions > before.versions) found.reviews = after.versions - before.versions;
  if (after.unresolved > before.unresolved) found.threads = after.unresolved - before.unresolved;
  if (after.review !== before.review && DECISIONS.has(after.review)) found.decisions = 1;
  // A turn that finished: one seen running, or one that ran between two
  // reads. A conversation just started, which has no turn yet, is not one.
  const answered = Object.entries(after.answers).filter(
    ([id, at]) => at !== "running" && id in before.answers && before.answers[id] !== at,
  ).length;
  if (answered) found.answers = answered;
  if (
    before.updated &&
    after.updated > before.updated &&
    !found.pushes &&
    !found.reviews &&
    !found.threads &&
    !found.decisions
  )
    found.github = 1;
  return found;
}

function add(a: Activity, b: Activity): Activity {
  const sum: Activity = { ...a };
  for (const kind of KINDS) if (b[kind]) sum[kind] = (sum[kind] ?? 0) + (b[kind] ?? 0);
  return sum;
}

/** The record after a read: each open pull request's changes added to what
 *  is unread, those open on screen (`viewing`) read, and a pull request seen
 *  the first time taken as it is, once the panel's conversations are read.
 *  Null when nothing changed. */
export function advance(
  stored: Stored,
  entries: Entry[],
  conversations: ConversationSummary[] | null,
  viewing: string[],
  now: number,
): Stored | null {
  const prs = { ...stored.prs };
  let changed = false;
  // A pull request is listed once per clone: read the one with most reviews.
  const open = new Map<string, Entry>();
  for (const entry of entries) {
    if (!isOpen(entry)) continue;
    const key = upNextKey(entry.pull_request);
    const seen = open.get(key);
    if (!seen || entry.version_count > seen.version_count) open.set(key, entry);
  }
  for (const [key, entry] of open) {
    const record = prs[key];
    // A first sight waits for the panel's conversations and GitHub's details.
    if (!record && (!conversations || entry.unresolved === null)) continue;
    const last = observe(entry, conversations, record?.last);
    const unread =
      viewing.includes(key) || !record ? {} : add(record.unread, changes(record.last, last));
    if (
      record &&
      JSON.stringify(record.last) === JSON.stringify(last) &&
      JSON.stringify(record.unread) === JSON.stringify(unread)
    )
      continue;
    prs[key] = { at: now, last, unread };
    changed = true;
  }
  if (!changed) return null;
  const keys = Object.keys(prs);
  if (keys.length > KEEP)
    for (const key of keys.sort((a, b) => prs[b]!.at - prs[a]!.at).slice(KEEP)) delete prs[key];
  return { prs };
}

function onVisibility(change: () => void) {
  document.addEventListener("visibilitychange", change);
  return () => document.removeEventListener("visibilitychange", change);
}

const record = () => uiState<Stored>("api/ui-state/activity", () => ({ prs: {} }));

/** Counts what happened on each open pull request since the person last had
 *  its page open, on this computer. `ready` holds the counting back until a
 *  complete read, so a failed one counts nothing; `viewing` holds the keys of
 *  the pull requests whose page is open (a group's page shows its members). */
export function usePullActivity(
  entries: Entry[],
  conversations: ConversationSummary[] | null,
  viewing: string[],
  ready: boolean,
) {
  const persistence = useUIState(record());
  const stored = persistence.value;
  // The reads last counted: a record saved, here or in another window, is
  // counted on by the next read, not by these again.
  const counted = useRef<unknown[]>([]);
  const page = viewing.join(" ");
  const visible = useSyncExternalStore(onVisibility, () => !document.hidden);
  useEffect(() => {
    if (!ready || !persistence.ready || !visible) return;
    const reads = [entries, conversations, page];
    if (reads.every((read, i) => read === counted.current[i])) return;
    counted.current = reads;
    if (!advance(stored, entries, conversations, viewing, Date.now())) return;
    // Replayed onto a record another window saved meanwhile, these reads count
    // only the pull requests it saw as these did: the others it read anew.
    const seen = (prs: Stored["prs"], key: string) => JSON.stringify(prs[key]?.last);
    persistence.update((previous) => {
      const mine = entries.filter((entry) => {
        const key = upNextKey(entry.pull_request);
        return seen(previous.prs, key) === seen(stored.prs, key);
      });
      return advance(previous, mine, conversations, viewing, Date.now()) ?? previous;
    });
  }, [ready, persistence.ready, visible, stored, entries, conversations, page]);
  return (pr: Pick<Entry["pull_request"], "repository" | "number">): Activity | undefined => {
    const unread = stored.prs[upNextKey(pr)]?.unread;
    return activityCount(unread) > 0 ? unread : undefined;
  };
}
