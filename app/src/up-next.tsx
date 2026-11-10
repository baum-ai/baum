import { Stumpy } from "@baum/viewer/components/stumpy";
import { uiState, useUIState } from "./ui-state";
import {
  FilterBar,
  FilterButton,
  FilterSaveError,
  SaveButton,
  type SaveOption,
} from "./filter-menu";
import { matchesQuery, parseQuery, queryProblem, useFilterDraft } from "./pr-filter";
import {
  Fragment,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type ComponentType,
  type Dispatch,
  type ReactNode,
  type SetStateAction,
} from "react";
import { formatCount } from "@baum/viewer/components/format";
import {
  BranchIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  GitHubIcon,
  MergeIcon,
  StackIcon,
} from "@baum/viewer/components/icons";
import { InlineMarkdown, Markdown } from "@baum/viewer/components/markdown";
import { Lines } from "@baum/viewer/components/review-parts";
import { Alert, Badge, Button, CloseButton, LiveDot } from "@baum/viewer/components/ui";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import {
  RISK_SCORE_MEANING,
  sectionsInReviewOrder,
  type ReviewDocument,
  type RiskScoreLevel,
} from "@baum/viewer/review-document";
import {
  appURL,
  documentRequest,
  groupDocumentRequest,
  fixJob,
  fixLabel,
  isActiveJob,
  jobHref,
  agentName,
  jobReasonText,
  changedNothing,
  summaryLead,
  joinAnd,
  liveFor,
  navigate,
  openExternal,
  request,
  reviewJob,
  sameRepository,
  watchableFix,
  type Entry,
  type FixOnly,
  type Job,
  type LiveSession,
  type ReviewGroup,
} from "./api";
import { fixStep, type FixState } from "./fix";
import {
  COLOR_NAMES,
  foldedQuery,
  holds,
  INBOX_COLORS,
  withQuery,
  inboxesOf,
  nameProblem,
  useHeld,
  type Held,
  type Inbox,
  type InboxColor,
} from "./inboxes";
import { useDisclosure } from "@baum/viewer/components/use-disclosure";
import { MenuButton, useContextMenu, useMenuFocus, type MenuItem } from "./context-menu";
import { ListSection, type ListTone } from "./list";
import { useNow } from "./live";
import { peekHref } from "./sessions";
import {
  canMerge,
  defaultMergeMethod,
  mergeMethodLabel,
  mergeRefusal,
  MergeDialog,
  type MergeOptions,
} from "./merge";
import { useModal } from "./modal";
import { LoadingStatus, NineDots } from "./nine-dots";
import {
  GITHUB_KEY_LABEL,
  KeyBar,
  onBody,
  useGitHubKey,
  useLastRow,
  useShortcuts,
} from "./shortcuts";
import { PullRequestChecks, checksCount, checksSummary, useChecks } from "./checks";
import { ConflictingFiles } from "./conflicts";
import { RefreshButton, type RefreshRun } from "./shell";
import { ReviewThreads } from "./threads";
import { canUpdateBranch, UpdateBranchDialog } from "./unblock";
import { PRIMARY, SECONDARY, SMALL, SMALL_PRIMARY } from "./action-classes";
import { useToast } from "./toast";
import { EVENT_LABELS, ReviewButton, ReviewDialog, usePendingReview } from "./pending-review";
import { reviewable } from "./pending-review-model";
import type { ReviewEvent } from "./walkthrough-model";
import { cn } from "@baum/viewer/components/classnames";

/** What the person does next about a pull request: read a review they were
 *  asked for, unblock their own, merge it, bring it up to date with its base,
 *  or give baum a clone so a requested review can be written. */
export type UpNextKind = "review" | "fix" | "merge" | "update" | "clone";
/** What an inbox matches a pull request as: its kind in the queue, or
 *  `other` for someone else's that waits on nothing from the person. */
export type QueueKind = UpNextKind | "other";
/** A pull request as the inboxes match and count it. */
export type QueueItem = { key: string; kind: QueueKind; entry: Entry };

export type UpNextItem = {
  /** The pull request across clones: repository and number. */
  key: string;
  kind: UpNextKind;
  entry: Entry;
  /** Why it is the person's to do now, in a sentence built from its state;
   *  empty when its group says it all. */
  why: string;
  /** The reading estimate of a review; null when there is none. */
  minutes: number | null;
  /** The agent sessions working on its branch now. */
  working: LiveSession[];
  /** The latest fix of a pull request in the queue as a fix, when it failed. */
  failedFix?: Job;
  /** The latest fix, when it ended without changing anything: not a failure,
   *  so it is shown with the agent's reason instead. */
  unchangedFix?: Job;
};

// The order of the queue, which is the order of its groups: what someone
// waits on the person to read, then what finishes a pull request, then what
// an agent can take on.
const KINDS: Record<UpNextKind, { label: string; done: string; order: number }> = {
  review: { label: "Review", done: "Reviewed", order: 0 },
  // It leaves the queue only when the review request goes, whoever answered it.
  clone: { label: "Clone", done: "Handled", order: 1 },
  merge: { label: "Merge", done: "Merged", order: 2 },
  fix: { label: "Fix", done: "Fixed", order: 3 },
  update: { label: "Update", done: "Updated", order: 4 },
};

/** The list's groups, by what the person does about their pull requests. */
export const GROUPS: { id: "eyes" | "merge" | "agent"; title: string; kinds: UpNextKind[] }[] = [
  { id: "eyes", title: "Needs your eyes", kinds: ["review", "clone"] },
  { id: "merge", title: "Ready to merge", kinds: ["merge"] },
  { id: "agent", title: "Hand to an agent", kinds: ["fix", "update"] },
];
/** Each group's dot, as Pull requests colours the same states. */
const GROUP_TONES: Record<(typeof GROUPS)[number]["id"] | "waiting" | "others", ListTone> = {
  eyes: "caution",
  merge: "positive",
  agent: "critical",
  waiting: "info",
  others: "quiet",
};
/** The section of other people's pull requests that wait on nothing from
 *  the person, after the person's own that wait. */
export const OTHERS_TITLE = "Not waiting on you";

export function upNextKey(pr: Pick<Entry["pull_request"], "repository" | "number">) {
  return `${pr.repository.toLowerCase()}#${pr.number}`;
}

const isOpen = (entry: Entry) =>
  !entry.historical &&
  (!entry.pull_request.state || entry.pull_request.state.toUpperCase() === "OPEN");

/** Needs the author: changes requested, a conflict, failing checks or review
 *  threads left open. */
function blocked(entry: Entry) {
  return (
    entry.review === "changes_requested" ||
    entry.merge === "conflict" ||
    entry.checks === "failing" ||
    (entry.unresolved ?? 0) > 0
  );
}

function kindOf(entry: Entry, jobs: Job[]): UpNextKind | null {
  const pr = entry.pull_request;
  if (!isOpen(entry)) return null;
  // Assigned someone else's pull request, the person is asked to look at it.
  if (pr.review_requested_from_me || (pr.assigned_to_me && !pr.authored_by_me))
    return entry.needs_clone ? "clone" : "review";
  if (!pr.authored_by_me) return null;
  // An agent already fixing it leaves nothing for the person until it ends.
  if (isActiveJob(fixJob(entry, jobs))) return null;
  if (blocked(entry)) return "fix";
  // GitHub can call a head just pushed clean before its checks are queued;
  // checks running say the merge is not ready yet, whatever the merge state.
  if (canMerge(entry) && entry.checks !== "running") return "merge";
  if (canUpdateBranch(entry)) return "update";
  return null;
}

const plural = (n: number, one: string, many = `${one}s`) =>
  `${formatCount(n)} ${n === 1 ? one : many}`;

function riskText(entry: Entry) {
  const score = entry.risk_score?.score;
  if (score == null) return "";
  return `${entry.risk_score?.status === "lower_bound" ? "at least " : ""}${score} of 5`;
}

const CHECK_WORDS: Record<string, string> = {
  passing: "Checks passing",
  failing: "Checks failing",
  running: "Checks running",
  none: "No checks",
};

/** What a group's member waits on, in a few words: what needs fixing
 *  ("Checks failing · Conflicts"), "Ready to merge", or else its kind. */
function memberLabel(kind: UpNextKind, entry: Entry): string {
  if (kind === "merge") return "Ready to merge";
  if (kind !== "fix") return KINDS[kind].label;
  const unresolved = entry.unresolved ?? 0;
  const problems = [
    entry.checks === "failing" ? "Checks failing" : "",
    entry.merge === "conflict" ? "Conflicts" : "",
    unresolved > 0
      ? `${plural(unresolved, "unresolved thread")}${entry.unresolved_capped ? " or more" : ""}`
      : "",
    entry.review === "changes_requested" ? "Changes requested" : "",
  ].filter(Boolean);
  return problems.length ? problems.join(" · ") : KINDS.fix.label;
}

function why(kind: UpNextKind, entry: Entry, working: number): string {
  const pr = entry.pull_request;
  switch (kind) {
    case "review": {
      const review =
        entry.status === "generating"
          ? "baum is writing its review now."
          : entry.status === "failed"
            ? "baum's review failed."
            : entry.version_count === 0
              ? "There is no baum review yet."
              : entry.status === "outdated" || entry.stale
                ? "baum's review covers an earlier commit."
                : `baum's review is ready${riskText(entry) ? `, risk ${riskText(entry)}` : ""}.`;
      const agents =
        working > 0
          ? ` ${plural(working, "agent session")} ${working === 1 ? "is" : "are"} still working on its branch, so reviewing now catches problems before more commits pile up.`
          : "";
      return `${review}${agents}`;
    }
    case "fix":
      // What needs fixing lists it, each problem opening to its detail.
      return "";
    case "merge":
      // Its group already says it is ready to merge.
      return "";
    case "update":
      // Only an approved branch with passing checks is held back by being
      // behind alone; otherwise updating is one step of several.
      return entry.review === "approved" && entry.checks === "passing"
        ? "Approved, but behind its base. Update the branch and it can merge."
        : "Behind its base. Update the branch to bring it up to date.";
    case "clone":
      return `baum has no local clone of ${pr.repository}, so there is no review to read yet. Once it has one, the review generates on its own.`;
  }
}

/** The open pull requests of stacks stacked on another open one of their
 *  stack, by key, each with the lowest open one under it: the one that must
 *  merge first. A member is stacked on the one whose head branch is its base. */
export function stackBases(entries: Entry[], groups: ReviewGroup[]): Map<string, number> {
  const bases = new Map<string, number>();
  for (const group of groups) {
    if (!group.stack) continue;
    const members = entries.filter(
      (entry) =>
        isOpen(entry) &&
        sameRepository(entry.pull_request.repository, group.repository) &&
        group.members.includes(entry.pull_request.number),
    );
    const byHead = new Map<string, Entry>();
    for (const entry of members)
      if (entry.pull_request.head_ref) byHead.set(entry.pull_request.head_ref, entry);
    // The open member whose head branch is this one's base, if any.
    const under = (entry: Entry) => byHead.get(entry.pull_request.base_ref ?? "");
    for (const entry of members) {
      let base = under(entry);
      if (!base || base.pull_request.number === entry.pull_request.number) continue;
      // Down the stack to its lowest open member; a cycle stops the walk.
      const seen = new Set([entry.pull_request.number, base.pull_request.number]);
      for (
        let next = under(base);
        next && !seen.has(next.pull_request.number);
        next = under(base)
      ) {
        seen.add(next.pull_request.number);
        base = next;
      }
      bases.set(upNextKey(entry.pull_request), base.pull_request.number);
    }
  }
  return bases;
}

/** The pull requests waiting on the person, in the order to take them, their
 *  own that wait on someone or something else, and everyone else's open ones
 *  (`others`), oldest first. Each pull request is listed once, whichever
 *  clones hold it. A stacked pull request GitHub would merge is not ready
 *  while an open one of its stack lies under it: it waits on that one
 *  (`stackedOn`), which must merge first. */
export function upNext(
  entries: Entry[],
  live: LiveSession[] = [],
  jobs: Job[] = [],
  groups: ReviewGroup[] = [],
): {
  items: UpNextItem[];
  waiting: Entry[];
  others: Entry[];
  fixing: { entry: Entry; job: Job }[];
  stackedOn: Map<string, number>;
} {
  // Of the clones that hold a pull request, the one with a readable saved
  // review and a clone speaks for it.
  const score = (entry: Entry) =>
    (entry.version_count > 0 ? 4 : 0) + (entry.store_error ? 0 : 2) + (entry.needs_clone ? 0 : 1);
  const best = new Map<string, Entry>();
  for (const entry of entries) {
    const key = upNextKey(entry.pull_request);
    const current = best.get(key);
    if (!current || score(entry) > score(current)) best.set(key, entry);
  }
  const items: UpNextItem[] = [];
  const waiting: Entry[] = [];
  const others: Entry[] = [];
  const fixing: { entry: Entry; job: Job }[] = [];
  const stackedOn = stackBases([...best.values()], groups);
  for (const [key, entry] of best) {
    const own = kindOf(entry, jobs);
    const kind = own === "merge" && stackedOn.has(key) ? null : own;
    if (!kind) {
      if (!isOpen(entry)) continue;
      if (entry.pull_request.authored_by_me) {
        waiting.push(entry);
        const job = fixJob(entry, jobs);
        if (job && isActiveJob(job)) fixing.push({ entry, job });
      } else others.push(entry);
      continue;
    }
    const last = kind === "fix" ? fixJob(entry, jobs) : undefined;
    const working = liveFor(live, entry.pull_request).filter((s) => s.state === "running");
    items.push({
      key,
      kind,
      entry,
      why: why(kind, entry, working.length),
      minutes: kind === "review" ? (entry.review_minutes ?? null) : null,
      working,
      ...(last && changedNothing(last)
        ? { unchangedFix: last }
        : last?.state === "failed"
          ? { failedFix: last }
          : {}),
    });
  }
  // A review ready to read comes before one still being written; then the
  // riskier first. Otherwise the oldest pull request first.
  const readable = (item: UpNextItem) =>
    item.entry.version_count > 0 && item.entry.status !== "generating" ? 0 : 1;
  const risk = (item: UpNextItem) => item.entry.risk_score?.score ?? 0;
  items.sort(
    (a, b) =>
      KINDS[a.kind].order - KINDS[b.kind].order ||
      (a.kind === "review" ? readable(a) - readable(b) || risk(b) - risk(a) : 0) ||
      a.entry.pull_request.number - b.entry.pull_request.number,
  );
  others.sort((a, b) => a.pull_request.number - b.pull_request.number);
  return { items, waiting, others, fixing, stackedOn };
}

type Seen = {
  kind: UpNextKind;
  title: string;
  repository: string;
  number: number;
  /** Its GitHub page; absent in records kept before it was. */
  url?: string;
  /** Its reading estimate, summed into the work done once it is cleared. */
  minutes?: number | null;
  /** When it left the queue; absent while it is in it. */
  cleared?: number;
  /** Its row as last in the queue, which filters test once it is done;
   *  absent in records kept before it was. */
  last?: Snapshot;
};
/** What of a row the filters read (`pr-filter.ts`), kept small so the
 *  inbox's record stays within what the daemon stores. */
type Snapshot = Pick<Entry, "review" | "checks" | "merge"> & {
  risk_score: { score: number | null } | null;
  pull_request: Pick<
    Entry["pull_request"],
    | "repository"
    | "number"
    | "title"
    | "author"
    | "author_bot"
    | "draft"
    | "authored_by_me"
    | "review_requested_from_me"
    | "assigned_to_me"
    | "base_ref"
    | "behind"
  >;
};
function snapshot(entry: Entry): Snapshot {
  const pr = entry.pull_request;
  return {
    review: entry.review,
    checks: entry.checks,
    merge: entry.merge,
    risk_score: entry.risk_score ? { score: entry.risk_score.score } : null,
    pull_request: {
      repository: pr.repository,
      number: pr.number,
      title: pr.title,
      author: pr.author,
      author_bot: pr.author_bot,
      draft: pr.draft,
      authored_by_me: pr.authored_by_me,
      review_requested_from_me: pr.review_requested_from_me,
      assigned_to_me: pr.assigned_to_me,
      base_ref: pr.base_ref,
      behind: pr.behind,
    },
  };
}
/** What the inbox remembers on this computer: today's pull requests and
 *  which it cleared, and the person's snoozes. */
type Stored = {
  day: string;
  seen: Record<string, Seen>;
  /** Snoozes until a time. */
  snoozed: Record<string, number>;
  /** Snoozes until the pull request changes: the head it had then. */
  held: Record<string, string>;
};

/** A snooze's end: a time, or the pull request's next push. */
export type SnoozeEnd = number | "change";

function dayOf(time: number) {
  const date = new Date(time);
  return `${date.getFullYear()}-${date.getMonth() + 1}-${date.getDate()}`;
}

/** A new day starts empty, keeping only the snoozes that still run. */
function fresh(
  day: string,
  snoozed: Record<string, number> = {},
  held: Record<string, string> = {},
): Stored {
  return { day, seen: {}, snoozed, held };
}

function stillSnoozed(snoozed: Record<string, number>, now: number) {
  return Object.fromEntries(Object.entries(snoozed).filter(([, until]) => until > now));
}

/** The snoozes offered at `now`: an hour, this afternoon while 3 PM is more
 *  than an hour away, tomorrow morning, and until the next push. */
export function snoozeOptions(now: number): { label: string; until: SnoozeEnd }[] {
  const afternoon = new Date(now);
  afternoon.setHours(15, 0, 0, 0);
  const morning = new Date(now);
  morning.setDate(morning.getDate() + 1);
  morning.setHours(9, 0, 0, 0);
  return [
    { label: "1 hour", until: now + 3_600_000 },
    ...(afternoon.getTime() - now > 3_600_000
      ? [{ label: "This afternoon", until: afternoon.getTime() }]
      : []),
    { label: "Tomorrow morning", until: morning.getTime() },
    { label: "Until it changes", until: "change" as const },
  ];
}

export function snoozeText(until: SnoozeEnd, now: number) {
  if (until === "change") return "the next push";
  const date = new Date(until);
  const time = date.toLocaleTimeString([], {
    hour: "numeric",
    ...(date.getMinutes() ? { minute: "2-digit" } : {}),
  });
  return dayOf(until) === dayOf(now) ? time : `tomorrow ${time}`;
}

export type UpNextState = ReturnType<typeof useUpNext>;

/** The inbox's queue with the person's snoozes applied, and what they cleared
 *  today. `ready` holds the bookkeeping back until a complete read, so an
 *  empty first read or a failed one clears nothing.
 *
 *  A pull request that left the queue counts as cleared only when that says
 *  the person did their part: it closed or merged, the review request went,
 *  or their fix or branch update left it waiting on checks or reviewers. One
 *  of theirs that merely stopped being mergeable (a push, checks running)
 *  waits uncleared, and nothing of a repository missing from the read is
 *  cleared, since its pull requests may only be absent for now. */
export function useUpNext(
  entries: Entry[],
  live: LiveSession[],
  jobs: Job[],
  ready: boolean,
  groups: ReviewGroup[] = [],
) {
  const now = useNow(30_000);
  const persistence = useUIState(
    uiState<Stored>("api/ui-state/inbox", () => fresh(dayOf(Date.now()))),
  );
  const stored = persistence.value;
  const day = dayOf(now);
  const state =
    stored.day === day ? stored : fresh(day, stillSnoozed(stored.snoozed, now), stored.held);
  const { items, waiting, others, fixing, stackedOn } = upNext(entries, live, jobs, groups);
  const snoozedNow = (entry: Entry) => {
    const key = upNextKey(entry.pull_request);
    return (state.snoozed[key] ?? 0) > now || state.held[key] === entry.pull_request.head_sha;
  };
  const snoozed: { key: string; entry: Entry; kind?: QueueKind }[] = [
    ...items.filter((item) => snoozedNow(item.entry)),
    ...waiting.filter(snoozedNow).map((entry) => ({
      key: upNextKey(entry.pull_request),
      entry,
    })),
    ...others.filter(snoozedNow).map((entry) => ({
      key: upNextKey(entry.pull_request),
      entry,
      kind: "other" as const,
    })),
  ];
  const queue = items.filter((item) => !snoozedNow(item.entry));
  const update = (change: (state: Stored) => Stored) =>
    persistence.update((previous) => {
      const time = Date.now();
      const base =
        previous.day === dayOf(time)
          ? previous
          : fresh(dayOf(time), stillSnoozed(previous.snoozed, time), previous.held);
      const next = change(base);
      return next;
    });
  // Record what is in the queue and when something leaves it. A pull request
  // that comes back is no longer cleared.
  const waitingKeys = new Set(waiting.map((entry) => upNextKey(entry.pull_request)));
  const listed = new Set(
    entries.filter(isOpen).map((entry) => entry.pull_request.repository.toLowerCase()),
  );
  const signature = [
    ...items.map(
      (item) => `${item.key}:${item.kind}:${item.minutes}:${JSON.stringify(item.entry)}`,
    ),
    ...entries
      .filter((entry) => waitingKeys.has(upNextKey(entry.pull_request)))
      .map((entry) => {
        const job = fixJob(entry, jobs);
        return `waiting:${upNextKey(entry.pull_request)}:${entry.pull_request.head_sha}:${job?.id ?? ""}:${job?.state ?? ""}`;
      }),
    ...[...listed].map((repository) => `listed:${repository}`),
    // A snooze until the next push of someone else's pull request ends on it.
    ...others.map(
      (entry) => `other:${upNextKey(entry.pull_request)}:${entry.pull_request.head_sha}`,
    ),
  ].join("\n");
  useEffect(() => {
    if (!ready || !persistence.ready) return;
    const keys = new Set(items.map((item) => item.key));
    const heads = new Map(
      [...items.map((item) => item.entry), ...waiting, ...others].map((entry) => [
        upNextKey(entry.pull_request),
        entry.pull_request.head_sha,
      ]),
    );
    // A fix counts once it pushed: not while it runs, nor after it failed.
    const fixed = (key: string) => {
      const entry = entries.find((e) => upNextKey(e.pull_request) === key);
      const job = entry && fixJob(entry, jobs);
      return !job || (job.state === "completed" && !!job.commit);
    };
    const clears = (key: string, record: Seen) =>
      !keys.has(key) &&
      record.cleared == null &&
      listed.has(record.repository.toLowerCase()) &&
      (!waitingKeys.has(key) || (record.kind === "fix" && fixed(key)) || record.kind === "update");
    // A snooze until the next push ends once the pull request is in the
    // inbox on another head, or gone from it.
    const pushed = (key: string, head: string) => heads.get(key) !== head;
    const changed =
      stored.day !== day ||
      items.some((item) => {
        const seen = stored.seen[item.key];
        return (
          !seen ||
          seen.cleared != null ||
          seen.kind !== item.kind ||
          seen.title !== item.entry.pull_request.title ||
          seen.minutes !== item.minutes ||
          JSON.stringify(seen.last) !== JSON.stringify(snapshot(item.entry))
        );
      }) ||
      Object.entries(stored.seen).some(([key, record]) => clears(key, record)) ||
      Object.entries(stored.held).some(([key, head]) => pushed(key, head));
    if (!changed) return;
    update((previous) => {
      const seen: Record<string, Seen> = {};
      for (const [key, record] of Object.entries(previous.seen))
        seen[key] = clears(key, record) ? { ...record, cleared: Date.now() } : record;
      for (const item of items) {
        const pr = item.entry.pull_request;
        seen[item.key] = {
          kind: item.kind,
          title: pr.title,
          repository: pr.repository,
          number: pr.number,
          url: pr.url,
          minutes: item.minutes,
          last: snapshot(item.entry),
        };
      }
      const held = Object.fromEntries(
        Object.entries(previous.held).filter(([key, head]) => !pushed(key, head)),
      );
      return { ...previous, seen, held };
    });
    // `signature` stands for `items`.
  }, [ready, signature, day, persistence.ready]);
  const snoozedKeys = new Set(snoozed.map((item) => item.key));
  const cleared = Object.entries(state.seen)
    .filter(([key, seen]) => seen.cleared != null && !snoozedKeys.has(key))
    .sort(([, a], [, b]) => (b.cleared ?? 0) - (a.cleared ?? 0))
    // Its row in the latest read, which a merged pull request keeps while a
    // saved review holds it, so its page can open.
    .map(([key, seen]) => ({
      key,
      ...seen,
      entry: entries.find((entry) => upNextKey(entry.pull_request) === key),
    }));
  return {
    persistence,
    now,
    queue,
    snoozed,
    waiting: waiting.filter((entry) => !snoozedNow(entry)),
    /** Everyone else's open pull requests that wait on nothing from the
     *  person, unsnoozed. */
    others: others.filter((entry) => !snoozedNow(entry)),
    fixing: fixing.filter(({ entry }) => !snoozedNow(entry)),
    cleared,
    /** The open pull request each stacked one waits on to merge first. */
    stackedOn,
    /** The fix whose transcript `t` opens for a pull request. */
    watchable: (entry: Entry) => watchableFix(entry, jobs),
    snoozedUntil: (key: string): SnoozeEnd =>
      key in state.held ? "change" : (state.snoozed[key] ?? 0),
    snooze: (item: Pick<UpNextItem, "key" | "entry">, until: SnoozeEnd) =>
      update((s) =>
        until === "change"
          ? { ...s, held: { ...s.held, [item.key]: item.entry.pull_request.head_sha } }
          : { ...s, snoozed: { ...s.snoozed, [item.key]: until } },
      ),
    wake: (key: string) =>
      update((s) => {
        const { [key]: _until, ...snoozed } = s.snoozed;
        const { [key]: _head, ...held } = s.held;
        return { ...s, snoozed, held };
      }),
  };
}

/** What one of the person's own pull requests waits on, in a few words; a
 *  stacked one waits on the open pull request under it (`base`). One GitHub
 *  blocks with no check reported and no approval required waits on checks
 *  that never started, such as a CI run that failed to start: "No checks
 *  reported", not a review. */
export function waitingOn(entry: Entry, base?: number) {
  if (entry.pull_request.draft || entry.merge === "draft") return "Draft";
  if (base != null) return `Waiting on #${base}`;
  if (entry.checks === "running") return "Checks running";
  if (entry.review === "changes_requested") return "Changes requested";
  if (entry.pull_request.review_required) return "Waiting on review";
  const unchecked = entry.merge === "blocked" && entry.checks === "none";
  if (unchecked && entry.pull_request.review_required === false) return "No checks reported";
  if (entry.merge === "blocked") return "Blocked on GitHub";
  return "Waiting on GitHub";
}

/** What a waiting row waits on: the person's own, or where someone else's
 *  stands. */
function rowWaiting(row: { entry: Entry; base?: number; other?: boolean }) {
  return row.other ? othersState(row.entry) : waitingOn(row.entry, row.base);
}

/** Where someone else's pull request that waits on nothing from the person
 *  stands, in a few words: no one assigned or asked to review it is
 *  "Unclaimed". */
export function othersState(entry: Entry) {
  const pr = entry.pull_request;
  if (pr.draft || entry.merge === "draft") return "Draft";
  if (!pr.assigned && !pr.review_requested) return "Unclaimed";
  if (entry.review === "approved") return "Approved";
  if (entry.review === "changes_requested") return "Changes requested";
  return pr.review_requested ? "Review requested" : "Assigned";
}

/** The pull requests the inboxes match and count: the queue's items and
 *  everyone else's open ones (`other`). */
export function queueItems(inbox: { queue: UpNextItem[]; others: Entry[] }): QueueItem[] {
  return [
    ...inbox.queue,
    ...inbox.others.map((entry) => ({
      key: upNextKey(entry.pull_request),
      kind: "other" as const,
      entry,
    })),
  ];
}

/** A pull request's page, carrying the inbox it was opened from so back
 *  returns there. */
function prHref(entry: Entry, cloneFor: (entry: Entry) => string, inbox?: Inbox) {
  const pr = entry.pull_request;
  const clone = cloneFor(entry);
  return appURL(
    `?${inbox ? `inbox=${encodeURIComponent(inbox.id)}&` : ""}${clone ? `clone=${encodeURIComponent(clone)}` : `repository=${encodeURIComponent(pr.repository)}`}&pr=${pr.number}`,
  );
}

function minutesText(minutes: number) {
  return minutes < 1 ? "<1 min" : `~${formatCount(Math.round(minutes))} min`;
}

const sumMinutes = (items: { minutes?: number | null }[]) =>
  items.reduce((sum, item) => sum + (typeof item.minutes === "number" ? item.minutes : 0), 0);

/** A row of the list: a queued item, a pull request an agent is fixing, one
 *  of the person's own that waits on checks or reviewers, or someone else's
 *  that waits on nothing from the person (`other`, a waiting row too). */
type Row = {
  key: string;
  entry: Entry;
  item?: UpNextItem;
  job?: Job;
  waiting?: boolean;
  other?: boolean;
  /** The open pull request of its stack a waiting row must merge after. */
  base?: number;
};
/** A review group or stack listed as one row, with those of its members the
 *  list shows, in the group's order. */
type GroupRow = { key: string; group: ReviewGroup; members: Row[] };
type Line = Row | GroupRow;
const isGroup = (line: Line | undefined): line is GroupRow => !!line && "group" in line;

export const groupKey = (group: ReviewGroup) => `group:${group.id}`;

/** A stack follows its first shown open member in base-to-tip order. Other
 *  groups follow their first member in the action list. Each appears once,
 *  with its members nested under it and retaining their own states. */
export function withGroups<S extends { rows: Row[] }>(
  sections: S[],
  groups: ReviewGroup[],
): (Omit<S, "rows"> & { lines: Line[] })[] {
  const owner = new Map<Row, GroupRow>();
  for (const group of groups) {
    const members = sections
      .flatMap((section) => section.rows)
      .filter(
        (row) =>
          !owner.has(row) &&
          sameRepository(row.entry.pull_request.repository, group.repository) &&
          group.members.includes(row.entry.pull_request.number),
      )
      .sort(
        (a, b) =>
          group.members.indexOf(a.entry.pull_request.number) -
          group.members.indexOf(b.entry.pull_request.number),
      );
    if (members.length === 0) continue;
    const line: GroupRow = { key: groupKey(group), group, members };
    for (const member of members) owner.set(member, line);
  }
  const placed = new Set<GroupRow>();
  return sections.map(({ rows, ...section }) => {
    const lines: Line[] = [];
    for (const row of rows) {
      const group = owner.get(row);
      if (!group) lines.push(row);
      else if (!placed.has(group) && (!group.group.stack || group.members[0] === row)) {
        placed.add(group);
        lines.push(group);
      }
    }
    return { ...section, lines };
  });
}

/** The pull requests of the list's lines, a group's members in its place. */
const rowsOf = (lines: Line[]) => lines.flatMap((line) => (isGroup(line) ? line.members : [line]));

/** Saved reviews read so far, by clone, number and version. */
type SavedReviewDocs = Record<string, ReviewDocument | "failed">;
export type SavedReviews = [SavedReviewDocs, Dispatch<SetStateAction<SavedReviewDocs>>];

/** The saved review of a pull request, once read, or "failed"; read again
 *  only when a new version is saved. Nothing while it is read, or for a pull
 *  request without a saved review. `shared` keeps what was read for a parent
 *  that outlives the caller, as the Pull requests list does for its peeks. */
export function useSavedReview(entry: Entry | undefined, clone: string, shared?: SavedReviews) {
  const own = useState<SavedReviewDocs>({});
  const [docs, setDocs] = shared ?? own;
  const id =
    entry && clone && entry.version_count > 0
      ? `${clone}#${entry.pull_request.number}@${entry.latest?.version ?? 0}`
      : "";
  useEffect(() => {
    if (!id || docs[id] || !entry) return;
    const abort = new AbortController();
    documentRequest(clone, entry.pull_request.number, undefined, abort.signal).then(
      (doc) => setDocs((all) => ({ ...all, [id]: doc })),
      () => {
        if (!abort.signal.aborted) setDocs((all) => ({ ...all, [id]: "failed" }));
      },
    );
    return () => abort.abort();
    // `id` stands for the entry and clone.
  }, [id]);
  return id ? docs[id] : undefined;
}

/** Prefer a saved review; while the first is being written, show its draft. */
function useReviewDocument(entry: Entry | undefined, clone: string, job: Job | undefined) {
  const doc = useSavedReview(entry, clone);
  const [draft, setDraft] = useState<{ job: string; doc: ReviewDocument } | null>(null);
  const id =
    entry?.version_count === 0 && isActiveJob(job) && job?.draft ? `${job.id}@${job.draft}` : "";
  useEffect(() => {
    if (!id || !job) return;
    const abort = new AbortController();
    request<{ document?: ReviewDocument }>(
      `api/jobs/${encodeURIComponent(job.id)}/draft`,
      undefined,
      "GET",
      abort.signal,
    )
      .then(({ document }) => {
        if (!abort.signal.aborted && document && Array.isArray(document.sections))
          setDraft({ job: job.id, doc: document });
      })
      .catch(() => {});
    return () => abort.abort();
  }, [id]);
  return doc && doc !== "failed" ? doc : id && draft && draft.job === job?.id ? draft.doc : null;
}

/** The row's inboxes, in the sidebar's order. A pull request an agent is
 *  fixing is in those that take a fix; one that waits on others is in those
 *  that ask for no kind of work. */
function rowInboxes(row: Row, inboxes: Inbox[], held: Held | null) {
  return inboxesOf(
    row.item ?? (row.job ? { kind: "fix" as const, entry: row.entry } : { entry: row.entry }),
    inboxes,
    held,
  );
}

export function InboxTag({ inbox }: { inbox: Inbox }) {
  return (
    <span className={`upnext-tag inbox-${inbox.color}`}>
      <span aria-hidden="true" className="inbox-dot" />
      <span className="upnext-tag-name">{inbox.name}</span>
    </span>
  );
}

/** The six inbox colours as a radio group; `pick` hears each click. */
export function ColourSwatches({
  color: current,
  pick,
  className,
}: {
  color: InboxColor;
  pick: (color: InboxColor) => void;
  className?: string;
}) {
  return (
    <div
      role="radiogroup"
      aria-label="Colour"
      className={className ? `inbox-swatches ${className}` : "inbox-swatches"}
    >
      {INBOX_COLORS.map((color) => (
        <button
          key={color}
          type="button"
          role="radio"
          aria-checked={current === color}
          aria-label={COLOR_NAMES[color]}
          title={COLOR_NAMES[color]}
          className={`inbox-swatch inbox-${color}`}
          onClick={() => pick(color)}
        />
      ))}
    </div>
  );
}

/** An inbox's colour and name at the head of its queue, each changed in
 *  place: the dot opens the six colours, and the name becomes a field (Enter
 *  or leaving it saves, Esc keeps the old name). */
function InboxHeadEdit({
  inbox,
  inboxes,
  update,
}: {
  inbox: Inbox;
  inboxes: Inbox[];
  update: (patch: Partial<Inbox>) => void;
}) {
  const colours = useDisclosure();
  const [draft, setDraft] = useState<string | null>(null);
  const [problem, setProblem] = useState("");
  const commit = () => {
    if (draft === null) return;
    const name = draft.trim();
    const why = name === inbox.name ? "" : nameProblem(name, inboxes, inbox.id);
    if (why) {
      setProblem(why);
      return;
    }
    setDraft(null);
    setProblem("");
    if (name !== inbox.name) update({ name });
  };
  return (
    <>
      <details {...colours.detailsProps} className="inbox-head-colour">
        <summary
          aria-label={`Colour: ${COLOR_NAMES[inbox.color]}`}
          title="Change the colour"
          className="inbox-head-dot"
        >
          <span aria-hidden="true" className="inbox-dot inbox-dot-large" />
        </summary>
        {colours.open ? (
          <ColourSwatches
            color={inbox.color}
            className="inbox-head-swatches"
            pick={(color) => {
              colours.close();
              if (color !== inbox.color) update({ color });
            }}
          />
        ) : null}
      </details>
      {draft === null ? (
        <h1>
          <button
            type="button"
            className="inbox-head-name"
            title="Rename"
            onClick={() => setDraft(inbox.name)}
          >
            {inbox.name}
          </button>
        </h1>
      ) : (
        <span className="inbox-head-rename">
          <input
            aria-label="Inbox name"
            className="local-field"
            // biome-ignore lint/a11y/noAutofocus: the name was clicked to be typed
            autoFocus
            maxLength={40}
            value={draft}
            aria-invalid={problem ? true : undefined}
            onChange={(event) => {
              setDraft(event.target.value);
              setProblem("");
            }}
            onBlur={commit}
            onKeyDown={(event) => {
              if (event.key === "Enter") commit();
              else if (event.key === "Escape") {
                event.preventDefault();
                setDraft(null);
                setProblem("");
              }
            }}
          />
          {problem ? (
            <span role="alert" className="inbox-head-problem">
              {problem}
            </span>
          ) : null}
        </span>
      )}
    </>
  );
}

/** The inbox: what waits on the person, grouped by what they do about it, and
 *  the selected pull request's panel beside it. In one of the person's inboxes
 *  (`selected`) every list, what was done today among them, holds only what
 *  matches that inbox. */
export function UpNextPage({
  inbox: all,
  inboxes = [],
  held = null,
  selected: scope,
  onEdit,
  loading,
  hasRepositories,
  cloneFor,
  fix,
  fixState = () => ({ starting: false, error: "" }),
  generate,
  generateState = () => ({ starting: false, error: "" }),
  changed,
  refresh,
  refreshing = false,
  reviewGroups = [],
  entries = [],
  jobs = [],
  saveAsInbox,
  updateInbox,
  inboxSaves = 0,
  repositories = [],
  Link,
}: {
  inbox: UpNextState;
  /** Every pull request read, open or not, for what a group's panel says of
   *  members the queue does not list, as those merged. */
  entries?: Entry[];
  jobs?: Job[];
  /** All of the person's inboxes, for the tags. */
  inboxes?: Inbox[];
  /** Which inboxes hold each pull request, as the daemon matched them. */
  held?: Held | null;
  /** The review groups and stacks, whose shown members the list nests under one row. */
  reviewGroups?: ReviewGroup[];
  /** The saved inbox whose queue this is. */
  selected: Inbox;
  /** Opens the inbox's edit page, from a right click on its head. */
  onEdit?: () => void;
  loading: boolean;
  hasRepositories: boolean;
  cloneFor: (entry: Entry) => string;
  /** Starts the pull request's fix at once. */
  fix: (entry: Entry, only?: FixOnly) => void;
  /** A pull request's fix that is starting or did not start. */
  fixState?: (entry: Entry) => FixState;
  /** Starts the review of a pull request, as its page's Generate does. */
  generate?: (entry: Entry) => void;
  /** A pull request's review that is starting or did not start. */
  generateState?: (entry: Entry) => FixState;
  /** Reloads the inbox after a write to GitHub. */
  changed: () => Promise<void>;
  /** Reads the pull requests from GitHub again, from the head's refresh. */
  refresh?: (run: RefreshRun) => Promise<unknown>;
  /** A read of every repository is under way, the first or a refresh. */
  refreshing?: boolean;
  /** Makes a new inbox catching what the page shows with its filter: from
   *  the inbox shown (`base`) with its query and the filter, or else anything
   *  waiting that matches the filter. */
  saveAsInbox?: (query: string, base: Inbox) => void;
  /** Changes the inbox shown: its query (the page's filter added), name or
   *  colour. */
  updateInbox?: (patch: Partial<Inbox>) => void;
  /** How many inbox saves the daemon has accepted. */
  inboxSaves?: number;
  /** The person's repositories, offered by the filter with nothing waiting. */
  repositories?: readonly string[];
  Link: ComponentType<ViewerLinkProps>;
}) {
  const [merging, setMerging] = useState<Entry | null>(null);
  const [reviewing, setReviewing] = useState<{ entry: Entry; event: ReviewEvent } | null>(null);
  const [mergingAll, setMergingAll] = useState<Entry[] | null>(null);
  const [updating, setUpdating] = useState<Entry | null>(null);
  // Kept for when the inbox is drawn again, back from a pull request.
  const [chosen, setChosen] = useLastRow("inbox");
  const [menu, setMenu] = useState(false);
  const [snoozedOpen, setSnoozedOpen] = useState(false);
  const [doneOpen, setDoneOpen] = useState(false);
  const [toast, setToast] = useState<{
    id: number;
    text: string;
    undo?: () => void;
  } | null>(null);
  const list = useRef<HTMLDivElement>(null);
  const detail = useRef<HTMLDivElement>(null);
  // The page's filter narrows what it shows, on top of its inbox.
  // The page's filter. In a saved inbox it starts as the inbox's own query,
  // and a change is a draft that narrows the queue until Update inbox saves it
  // (kept per inbox; none kept is the saved query).
  const saved = foldedQuery(scope);
  const quick = useFilterDraft(`inbox:${scope.id}`, saved, inboxSaves);
  const { query, set: setQuery } = quick;
  const filtering = quick.dirty;
  useEffect(() => {
    const create = (event: KeyboardEvent) => {
      if (
        event.key !== "Enter" ||
        !(event.metaKey || event.ctrlKey) ||
        event.altKey ||
        event.shiftKey ||
        event.isComposing ||
        event.defaultPrevented ||
        !saveAsInbox ||
        document.querySelector("dialog[open]")
      )
        return;
      const text =
        event.target instanceof HTMLTextAreaElement && event.target.closest(".filter-menu")
          ? event.target.value
          : query;
      if (queryProblem(text, true)) return;
      event.preventDefault();
      if (event.target instanceof HTMLTextAreaElement) event.target.blur();
      saveAsInbox(text, scope);
    };
    window.addEventListener("keydown", create);
    return () => window.removeEventListener("keydown", create);
  }, [query, saveAsInbox, scope]);
  // The daemon says what the filter holds, asked again as it changes.
  const view = useHeld(held, [query]);
  const inScope = (item: { kind?: QueueKind; entry: Entry }) => holds(view, query, item);
  const queue = all.queue.filter(inScope);
  const snoozed = all.snoozed.filter(inScope);
  const fixing = all.fixing.filter(({ entry }) => inScope({ kind: "fix", entry }));
  // A done pull request matches as it was in the queue.
  const cleared = all.cleared.filter((item) => {
    // The snapshot holds every field the filters read.
    const entry = (item.last as Entry | undefined) ?? item.entry;
    return entry ? matchesQuery(query, entry, item.kind) : !parseQuery(query).expr;
  });
  const waiting = all.waiting.filter((entry) => inScope({ entry }));
  const others = all.others.filter((entry) => inScope({ kind: "other", entry }));
  // What the filter's counts and values come from: the whole queue, each
  // pull request with what it waits for.
  const kinds = new Map<Entry, QueueKind>([
    ...all.queue.map((item): [Entry, QueueKind] => [item.entry, item.kind]),
    ...all.fixing.map(({ entry }): [Entry, QueueKind] => [entry, "fix"]),
    ...all.others.map((entry): [Entry, QueueKind] => [entry, "other"]),
  ]);
  const unfiltered = [...kinds.keys(), ...all.waiting];
  const inbox = { ...all, queue, snoozed, fixing, cleared, waiting, others };
  const { now } = all;
  // Those an agent is fixing are listed under Hand to an agent.
  const fixingKeys = new Set(fixing.map(({ entry }) => upNextKey(entry.pull_request)));
  const ownWaiting = waiting.filter((entry) => !fixingKeys.has(upNextKey(entry.pull_request)));
  const sections = [
    ...GROUPS.map((group) => ({
      id: group.id as keyof typeof GROUP_TONES,
      title: group.title,
      rows: [
        ...queue
          .filter((item) => group.kinds.includes(item.kind))
          .map((item): Row => ({ key: item.key, entry: item.entry, item })),
        ...(group.id === "agent"
          ? fixing.map(
              ({ entry, job }): Row => ({ key: upNextKey(entry.pull_request), entry, job }),
            )
          : []),
      ],
    })),
    {
      id: "waiting" as const,
      title: "Waiting",
      rows: ownWaiting.map(
        (entry): Row => ({
          key: upNextKey(entry.pull_request),
          entry,
          waiting: true,
          base: inbox.stackedOn.get(upNextKey(entry.pull_request)),
        }),
      ),
    },
    {
      id: "others" as const,
      title: OTHERS_TITLE,
      rows: others.map(
        (entry): Row => ({ key: upNextKey(entry.pull_request), entry, waiting: true, other: true }),
      ),
    },
  ];
  const groups = withGroups(sections, reviewGroups).filter((group) => group.lines.length > 0);
  // A group shows its members when opened, or while one of them is chosen.
  const [opened, setOpened] = useState<Set<string>>(new Set());
  const groupOpen = (line: GroupRow) =>
    opened.has(line.key) || line.members.some((member) => member.key === chosen);
  const visible = (lines: Line[]) =>
    lines.flatMap((line): Line[] =>
      isGroup(line) && groupOpen(line) ? [line, ...line.members] : [line],
    );
  // The rows the keys move through, in the order drawn.
  const rows = groups.flatMap((group) => visible(group.lines));
  // A selected pull request that leaves the list hands the selection to the
  // one that takes its place. Waiting rows come last and are shown only when
  // picked, so an emptied queue still reads as inbox zero; an inbox of
  // others' pull requests alone (as Unclaimed) opens on its first.
  const last = useRef(0);
  const found = chosen ? rows.findIndex((row) => row.key === chosen) : -1;
  const actionable = rows.filter(
    (row) => !(isGroup(row) ? row.members.every((member) => member.waiting) : row.waiting),
  ).length;
  const firstOther = rows.findIndex((row) =>
    isGroup(row) ? row.members.some((member) => member.other) : row.other,
  );
  const index =
    found >= 0 ? found : actionable > 0 ? Math.min(last.current, actionable - 1) : firstOther;
  const line = index >= 0 ? rows[index] : undefined;
  const selected = isGroup(line) ? undefined : line;
  const selectedGroup = isGroup(line) ? line : undefined;
  useGitHubKey(selected?.entry.pull_request.url || undefined);
  useEffect(() => {
    if (index >= 0) last.current = index;
  });
  const select = (key: string) => {
    setChosen(key);
    setMenu(false);
    // A group stays open once the selection has been on a member.
    const group = groupOf(key);
    if (group && !opened.has(group.key)) setOpened((all) => new Set(all).add(group.key));
  };
  const toggleGroup = (group: GroupRow, open = !groupOpen(group)) => {
    setOpened((all) => {
      const next = new Set(all);
      if (open) next.add(group.key);
      else next.delete(group.key);
      return next;
    });
    // Closing a group with a chosen member hands the selection to the group.
    if (!open && group.members.some((member) => member.key === chosen)) select(group.key);
  };
  const groupOf = (key: string) =>
    groups
      .flatMap((group) => group.lines)
      .find((line): line is GroupRow => isGroup(line) && line.members.some((m) => m.key === key));
  // The list is one tab stop, on the selected row (or the first, with none
  // selected), and the focus follows the selection while it is in the list,
  // so the ring and the keys always act on the same row.
  const tabbable = line?.key ?? rows[0]?.key;
  useEffect(() => {
    if (!line) return;
    const element = Array.from(
      list.current?.querySelectorAll<HTMLElement>("[data-row]") ?? [],
    ).find((element) => element.dataset.row === line.key);
    element?.scrollIntoView?.({ block: "nearest" });
    if (list.current?.contains(document.activeElement))
      element?.querySelector<HTMLElement>(".upnext-row")?.focus({ preventScroll: true });
  }, [line?.key]);
  useEffect(() => {
    if (!toast) return;
    const timer = setTimeout(() => setToast((t) => (t?.id === toast.id ? null : t)), 5000);
    return () => clearTimeout(timer);
  }, [toast?.id]);
  const doc = useReviewDocument(
    selected?.entry,
    selected ? cloneFor(selected.entry) : "",
    selected ? reviewJob(selected.entry, jobs) : undefined,
  );
  const groupDoc = useGroupDocument(selectedGroup?.group);
  // The panel's fix button, which `f` presses too: a Fix item whose pull
  // request offers a fix and has a clone.
  const selectedFix =
    selected?.item?.kind === "fix" && fixLabel(selected.entry) && cloneFor(selected.entry)
      ? () => fix(selected.entry)
      : undefined;
  const selectedFixState = selected ? fixState(selected.entry) : { starting: false, error: "" };
  // The panel's generate button, shown while there is no review: it needs a
  // clone to write one in.
  const selectedGenerate =
    selected && generate && cloneFor(selected.entry) && !selected.entry.needs_clone
      ? () => generate(selected.entry)
      : undefined;
  const options = snoozeOptions(now);
  const snooze = (until: SnoozeEnd) => {
    const item = selected;
    if (!item) return;
    inbox.snooze(item, until);
    setMenu(false);
    setToast({
      id: Date.now(),
      text: `Snoozed #${item.entry.pull_request.number} until ${snoozeText(until, now)}`,
      undo: () => {
        inbox.wake(item.key);
        setChosen(item.key);
      },
    });
  };
  const undo = () => {
    if (!toast?.undo) return;
    toast.undo();
    setToast(null);
  };
  useShortcuts((event) => {
    if (loading) return;
    const key = event.key;
    if (menu && /^[1-9]$/.test(key)) {
      const option = options[Number(key) - 1];
      if (!option) return;
      event.preventDefault();
      snooze(option.until);
      return;
    }
    if (key === "Escape" && menu) {
      event.preventDefault();
      setMenu(false);
    } else if (key === "j" || key === "ArrowDown" || key === "k" || key === "ArrowUp") {
      if (rows.length === 0) return;
      event.preventDefault();
      const step = key === "j" || key === "ArrowDown" ? 1 : -1;
      select(rows[Math.min(rows.length - 1, Math.max(0, index + step))]!.key);
    } else if (key === " ") {
      // Space on a link or button presses it, except on a row, where it opens
      // or closes the row's group: from a member, back to its group, which
      // closes.
      if (
        !onBody(event) &&
        event.target instanceof HTMLElement &&
        event.target.closest("a, button") &&
        !event.target.closest(".upnext-row")
      )
        return;
      if (selectedGroup) {
        event.preventDefault();
        toggleGroup(selectedGroup);
        return;
      }
      const group = selected ? groupOf(selected.key) : undefined;
      if (!group) return;
      event.preventDefault();
      toggleGroup(group, false);
      select(group.key);
    } else if (key === "Enter") {
      // Enter on a link or button already does what it says, except on a
      // row, where it opens the pull request like anywhere else in the list.
      if (
        !onBody(event) &&
        event.target instanceof HTMLElement &&
        event.target.closest("a, button") &&
        !event.target.closest(".upnext-row")
      )
        return;
      const primary = detail.current?.querySelector<HTMLElement>(".upnext-primary");
      if (!primary) return;
      event.preventDefault();
      primary.click();
    } else if (key === "s") {
      if (!selected) return;
      event.preventDefault();
      setMenu((open) => !open);
    } else if (key === "v") {
      // As a click on the panel's review button: its menu of verdicts.
      const button = detail.current?.querySelector<HTMLElement>("[data-review-button]");
      if (!button) return;
      event.preventDefault();
      setMenu(false);
      button.click();
    } else if (key === "m") {
      if (selected?.item?.kind !== "merge") return;
      event.preventDefault();
      setMenu(false);
      setMerging(selected.entry);
    } else if (key === "f") {
      if (!selectedFix || selectedFixState.starting) return;
      event.preventDefault();
      setMenu(false);
      selectedFix();
    } else if (key === "t") {
      const run = selected && inbox.watchable(selected.entry);
      if (!run) return;
      event.preventDefault();
      navigate(jobHref(run));
    } else if (key === "u") {
      if (!toast?.undo) return;
      event.preventDefault();
      undo();
    }
  });
  const yours = queue.length;
  const minutes = Math.round(sumMinutes(queue));
  const summary = loading
    ? "Loading your inbox…"
    : actionable === 0
      ? cleared.length
        ? `Nothing left. ${formatCount(cleared.length)} handled today.`
        : "Nothing waiting."
      : [
          yours ? `${plural(yours, "thing")} for you` : "",
          minutes ? `about ${plural(minutes, "minute")}` : "",
          fixing.length ? `${formatCount(fixing.length)} with agents` : "",
          cleared.length ? `${formatCount(cleared.length)} done` : "",
        ]
          .filter(Boolean)
          .join(" · ");
  const total = cleared.length + queue.length;
  const refreshButton = refresh ? (
    <RefreshButton refresh={refresh} busy={refreshing} label="Refresh from GitHub" />
  ) : null;
  // A snoozed pull request's actions, behind its ⋯ and a right click on it.
  const actionsMenu = useContextMenu();
  const prName = (pr: { repository: string; number: number }) => `${pr.repository} #${pr.number}`;
  const snoozedName = (item: (typeof snoozed)[number]) => prName(item.entry.pull_request);
  // A queued pull request's actions, behind a right click on its row (or ⌘.
  // on it): what its panel's buttons and its keys do, with the key beside
  // each. Snooze… picks the row and opens the panel's snooze choices.
  const rowActions = (row: Row): MenuItem[] => {
    const { entry, item, job, waiting } = row;
    const pr = entry.pull_request;
    const items: MenuItem[] = [
      { label: "Open", hint: "↵", onSelect: () => navigate(prHref(entry, cloneFor, scope)) },
    ];
    const watchable = inbox.watchable(entry);
    const run = job ?? watchable;
    if (run)
      items.push({
        label: isActiveJob(run) ? "Watch the agent" : "Fix transcript",
        hint: watchable ? "t" : undefined,
        onSelect: () => navigate(jobHref(run)),
      });
    if (!job && !waiting) {
      const label = fixLabel(entry);
      if (item?.kind === "merge")
        items.push({ label: "Merge…", hint: "m", onSelect: () => setMerging(entry) });
      else if (item?.kind === "update")
        items.push({ label: "Update the branch…", onSelect: () => setUpdating(entry) });
      else if (item?.kind === "fix" && label && cloneFor(entry) && !fixState(entry).starting)
        items.push({ label, hint: "f", onSelect: () => fix(entry) });
    }
    if (reviewable(entry))
      for (const event of pr.authored_by_me
        ? (["COMMENT"] as const)
        : (["APPROVE", "COMMENT", "REQUEST_CHANGES"] as const))
        items.push({
          label: `${EVENT_LABELS[event]}…`,
          onSelect: () => setReviewing({ entry, event }),
        });
    items.push({
      label: "Snooze…",
      hint: "s",
      onSelect: () => {
        select(row.key);
        setMenu(true);
      },
    });
    if (pr.url)
      items.push({
        label: "Open on GitHub",
        hint: GITHUB_KEY_LABEL,
        onSelect: () => openExternal(pr.url),
      });
    return items;
  };
  const groupActions = (line: GroupRow): MenuItem[] => [
    {
      label: "Open",
      hint: "↵",
      onSelect: () =>
        navigate(
          appURL(
            `?inbox=${encodeURIComponent(scope.id)}&group=${encodeURIComponent(line.group.id)}`,
          ),
        ),
    },
    {
      label: groupOpen(line) ? "Hide pull requests" : "Show pull requests",
      hint: "Space",
      onSelect: () => toggleGroup(line),
    },
  ];
  // A done one's page while the app still lists it, and its GitHub page.
  const doneActions = ({ entry, url }: (typeof cleared)[number]): MenuItem[] => [
    ...(entry ? [{ label: "Open", onSelect: () => navigate(prHref(entry, cloneFor)) }] : []),
    ...(url ? [{ label: "Open on GitHub", onSelect: () => openExternal(url) }] : []),
  ];
  const snoozedActions = (item: (typeof snoozed)[number]): MenuItem[] => [
    {
      label: "Bring back",
      onSelect: () => {
        inbox.wake(item.key);
        select(item.key);
      },
    },
  ];
  const alsoIn = (row: Row) =>
    rowInboxes(row, inboxes, view).filter((other) => other.id !== scope.id);
  const saveOptions: SaveOption[] =
    !filtering || queryProblem(query)
      ? []
      : [
          ...(updateInbox
            ? [
                {
                  label: "Update inbox",
                  // The draft goes once the inbox holds it, so a failed save keeps it.
                  onSelect: () => updateInbox(withQuery(scope, query)),
                },
              ]
            : []),
          ...(saveAsInbox
            ? [
                {
                  label: "Create inbox",
                  onSelect: () => saveAsInbox(query, scope),
                },
              ]
            : []),
          { label: "Discard changes", onSelect: () => quick.reset() },
        ];
  // What the filter hides: of the inbox's own, in a saved inbox.
  const hidden = unfiltered.filter((entry) =>
    holds(view, saved, { entry, kind: kinds.get(entry) }),
  ).length;
  // The filter offers every repository, organization, author and branch
  // known, not only those of what the inbox holds now.
  const known = { entries, repositories };
  const filterButton = (
    <FilterButton
      query={query}
      onChange={setQuery}
      entries={unfiltered}
      known={known}
      kindOf={(entry) => kinds.get(entry)}
      inbox
      footer={
        <>
          <FilterBar
            // A page's text is its own: moving to another inbox starts afresh.
            key={scope.id}
            query={query}
            onChange={setQuery}
            entries={unfiltered}
            known={known}
            inbox
            clearable={false}
            actions={<SaveButton options={saveOptions} />}
          />
          <FilterSaveError error={quick.error} retry={quick.retry} />
        </>
      }
    />
  );
  return (
    <>
      <div className="upnext-page">
        <div className="upnext-list">
          <header className="upnext-list-head">
            <div
              className={`upnext-list-title inbox-${scope.color}`}
              onContextMenu={(event) =>
                onEdit &&
                actionsMenu.open(event, [{ label: "Edit…", onSelect: onEdit }], scope.name)
              }
            >
              {updateInbox ? (
                <InboxHeadEdit inbox={scope} inboxes={inboxes} update={updateInbox} />
              ) : (
                <>
                  <span aria-hidden="true" className="inbox-dot inbox-dot-large" />
                  <h1>{scope.name}</h1>
                </>
              )}
              {filterButton}
              {refreshButton}
            </div>
            <p className="upnext-list-summary">{summary}</p>
            <div
              className="upnext-bar"
              role="progressbar"
              aria-label="Cleared today"
              aria-valuemin={0}
              aria-valuemax={total}
              aria-valuenow={cleared.length}
            >
              <div style={{ width: `${total ? (100 * cleared.length) / total : 0}%` }} />
            </div>
          </header>
          <div ref={list} className="upnext-list-body">
            {actionsMenu.menu}
            {groups.map((group) => {
              // A stack merges from its base up, one at a time, so its
              // members stay out of Merge all.
              const ready =
                group.id === "merge"
                  ? group.lines
                      .flatMap((line) =>
                        isGroup(line) ? (line.group.stack ? [] : line.members) : [line],
                      )
                      .filter((row) => row.item?.kind === "merge")
                      .map((row) => row.entry)
                  : [];
              const member = (row: Row, group?: GroupRow) => (
                <li
                  key={row.key}
                  data-row={row.key}
                  data-focused={row.key === line?.key || undefined}
                  className={`list-row upnext-item${group ? " upnext-member" : ""}`}
                  onContextMenu={(event) =>
                    actionsMenu.open(event, rowActions(row), prName(row.entry.pull_request))
                  }
                >
                  <ListRow
                    row={row}
                    on={row.key === line?.key}
                    tabbable={row.key === tabbable}
                    pick={select}
                    inGroup={!!group}
                  />
                </li>
              );
              return (
                <ListSection
                  key={group.id}
                  title={group.title}
                  count={rowsOf(group.lines).length}
                  tone={GROUP_TONES[group.id]}
                  action={
                    ready.length > 1 ? (
                      <button
                        type="button"
                        className={cn("upnext-batch", SMALL_PRIMARY)}
                        onClick={() => setMergingAll(ready)}
                      >
                        Merge all {formatCount(ready.length)}
                      </button>
                    ) : undefined
                  }
                >
                  {group.lines.map((entry) =>
                    isGroup(entry) ? (
                      <Fragment key={entry.key}>
                        <li
                          data-row={entry.key}
                          data-focused={entry.key === line?.key || undefined}
                          className="list-row upnext-item"
                          onContextMenu={(event) =>
                            actionsMenu.open(event, groupActions(entry), entry.group.name)
                          }
                        >
                          <GroupListRow
                            line={entry}
                            open={groupOpen(entry)}
                            toggle={() => toggleGroup(entry)}
                            on={entry.key === line?.key}
                            tabbable={entry.key === tabbable}
                            pick={select}
                          />
                        </li>
                        {groupOpen(entry) ? entry.members.map((row) => member(row, entry)) : null}
                      </Fragment>
                    ) : (
                      member(entry)
                    ),
                  )}
                </ListSection>
              );
            })}
            {snoozed.length > 0 ? (
              <ListSection
                title="Snoozed"
                count={snoozed.length}
                collapsed={!snoozedOpen}
                toggle={() => setSnoozedOpen((open) => !open)}
                listClassName="upnext-snoozed"
              >
                {snoozed.map((item) => (
                  <li
                    key={item.key}
                    className="list-row"
                    onContextMenu={(event) =>
                      actionsMenu.open(event, snoozedActions(item), snoozedName(item))
                    }
                  >
                    <span className="upnext-snoozed-title">
                      #{item.entry.pull_request.number} {item.entry.pull_request.title}
                    </span>
                    <span className="upnext-snoozed-until">
                      {snoozeText(inbox.snoozedUntil(item.key), now)}
                    </span>
                    <MenuButton
                      label={snoozedName(item)}
                      items={() => snoozedActions(item)}
                      menu={actionsMenu}
                    />
                  </li>
                ))}
              </ListSection>
            ) : null}
            {cleared.length > 0 ? (
              <ListSection
                title="Done today"
                count={cleared.length}
                tone="positive"
                collapsed={!doneOpen}
                toggle={() => setDoneOpen((open) => !open)}
                listClassName="upnext-done"
              >
                {cleared.map((item) => (
                  <li
                    key={item.key}
                    title={`${item.repository} #${item.number}`}
                    className="list-row"
                    onContextMenu={(event) =>
                      actionsMenu.open(event, doneActions(item), prName(item))
                    }
                  >
                    <span aria-hidden="true" className="upnext-done-check">
                      ✓
                    </span>
                    {/* Its page while the app still lists it, otherwise GitHub. */}
                    {item.entry ? (
                      <Link href={prHref(item.entry, cloneFor, scope)} className="upnext-done-text">
                        {KINDS[item.kind].done} #{item.number} {item.title}
                      </Link>
                    ) : item.url ? (
                      <a
                        href={item.url}
                        target="_blank"
                        rel="noreferrer"
                        className="upnext-done-text"
                      >
                        {KINDS[item.kind].done} #{item.number} {item.title}
                      </a>
                    ) : (
                      <span className="upnext-done-text">
                        {KINDS[item.kind].done} #{item.number} {item.title}
                      </span>
                    )}
                  </li>
                ))}
              </ListSection>
            ) : null}
          </div>
          <KeyBar place="inbox" className="upnext-keys" />
        </div>
        <div ref={detail} className="upnext-detail">
          {loading ? (
            <div className="upnext-panel">
              <LoadingStatus block>Loading your inbox…</LoadingStatus>
            </div>
          ) : selected ? (
            <Panel
              key={selected.key}
              row={selected}
              doc={doc}
              tags={alsoIn(selected)}
              href={prHref(selected.entry, cloneFor, scope)}
              Link={Link}
              fix={selectedFix}
              fixState={selectedFixState}
              generate={selectedGenerate}
              generateState={generateState(selected.entry)}
              merge={() => setMerging(selected.entry)}
              review={(event) => setReviewing({ entry: selected.entry, event })}
              update={() => setUpdating(selected.entry)}
              menu={menu}
              setMenu={setMenu}
              options={options}
              snooze={snooze}
              now={now}
            />
          ) : selectedGroup ? (
            <GroupPanel
              key={selectedGroup.key}
              line={selectedGroup}
              doc={groupDoc}
              entries={entries}
              href={appURL(
                `?inbox=${encodeURIComponent(scope.id)}&group=${encodeURIComponent(selectedGroup.group.id)}`,
              )}
              pick={(key) => {
                toggleGroup(selectedGroup, true);
                select(key);
              }}
              Link={Link}
            />
          ) : filtering &&
            hidden > 0 &&
            // Waiting rows are never selected, so the lists say what is left.
            queue.length + fixing.length + waiting.length + others.length + snoozed.length === 0 ? (
            <div className="upnext-zero">
              <h2>Nothing matches the filter</h2>
              <p>
                {formatCount(hidden)} pull {hidden === 1 ? "request is" : "requests are"} hidden by
                it.
              </p>
              <Button onClick={() => quick.reset()}>Discard the changes</Button>
            </div>
          ) : (
            <Zero inbox={inbox} hasRepositories={hasRepositories} Link={Link} />
          )}
        </div>
        {toast ? (
          <div role="status" className="upnext-toast">
            <span>{toast.text}</span>
            {toast.undo ? (
              <button type="button" className={SMALL} onClick={undo}>
                Undo
                <kbd aria-hidden="true" className="key-hint">
                  u
                </kbd>
              </button>
            ) : null}
          </div>
        ) : null}
      </div>
      {merging ? (
        <MergeDialog
          key={upNextKey(merging.pull_request)}
          entry={merging}
          close={() => setMerging(null)}
          merged={changed}
        />
      ) : null}
      {reviewing ? (
        <ReviewDialog
          key={upNextKey(reviewing.entry.pull_request)}
          entry={reviewing.entry}
          event={reviewing.event}
          close={() => setReviewing(null)}
          // The dialog says it landed in the app's toast.
          sent={() => void changed()}
        />
      ) : null}
      {updating ? (
        <UpdateBranchDialog
          key={upNextKey(updating.pull_request)}
          entry={updating}
          close={() => setUpdating(null)}
          updated={changed}
        />
      ) : null}
      {mergingAll ? (
        <MergeReadyDialog entries={mergingAll} close={() => setMergingAll(null)} merged={changed} />
      ) : null}
    </>
  );
}

function ListRow({
  row,
  on,
  tabbable,
  pick,
  inGroup = false,
}: {
  row: Row;
  on: boolean;
  tabbable: boolean;
  pick: (key: string) => void;
  /** The row is a member nested under its review group's row. */
  inGroup?: boolean;
}) {
  const pr = row.entry.pull_request;
  // The section heading already names the kind, so the line starts with the
  // pull request's number. Under a group, whose members may each be in
  // another section, it says what the member waits on (what needs fixing, or
  // Ready to merge), and the group names the repository.
  const repository = inGroup ? "" : pr.repository;
  const sub = [
    `#${pr.number}`,
    ...(row.job
      ? ["Agent fixing", fixStep(row.job)]
      : row.waiting
        ? [rowWaiting(row), repository]
        : row.item?.failedFix || row.item?.unchangedFix
          ? [
              inGroup ? memberLabel(row.item.kind, row.entry) : "",
              row.item.failedFix ? "Last fix failed" : "Last fix changed nothing",
            ]
          : [
              inGroup && row.item ? memberLabel(row.item.kind, row.entry) : repository,
              row.item?.minutes != null ? minutesText(row.item.minutes) : "",
            ]),
  ]
    .filter(Boolean)
    .join(" · ");
  const hot = (row.entry.risk_score?.score ?? 0) >= 5;
  return (
    <button
      type="button"
      className="upnext-row"
      tabIndex={tabbable ? 0 : -1}
      aria-current={on || undefined}
      onClick={() => pick(row.key)}
    >
      <span className="upnext-row-text">
        <span className="upnext-row-title">{pr.title || "Untitled pull request"}</span>
        <span className="upnext-row-line">
          <span
            className={`upnext-row-sub${row.job ? " upnext-row-working" : row.item?.failedFix ? " upnext-row-failed" : ""}`}
          >
            {row.job ? (
              <>
                <NineDots size={10} />
                <span className="upnext-row-working-text">{sub}</span>
              </>
            ) : (
              sub
            )}
          </span>
        </span>
      </span>
      {hot ? (
        <span className="upnext-hot" role="img" aria-label="Critical risk" title="Critical risk" />
      ) : null}
    </button>
  );
}

/** A group's kind in words: "Stack of 3" or "Group of 2". */
function groupName(group: ReviewGroup) {
  // A stack's merged and closed members still count: it is the stack it was.
  const size = group.members.length + (group.landed?.length ?? 0);
  return `${group.stack ? "Stack" : "Group"} of ${formatCount(size)}`;
}

/** What a group's shown members wait on, in words: "1 to merge and 2 to fix". */
function memberKinds(members: Row[]) {
  const counts = new Map<string, number>();
  for (const member of members) {
    const word = member.job
      ? "with an agent"
      : member.waiting
        ? "waiting"
        : `to ${KINDS[member.item?.kind ?? "fix"].label.toLowerCase()}`;
    counts.set(word, (counts.get(word) ?? 0) + 1);
  }
  return joinAnd([...counts].map(([word, n]) => `${formatCount(n)} ${word}`));
}

/** A review group's row: it opens to its members, nested under it. */
function GroupListRow({
  line,
  open,
  toggle,
  on,
  tabbable,
  pick,
}: {
  line: GroupRow;
  open: boolean;
  toggle: () => void;
  on: boolean;
  tabbable: boolean;
  pick: (key: string) => void;
}) {
  const { group, members } = line;
  const hot = members.some((member) => (member.entry.risk_score?.score ?? 0) >= 5);
  return (
    <div className="upnext-group-line">
      <button
        type="button"
        className="upnext-group-toggle"
        tabIndex={-1}
        aria-expanded={open}
        aria-label={`${open ? "Hide" : "Show"} the pull requests of ${group.name}`}
        onClick={toggle}
      >
        <ChevronRightIcon size={12} className="upnext-chevron" />
      </button>
      <button
        type="button"
        className="upnext-row"
        tabIndex={tabbable ? 0 : -1}
        aria-current={on || undefined}
        aria-expanded={open}
        onClick={() => pick(line.key)}
      >
        <span className="upnext-row-text">
          <span className="upnext-row-title">
            {group.stack ? <StackIcon size={13} className="upnext-stack-icon" /> : null}
            {group.name}
          </span>
          <span className="upnext-row-sub">
            {[groupName(group), group.repository, memberKinds(members)].join(" · ")}
          </span>
        </span>
        {hot ? (
          <span
            className="upnext-hot"
            role="img"
            aria-label="Critical risk"
            title="Critical risk"
          />
        ) : null}
      </button>
    </div>
  );
}

type PanelRow = {
  badge?: string;
  main: string;
  sub?: string;
  /** Lines added and deleted. */
  lines?: { additions: number; deletions: number };
  /** What the row opens to, read from GitHub or the clone when it opens: the
   *  unresolved review threads, the checks or the conflicting files. */
  detail?: "threads" | "checks" | "conflicts";
  /** It starts open. */
  open?: boolean;
};
type PanelBlock = { title: string; lead?: string; rows: PanelRow[]; more?: string };

/** Known checks, conflicts and threads open to their details. An unexplained
 *  GitHub merge block is shown directly. A fix lists its known blockers under
 *  What needs fixing instead. */
function onGitHub(entry: Entry, fixing: boolean): PanelBlock[] {
  const unresolved = entry.unresolved ?? 0;
  const unexplained =
    entry.merge === "blocked" &&
    !entry.pull_request.draft &&
    !entry.pull_request.behind &&
    entry.checks !== "running" &&
    entry.checks !== "failing" &&
    !entry.pull_request.review_required &&
    entry.review !== "changes_requested" &&
    unresolved === 0;
  const rows: PanelRow[] = [
    ...(unexplained
      ? [
          {
            main: "Merge blocked by GitHub",
            sub: "GitHub has not supplied the reason. Check this connection's permissions and the pull request's rules on GitHub.",
          },
        ]
      : []),
    ...(CHECK_WORDS[entry.checks] &&
    entry.checks !== "none" &&
    !(fixing && entry.checks === "failing")
      ? [{ main: CHECK_WORDS[entry.checks]!, detail: "checks" as const }]
      : []),
    ...(entry.merge === "conflict" && !fixing
      ? [{ main: "Conflicts with its base", detail: "conflicts" as const }]
      : []),
    ...(unresolved > 0 && !fixing
      ? [
          {
            main: `${plural(unresolved, "unresolved review thread")}${entry.unresolved_capped ? " or more" : ""}`,
            detail: "threads" as const,
          },
        ]
      : []),
  ];
  return rows.length ? [{ title: "On GitHub", rows }] : [];
}

const SECTIONS_SHOWN = 6;

/** What a review says changed: its summary, then its sections in
 *  review order with their scores and lines; none without a review. */
function whatChanged(doc: ReviewDocument | null): PanelBlock[] {
  if (!doc) return [];
  const sections = sectionsInReviewOrder(doc.sections).filter(
    (section) => section.derived.state !== "stale",
  );
  return [
    {
      title: "What changed",
      lead: doc.summary.text,
      rows: sections.slice(0, SECTIONS_SHOWN).map((section) => ({
        badge:
          section.derived.review_score != null ? String(section.derived.review_score) : undefined,
        main: section.title,
        lines: section.derived,
      })),
      more:
        sections.length > SECTIONS_SHOWN
          ? `and ${plural(sections.length - SECTIONS_SHOWN, "more section")}`
          : undefined,
    },
  ];
}

/** The latest group review of the selected group, once read; none while it
 *  is read, without one, or when the read fails. */
function useGroupDocument(group: ReviewGroup | undefined) {
  const [doc, setDoc] = useState<{ id: string; doc: ReviewDocument } | null>(null);
  // A new version leaves `updated_at` alone, so its count is in the key too.
  const id =
    group && group.version_count > 0
      ? `${group.id}@${group.version_count}@${group.updated_at}`
      : "";
  useEffect(() => {
    if (!id || !group) return;
    const abort = new AbortController();
    groupDocumentRequest(group.id, undefined, abort.signal)
      .then((read) => setDoc({ id, doc: read }))
      .catch(() => {});
    return () => abort.abort();
  }, [id]);
  return doc?.id === id ? doc.doc : null;
}

/** What the panel lists under the pull request, by kind: what changed for a
 *  review or a merge, what blocks it for a fix, where it stands for an
 *  update, and what baum will do for a clone. */
function blocks(row: Row, doc: ReviewDocument | null): PanelBlock[] {
  const entry = row.entry;
  const pr = entry.pull_request;
  const unresolved = entry.unresolved ?? 0;
  const changes = whatChanged(doc);
  const github = onGitHub(entry, row.item?.kind === "fix" && !row.job);
  if (row.job) return [...changes, ...github];
  // Waiting pull requests keep their GitHub state ahead of the saved review.
  if (row.waiting) return [...github, ...changes];
  switch (row.item?.kind) {
    case "review":
    case "merge":
      return [...changes, ...github];
    case "fix":
      return [
        {
          title: "What needs fixing",
          rows: [
            ...(entry.review === "changes_requested"
              ? [
                  {
                    main: "Changes requested",
                    sub: "A reviewer asked for changes before it merges.",
                  },
                ]
              : []),
            ...(unresolved > 0
              ? [
                  {
                    main: `${plural(unresolved, "unresolved review thread")}${entry.unresolved_capped ? " or more" : ""}`,
                    detail: "threads" as const,
                    open: true,
                  },
                ]
              : []),
            ...(entry.merge === "conflict"
              ? [
                  {
                    main: "Conflicts with its base",
                    detail: "conflicts" as const,
                    open: true,
                  },
                ]
              : []),
            ...(entry.checks === "failing"
              ? [{ main: "Checks failing", detail: "checks" as const, open: true }]
              : []),
          ],
        },
        ...github,
        ...changes,
      ];
    case "update":
      // Its sentence says it is behind and what updating does.
      return [...github, ...changes];
    case "clone":
      return [
        {
          title: "What baum will do",
          rows: [
            {
              main: `Use a local clone of ${pr.repository}`,
              sub: "Choose a folder that holds it, or clone it.",
            },
            { main: "Generate the review", sub: "Usually a few minutes." },
          ],
        },
        // Conflicting files are worked out in the clone it lacks.
        ...github
          .map((block) => ({ ...block, rows: block.rows.filter((r) => r.detail !== "conflicts") }))
          .filter((block) => block.rows.length > 0),
      ];
    default:
      return [...changes, ...github];
  }
}

function Panel({
  row,
  doc,
  tags,
  href,
  Link,
  fix,
  fixState,
  generate,
  generateState,
  merge,
  review,
  update,
  menu,
  setMenu,
  options,
  snooze,
  now,
}: {
  row: Row;
  doc: ReviewDocument | null;
  /** The other inboxes it is in, or all of them in the Inbox. */
  tags: Inbox[];
  href: string;
  Link: ComponentType<ViewerLinkProps>;
  fix?: () => void;
  fixState: FixState;
  /** Starts the review; none without a clone to write it in. */
  generate?: () => void;
  generateState: FixState;
  merge: () => void;
  review: (event: ReviewEvent) => void;
  update: () => void;
  menu: boolean;
  setMenu: (open: boolean | ((open: boolean) => boolean)) => void;
  options: { label: string; until: SnoozeEnd }[];
  snooze: (until: SnoozeEnd) => void;
  now: number;
}) {
  const { entry, item, job, waiting } = row;
  const pr = entry.pull_request;
  const kind = item?.kind ?? "fix";
  const pending = usePendingReview(entry);
  const menuRef = useRef<HTMLDivElement>(null);
  const menuFocus = useMenuFocus(menu, () => setMenu(false));
  useEffect(() => {
    if (!menu) return;
    const close = (event: MouseEvent) => {
      if (!menuRef.current?.contains(event.target as Node)) setMenu(false);
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [menu]);
  const who = pr.review_requested_from_me
    ? `${pr.author ?? "Someone"} asked for your review`
    : pr.authored_by_me
      ? "Yours"
      : pr.assigned_to_me
        ? `${pr.author ?? "Someone"}'s, assigned to you`
        : `By ${pr.author ?? "someone else"}`;
  // Opening the pull request is always the default; what the kind asks for
  // follows it. A merge or branch update opens its confirmation; a fix starts
  // at once.
  const actions = (
    <>
      <Link href={href} className={cn("upnext-primary", PRIMARY)}>
        Open
        <kbd aria-hidden="true" className="key-hint">
          ↵
        </kbd>
      </Link>
      {job ? (
        <Link href={jobHref(job)} className={cn("upnext-secondary", SECONDARY)}>
          Watch the agent
          {job.has_transcript ? (
            <kbd aria-hidden="true" className="key-hint">
              t
            </kbd>
          ) : null}
        </Link>
      ) : waiting ? (
        pr.url ? (
          <a
            href={pr.url}
            target="_blank"
            rel="noreferrer"
            className={cn("upnext-secondary", SECONDARY)}
          >
            Open on GitHub
          </a>
        ) : null
      ) : kind === "merge" ? (
        <button type="button" className={cn("upnext-secondary", SECONDARY)} onClick={merge}>
          Merge
          <kbd aria-hidden="true" className="key-hint">
            m
          </kbd>
        </button>
      ) : kind === "update" ? (
        <button type="button" className={cn("upnext-secondary", SECONDARY)} onClick={update}>
          Update the branch
        </button>
      ) : kind === "fix" && fix ? (
        <button
          type="button"
          className={cn("upnext-secondary", SECONDARY)}
          disabled={fixState.starting}
          onClick={fix}
        >
          {fixState.starting ? (
            "Starting…"
          ) : (
            <>
              {fixLabel(entry)}
              <kbd aria-hidden="true" className="key-hint">
                f
              </kbd>
            </>
          )}
        </button>
      ) : null}
    </>
  );
  const score = entry.risk_score?.score ?? null;
  const reason = doc?.summary.risk_score?.reasons[0];
  const reasonTitle = reason
    ? (doc?.sections.find((section) => section.id === reason.section)?.title ?? "")
    : "";
  return (
    <article className="upnext-panel" aria-labelledby="upnext-title">
      <div className="upnext-panel-meta">
        {waiting ? (
          <span className="upnext-chip upnext-chip-waiting">{rowWaiting(row)}</span>
        ) : (
          <span className={`upnext-chip upnext-chip-${kind}`}>{KINDS[kind].label}</span>
        )}
        <span className="upnext-panel-where">
          {pr.repository} · #{pr.number} · {who}
          {pr.draft ? " · Draft" : ""}
          {pr.fork ? " · Fork" : ""}
        </span>
        {tags.length > 0 ? (
          <span className="upnext-tags" role="group" aria-label="Inboxes">
            {tags.map((tag) => (
              <InboxTag key={tag.id} inbox={tag} />
            ))}
          </span>
        ) : null}
      </div>
      <h2 id="upnext-title" className="upnext-title">
        <Link href={href}>{pr.title || "Untitled pull request"}</Link>
      </h2>
      <Facts entry={entry} />
      {item?.why ? <p className="upnext-why">{item.why}</p> : null}
      {job ? (
        <div className="upnext-working" role="status">
          <NineDots size={14} />
          <span>An agent is fixing it{fixStep(job) ? ` · ${fixStep(job)}` : ""}.</span>
          <span className="upnext-working-aside">You can move on</span>
        </div>
      ) : null}
      {fixState.error ? (
        <div className="upnext-failed" role="alert">
          Couldn't start the fix: {fixState.error}
        </div>
      ) : null}
      {item?.failedFix ? (
        <div className="upnext-failed" role="status">
          The last fix failed
          {item.failedFix.reason ? `: ${jobReasonText(item.failedFix.reason)}` : "."}{" "}
          <Link href={jobHref(item.failedFix)}>
            See what the agent did
            {item.failedFix.has_transcript ? (
              <kbd aria-hidden="true" className="key-hint">
                t
              </kbd>
            ) : null}
          </Link>
        </div>
      ) : null}
      {item?.unchangedFix ? <UnchangedFix job={item.unchangedFix} Link={Link} /> : null}
      <div className="upnext-actions">
        {actions}
        {reviewable(entry) ? (
          <ReviewButton
            entry={entry}
            count={pending.value.comments.length}
            review={review}
            className="upnext-secondary"
          />
        ) : null}
        <div className="upnext-snooze" ref={menuRef}>
          <button
            type="button"
            className={cn("upnext-secondary", SECONDARY)}
            aria-haspopup="menu"
            aria-expanded={menu}
            onClick={() => setMenu((open) => !open)}
          >
            Snooze
            <ChevronDownIcon size={14} className="text-(--text-tertiary)" />
            <kbd aria-hidden="true" className="key-hint">
              s
            </kbd>
          </button>
          {menu ? (
            <div role="menu" className="upnext-menu" {...menuFocus}>
              {options.map((option, index) => (
                <button
                  key={option.label}
                  type="button"
                  role="menuitem"
                  onClick={() => snooze(option.until)}
                >
                  <span className="upnext-menu-key">{index + 1}</span>
                  <span className="upnext-menu-label">{option.label}</span>
                  <span className="upnext-menu-when">{snoozeText(option.until, now)}</span>
                </button>
              ))}
            </div>
          ) : null}
        </div>
      </div>
      {/* Until the review's summary is written there is nothing under the
          actions to read, so say it is on its way. */}
      {entry.status === "generating" && !doc?.summary.text ? (
        <div className="upnext-working" role="status">
          <NineDots size={14} />
          <span>Generating the review.</span>
        </div>
      ) : kind !== "clone" &&
        entry.status !== "generating" &&
        (entry.status === "failed" || entry.version_count === 0) ? (
        // A clone item says what baum will do instead.
        <NoReview entry={entry} generate={generate} state={generateState} />
      ) : null}
      {score != null ? (
        <RiskCard score={score} text={riskText(entry)}>
          {reason ? (
            <div className="upnext-risk-copy">
              {reasonTitle ? (
                <strong>
                  <InlineMarkdown>{reasonTitle}</InlineMarkdown>
                </strong>
              ) : null}
              <Markdown html={false} className="upnext-prose">
                {reason.text}
              </Markdown>
            </div>
          ) : null}
        </RiskCard>
      ) : null}
      {blocks(row, doc).map((block) => (
        <section key={block.title} className="upnext-block" aria-label={block.title}>
          <h3 className="upnext-label">{block.title}</h3>
          {block.lead ? (
            <Markdown html={false} className="upnext-lead upnext-prose">
              {block.lead}
            </Markdown>
          ) : null}
          <ul>
            {block.rows.map((line) => (
              <BlockRow key={line.main} line={line} entry={entry} />
            ))}
          </ul>
          {block.more ? <p className="upnext-block-more">{block.more}</p> : null}
        </section>
      ))}
      {item && item.working.length > 0 ? (
        <p className="upnext-agents">
          <LiveDot className="text-(--status-positive)" />
          {/* Each opens on Sessions with that session selected. */}
          {item.working.length === 1 ? (
            <Link href={peekHref(item.working[0]!)}>
              1 agent session working on its branch right now
            </Link>
          ) : (
            <span>
              {plural(item.working.length, "agent session")} working on its branch right now:{" "}
              {item.working.map((session, index) => (
                <Fragment key={peekHref(session)}>
                  {index > 0 ? " · " : ""}
                  <Link href={peekHref(session)}>{agentName(session.agent)}</Link>
                </Fragment>
              ))}
            </span>
          )}
        </p>
      ) : null}
    </article>
  );
}

/** Where the review stands while there is none to read, or the last one
 *  failed, with the button that writes it. */
function NoReview({
  entry,
  generate,
  state,
}: {
  entry: Entry;
  generate?: () => void;
  state: FixState;
}) {
  const failed = entry.status === "failed";
  const text = failed
    ? `baum's review failed${entry.failure_reason ? `: ${entry.failure_reason}` : "."}`
    : entry.needs_clone
      ? `No review yet. baum needs a local clone of ${entry.pull_request.repository} to write one; choose a folder or clone it in Repositories.`
      : "No review yet.";
  return (
    <div className="upnext-noreview" role={state.error ? "alert" : undefined}>
      <div className="upnext-noreview-copy">
        <span>{text}</span>
        {state.error ? (
          <span className="upnext-noreview-sub">Couldn't start the review: {state.error}</span>
        ) : generate && !failed ? (
          <span className="upnext-noreview-sub">Writing one usually takes a few minutes.</span>
        ) : null}
      </div>
      {generate ? (
        <button
          type="button"
          className={cn("upnext-secondary", SECONDARY)}
          disabled={state.starting}
          onClick={generate}
        >
          {state.starting ? "Starting…" : failed ? "Try again" : "Generate"}
        </button>
      ) : null}
    </div>
  );
}

/** The pull request's facts under its title: which branch merges where, its
 *  size, baum's estimate of the time to review it and its page on GitHub. */
function Facts({ entry }: { entry: Entry }) {
  const pr = entry.pull_request;
  const files = pr.changed_files ?? 0;
  const sized = (pr.additions ?? 0) + (pr.deletions ?? 0) + files > 0;
  return (
    <dl className="upnext-facts upnext-pr-facts">
      <div className="upnext-branch-fact">
        <dt className="sr-only">{pr.head_ref || pr.base_ref ? "Branch" : "GitHub"}</dt>
        <dd className="upnext-branches">
          {pr.head_ref || pr.base_ref ? (
            <>
              <BranchIcon size={14} />
              <code title={pr.head_ref}>{pr.head_ref || "?"}</code>
              <span aria-label="into">→</span>
              <code title={pr.base_ref}>{pr.base_ref || "?"}</code>
            </>
          ) : null}
          <a
            className="upnext-github"
            href={pr.url}
            target="_blank"
            rel="noreferrer"
            aria-label={`Open ${pr.repository}#${pr.number} on GitHub`}
            title={`Open ${pr.repository}#${pr.number} on GitHub`}
          >
            <GitHubIcon />
          </a>
        </dd>
      </div>
      {sized ? (
        <div>
          <dt className="sr-only">Size</dt>
          <dd>
            <Lines
              additions={pr.additions ?? 0}
              deletions={pr.deletions ?? 0}
              format={formatCount}
            />
            {files ? <span className="upnext-fact-aside"> · {plural(files, "file")}</span> : null}
          </dd>
        </div>
      ) : null}
      {entry.review_minutes != null ? (
        <div>
          <dt className="sr-only">Time to review</dt>
          <dd title="baum's estimate of the time to read the change">
            {minutesText(entry.review_minutes)} review
          </dd>
        </div>
      ) : null}
    </dl>
  );
}

/** A row of a panel block; one with a detail opens to it, read when it
 *  opens. */
function BlockRow({ line, entry }: { line: PanelRow; entry: Entry }) {
  const [open, setOpen] = useState(line.open ?? false);
  const id = useId();
  const pr = entry.pull_request;
  // A checks row reads them at once, open or not, to count them beside its
  // title; the list it opens to shows the same read.
  const checks = useChecks(pr.repository, pr.number, pr.head_sha, line.detail === "checks");
  const count = checks.checks?.length ? checksCount(checks.checks) : null;
  const text = (
    <span className="upnext-block-text">
      {line.lines ? (
        <InlineMarkdown>{line.main}</InlineMarkdown>
      ) : line.detail === "checks" && count ? (
        <span className="upnext-block-title">
          <span>{line.main}</span>
          <Badge tone={count.tone} title={checksSummary(checks.checks!)}>
            {count.text}
          </Badge>
        </span>
      ) : (
        <span>{line.main}</span>
      )}
      {line.sub ? <span className="upnext-block-sub">{line.sub}</span> : null}
    </span>
  );
  return (
    <li className={line.detail ? "upnext-has-threads" : undefined}>
      {line.badge ? (
        <span className={`upnext-badge upnext-badge-${line.badge === "✓" ? "done" : line.badge}`}>
          {line.badge}
        </span>
      ) : null}
      {line.detail ? (
        <button
          type="button"
          className="upnext-detail-toggle"
          aria-expanded={open}
          aria-controls={id}
          onClick={() => setOpen((open) => !open)}
        >
          <ChevronRightIcon size={12} className="upnext-chevron" />
          {text}
        </button>
      ) : (
        text
      )}
      {line.lines ? (
        <Lines
          additions={line.lines.additions}
          deletions={line.lines.deletions}
          format={formatCount}
          className="upnext-block-right"
        />
      ) : null}
      {line.detail && open ? (
        <div id={id} className="upnext-detail-body">
          {line.detail === "threads" ? (
            <ReviewThreads
              repository={pr.repository}
              number={pr.number}
              unresolved={entry.unresolved ?? 0}
              url={pr.url}
            />
          ) : line.detail === "checks" ? (
            <PullRequestChecks
              repository={pr.repository}
              number={pr.number}
              head={pr.head_sha}
              url={pr.url}
              canRetry={pr.viewer_can_push === true}
              checks={checks}
            />
          ) : (
            <ConflictingFiles
              repository={pr.repository}
              number={pr.number}
              head={pr.head_sha}
              url={pr.url}
            />
          )}
        </div>
      ) : null}
    </li>
  );
}

/** The risk card of a pull request's or a group's panel: its score as five
 *  blocks, in words, and what the review says of it below (`children`). */
function RiskCard({
  score,
  text,
  children,
}: {
  score: number;
  /** The score in words: "3 of 5", or "at least 3 of 5". */
  text: string;
  children?: ReactNode;
}) {
  return (
    <div className="upnext-risk">
      <div className="upnext-risk-head">
        <span className="upnext-label">Risk</span>
        <span className="upnext-risk-blocks" aria-hidden="true">
          {[1, 2, 3, 4, 5].map((n) => (
            <span
              key={n}
              className={
                n <= score ? `upnext-risk-on upnext-risk-${Math.min(score, 5)}` : undefined
              }
            />
          ))}
        </span>
        <span className="upnext-risk-text">
          {text} · {RISK_SCORE_MEANING[score as RiskScoreLevel]}
        </span>
      </div>
      {children}
    </div>
  );
}

/** The last fix ended without changing anything, which is the agent's
 *  judgement, not an error: said in neutral words, with the agent's reason
 *  (the first paragraph of what it said) and the way to the rest. */
function UnchangedFix({ job, Link }: { job: Job; Link: ComponentType<ViewerLinkProps> }) {
  const why = summaryLead(job.summary);
  return (
    <div className="upnext-unchanged" role="status">
      <p className="upnext-unchanged-title">The last fix changed nothing, so nothing was pushed.</p>
      {why ? (
        <Markdown html={false} className="upnext-prose upnext-unchanged-why">
          {why}
        </Markdown>
      ) : null}
      <Link href={jobHref(job)}>
        {why ? "Read what the agent said" : "See what the agent did"}
        {job.has_transcript ? (
          <kbd aria-hidden="true" className="key-hint">
            t
          </kbd>
        ) : null}
      </Link>
    </div>
  );
}

/** What a group's member waits on, in a few words, and the tone of its dot:
 *  an agent at work, what needs fixing, ready to merge, or waiting. */
function memberState(row: Row): { text: string; tone: string } {
  if (row.job) {
    const step = fixStep(row.job);
    return { text: step ? `Agent fixing · ${step}` : "Agent fixing", tone: "working" };
  }
  if (row.waiting) return { text: rowWaiting(row), tone: "waiting" };
  if (!row.item) return { text: "", tone: "waiting" };
  const label = memberLabel(row.item.kind, row.entry);
  return {
    text: row.item.failedFix
      ? `${label} · Last fix failed`
      : row.item.unchangedFix
        ? `${label} · Last fix changed nothing`
        : label,
    tone: row.item.kind,
  };
}

/** A review group's panel, laid out as a pull request's: its base and facts
 *  summed, its review's state, its risk, and its shown members in order, a
 *  stack's from its base up, each with what it waits on. Picking a member
 *  previews it. */
function GroupPanel({
  line,
  doc,
  entries,
  href,
  pick,
  Link,
}: {
  line: GroupRow;
  /** Its latest group review, once read. */
  doc: ReviewDocument | null;
  /** Every pull request read, for the members the queue does not list. */
  entries: Entry[];
  href: string;
  pick: (key: string) => void;
  Link: ComponentType<ViewerLinkProps>;
}) {
  const { group, members } = line;
  const [listOpen, setListOpen] = useState(true);
  const listId = useId();
  // Every member, a stack's from its base up: those that landed (merged or
  // closed, which the daemon lists apart, ascending, below the open ones),
  // then the open ones in the group's order. Those the queue lists preview
  // when picked; an open one it does not list shows as read, and one not
  // read at all is left out.
  const read = (number: number) =>
    entries.find(
      (entry) =>
        entry.pull_request.number === number &&
        sameRepository(entry.pull_request.repository, group.repository),
    );
  const landed = group.landed ?? [];
  const total = group.members.length + landed.length;
  const merged = landed.filter((member) => member.state === "merged").length;
  const rows: {
    number: number;
    row?: Row;
    entry?: Entry;
    state: { text: string; tone: string };
  }[] = [
    ...landed.map((member) => ({
      number: member.number,
      entry: read(member.number),
      state:
        member.state === "merged"
          ? { text: "Merged", tone: "merged" }
          : member.state === "closed"
            ? { text: "Closed", tone: "waiting" }
            : { text: "No longer open", tone: "waiting" },
    })),
    ...group.members.flatMap((number) => {
      const row = members.find((member) => member.entry.pull_request.number === number);
      if (row) return [{ number, row, entry: row.entry, state: memberState(row) }];
      const entry = read(number);
      return entry
        ? [{ number, entry, state: { text: "Nothing to do now", tone: "waiting" } }]
        : [];
    }),
  ];
  const unread = total - rows.length;
  const reason = doc?.summary.risk_score?.reasons[0];
  const reasonTitle = reason
    ? (doc?.sections.find((section) => section.id === reason.section)?.title ?? "")
    : "";
  const sum = (value: (row: Row) => number | null | undefined) =>
    members.reduce((total, row) => total + (value(row) ?? 0), 0);
  const additions = sum((row) => row.entry.pull_request.additions);
  const deletions = sum((row) => row.entry.pull_request.deletions);
  const files = sum((row) => row.entry.pull_request.changed_files);
  const minutes = members.some((row) => row.entry.review_minutes != null)
    ? sum((row) => row.entry.review_minutes)
    : null;
  // Where the group merges: the one base branch that is not a member's own,
  // as main under a stack.
  const heads = new Set(members.map((row) => row.entry.pull_request.head_ref).filter(Boolean));
  const bases = new Set(
    members
      .map((row) => row.entry.pull_request.base_ref)
      .filter((base) => base && !heads.has(base)),
  );
  const base = bases.size === 1 ? [...bases][0] : "";
  // A ready review says so through its risk and What changed; only its
  // absence or age needs a word.
  const review =
    group.version_count === 0
      ? "There is no group review yet; generate one from its page."
      : group.outdated
        ? "The group review covers earlier commits of its pull requests."
        : "";
  const score = group.risk_score?.score ?? null;
  const kind = group.stack ? "stack" : "group";
  return (
    <article className="upnext-panel" aria-labelledby="upnext-title">
      <div className="upnext-panel-meta">
        <span className="upnext-chip upnext-chip-waiting">{group.stack ? "Stack" : "Group"}</span>
        <span className="upnext-panel-where">
          {group.repository} · {plural(total, "pull request")}
        </span>
      </div>
      <h2 id="upnext-title" className="upnext-title">
        <Link href={href}>{group.name}</Link>
      </h2>
      <dl className="upnext-facts upnext-pr-facts">
        {base ? (
          <div className="upnext-branch-fact">
            <dt className="sr-only">Base</dt>
            <dd className="upnext-branches">
              {group.stack ? <StackIcon size={14} /> : <BranchIcon size={14} />}
              <span>{group.stack ? "Stacked on" : "Into"}</span>
              <code title={base}>{base}</code>
            </dd>
          </div>
        ) : null}
        {additions + deletions + files > 0 ? (
          <div>
            <dt className="sr-only">Size</dt>
            <dd>
              <Lines additions={additions} deletions={deletions} format={formatCount} />
              {files ? <span className="upnext-fact-aside"> · {plural(files, "file")}</span> : null}
            </dd>
          </div>
        ) : null}
        {minutes != null ? (
          <div>
            <dt className="sr-only">Time to review</dt>
            <dd title="baum's estimates of the time to read each change, summed">
              {minutesText(minutes)} review
            </dd>
          </div>
        ) : null}
      </dl>
      {review ? <p className="upnext-why">{review}</p> : null}
      <div className="upnext-actions">
        <Link href={href} className={cn("upnext-primary", PRIMARY)}>
          Open the {kind}
          <kbd aria-hidden="true" className="key-hint">
            ↵
          </kbd>
        </Link>
      </div>
      {score != null ? (
        <RiskCard
          score={score}
          text={`${group.risk_score?.status === "lower_bound" ? "at least " : ""}${score} of 5`}
        >
          {reason ? (
            <div className="upnext-risk-copy">
              {reasonTitle ? (
                <strong>
                  <InlineMarkdown>{reasonTitle}</InlineMarkdown>
                </strong>
              ) : null}
              <Markdown html={false} className="upnext-prose">
                {reason.text}
              </Markdown>
            </div>
          ) : null}
        </RiskCard>
      ) : null}
      {whatChanged(doc).map((block) => (
        <section key={block.title} className="upnext-block" aria-label={block.title}>
          <h3 className="upnext-label">{block.title}</h3>
          {block.lead ? (
            <Markdown html={false} className="upnext-lead upnext-prose">
              {block.lead}
            </Markdown>
          ) : null}
          <ul>
            {block.rows.map((row) => (
              <li key={row.main}>
                {row.badge ? (
                  <span className={`upnext-badge upnext-badge-${row.badge}`}>{row.badge}</span>
                ) : null}
                <span className="upnext-block-text">
                  <InlineMarkdown>{row.main}</InlineMarkdown>
                </span>
                {row.lines ? (
                  <Lines
                    additions={row.lines.additions}
                    deletions={row.lines.deletions}
                    format={formatCount}
                    className="upnext-block-right"
                  />
                ) : null}
              </li>
            ))}
          </ul>
          {block.more ? <p className="upnext-block-more">{block.more}</p> : null}
        </section>
      ))}
      <section className="upnext-block" aria-label="Pull requests">
        <h3 className="upnext-label upnext-members-head">
          <button
            type="button"
            className="upnext-members-toggle"
            aria-expanded={listOpen}
            aria-controls={listId}
            onClick={() => setListOpen((open) => !open)}
          >
            <ChevronRightIcon size={12} className="upnext-chevron" />
            {plural(total, "pull request")}
          </button>
          {merged > 0 ? (
            <Badge tone="merged" size="compact" className="gap-1">
              <MergeIcon size={11} className="flex-none" />
              {formatCount(merged)} merged
            </Badge>
          ) : null}
        </h3>
        {listOpen ? (
          <ul id={listId} className={cn("upnext-members", group.stack && "upnext-members-stack")}>
            {rows.map(({ number, row, entry, state }) => {
              const pr = entry?.pull_request;
              const text = (
                <span className="upnext-block-text">
                  <span className="upnext-member-title" title={pr?.title}>
                    {pr ? pr.title || "Untitled pull request" : `Pull request #${number}`}
                  </span>
                  <span className="upnext-block-sub">
                    #{number}
                    {state.text ? ` · ${state.text}` : ""}
                  </span>
                </span>
              );
              return (
                <li key={number} className={row ? undefined : "upnext-member-other"}>
                  <span
                    className={`upnext-member-dot upnext-member-${state.tone}`}
                    aria-hidden="true"
                  />
                  {row ? (
                    <button
                      type="button"
                      className="upnext-member-pick"
                      onClick={() => pick(row.key)}
                    >
                      {text}
                    </button>
                  ) : (
                    text
                  )}
                  {pr && (pr.additions || pr.deletions) ? (
                    <Lines
                      additions={pr.additions ?? 0}
                      deletions={pr.deletions ?? 0}
                      format={formatCount}
                      className="upnext-block-right"
                    />
                  ) : null}
                </li>
              );
            })}
          </ul>
        ) : null}
        {listOpen && unread > 0 ? (
          <p className="upnext-block-more">
            and {plural(unread, "more pull request")} baum has not read
          </p>
        ) : null}
      </section>
    </article>
  );
}

function Zero({
  inbox,
  hasRepositories,
  Link,
}: {
  inbox: UpNextState;
  hasRepositories: boolean;
  Link: ComponentType<ViewerLinkProps>;
}) {
  // Stumpy marks the edges of the work, as on hosted Focus: awake while there
  // is nothing to read yet, dozing once nothing is waiting.
  if (!hasRepositories)
    return (
      <div className="upnext-zero">
        <Stumpy mood="awake" size={88} className="upnext-zero-stumpy" />
        <h2>Nothing to do yet</h2>
        <p>
          Pick GitHub repositories in <Link href={appURL("?page=repositories")}>Repositories</Link>{" "}
          to get started.
        </p>
      </div>
    );
  const { snoozed, cleared } = inbox;
  return (
    <div className="upnext-zero">
      <Stumpy mood="dozing" size={88} className="upnext-zero-stumpy" />
      <h2>Nothing here right now.</h2>
      <p>
        {snoozed.length > 0
          ? `Everything here is handled except ${plural(snoozed.length, "snoozed pull request")}; they come back on their own.`
          : cleared.length > 0
            ? `All ${formatCount(cleared.length)} handled. Nothing needs you right now.`
            : "It fills up as matching pull requests come in."}
      </p>
    </div>
  );
}

type Merge = {
  entry: Entry;
  options?: MergeOptions;
  refusal?: string;
  state: "checking" | "ready" | "merging" | "merged" | "failed";
  error?: string;
};

/** Merges every ready pull request in one confirmation, each with GitHub's
 *  default method for it and at the head commit the inbox showed. One that
 *  GitHub refuses is named with the reason; the others still merge. */
export function MergeReadyDialog({
  entries,
  close,
  merged,
}: {
  entries: Entry[];
  close: () => void;
  merged: () => Promise<void>;
}) {
  const dialog = useModal();
  const toast = useToast();
  const [merges, setMerges] = useState<Merge[]>(() =>
    entries.map((entry) => ({ entry, state: "checking" })),
  );
  const [running, setRunning] = useState(false);
  const [finished, setFinished] = useState(false);
  // Leaving the page or the dialog stops the merges not yet started; one
  // already sent to GitHub finishes there.
  const gone = useRef(false);
  useLayoutEffect(() => {
    gone.current = false;
    return () => {
      gone.current = true;
    };
  }, []);
  // The merge button goes once the merges ran: Close takes the focus.
  const closeButton = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (finished) closeButton.current?.focus();
  }, [finished]);
  const set = (index: number, patch: Partial<Merge>) =>
    setMerges((all) => all.map((merge, i) => (i === index ? { ...merge, ...patch } : merge)));
  useEffect(() => {
    const abort = new AbortController();
    entries.forEach((entry, index) => {
      const pr = entry.pull_request;
      request<MergeOptions>(
        `api/prs/merge-options?repository=${encodeURIComponent(pr.repository)}&pr=${pr.number}`,
        undefined,
        "GET",
        abort.signal,
      ).then(
        (options) => {
          const refusal = mergeRefusal(options, entry);
          set(index, { options, refusal, state: refusal ? "failed" : "ready" });
        },
        (e) => {
          if (!abort.signal.aborted)
            set(index, {
              state: "failed",
              error: e instanceof Error ? e.message : "Local request failed.",
            });
        },
      );
    });
    return () => abort.abort();
    // The entries are fixed for the dialog's life.
  }, []);
  const mergeable = merges.filter((merge) => merge.state === "ready");
  const checking = merges.some((merge) => merge.state === "checking");
  const mergeAll = async () => {
    setRunning(true);
    let done = 0;
    let refused = 0;
    for (const [index, merge] of merges.entries()) {
      if (gone.current) break;
      if (merge.state !== "ready" || !merge.options) continue;
      set(index, { state: "merging" });
      try {
        await request("api/prs/merge", {
          repository: merge.entry.pull_request.repository,
          pr: merge.entry.pull_request.number,
          method: defaultMergeMethod(merge.options),
          head_sha: merge.entry.pull_request.head_sha,
        });
        set(index, { state: "merged" });
        done++;
      } catch (e) {
        refused++;
        set(index, {
          state: "failed",
          error: e instanceof Error ? e.message : "Local request failed.",
        });
      }
    }
    setRunning(false);
    setFinished(true);
    // The dialog lists each; the toast stays in sight once it is closed.
    toast({
      title: done
        ? `Merged ${formatCount(done)} pull request${done === 1 ? "" : "s"} on GitHub`
        : "Nothing was merged",
      detail: refused ? `GitHub refused ${formatCount(refused)}; the dialog says why.` : undefined,
      error: done === 0 && refused > 0,
    });
    await merged();
  };
  return (
    <dialog
      ref={dialog}
      onCancel={(event) => {
        if (running) event.preventDefault();
        else close();
      }}
      // The browser can close it without a cancel the page may refuse.
      onClose={close}
      aria-labelledby="merge-ready-title"
    >
      <div className="flex items-start justify-between gap-3">
        <h2 id="merge-ready-title" className="text-lg font-semibold">
          Merge {formatCount(entries.length)} ready pull requests?
        </h2>
        {finished ? <CloseButton ref={closeButton} onClick={close} /> : null}
      </div>
      <p className="my-3 text-(--text-secondary)">
        Each merges with GitHub's default method for you in its repository, and only at the commit
        shown here.
      </p>
      <ul className="upnext-merges" aria-live="polite">
        {merges.map((merge) => {
          const pr = merge.entry.pull_request;
          return (
            <li key={upNextKey(pr)}>
              <span className="min-w-0">
                <span className="block truncate font-medium">
                  #{pr.number} {pr.title}
                </span>
                <span className="block text-xs text-(--text-tertiary)">
                  {pr.repository}
                  {merge.options && !merge.refusal
                    ? ` · ${mergeMethodLabel(defaultMergeMethod(merge.options))}`
                    : ""}
                </span>
                {merge.refusal || merge.error ? (
                  <span className="block text-xs text-(--status-critical)">
                    {merge.refusal || merge.error}
                  </span>
                ) : null}
              </span>
              <span className="upnext-merge-state">
                {merge.state === "checking" || merge.state === "merging" ? (
                  <NineDots size={14} />
                ) : merge.state === "merged" ? (
                  <span className="local-merged">
                    <MergeIcon size={13} className="flex-none" />
                    Merged
                  </span>
                ) : merge.state === "failed" ? (
                  <span className="text-(--status-critical)">Not merged</span>
                ) : null}
              </span>
            </li>
          );
        })}
      </ul>
      {finished && merges.some((merge) => merge.state === "failed") ? (
        <Alert>Some pull requests were not merged; the reasons are beside them.</Alert>
      ) : null}
      {finished ? null : (
        <div className="mt-4 flex justify-end gap-2">
          <Button variant="ghost" size="compact" disabled={running} onClick={close}>
            Cancel
          </Button>
          <Button
            size="compact"
            disabled={checking || running || mergeable.length === 0}
            onClick={() => void mergeAll()}
          >
            {running ? (
              <>
                <NineDots tone="text" />
                Merging…
              </>
            ) : (
              `Merge ${formatCount(mergeable.length)} ${mergeable.length === 1 ? "pull request" : "pull requests"}`
            )}
          </Button>
        </div>
      )}
    </dialog>
  );
}
