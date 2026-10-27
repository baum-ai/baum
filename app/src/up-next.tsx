import { useEffect, useId, useLayoutEffect, useRef, useState, type ComponentType } from "react";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import { formatCount } from "@baum/viewer/components/format";
import { CheckIcon, LightbulbIcon, MergeIcon, RefreshIcon } from "@baum/viewer/components/icons";
import { actionClassName } from "@baum/viewer/components/actions";
import { Alert, Badge, Button, type BadgeTone } from "@baum/viewer/components/ui";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import {
  appURL,
  fixJob,
  fixLabel,
  isActiveJob,
  joinAnd,
  liveFor,
  request,
  type Entry,
  type Job,
  type LiveSession,
} from "./api";
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
import { PageBar } from "./shell";
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
  /** Why it is the person's to do now, in a sentence built from its state. */
  why: string;
  facts: { label: string; value: string }[];
  /** The reading estimate of a review; null when there is none. */
  minutes: number | null;
};

// The order of the queue: someone else waits on a review, the person's own
// blocked work next, then what finishes a pull request, then the setup a
// requested review needs.
const KINDS: Record<UpNextKind, { label: string; tone: BadgeTone; done: string; order: number }> = {
  review: { label: "Review", tone: "caution", done: "Reviewed", order: 0 },
  fix: { label: "Fix", tone: "critical", done: "Fixed", order: 1 },
  merge: { label: "Merge", tone: "positive", done: "Merged", order: 2 },
  update: { label: "Update", tone: "info", done: "Updated", order: 3 },
  // It leaves the queue only when the review request goes, whoever answered it.
  clone: { label: "Clone", tone: "neutral", done: "Handled", order: 4 },
};

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
  passing: "Passing",
  failing: "Failing",
  running: "Running",
  none: "None",
};

function agentsText(sessions: LiveSession[]) {
  const working = sessions.filter((s) => s.state === "running").length;
  const idle = sessions.length - working;
  return joinAnd([working ? `${working} working` : "", idle ? `${idle} idle` : ""].filter(Boolean));
}

function baumReview(entry: Entry) {
  if (entry.status === "generating") return "Generating";
  if (entry.status === "failed") return "Failed";
  if (entry.version_count === 0) return "None yet";
  return entry.status === "outdated" || entry.stale ? "Outdated" : "Ready";
}

function why(kind: UpNextKind, entry: Entry, sessions: LiveSession[]): string {
  const pr = entry.pull_request;
  const working = sessions.filter((s) => s.state === "running").length;
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
      return `${pr.author ? `${pr.author} asked` : "Asked"} for your review. ${review}${agents}`;
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
      return `Yours. ${sentence.charAt(0).toUpperCase()}${sentence.slice(1)}.${
        fixLabel(entry) ? " An agent can fix it while you do something else." : ""
      }`;
    }
    case "merge":
      return entry.checks === "passing"
        ? "Yours. Checks pass and GitHub would merge it now."
        : "Yours. GitHub would merge it now.";
    case "update":
      // Only an approved branch with passing checks is held back by being
      // behind alone; otherwise updating is one step of several.
      return entry.review === "approved" && entry.checks === "passing"
        ? "Yours and approved, but behind its base. Update the branch and it can merge."
        : "Yours, but behind its base. Update the branch to bring it up to date.";
    case "clone":
      return `${pr.author ? `${pr.author} asked` : "Asked"} for your review, but baum has no local clone of ${pr.repository}, so there is no review yet.`;
  }
}

function facts(kind: UpNextKind, entry: Entry, sessions: LiveSession[]) {
  const unresolved = entry.unresolved ?? 0;
  const all: Record<string, string> = {
    Risk: riskText(entry),
    "baum review": baumReview(entry),
    Review: REVIEW_WORDS[entry.review] ?? "",
    Checks: CHECK_WORDS[entry.checks] ?? "",
    Unresolved:
      unresolved > 0 ? `${formatCount(unresolved)}${entry.unresolved_capped ? "+" : ""}` : "",
    Merge: entry.merge === "conflict" ? "Conflict" : entry.pull_request.behind ? "Behind base" : "",
    Agents: agentsText(sessions),
  };
  const shown: Record<UpNextKind, string[]> = {
    review: ["Risk", "baum review", "Review", "Agents"],
    fix: ["Review", "Unresolved", "Merge", "Checks"],
    merge: ["Risk", "Checks", "Review"],
    update: ["Review", "Merge", "Checks"],
    clone: ["Review", "Checks"],
  };
  return shown[kind]
    .map((label) => ({ label, value: all[label] ?? "" }))
    .filter((fact) => fact.value);
}

/** The pull requests waiting on the person, in the order to take them, and
 *  their own that wait on someone or something else. Each pull request is
 *  listed once, whichever clones hold it. */
export function upNext(
  entries: Entry[],
  live: LiveSession[] = [],
  jobs: Job[] = [],
): { items: UpNextItem[]; waiting: Entry[] } {
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
  for (const [key, entry] of best) {
    const kind = kindOf(entry, jobs);
    if (!kind) {
      if (isOpen(entry) && entry.pull_request.authored_by_me) waiting.push(entry);
      continue;
    }
    const sessions = liveFor(live, entry.pull_request);
    items.push({
      key,
      kind,
      entry,
      why: why(kind, entry, sessions),
      facts: facts(kind, entry, sessions),
      minutes: kind === "review" ? entry.review_minutes : null,
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
  return { items, waiting };
}

type Seen = {
  kind: UpNextKind;
  title: string;
  repository: string;
  number: number;
  /** When it left the queue; absent while it is in it. */
  cleared?: number;
};
/** What the inbox remembers on this computer: today's pull requests and
 *  which it cleared, the person's skips and promotion, and their snoozes. */
type Stored = {
  day: string;
  seen: Record<string, Seen>;
  skipped: string[];
  promoted: string | null;
  snoozed: Record<string, number>;
};
const STORAGE_KEY = "baum.inbox.v1";

function dayOf(time: number) {
  const date = new Date(time);
  return `${date.getFullYear()}-${date.getMonth() + 1}-${date.getDate()}`;
}

/** A new day starts empty, keeping only the snoozes that still run. */
function fresh(day: string, snoozed: Record<string, number> = {}): Stored {
  return { day, seen: {}, skipped: [], promoted: null, snoozed };
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
      return stored.day === dayOf(now)
        ? {
            day: stored.day,
            seen: record(stored.seen) as Record<string, Seen>,
            skipped: Array.isArray(stored.skipped)
              ? stored.skipped.filter((key) => typeof key === "string")
              : [],
            promoted: typeof stored.promoted === "string" ? stored.promoted : null,
            snoozed,
          }
        : fresh(dayOf(now), snoozed);
    }
  } catch {
    // Storage may be unavailable or hold something else; start empty.
  }
  return fresh(dayOf(now));
}

/** The next whole hour three hours from now, or 9 AM tomorrow once that is
 *  past 6 PM. */
export function snoozeUntil(now: number): number {
  const until = new Date(now + 3 * 3_600_000);
  if (until.getMinutes() || until.getSeconds() || until.getMilliseconds())
    until.setHours(until.getHours() + 1, 0, 0, 0);
  if (dayOf(until.getTime()) !== dayOf(now) || until.getHours() > 18) {
    const morning = new Date(now);
    morning.setDate(morning.getDate() + 1);
    morning.setHours(9, 0, 0, 0);
    return morning.getTime();
  }
  return until.getTime();
}

export function snoozeText(until: number, now: number) {
  const time = new Date(until).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  return dayOf(until) === dayOf(now) ? time : `tomorrow ${time}`;
}

export type UpNextState = ReturnType<typeof useUpNext>;

/** The inbox's queue with the person's skips, promotion and snoozes applied,
 *  and what they cleared today. `ready` holds the bookkeeping back until a
 *  complete read, so an empty first read or a failed one clears nothing.
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
  const state = stored.day === day ? stored : fresh(day, stillSnoozed(stored.snoozed, now));
  const { items, waiting } = upNext(entries, live, jobs);
  const snoozed = items.filter((item) => (state.snoozed[item.key] ?? 0) > now);
  const active = items.filter((item) => !((state.snoozed[item.key] ?? 0) > now));
  const rank = (item: UpNextItem) =>
    item.key === state.promoted
      ? -1
      : state.skipped.includes(item.key)
        ? items.length + state.skipped.indexOf(item.key)
        : items.indexOf(item);
  const queue = active.slice().sort((a, b) => rank(a) - rank(b));
  const update = (change: (state: Stored) => Stored) =>
    setStored((previous) => {
      const time = Date.now();
      const base =
        previous.day === dayOf(time)
          ? previous
          : fresh(dayOf(time), stillSnoozed(previous.snoozed, time));
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
    ...items.map((item) => `${item.key}:${item.kind}:${item.entry.pull_request.title}`),
    ...[...waitingKeys].map((key) => `waiting:${key}`),
    ...[...listed].map((repository) => `listed:${repository}`),
  ].join("\n");
  useEffect(() => {
    if (!ready) return;
    const keys = new Set(items.map((item) => item.key));
    const clears = (key: string, record: Seen) =>
      !keys.has(key) &&
      record.cleared == null &&
      listed.has(record.repository.toLowerCase()) &&
      (!waitingKeys.has(key) || record.kind === "fix" || record.kind === "update");
    const changed =
      stored.day !== day ||
      items.some((item) => {
        const seen = stored.seen[item.key];
        return (
          !seen ||
          seen.cleared != null ||
          seen.kind !== item.kind ||
          seen.title !== item.entry.pull_request.title
        );
      }) ||
      Object.entries(stored.seen).some(([key, record]) => clears(key, record));
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
        };
      }
      return {
        ...previous,
        seen,
        skipped: previous.skipped.filter((key) => keys.has(key)),
        promoted: previous.promoted && keys.has(previous.promoted) ? previous.promoted : null,
      };
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
    cleared,
    snoozedUntil: (key: string) => state.snoozed[key] ?? 0,
    skip: (key: string) =>
      update((s) => ({
        ...s,
        skipped: [...s.skipped.filter((k) => k !== key), key],
        promoted: s.promoted === key ? null : s.promoted,
      })),
    promote: (key: string) =>
      update((s) => ({ ...s, promoted: key, skipped: s.skipped.filter((k) => k !== key) })),
    snooze: (key: string) =>
      update((s) => ({
        ...s,
        snoozed: { ...s.snoozed, [key]: snoozeUntil(Date.now()) },
        promoted: s.promoted === key ? null : s.promoted,
      })),
    wake: () => update((s) => ({ ...s, snoozed: {} })),
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

/** The inbox: the one thing to do next, what follows it, and what the person
 *  cleared today. */
export function UpNextPage({
  inbox,
  loading,
  hasRepositories,
  cloneFor,
  fix,
  changed,
  refresh,
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
  /** Refreshes from GitHub; absent while a read runs. */
  refresh?: () => void;
  Link: ComponentType<ViewerLinkProps>;
}) {
  const [merging, setMerging] = useState<Entry | null>(null);
  const [mergingAll, setMergingAll] = useState<Entry[] | null>(null);
  const [updating, setUpdating] = useState<Entry | null>(null);
  const { queue, cleared } = inbox;
  const current = queue[0];
  const rest = queue.slice(1);
  const ready = queue.filter((item) => item.kind === "merge").map((item) => item.entry);
  const minutes = queue.reduce((sum, item) => sum + (item.minutes ?? 0), 0);
  const detail = loading
    ? undefined
    : queue.length === 0
      ? "Nothing waiting on you"
      : `${plural(queue.length, "thing")} waiting on you${minutes ? `, about ${formatCount(Math.round(minutes))} min of reading` : ""}, sorted by what unblocks the most`;
  return (
    <>
      <PageBar
        title="Inbox"
        detail={detail}
        actions={
          <>
            {ready.length > 1 ? (
              <Button variant="secondary" size="compact" onClick={() => setMergingAll(ready)}>
                <MergeIcon size={14} className="mr-1.5 flex-none" />
                Merge {formatCount(ready.length)} ready
              </Button>
            ) : null}
            <button
              type="button"
              aria-label="Refresh from GitHub"
              title="Refresh from GitHub"
              disabled={!refresh}
              onClick={refresh}
              className="local-icon-button"
            >
              <RefreshIcon size={16} className={refresh ? undefined : "local-spin"} />
            </button>
          </>
        }
      />
      {loading ? (
        <div className="local-page-body">
          <LoadingStatus block>Loading your inbox…</LoadingStatus>
        </div>
      ) : (
        <div className="upnext">
          <div className="upnext-main">
            {current ? (
              <Card
                key={current.key}
                item={current}
                href={prHref(current.entry, cloneFor)}
                Link={Link}
                fix={
                  fixLabel(current.entry) && cloneFor(current.entry)
                    ? () => fix(current.entry)
                    : undefined
                }
                merge={() => setMerging(current.entry)}
                update={() => setUpdating(current.entry)}
                snooze={() => inbox.snooze(current.key)}
                snoozeLabel={`Snooze until ${snoozeText(snoozeUntil(inbox.now), inbox.now)}`}
                skip={rest.length > 0 ? () => inbox.skip(current.key) : undefined}
              />
            ) : (
              <Zero inbox={inbox} hasRepositories={hasRepositories} Link={Link} />
            )}
            {rest.length > 0 ? <Queue items={rest} promote={inbox.promote} /> : null}
            <Elsewhere inbox={inbox} Link={Link} />
          </div>
          <Cleared cleared={cleared} remaining={queue.length} />
        </div>
      )}
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

function KindBadge({ kind, className }: { kind: UpNextKind; className?: string }) {
  return (
    <Badge tone={KINDS[kind].tone} className={cn("font-semibold", className)}>
      {KINDS[kind].label}
    </Badge>
  );
}

function Card({
  item,
  href,
  Link,
  fix,
  merge,
  update,
  snooze,
  snoozeLabel,
  skip,
}: {
  item: UpNextItem;
  href: string;
  Link: ComponentType<ViewerLinkProps>;
  fix?: () => void;
  merge: () => void;
  update: () => void;
  snooze: () => void;
  snoozeLabel: string;
  skip?: () => void;
}) {
  const title = useId();
  const pr = item.entry.pull_request;
  const primary = actionClassName({ variant: "primary", size: "standard" });
  const secondary = actionClassName({ variant: "secondary", size: "standard" });
  const open = (
    <Link href={href} className={secondary}>
      Open pull request
    </Link>
  );
  // The one thing the card asks for; a write to GitHub opens its confirmation.
  const action =
    item.kind === "review" ? (
      <Link href={href} className={primary}>
        Start reviewing
      </Link>
    ) : item.kind === "clone" ? (
      <Link href={href} className={primary}>
        Choose a local clone
      </Link>
    ) : item.kind === "merge" ? (
      <>
        <Button onClick={merge}>Merge</Button>
        {open}
      </>
    ) : item.kind === "update" ? (
      <>
        <Button onClick={update}>Update branch</Button>
        {open}
      </>
    ) : fix ? (
      <>
        <Button onClick={fix}>{fixLabel(item.entry)}</Button>
        {open}
      </>
    ) : (
      <Link href={href} className={primary}>
        Open pull request
      </Link>
    );
  return (
    <article aria-labelledby={title} className="upnext-card">
      <div className="upnext-card-meta">
        <span className="upnext-eyebrow">Up next</span>
        <KindBadge kind={item.kind} />
        <span className="text-xs text-(--text-tertiary)">
          {pr.repository} · #{pr.number}
          {pr.draft ? " · Draft" : ""}
          {pr.fork ? " · Fork" : ""}
        </span>
        {item.minutes != null ? (
          <span className="upnext-minutes" title="baum's estimate of the time to read the change">
            {minutesText(item.minutes)}
          </span>
        ) : null}
      </div>
      <h2 id={title} className="upnext-title text-2xl font-semibold">
        <Link href={href} className="hover:underline">
          {pr.title || "Untitled pull request"}
        </Link>
      </h2>
      <p className="upnext-why">
        <LightbulbIcon size={16} className="mt-0.5 flex-none text-(--text-tertiary)" />
        <span>
          <strong>Why now:</strong> {item.why}
        </span>
      </p>
      {item.facts.length > 0 ? (
        <dl className="upnext-facts">
          {item.facts.map((fact) => (
            <div key={fact.label}>
              <dt>{fact.label}</dt>
              <dd>{fact.value}</dd>
            </div>
          ))}
        </dl>
      ) : null}
      <div className="upnext-actions">
        {action}
        <Button variant="secondary" onClick={snooze}>
          {snoozeLabel}
        </Button>
        {skip ? (
          <Button variant="ghost" onClick={skip}>
            Skip
          </Button>
        ) : null}
      </div>
    </article>
  );
}

function Queue({ items, promote }: { items: UpNextItem[]; promote: (key: string) => void }) {
  const heading = useId();
  const minutes = items.reduce((sum, item) => sum + (item.minutes ?? 0), 0);
  return (
    <section aria-labelledby={heading} className="upnext-queue">
      <div className="upnext-queue-head">
        <h2 id={heading} className="upnext-eyebrow">
          Then
        </h2>
        <span className="text-xs text-(--text-tertiary)">
          {formatCount(items.length)} more
          {minutes ? ` · about ${formatCount(Math.round(minutes))} min of reading` : ""}
        </span>
      </div>
      <ol>
        {items.map((item, index) => {
          const pr = item.entry.pull_request;
          return (
            <li key={item.key}>
              <button
                type="button"
                className={cn("upnext-row", FOCUS_RING)}
                onClick={() => promote(item.key)}
                title="Take this one next"
              >
                <span className="upnext-row-number">{index + 2}</span>
                <KindBadge kind={item.kind} className="upnext-row-kind" />
                <span className="min-w-0 flex-1">
                  <span className="upnext-row-title">{pr.title || "Untitled pull request"}</span>
                  <span className="block text-xs text-(--text-tertiary)">
                    {pr.repository} · #{pr.number}
                  </span>
                </span>
                {item.minutes != null ? (
                  <span className="upnext-minutes">{minutesText(item.minutes)}</span>
                ) : null}
                <span className="sr-only">: take this one next</span>
              </button>
            </li>
          );
        })}
      </ol>
    </section>
  );
}

/** What is not in the queue: snoozed pull requests and the person's own that
 *  wait on someone or something else. */
function Elsewhere({ inbox, Link }: { inbox: UpNextState; Link: ComponentType<ViewerLinkProps> }) {
  const { snoozed, waiting, now } = inbox;
  if (snoozed.length === 0 && waiting.length === 0) return null;
  const latest = Math.max(...snoozed.map((item) => inbox.snoozedUntil(item.key)));
  return (
    <div className="upnext-elsewhere">
      {snoozed.length > 0 ? (
        <p>
          {plural(snoozed.length, "pull request")} snoozed until {snoozeText(latest, now)}:{" "}
          {snoozed.map((item) => `#${item.entry.pull_request.number}`).join(", ")}.{" "}
          <button type="button" className="upnext-link" onClick={inbox.wake}>
            Bring them back
          </button>
        </p>
      ) : null}
      {waiting.length > 0 ? (
        <p>
          {plural(waiting.length, "pull request")} of yours{" "}
          {waiting.length === 1 ? "waits" : "wait"} on reviewers, checks or an agent.{" "}
          <Link href={appURL("?page=pulls&filter=mine")}>See them in Pull requests</Link>
        </p>
      ) : null}
    </div>
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
        <h2 className="text-2xl font-semibold">Nothing to do yet</h2>
        <p>
          Pick GitHub repositories in <Link href={appURL("?page=repositories")}>Repositories</Link>{" "}
          to get started.
        </p>
      </div>
    );
  const { snoozed, cleared } = inbox;
  return (
    <div className="upnext-zero">
      <h2 className="text-2xl font-semibold">Inbox zero.</h2>
      <p>
        {snoozed.length > 0
          ? `Everything else is handled; ${plural(snoozed.length, "pull request")} ${snoozed.length === 1 ? "is" : "are"} snoozed.`
          : cleared.length > 0
            ? `All ${formatCount(cleared.length)} handled today.`
            : "No review is asked of you and none of your pull requests needs you."}{" "}
        Everyone else's are in <Link href={appURL("?page=pulls")}>Pull requests</Link>.
      </p>
    </div>
  );
}

const RING = 2 * Math.PI * 42;

function Cleared({ cleared, remaining }: { cleared: UpNextState["cleared"]; remaining: number }) {
  const total = cleared.length + remaining;
  const share = total ? cleared.length / total : 0;
  const label = `${formatCount(cleared.length)} of ${formatCount(total)} cleared today`;
  return (
    <aside aria-label="Cleared today" className="upnext-aside">
      <div className="flex items-center gap-4">
        <div className="upnext-ring" role="img" aria-label={label}>
          <svg viewBox="0 0 100 100" aria-hidden="true">
            <circle cx="50" cy="50" r="42" className="upnext-ring-track" />
            {/* A round cap on an empty arc would still draw a dot. */}
            {share > 0 ? (
              <circle
                cx="50"
                cy="50"
                r="42"
                className="upnext-ring-fill"
                strokeDasharray={`${RING * share} ${RING}`}
                transform="rotate(-90 50 50)"
              />
            ) : null}
          </svg>
          <span aria-hidden="true" className="text-lg font-semibold tabular-nums">
            {formatCount(cleared.length)}/{formatCount(total)}
          </span>
        </div>
        <div className="grid gap-1">
          <span className="text-base font-semibold">Cleared today</span>
          <span className="text-xs text-(--text-secondary)">
            {cleared.length
              ? `${formatCount(remaining)} to go.`
              : "Clear the card on the left to start."}
          </span>
        </div>
      </div>
      {cleared.length > 0 ? (
        <ul className="upnext-cleared">
          {cleared.map((item) => (
            <li key={item.key} title={`${item.repository} #${item.number}`}>
              <span className="upnext-check">
                <CheckIcon size={12} />
              </span>
              <span className="min-w-0 truncate">
                {KINDS[item.kind].done} #{item.number} {item.title}
              </span>
            </li>
          ))}
        </ul>
      ) : (
        <p className="upnext-cleared-empty">Nothing cleared yet. The ring fills as you go.</p>
      )}
    </aside>
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
