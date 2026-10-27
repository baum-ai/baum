import { useEffect, useLayoutEffect, useRef, useState, type ComponentType } from "react";
import { formatCount } from "@baum/viewer/components/format";
import { Lines } from "@baum/viewer/components/review-parts";
import { Alert, Button } from "@baum/viewer/components/ui";
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
  fixJob,
  fixLabel,
  isActiveJob,
  jobReasonText,
  joinAnd,
  liveFor,
  request,
  type Entry,
  type Job,
  type LiveSession,
} from "./api";
import { phaseText } from "./fix";
import { useNow } from "./live";
import {
  canMerge,
  defaultMergeMethod,
  mergeMethodLabel,
  mergeRefusal,
  MergeDialog,
  type MergeOptions,
} from "./merge";
import { LoadingStatus, NineDots } from "./nine-dots";
import { onBody, useShortcuts } from "./shortcuts";
import { canUpdateBranch, UpdateBranchDialog } from "./unblock";

/** What the person does next about a pull request: read a review they were
 *  asked for, unblock their own, merge it, bring it up to date with its base,
 *  or give baum a clone so a requested review can be written. */
export type UpNextKind = "review" | "fix" | "merge" | "update" | "clone";

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
  /** Agent sessions working on its branch now. */
  working: number;
  /** The latest fix of a pull request in the queue as a fix, when it failed. */
  failedFix?: Job;
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
const GROUPS: { id: "eyes" | "merge" | "agent"; title: string; kinds: UpNextKind[] }[] = [
  { id: "eyes", title: "Needs your eyes", kinds: ["review", "clone"] },
  { id: "merge", title: "Ready to merge", kinds: ["merge"] },
  { id: "agent", title: "Hand to an agent", kinds: ["fix", "update"] },
];

/** A fix job's transcript when it has one, else the job in the list. */
function jobHref(job: Job) {
  return job.has_transcript
    ? appURL(`?page=jobs&job=${encodeURIComponent(job.id)}`)
    : appURL(`?page=jobs#job-${encodeURIComponent(job.id)}`);
}

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
  if (pr.review_requested_from_me) return entry.needs_clone ? "clone" : "review";
  if (!pr.authored_by_me) return null;
  // An agent already fixing it leaves nothing for the person until it ends.
  if (isActiveJob(fixJob(entry, jobs))) return null;
  if (blocked(entry)) return "fix";
  if (canMerge(entry)) return "merge";
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

const REVIEW_WORDS: Record<string, string> = {
  approved: "Approved",
  changes_requested: "Changes requested",
  commented: "Commented",
};
const CHECK_WORDS: Record<string, string> = {
  passing: "Checks passing",
  failing: "Checks failing",
  running: "Checks running",
  none: "No checks",
};

function why(kind: UpNextKind, entry: Entry, working: number): string {
  const pr = entry.pull_request;
  switch (kind) {
    case "review": {
      const review =
        entry.status === "generating"
          ? "baum is writing its review now."
          : entry.status === "failed"
            ? "baum's review failed; its page can try again."
            : entry.version_count === 0
              ? "There is no baum review yet; generate one from its page."
              : entry.status === "outdated" || entry.stale
                ? "baum's review covers an earlier commit."
                : `baum's review is ready${riskText(entry) ? `, risk ${riskText(entry)}` : ""}.`;
      const agents =
        working > 0
          ? ` ${plural(working, "agent session")} ${working === 1 ? "is" : "are"} still working on its branch, so reviewing now catches problems before more commits pile up.`
          : "";
      return `${review}${agents}`;
    }
    case "fix": {
      const unresolved = entry.unresolved ?? 0;
      const problems = [
        entry.review === "changes_requested" ? "changes were requested" : "",
        unresolved > 0
          ? `${plural(unresolved, "review thread")}${entry.unresolved_capped ? " or more" : ""} ${unresolved === 1 ? "is" : "are"} unresolved`
          : "",
        entry.merge === "conflict" ? "it conflicts with its base" : "",
        entry.checks === "failing" ? "checks are failing" : "",
      ].filter(Boolean);
      const sentence = joinAnd(problems);
      return `${sentence.charAt(0).toUpperCase()}${sentence.slice(1)}.${
        fixLabel(entry) ? " An agent can fix it while you do something else." : ""
      }`;
    }
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

/** The pull requests waiting on the person, in the order to take them, and
 *  their own that wait on someone or something else. Each pull request is
 *  listed once, whichever clones hold it. */
export function upNext(
  entries: Entry[],
  live: LiveSession[] = [],
  jobs: Job[] = [],
): { items: UpNextItem[]; waiting: Entry[]; fixing: { entry: Entry; job: Job }[] } {
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
  const fixing: { entry: Entry; job: Job }[] = [];
  for (const [key, entry] of best) {
    const kind = kindOf(entry, jobs);
    if (!kind) {
      if (isOpen(entry) && entry.pull_request.authored_by_me) {
        waiting.push(entry);
        const job = fixJob(entry, jobs);
        if (job && isActiveJob(job)) fixing.push({ entry, job });
      }
      continue;
    }
    const last = kind === "fix" ? fixJob(entry, jobs) : undefined;
    const working = liveFor(live, entry.pull_request).filter((s) => s.state === "running").length;
    items.push({
      key,
      kind,
      entry,
      why: why(kind, entry, working),
      minutes: kind === "review" ? (entry.review_minutes ?? null) : null,
      working,
      ...(last?.state === "failed" ? { failedFix: last } : {}),
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
  return { items, waiting, fixing };
}

type Seen = {
  kind: UpNextKind;
  title: string;
  repository: string;
  number: number;
  /** Its reading estimate, summed into the work done once it is cleared. */
  minutes?: number | null;
  /** When it left the queue; absent while it is in it. */
  cleared?: number;
};
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
const STORAGE_KEY = "baum.inbox.v1";

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

function load(now: number): Stored {
  try {
    const stored = JSON.parse(
      localStorage.getItem(STORAGE_KEY) ?? "null",
    ) as Partial<Stored> | null;
    const record = (value: unknown) =>
      value && typeof value === "object" && !Array.isArray(value) ? value : {};
    if (stored && typeof stored === "object") {
      // Rebuilt field by field: whatever else the storage holds, the page works.
      const snoozed = stillSnoozed(record(stored.snoozed) as Record<string, number>, now);
      const held = Object.fromEntries(
        Object.entries(record(stored.held)).filter(([, head]) => typeof head === "string"),
      ) as Record<string, string>;
      return stored.day === dayOf(now)
        ? {
            day: stored.day,
            seen: record(stored.seen) as Record<string, Seen>,
            snoozed,
            held,
          }
        : fresh(dayOf(now), snoozed, held);
    }
  } catch {
    // Storage may be unavailable or hold something else; start empty.
  }
  return fresh(dayOf(now));
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
export function useUpNext(entries: Entry[], live: LiveSession[], jobs: Job[], ready: boolean) {
  const now = useNow(30_000);
  const [stored, setStored] = useState(() => load(Date.now()));
  const day = dayOf(now);
  const state =
    stored.day === day ? stored : fresh(day, stillSnoozed(stored.snoozed, now), stored.held);
  const { items, waiting, fixing } = upNext(entries, live, jobs);
  const snoozedNow = (item: UpNextItem) =>
    (state.snoozed[item.key] ?? 0) > now ||
    state.held[item.key] === item.entry.pull_request.head_sha;
  const snoozed = items.filter(snoozedNow);
  const queue = items.filter((item) => !snoozedNow(item));
  const update = (change: (state: Stored) => Stored) =>
    setStored((previous) => {
      const time = Date.now();
      const base =
        previous.day === dayOf(time)
          ? previous
          : fresh(dayOf(time), stillSnoozed(previous.snoozed, time), previous.held);
      const next = change(base);
      try {
        localStorage.setItem(STORAGE_KEY, JSON.stringify(next));
      } catch {
        // Not remembered across reloads, but the page still works.
      }
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
      (item) =>
        `${item.key}:${item.kind}:${item.minutes}:${item.entry.pull_request.head_sha}:${item.entry.pull_request.title}`,
    ),
    ...entries
      .filter((entry) => waitingKeys.has(upNextKey(entry.pull_request)))
      .map((entry) => {
        const job = fixJob(entry, jobs);
        return `waiting:${upNextKey(entry.pull_request)}:${job?.id ?? ""}:${job?.state ?? ""}`;
      }),
    ...[...listed].map((repository) => `listed:${repository}`),
  ].join("\n");
  useEffect(() => {
    if (!ready) return;
    const keys = new Set(items.map((item) => item.key));
    const heads = new Map(items.map((item) => [item.key, item.entry.pull_request.head_sha]));
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
    // queue on another head, or gone from it.
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
          seen.minutes !== item.minutes
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
          minutes: item.minutes,
        };
      }
      const held = Object.fromEntries(
        Object.entries(previous.held).filter(([key, head]) => !pushed(key, head)),
      );
      return { ...previous, seen, held };
    });
    // `signature` stands for `items`.
  }, [ready, signature, day]);
  const cleared = Object.entries(state.seen)
    .filter(([, seen]) => seen.cleared != null)
    .sort(([, a], [, b]) => (b.cleared ?? 0) - (a.cleared ?? 0))
    .map(([key, seen]) => ({ key, ...seen }));
  return {
    now,
    queue,
    snoozed,
    waiting,
    fixing,
    cleared,
    snoozedUntil: (key: string): SnoozeEnd =>
      key in state.held ? "change" : (state.snoozed[key] ?? 0),
    snooze: (item: UpNextItem, until: SnoozeEnd) =>
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

function prHref(entry: Entry, cloneFor: (entry: Entry) => string) {
  const pr = entry.pull_request;
  const clone = cloneFor(entry);
  return appURL(
    `?${clone ? `clone=${encodeURIComponent(clone)}` : `repository=${encodeURIComponent(pr.repository)}`}&pr=${pr.number}`,
  );
}

function minutesText(minutes: number) {
  return minutes < 1 ? "<1 min" : `~${formatCount(Math.round(minutes))} min`;
}

const sumMinutes = (items: { minutes?: number | null }[]) =>
  items.reduce((sum, item) => sum + (typeof item.minutes === "number" ? item.minutes : 0), 0);

/** One row of the list: a pull request in the queue, or one of the person's
 *  own that an agent is fixing now. */
type Row = { key: string; entry: Entry; item?: UpNextItem; job?: Job };

/** The saved review of the selected pull request, once read; read again only
 *  when a new version is saved. */
function useReviewDocument(entry: Entry | undefined, clone: string) {
  const [docs, setDocs] = useState<Record<string, ReviewDocument | "failed">>({});
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
  const doc = id ? docs[id] : undefined;
  return doc && doc !== "failed" ? doc : null;
}

/** The inbox: what waits on the person, grouped by what they do about it, and
 *  the selected pull request's panel beside it. */
export function UpNextPage({
  inbox,
  loading,
  hasRepositories,
  cloneFor,
  fix,
  changed,
  Link,
}: {
  inbox: UpNextState;
  loading: boolean;
  hasRepositories: boolean;
  cloneFor: (entry: Entry) => string;
  /** Opens the fix confirmation. */
  fix: (entry: Entry) => void;
  /** Reloads the inbox after a write to GitHub. */
  changed: () => Promise<void>;
  Link: ComponentType<ViewerLinkProps>;
}) {
  const [merging, setMerging] = useState<Entry | null>(null);
  const [mergingAll, setMergingAll] = useState<Entry[] | null>(null);
  const [updating, setUpdating] = useState<Entry | null>(null);
  const [chosen, setChosen] = useState<string | null>(null);
  const [menu, setMenu] = useState(false);
  const [snoozedOpen, setSnoozedOpen] = useState(false);
  const [doneOpen, setDoneOpen] = useState(false);
  const [toast, setToast] = useState<{ id: number; text: string; undo?: () => void } | null>(null);
  const list = useRef<HTMLDivElement>(null);
  const detail = useRef<HTMLDivElement>(null);
  const { queue, cleared, snoozed, fixing, waiting, now } = inbox;
  const groups = GROUPS.map((group) => ({
    ...group,
    rows: [
      ...queue
        .filter((item) => group.kinds.includes(item.kind))
        .map((item): Row => ({ key: item.key, entry: item.entry, item })),
      ...(group.id === "agent"
        ? fixing.map(({ entry, job }): Row => ({ key: upNextKey(entry.pull_request), entry, job }))
        : []),
    ],
  })).filter((group) => group.rows.length > 0);
  const rows = groups.flatMap((group) => group.rows);
  // A selected pull request that leaves the list hands the selection to the
  // one that takes its place.
  const last = useRef(0);
  const found = chosen ? rows.findIndex((row) => row.key === chosen) : -1;
  const index = found >= 0 ? found : Math.min(last.current, rows.length - 1);
  const selected = index >= 0 ? rows[index] : undefined;
  useEffect(() => {
    if (index >= 0) last.current = index;
  });
  const select = (key: string) => {
    setChosen(key);
    setMenu(false);
  };
  useEffect(() => {
    if (!selected) return;
    Array.from(list.current?.querySelectorAll<HTMLElement>("[data-row]") ?? [])
      .find((element) => element.dataset.row === selected.key)
      ?.scrollIntoView?.({ block: "nearest" });
  }, [selected?.key]);
  useEffect(() => {
    if (!toast) return;
    const timer = setTimeout(() => setToast((t) => (t?.id === toast.id ? null : t)), 5000);
    return () => clearTimeout(timer);
  }, [toast?.id]);
  const doc = useReviewDocument(selected?.entry, selected ? cloneFor(selected.entry) : "");
  const options = snoozeOptions(now);
  const snooze = (until: SnoozeEnd) => {
    const item = selected?.item;
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
    } else if (key === "Enter") {
      // Enter on a link or button already does what it says.
      if (
        !onBody(event) &&
        event.target instanceof HTMLElement &&
        event.target.closest("a, button")
      )
        return;
      const primary = detail.current?.querySelector<HTMLElement>(".upnext-primary");
      if (!primary) return;
      event.preventDefault();
      primary.click();
    } else if (key === "s") {
      if (!selected?.item) return;
      event.preventDefault();
      setMenu((open) => !open);
    } else if (key === "m") {
      if (selected?.item?.kind !== "merge") return;
      event.preventDefault();
      setMenu(false);
      setMerging(selected.entry);
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
    : rows.length === 0
      ? cleared.length
        ? `Nothing left. ${formatCount(cleared.length)} handled today.`
        : "Nothing waits on you."
      : [
          yours ? `${plural(yours, "thing")} for you` : "",
          minutes ? `about ${plural(minutes, "minute")}` : "",
          fixing.length ? `${formatCount(fixing.length)} with agents` : "",
          cleared.length ? `${formatCount(cleared.length)} done` : "",
        ]
          .filter(Boolean)
          .join(" · ");
  const total = cleared.length + queue.length;
  const others = waiting.length - fixing.length;
  return (
    <>
      <div className="upnext-page">
        <div className="upnext-list">
          <header className="upnext-list-head">
            <h1>Up next</h1>
            <p>{summary}</p>
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
            {groups.map((group) => {
              const ready = group.id === "merge" ? group.rows.map((row) => row.entry) : [];
              return (
                <section key={group.id} aria-label={group.title} className="upnext-group">
                  <div className="upnext-group-head">
                    <h2>
                      {group.title} · {formatCount(group.rows.length)}
                    </h2>
                    {ready.length > 1 ? (
                      <button
                        type="button"
                        className="upnext-batch"
                        onClick={() => setMergingAll(ready)}
                      >
                        Merge all {formatCount(ready.length)}
                      </button>
                    ) : null}
                  </div>
                  <ul>
                    {group.rows.map((row) => (
                      <li key={row.key} data-row={row.key}>
                        <ListRow row={row} on={row.key === selected?.key} pick={select} />
                      </li>
                    ))}
                  </ul>
                </section>
              );
            })}
            {snoozed.length > 0 ? (
              <section aria-label="Snoozed" className="upnext-group">
                <div className="upnext-group-head">
                  <h2>
                    <button
                      type="button"
                      className="upnext-toggle"
                      aria-expanded={snoozedOpen}
                      onClick={() => setSnoozedOpen((open) => !open)}
                    >
                      {snoozedOpen ? "▾" : "▸"} Snoozed · {formatCount(snoozed.length)}
                    </button>
                  </h2>
                </div>
                <ul className="upnext-snoozed" hidden={!snoozedOpen}>
                  {snoozed.map((item) => (
                    <li key={item.key}>
                      <span className="upnext-snoozed-title">
                        #{item.entry.pull_request.number} {item.entry.pull_request.title}
                      </span>
                      <span className="upnext-snoozed-until">
                        {snoozeText(inbox.snoozedUntil(item.key), now)}
                      </span>
                      <button
                        type="button"
                        className="upnext-small"
                        onClick={() => {
                          inbox.wake(item.key);
                          select(item.key);
                        }}
                      >
                        Bring back
                      </button>
                    </li>
                  ))}
                </ul>
              </section>
            ) : null}
            {cleared.length > 0 ? (
              <section aria-label="Done today" className="upnext-group">
                <div className="upnext-group-head">
                  <h2>
                    <button
                      type="button"
                      className="upnext-toggle"
                      aria-expanded={doneOpen}
                      onClick={() => setDoneOpen((open) => !open)}
                    >
                      {doneOpen ? "▾" : "▸"} Done today · {formatCount(cleared.length)}
                    </button>
                  </h2>
                </div>
                {doneOpen ? (
                  <ul className="upnext-done">
                    {cleared.map((item) => (
                      <li key={item.key} title={`${item.repository} #${item.number}`}>
                        <span aria-hidden="true" className="upnext-done-check">
                          ✓
                        </span>
                        <span className="upnext-done-text">
                          {KINDS[item.kind].done} #{item.number} {item.title}
                        </span>
                      </li>
                    ))}
                  </ul>
                ) : null}
              </section>
            ) : null}
            {others > 0 ? (
              <p className="upnext-note">
                {plural(others, "pull request")} of yours {others === 1 ? "waits" : "wait"} on
                reviewers or checks.{" "}
                <Link href={appURL("?page=pulls&filter=mine")}>See them in Pull requests</Link>
              </p>
            ) : null}
          </div>
          <footer className="upnext-keys key-hint" aria-hidden="true">
            <span>
              <kbd>j</kbd> <kbd>k</kbd> move
            </span>
            <span>
              <kbd>↵</kbd> act
            </span>
            <span>
              <kbd>m</kbd> merge
            </span>
            <span>
              <kbd>s</kbd> snooze
            </span>
            <span>
              <kbd>u</kbd> undo
            </span>
          </footer>
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
              href={prHref(selected.entry, cloneFor)}
              Link={Link}
              fix={
                fixLabel(selected.entry) && cloneFor(selected.entry)
                  ? () => fix(selected.entry)
                  : undefined
              }
              merge={() => setMerging(selected.entry)}
              update={() => setUpdating(selected.entry)}
              menu={menu}
              setMenu={setMenu}
              options={options}
              snooze={snooze}
              now={now}
            />
          ) : (
            <Zero inbox={inbox} hasRepositories={hasRepositories} Link={Link} />
          )}
        </div>
        {toast ? (
          <div role="status" className="upnext-toast">
            <span>{toast.text}</span>
            {toast.undo ? (
              <button type="button" onClick={undo}>
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

function ListRow({ row, on, pick }: { row: Row; on: boolean; pick: (key: string) => void }) {
  const pr = row.entry.pull_request;
  const kind = row.item?.kind ?? "fix";
  const sub = row.job
    ? `Agent fixing · ${phaseText(row.job)}`
    : row.item?.failedFix
      ? `${KINDS[kind].label} · last fix failed`
      : [
          KINDS[kind].label,
          pr.repository,
          row.item?.minutes != null ? minutesText(row.item.minutes) : "",
        ]
          .filter(Boolean)
          .join(" · ");
  const hot = (row.entry.risk_score?.score ?? 0) >= 5;
  return (
    <button
      type="button"
      className="upnext-row"
      aria-current={on || undefined}
      onClick={() => pick(row.key)}
    >
      <span className={`upnext-mark${row.job ? " upnext-mark-working" : ""}`}>
        {row.job ? <NineDots size={12} /> : null}
      </span>
      <span className="upnext-row-text">
        <span className="upnext-row-title">{pr.title || "Untitled pull request"}</span>
        <span
          className={`upnext-row-sub${row.job ? " upnext-row-working" : row.item?.failedFix ? " upnext-row-failed" : ""}`}
        >
          {sub}
        </span>
      </span>
      {hot ? (
        <span className="upnext-hot" role="img" aria-label="Critical risk" title="Critical risk" />
      ) : null}
    </button>
  );
}

type PanelRow = {
  badge?: string;
  main: string;
  sub?: string;
  /** Lines added and deleted. */
  lines?: { additions: number; deletions: number };
};
type PanelBlock = { title: string; lead?: string; rows: PanelRow[]; more?: string };

const SECTIONS_SHOWN = 6;

/** What the panel lists under the pull request, by kind: what changed for a
 *  review or a merge, what blocks it for a fix, where it stands for an
 *  update, and what baum will do for a clone. */
function blocks(row: Row, doc: ReviewDocument | null): PanelBlock[] {
  const entry = row.entry;
  const pr = entry.pull_request;
  const unresolved = entry.unresolved ?? 0;
  const changes: PanelBlock[] = doc
    ? (() => {
        const sections = sectionsInReviewOrder(doc.sections).filter(
          (section) => section.derived.state !== "stale",
        );
        return [
          {
            title: "What changed",
            lead: doc.summary.text,
            rows: sections.slice(0, SECTIONS_SHOWN).map((section) => ({
              badge:
                section.derived.review_score != null
                  ? String(section.derived.review_score)
                  : undefined,
              main: section.title,
              lines: section.derived,
            })),
            more:
              sections.length > SECTIONS_SHOWN
                ? `and ${plural(sections.length - SECTIONS_SHOWN, "more section")}`
                : undefined,
          },
        ];
      })()
    : [];
  if (row.job) return changes;
  switch (row.item?.kind) {
    case "review":
    case "merge":
      return changes;
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
                    sub: "Open on GitHub until someone resolves them.",
                  },
                ]
              : []),
            ...(entry.merge === "conflict"
              ? [
                  {
                    main: "Conflicts with its base",
                    sub: "The branch and its base changed the same lines.",
                  },
                ]
              : []),
            ...(entry.checks === "failing" ? [{ main: "Checks failing" }] : []),
          ],
        },
        ...changes,
      ];
    case "update":
      return [
        {
          title: "Where it stands",
          rows: [
            {
              main: "Behind its base",
              sub: "Updating merges the base into the branch on GitHub and reruns its checks.",
            },
            ...(REVIEW_WORDS[entry.review] ? [{ main: REVIEW_WORDS[entry.review]! }] : []),
            ...(CHECK_WORDS[entry.checks] ? [{ main: CHECK_WORDS[entry.checks]! }] : []),
          ],
        },
        ...changes,
      ];
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
      ];
    default:
      return changes;
  }
}

function Panel({
  row,
  doc,
  href,
  Link,
  fix,
  merge,
  update,
  menu,
  setMenu,
  options,
  snooze,
  now,
}: {
  row: Row;
  doc: ReviewDocument | null;
  href: string;
  Link: ComponentType<ViewerLinkProps>;
  fix?: () => void;
  merge: () => void;
  update: () => void;
  menu: boolean;
  setMenu: (open: boolean | ((open: boolean) => boolean)) => void;
  options: { label: string; until: SnoozeEnd }[];
  snooze: (until: SnoozeEnd) => void;
  now: number;
}) {
  const { entry, item, job } = row;
  const pr = entry.pull_request;
  const kind = item?.kind ?? "fix";
  const menuRef = useRef<HTMLDivElement>(null);
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
    : "Yours";
  const github = (
    <a href={pr.url} target="_blank" rel="noreferrer" className="upnext-secondary">
      Open on GitHub
    </a>
  );
  // Opening the pull request is always the default; what the kind asks for
  // follows it, and a write to GitHub opens its confirmation.
  const actions = (
    <>
      <Link href={href} className="upnext-primary">
        Open
        <kbd aria-hidden="true" className="key-hint">
          ↵
        </kbd>
      </Link>
      {job ? (
        <Link href={jobHref(job)} className="upnext-secondary">
          Watch the agent
        </Link>
      ) : kind === "merge" ? (
        <button type="button" className="upnext-secondary" onClick={merge}>
          Merge
          <kbd aria-hidden="true" className="key-hint">
            m
          </kbd>
        </button>
      ) : kind === "update" ? (
        <button type="button" className="upnext-secondary" onClick={update}>
          Update the branch
        </button>
      ) : kind === "fix" && fix ? (
        <button type="button" className="upnext-secondary" onClick={fix}>
          {fixLabel(entry)}
        </button>
      ) : (
        github
      )}
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
        <span className={`upnext-chip upnext-chip-${kind}`}>{KINDS[kind].label}</span>
        <span className="upnext-panel-where">
          {pr.repository} · #{pr.number} · {who}
          {pr.draft ? " · Draft" : ""}
          {pr.fork ? " · Fork" : ""}
        </span>
        {item?.minutes != null ? (
          <span className="upnext-minutes" title="baum's estimate of the time to read the change">
            {minutesText(item.minutes)}
          </span>
        ) : null}
      </div>
      <h2 id="upnext-title" className="upnext-title">
        <Link href={href}>{pr.title || "Untitled pull request"}</Link>
      </h2>
      {item?.why ? <p className="upnext-why">{item.why}</p> : null}
      {job ? (
        <div className="upnext-working" role="status">
          <NineDots size={14} />
          <span>An agent is fixing it · {phaseText(job)}.</span>
          <span className="upnext-working-aside">You can move on</span>
        </div>
      ) : null}
      {item?.failedFix ? (
        <div className="upnext-failed" role="status">
          The last fix failed
          {item.failedFix.reason ? `: ${jobReasonText(item.failedFix.reason)}` : "."}{" "}
          <Link href={jobHref(item.failedFix)}>See what the agent did</Link>
        </div>
      ) : null}
      <div className="upnext-actions">
        {actions}
        {item ? (
          <div className="upnext-snooze" ref={menuRef}>
            <button
              type="button"
              className="upnext-secondary"
              aria-haspopup="menu"
              aria-expanded={menu}
              onClick={() => setMenu((open) => !open)}
            >
              Snooze ▾
              <kbd aria-hidden="true" className="key-hint">
                s
              </kbd>
            </button>
            {menu ? (
              <div role="menu" className="upnext-menu">
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
        ) : null}
      </div>
      {score != null ? (
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
              {riskText(entry)} · {RISK_SCORE_MEANING[score as RiskScoreLevel]}
            </span>
          </div>
          {reason ? (
            <p>
              {reasonTitle ? <strong>{reasonTitle}. </strong> : null}
              {reason.text}
            </p>
          ) : null}
        </div>
      ) : null}
      {blocks(row, doc).map((block) => (
        <section key={block.title} className="upnext-block" aria-label={block.title}>
          <h3 className="upnext-label">{block.title}</h3>
          {block.lead ? <p className="upnext-lead">{block.lead}</p> : null}
          <ul>
            {block.rows.map((line) => (
              <li key={line.main}>
                {line.badge ? (
                  <span
                    className={`upnext-badge upnext-badge-${line.badge === "✓" ? "done" : line.badge}`}
                  >
                    {line.badge}
                  </span>
                ) : null}
                <span className="upnext-block-text">
                  <span>{line.main}</span>
                  {line.sub ? <span className="upnext-block-sub">{line.sub}</span> : null}
                </span>
                {line.lines ? (
                  <Lines
                    additions={line.lines.additions}
                    deletions={line.lines.deletions}
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
      {item && item.working > 0 ? (
        <p className="upnext-agents">
          <span className="upnext-pulse" aria-hidden="true" />
          {plural(item.working, "agent session")} working on its branch right now
        </p>
      ) : null}
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
  if (!hasRepositories)
    return (
      <div className="upnext-zero">
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
      <h2>Inbox zero.</h2>
      <p>
        {snoozed.length > 0
          ? `Everything is handled except ${plural(snoozed.length, "snoozed pull request")}; they come back on their own.`
          : cleared.length > 0
            ? `All ${formatCount(cleared.length)} handled. Nothing needs you right now.`
            : "No review is asked of you and none of your pull requests needs you."}
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
function MergeReadyDialog({
  entries,
  close,
  merged,
}: {
  entries: Entry[];
  close: () => void;
  merged: () => Promise<void>;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
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
    dialog.current?.showModal();
    return () => {
      gone.current = true;
    };
  }, []);
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
      } catch (e) {
        set(index, {
          state: "failed",
          error: e instanceof Error ? e.message : "Local request failed.",
        });
      }
    }
    setRunning(false);
    setFinished(true);
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
      <h2 id="merge-ready-title" className="text-lg font-semibold">
        Merge {formatCount(entries.length)} ready pull requests?
      </h2>
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
                  <span className="text-(--status-positive)">Merged</span>
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
      <div className="mt-4 flex justify-end gap-2">
        <Button variant="ghost" size="compact" disabled={running} onClick={close}>
          {finished ? "Close" : "Cancel"}
        </Button>
        {finished ? null : (
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
        )}
      </div>
    </dialog>
  );
}
