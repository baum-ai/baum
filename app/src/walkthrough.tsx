import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { cn } from "@baum/viewer/components/classnames";
import {
  HunkEvidence,
  HunkPatchProvider,
  type HunkPatchSource,
} from "@baum/viewer/components/hunk-evidence";
import {
  ChevronRightIcon,
  CommentsIcon,
  ExternalLinkIcon,
  SearchIcon,
} from "@baum/viewer/components/icons";
import { Alert, Button, SegmentedControl } from "@baum/viewer/components/ui";
import type { ReviewDocument, RiskScoreLevel } from "@baum/viewer/review-document";
import { LocalRequestError, request, type ChatFocus, type Entry } from "./api";
import { stopFocus } from "./chat";
import { useShortcuts } from "./shortcuts";
import {
  buildTour,
  commentedStops,
  loadProgress,
  progressKey,
  reviewSubmission,
  saveProgress,
  type Progress,
  type ReviewEvent,
  type Stop,
  type Tour,
} from "./walkthrough-model";

export type WalkView = "tour" | "all";

const LABEL = "text-xs font-semibold tracking-[0.08em] text-(--text-tertiary) uppercase";

/** Whether `after` only repeats the title, as it does for a step written
 *  before steps had titles, whose title is its first sentence. */
const echoes = (stop: Stop) => !stop.before && stop.after.replace(/[.!?]\s*$/, "") === stop.title;

/** The review as a guided tour of its behaviours, riskiest chapter first, or
 *  as one list of all of them. Nobody has to sign off on every behaviour:
 *  moving past a stop or opening its details counts as having looked at it,
 *  and a comment is the only way to say something looks off. The comments go
 *  to GitHub as one review, each on the line its behaviour cites. */
export function Walkthrough({
  entry,
  document,
  view,
  setView,
  close,
  patches,
  changed,
  ask,
}: {
  entry: Entry;
  document: ReviewDocument;
  view: WalkView;
  setView: (view: WalkView) => void;
  /** Back to the full review. */
  close: () => void;
  patches: HunkPatchSource;
  changed: () => Promise<void>;
  /** The Ask panel: whether it is open, toggling it, and asking about a part
   *  of the review. Absent, the walkthrough offers no questions. */
  ask?: { open: boolean; toggle: () => void; about: (focus: ChatFocus) => void };
}) {
  const tour = useMemo(() => buildTour(document), [document]);
  const key = progressKey(document);
  const [progress, setProgress] = useState<Progress>(() => loadProgress(key));
  useEffect(() => {
    saveProgress(key, progress);
  }, [key, progress]);
  const seen = useMemo(() => new Set(progress.seen), [progress.seen]);
  const commented = (stop: Stop) => Boolean(progress.notes[stop.id]?.trim());
  const done = (stop: Stop) => seen.has(stop.id) || commented(stop);
  const markSeen = (...stops: Stop[]) =>
    setProgress((current) => {
      const ids = stops.map((stop) => stop.id).filter((id) => !current.seen.includes(id));
      return ids.length ? { ...current, seen: [...current.seen, ...ids] } : current;
    });
  const setNote = (stop: Stop, note: string) =>
    setProgress((current) => ({ ...current, notes: { ...current.notes, [stop.id]: note } }));

  const last = tour.stops.length + 1;
  // 0 is the introduction, 1… the stops, `last` the wrap-up.
  const [step, setStep] = useState(0);
  // The stop the list's focus is on, and the tour's last stop.
  const [cur, setCur] = useState(0);
  const [skipped, setSkipped] = useState(0);
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [open, setOpen] = useState<Set<string>>(new Set());
  const [filter, setFilter] = useState<"all" | "unseen" | "commented">("all");

  const go = (next: number, skip = 0) => {
    next = Math.max(0, Math.min(last, next));
    const leaving = tour.stops[step - 1];
    if (leaving && next !== step) markSeen(leaving);
    if (next >= 1 && next < last) setCur(next - 1);
    setStep(next);
    setSkipped(skip);
    setEditing(null);
  };
  // Next jumps over stops already looked at; Back and the timeline still
  // visit them.
  const forward = () => {
    if (step >= last) return;
    const from = step;
    const next = tour.stops.findIndex((stop, i) => i >= from && i !== step - 1 && !done(stop));
    go(next === -1 ? last : next + 1, Math.max(0, (next === -1 ? tour.stops.length : next) - from));
  };
  const toTour = (at?: number) => {
    if (at !== undefined) {
      setStep(at + 1);
      setCur(at);
      setSkipped(0);
    } else {
      // Back into the tour lands on the first stop not looked at from where
      // the reader was, wrapping to the start.
      const after = tour.stops.findIndex((stop, i) => i >= cur && !done(stop));
      const next = after === -1 ? tour.stops.findIndex((stop) => !done(stop)) : after;
      setStep(next === -1 ? last : next + 1);
      if (next !== -1) setCur(next);
      setSkipped(tour.stops.filter(done).length);
    }
    setView("tour");
  };
  const toAll = () => {
    const stop = tour.stops[step - 1];
    if (view === "tour" && stop) {
      setOpen((current) => new Set(current).add(stop.id));
      markSeen(stop);
    }
    setView("all");
  };
  const toggleDetails = (index: number) => {
    const stop = tour.stops[index]!;
    setCur(index);
    markSeen(stop);
    setOpen((current) => {
      const next = new Set(current);
      if (next.has(stop.id)) next.delete(stop.id);
      else next.add(stop.id);
      return next;
    });
  };
  const startComment = (stop: Stop) => {
    setDraft(progress.notes[stop.id] ?? "");
    setEditing(stop.id);
  };

  // Not looked at keeps the row the reader is on, which opening its details
  // or its comment has just marked looked at.
  const visible = tour.stops.flatMap((stop, index) =>
    filter === "all" ||
    (filter === "unseen" && (!done(stop) || index === cur)) ||
    (filter === "commented" && commented(stop))
      ? [index]
      : [],
  );
  const claim = step >= 1 && step < last ? tour.stops[step - 1]! : null;
  useShortcuts((event) => {
    const key = event.key;
    if (key === "e") {
      event.preventDefault();
      if (view === "tour") toAll();
      else toTour();
    } else if (view === "tour") {
      if (key === "ArrowRight" || key === "j") {
        event.preventDefault();
        forward();
      } else if (key === "ArrowLeft" || key === "k") {
        event.preventDefault();
        go(step - 1);
      } else if (key === "c" && claim) {
        event.preventDefault();
        startComment(claim);
      }
    } else {
      const position = visible.indexOf(cur);
      if (key === "j" || key === "ArrowDown") {
        event.preventDefault();
        setCur(visible[Math.min(visible.length - 1, position + 1)] ?? cur);
      } else if (key === "k" || key === "ArrowUp") {
        event.preventDefault();
        setCur(visible[Math.max(0, position - 1)] ?? cur);
      } else if (key === "o" || key === "Enter") {
        event.preventDefault();
        toggleDetails(cur);
      } else if (key === "c") {
        event.preventDefault();
        const stop = tour.stops[cur]!;
        setOpen((current) => new Set(current).add(stop.id));
        markSeen(stop);
        requestAnimationFrame(() =>
          window.document.getElementById(`walk-note-${stop.id}`)?.focus(),
        );
      } else if (key === "t") {
        event.preventDefault();
        toTour(cur);
      }
    }
  });

  const total = tour.stops.length;
  const seenCount = tour.stops.filter(done).length;
  const commentCount = tour.stops.filter(commented).length;
  const unseen = tour.stops.filter((stop) => !done(stop));
  const left = unseen.length
    ? tour.minutes
      ? `≈ ${Math.max(1, Math.round((tour.minutes * unseen.length) / total))} min left`
      : `${unseen.length} left`
    : "all looked at";

  if (total === 0)
    return (
      <div className="local-review-empty">
        <h2>Nothing to walk through</h2>
        <p>This review has no logical sections with changes to look at.</p>
        <Button size="compact" onClick={close}>
          Back to the full review
        </Button>
      </div>
    );

  return (
    <section aria-label="Walkthrough" className="walkthrough">
      <header className="flex flex-col gap-3 border-b border-(--border-subtle) px-6 pt-4 pb-3">
        <div className="flex flex-wrap items-center justify-between gap-4">
          <div className="flex min-w-0 flex-col gap-1">
            <h1 className="text-lg font-semibold">
              <span className="text-(--text-tertiary)">#{entry.pull_request.number}</span>{" "}
              {entry.pull_request.title}
            </h1>
            <p className="text-xs text-(--text-tertiary)">
              {entry.pull_request.repository} · Review v{document.review.version} ·{" "}
              {tour.chapters.length} {tour.chapters.length === 1 ? "chapter" : "chapters"}, {total}{" "}
              {total === 1 ? "behaviour" : "behaviours"} · {left}
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-3">
            <div className="flex min-w-44 flex-col gap-1">
              <p className="flex gap-3 text-xs tabular-nums">
                <span className="font-semibold">
                  {seenCount} of {total} looked at
                </span>
                <span className="font-semibold text-(--status-caution)">
                  {commentCount
                    ? `${commentCount} ${commentCount === 1 ? "comment" : "comments"}`
                    : "no comments"}
                </span>
              </p>
              <div
                aria-hidden
                className="flex h-1.5 overflow-hidden rounded-full bg-(--surface-sunken)"
              >
                <span
                  className="bg-(--status-positive) transition-[width] duration-300"
                  style={{ width: `${(100 * (seenCount - commentCount)) / total}%` }}
                />
                <span
                  className="bg-(--status-caution) transition-[width] duration-300"
                  style={{ width: `${(100 * commentCount) / total}%` }}
                />
              </div>
            </div>
            <SegmentedControl
              label="View"
              value={view}
              onChange={(next) => (next === "tour" ? toTour() : toAll())}
              items={[
                { value: "tour", label: "Guided tour" },
                { value: "all", label: "Everything" },
              ]}
            />
            {ask ? (
              <Button
                variant="secondary"
                size="compact"
                aria-pressed={ask.open}
                aria-keyshortcuts="a"
                data-ask-button=""
                onClick={ask.toggle}
              >
                Ask
              </Button>
            ) : null}
            <Button variant="secondary" size="compact" onClick={close}>
              Full review
            </Button>
          </div>
        </div>
        {view === "tour" ? (
          <Timeline tour={tour} step={step} done={done} commented={commented} go={go} />
        ) : null}
      </header>
      {entry.pull_request.head_sha && entry.pull_request.head_sha !== document.review.head ? (
        <p className="border-b border-(--border-subtle) bg-(--status-caution-bg) px-6 py-2 text-xs">
          The pull request has new commits since this review was generated. Comments attach to the
          reviewed commit and may show as outdated on GitHub.
        </p>
      ) : null}
      <HunkPatchProvider source={patches}>
        {view === "tour" ? (
          <div className="flex flex-1 flex-wrap">
            <div className="flex min-w-0 flex-[999_1_40rem] flex-col gap-5 px-8 py-6">
              {step === 0 ? (
                <Intro tour={tour} done={done} go={go} toAll={toAll} />
              ) : claim ? (
                <Claim
                  key={claim.id}
                  tour={tour}
                  stop={claim}
                  document={document}
                  seen={seen.has(claim.id)}
                  note={progress.notes[claim.id] ?? ""}
                  editing={editing === claim.id}
                  draft={draft}
                  setDraft={setDraft}
                  startComment={() => startComment(claim)}
                  saveComment={() => {
                    setNote(claim, draft.trim());
                    markSeen(claim);
                    setEditing(null);
                  }}
                  cancelComment={() => setEditing(null)}
                  removeComment={() => setNote(claim, "")}
                  ask={ask ? () => ask.about(stopFocus(document, claim)) : undefined}
                />
              ) : (
                <WrapUp
                  tour={tour}
                  unseen={unseen}
                  go={go}
                  submit={
                    <Submit
                      entry={entry}
                      document={document}
                      tour={tour}
                      progress={progress}
                      setProgress={setProgress}
                      seenCount={seenCount}
                      unseen={unseen.length}
                      changed={changed}
                    />
                  }
                />
              )}
              <div className="mt-auto flex flex-wrap items-center gap-2 pt-2">
                <Button variant="secondary" onClick={() => go(step - 1)} disabled={step === 0}>
                  Back
                </Button>
                <Button onClick={forward} disabled={step === last}>
                  {step === 0
                    ? "Start the tour"
                    : tour.stops.slice(step).some((stop) => !done(stop))
                      ? "Next"
                      : "Wrap up"}
                </Button>
                {skipped > 0 && step > 0 ? (
                  <span className="text-xs text-(--text-secondary)">
                    Skipped {skipped} {skipped === 1 ? "stop" : "stops"} you already looked at
                  </span>
                ) : null}
                <span className="ml-auto flex gap-3 text-xs text-(--text-tertiary)">
                  <span>
                    <kbd>←</kbd> <kbd>→</kbd> move
                  </span>
                  <span>
                    <kbd>c</kbd> comment
                  </span>
                  <span>
                    <kbd>e</kbd> everything
                  </span>
                </span>
              </div>
            </div>
            <Rail tour={tour} stop={claim} done={done} commented={commented} go={go} />
          </div>
        ) : (
          <div className="flex flex-1 flex-wrap">
            <div className="flex min-w-0 flex-[999_1_42rem] flex-col">
              <Ledger
                tour={tour}
                document={document}
                visible={visible}
                cur={cur}
                open={open}
                filter={filter}
                setFilter={setFilter}
                progress={progress}
                done={done}
                commented={commented}
                toggle={toggleDetails}
                expandAll={(expand) => {
                  const stops = visible.map((index) => tour.stops[index]!);
                  setOpen(expand ? new Set(stops.map((stop) => stop.id)) : new Set());
                  if (expand) markSeen(...stops);
                }}
                markAllSeen={() => markSeen(...tour.stops)}
                setNote={setNote}
                comment={(index) => {
                  const stop = tour.stops[index]!;
                  setCur(index);
                  markSeen(stop);
                  setOpen((current) => new Set(current).add(stop.id));
                  requestAnimationFrame(() =>
                    window.document.getElementById(`walk-note-${stop.id}`)?.focus(),
                  );
                }}
                walk={toTour}
                ask={
                  ask ? (index) => ask.about(stopFocus(document, tour.stops[index]!)) : undefined
                }
              />
            </div>
            <aside
              aria-label="Submit review"
              className="flex min-w-0 flex-[1_1_20rem] flex-col gap-3 border-l border-(--border-subtle) px-5 py-5"
            >
              <h2 className={LABEL}>Submit review</h2>
              <Submit
                entry={entry}
                document={document}
                tour={tour}
                progress={progress}
                setProgress={setProgress}
                seenCount={seenCount}
                unseen={unseen.length}
                changed={changed}
              />
            </aside>
          </div>
        )}
      </HunkPatchProvider>
    </section>
  );
}

function Timeline({
  tour,
  step,
  done,
  commented,
  go,
}: {
  tour: Tour;
  step: number;
  done: (stop: Stop) => boolean;
  commented: (stop: Stop) => boolean;
  go: (step: number) => void;
}) {
  const last = tour.stops.length + 1;
  const dot = (at: number, label: string) => {
    const stop = tour.stops[at - 1];
    const state = stop ? (commented(stop) ? "commented" : done(stop) ? "seen" : null) : null;
    return (
      <button
        key={at}
        type="button"
        onClick={() => go(at)}
        aria-label={
          label +
          (state === "commented" ? ", has your comment" : state === "seen" ? ", looked at" : "")
        }
        aria-current={at === step ? "step" : undefined}
        className={cn(
          "walk-dot",
          state === "commented" && "commented",
          state === "seen" && "seen",
          at === step && "current",
        )}
      />
    );
  };
  return (
    <nav aria-label="Tour stops" className="flex items-stretch gap-1.5">
      <div className="flex w-12 flex-none flex-col gap-1.5">
        <span className={cn(LABEL, "truncate")}>Intro</span>
        <div className="flex">{dot(0, "Intro")}</div>
      </div>
      {tour.chapters.map((chapter, index) => {
        const finished = chapter.stops.every((at) => done(tour.stops[at]!));
        return (
          <div
            key={chapter.id}
            className={cn("flex min-w-0 flex-col gap-1.5", finished && "opacity-70")}
            style={{ flex: `${chapter.stops.length} 1 0` }}
          >
            <span className={cn(LABEL, "truncate")}>
              {index + 1} · {finished ? "Done · " : ""}
              {chapter.title}
            </span>
            <div className="flex gap-0.5">
              {chapter.stops.map((at) => dot(at + 1, tour.stops[at]!.title))}
            </div>
          </div>
        );
      })}
      <div className="flex w-14 flex-none flex-col gap-1.5">
        <span className={cn(LABEL, "truncate")}>Submit</span>
        <div className="flex">{dot(last, "Submit")}</div>
      </div>
    </nav>
  );
}

function RiskBlocks({ level }: { level: RiskScoreLevel }) {
  return (
    <span aria-hidden className="flex gap-0.5">
      {[1, 2, 3, 4, 5].map((n) => (
        <span
          key={n}
          className={cn(
            "h-1.5 w-4 rounded-sm",
            n > level
              ? "bg-(--surface-sunken)"
              : level >= 3
                ? "bg-(--status-caution)"
                : "bg-(--text-tertiary)",
          )}
        />
      ))}
    </span>
  );
}

function Intro({
  tour,
  done,
  go,
  toAll,
}: {
  tour: Tour;
  done: (stop: Stop) => boolean;
  go: (step: number, skip?: number) => void;
  toAll: () => void;
}) {
  return (
    <div className="flex max-w-3xl flex-col gap-5">
      <h2 className={LABEL}>What this pull request does</h2>
      <p className="text-2xl leading-normal">{tour.intro}</p>
      <div className="flex flex-col gap-2.5">
        <h2 className={LABEL}>The route · riskiest first</h2>
        {tour.chapters.map((chapter, index) => {
          const open = chapter.stops.filter((at) => !done(tour.stops[at]!));
          const first = chapter.stops[0]!;
          return (
            <button
              key={chapter.id}
              type="button"
              className="walk-route"
              onClick={() => go((open[0] ?? first) + 1, open.length ? open[0]! - first : 0)}
            >
              <span className="walk-route-number">{index + 1}</span>
              <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                <span className="font-semibold">{chapter.title}</span>
                <span className="text-xs text-(--text-secondary)">
                  {chapter.stops.length} {chapter.stops.length === 1 ? "behaviour" : "behaviours"}
                  {chapter.meaning ? ` · ${chapter.meaning.toLowerCase()}` : ""}
                </span>
              </span>
              <StopBar tour={tour} stops={chapter.stops} done={done} />
              {chapter.level ? (
                <span className={cn("walk-risk", chapter.level >= 3 && "high")}>
                  Risk {chapter.level}
                </span>
              ) : null}
            </button>
          );
        })}
      </div>
      <p className="text-(--text-secondary)">
        Read each stop and move on. You only need to act when something looks off: leave a comment
        and it goes into the review. The code is one click away when you want it. Prefer to scan?{" "}
        <button type="button" className="walk-link" onClick={toAll}>
          See everything at once
        </button>
        .
      </p>
    </div>
  );
}

function StopBar({
  tour,
  stops,
  done,
}: {
  tour: Tour;
  stops: number[];
  done: (stop: Stop) => boolean;
}) {
  return (
    <span aria-hidden className="flex w-20 flex-none gap-0.5">
      {stops.map((at) => {
        const stop = tour.stops[at]!;
        return (
          <span
            key={stop.id}
            className={cn(
              "h-1.5 flex-1 rounded-sm",
              done(stop) ? "bg-(--status-positive)" : "bg-(--surface-sunken)",
            )}
          />
        );
      })}
    </span>
  );
}

function BeforeAfter({ stop }: { stop: Stop }) {
  if (echoes(stop)) return null;
  return (
    <div className="flex flex-wrap items-stretch gap-2.5">
      {stop.before ? (
        <>
          <div className="walk-panel flex-[1_1_16rem]">
            <h3 className={LABEL}>Before</h3>
            <p>{stop.before}</p>
          </div>
          <span aria-hidden className="flex items-center text-(--text-tertiary)">
            <ChevronRightIcon />
          </span>
        </>
      ) : null}
      <div className="walk-panel after flex-[1_1_16rem]">
        <h3 className={LABEL}>{stop.before ? "After" : "New behaviour"}</h3>
        <p>{stop.after}</p>
      </div>
    </div>
  );
}

function Claim({
  tour,
  stop,
  document,
  seen,
  note,
  editing,
  draft,
  setDraft,
  startComment,
  saveComment,
  cancelComment,
  removeComment,
  ask,
}: {
  tour: Tour;
  stop: Stop;
  document: ReviewDocument;
  seen: boolean;
  note: string;
  editing: boolean;
  draft: string;
  setDraft: (draft: string) => void;
  startComment: () => void;
  saveComment: () => void;
  cancelComment: () => void;
  removeComment: () => void;
  /** Asks the review's agent about this stop. */
  ask?: () => void;
}) {
  const chapter = tour.chapters[stop.chapter]!;
  const hasNote = Boolean(note.trim());
  const target = stop.where || "the review";
  return (
    <article aria-labelledby={`walk-title-${stop.id}`} className="flex max-w-3xl flex-col gap-5">
      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2.5">
          <span className={LABEL}>
            Chapter {stop.chapter + 1} · {chapter.title}
          </span>
          {hasNote ? (
            <span className="walk-badge commented">Your comment</span>
          ) : seen ? (
            <span className="walk-badge">Looked at</span>
          ) : null}
        </div>
        <h2 id={`walk-title-${stop.id}`} className="text-2xl font-semibold">
          {stop.title}
        </h2>
        {stop.text && stop.text !== stop.after ? (
          <p className="text-(--text-secondary)">{stop.text}</p>
        ) : null}
      </div>
      <BeforeAfter stop={stop} />
      {stop.question ? (
        <div className="walk-panel question">
          <h3 className={cn(LABEL, "flex items-center gap-2")}>
            <SearchIcon size={14} />
            Ask yourself
          </h3>
          <p className="text-lg">{stop.question}</p>
        </div>
      ) : null}
      {editing ? (
        <div className="walk-panel comment">
          <label htmlFor={`walk-draft-${stop.id}`} className="font-semibold">
            What looks off? It goes into the review, attached to {target}.
          </label>
          <textarea
            id={`walk-draft-${stop.id}`}
            // The reader asked for the box: it takes the typing at once.
            autoFocus
            value={draft}
            placeholder={stop.question ?? ""}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) saveComment();
              if (event.key === "Escape") cancelComment();
            }}
            className="walk-textarea"
          />
          <div className="flex gap-2">
            <Button size="compact" onClick={saveComment} disabled={!draft.trim()}>
              Save comment
            </Button>
            <Button variant="secondary" size="compact" onClick={cancelComment}>
              {hasNote ? "Cancel" : "Never mind"}
            </Button>
          </div>
        </div>
      ) : hasNote ? (
        <div className="walk-panel comment saved">
          <CommentsIcon size={16} className="mt-0.5 flex-none text-(--status-caution)" />
          <div className="flex min-w-0 flex-1 flex-col gap-1">
            <span className="text-xs text-(--text-secondary)">Your comment on {target}</span>
            <p className="whitespace-pre-line">{note}</p>
          </div>
          <Button variant="secondary" size="compact" onClick={startComment}>
            Edit
          </Button>
          <Button variant="secondary" size="compact" onClick={removeComment}>
            Remove
          </Button>
        </div>
      ) : (
        <div className="flex flex-wrap items-center gap-3">
          <Button variant="secondary" onClick={startComment} className="gap-2">
            <CommentsIcon size={15} />
            Something looks off <kbd>c</kbd>
          </Button>
          <span className="text-xs text-(--text-secondary)">Nothing to say? Just move on.</span>
        </div>
      )}
      {ask ? (
        <div>
          <Button variant="secondary" onClick={ask}>
            Ask about this
          </Button>
        </div>
      ) : null}
      {stop.anchors.length ? (
        <div className="walk-evidence">
          <h3 className={LABEL}>Evidence</h3>
          <HunkEvidence document={document} anchors={stop.anchors} />
        </div>
      ) : null}
    </article>
  );
}

function Rail({
  tour,
  stop,
  done,
  commented,
  go,
}: {
  tour: Tour;
  stop: Stop | null;
  done: (stop: Stop) => boolean;
  commented: (stop: Stop) => boolean;
  go: (step: number) => void;
}) {
  const chapter = stop ? tour.chapters[stop.chapter]! : null;
  const level = chapter ? chapter.level : tour.level;
  return (
    <aside
      aria-label={chapter ? "Chapter" : "Pull request risk"}
      className="flex min-w-0 flex-[1_1_20rem] flex-col gap-4 border-l border-(--border-subtle) bg-(--surface-aside) px-5 py-6"
    >
      <div className="flex flex-col gap-2">
        <h2 className={LABEL}>
          {chapter ? `Chapter ${stop!.chapter + 1} risk` : "Pull request risk"}
        </h2>
        <p className="font-semibold">{chapter ? chapter.title : "Risk score"}</p>
        {level ? (
          <p className="flex items-center gap-2.5 text-xs">
            <RiskBlocks level={level} />
            <span>
              <strong>{level} of 5</strong>
              {chapter?.meaning ? ` · ${chapter.meaning}` : ""}
            </span>
          </p>
        ) : null}
      </div>
      {chapter ? (
        chapter.why ? (
          <p className="text-(--text-secondary)">{chapter.why}</p>
        ) : null
      ) : (
        tour.risks.map((risk, index) => {
          // Several reasons can name one chapter: the first carries its link.
          const linked =
            risk.stop !== null &&
            tour.risks.findIndex((other) => other.stop === risk.stop) === index;
          return (
            <div key={risk.text} className="flex flex-col gap-1 text-(--text-secondary)">
              <p>{risk.text}</p>
              {linked ? (
                <button
                  type="button"
                  className="walk-link self-start text-left"
                  onClick={() => go(risk.stop! + 1)}
                >
                  Go to {tour.chapters[tour.stops[risk.stop!]!.chapter]!.title} →
                </button>
              ) : null}
            </div>
          );
        })
      )}
      {!chapter && tour.releaseNote ? (
        <div className="flex flex-col gap-1.5 border-t border-(--border-subtle) pt-3">
          <h2 className={LABEL}>Release note</h2>
          <p className="text-(--text-secondary)">{tour.releaseNote}</p>
        </div>
      ) : null}
      {chapter && stop ? (
        <>
          <div className="flex flex-col gap-1 border-t border-(--border-subtle) pt-3">
            <h2 className={cn(LABEL, "pb-1")}>In this chapter</h2>
            {chapter.stops.map((at) => {
              const other = tour.stops[at]!;
              const state = commented(other) ? "commented" : done(other) ? "seen" : null;
              return (
                <button
                  key={other.id}
                  type="button"
                  onClick={() => go(at + 1)}
                  aria-current={other.id === stop.id ? "step" : undefined}
                  className="walk-chapter-stop"
                >
                  <span aria-hidden className={cn("walk-state-dot", state)} />
                  <span className="min-w-0 flex-1">{other.title}</span>
                </button>
              );
            })}
          </div>
          {stop.tests.length ? (
            <div className="flex flex-col gap-1.5 border-t border-(--border-subtle) pt-3">
              <h2 className={LABEL}>Tests for this behaviour</h2>
              <div className="flex flex-wrap gap-1.5">
                {stop.tests.map((test) => (
                  <code key={test} className="walk-chip">
                    {test}
                  </code>
                ))}
              </div>
            </div>
          ) : null}
        </>
      ) : null}
    </aside>
  );
}

function WrapUp({
  tour,
  unseen,
  go,
  submit,
}: {
  tour: Tour;
  unseen: Stop[];
  go: (step: number) => void;
  submit: ReactNode;
}) {
  return (
    <div className="flex max-w-2xl flex-col gap-4">
      <h2 className={LABEL}>Wrap-up</h2>
      <p className="text-lg">
        That’s the whole change: {tour.stops.length}{" "}
        {tour.stops.length === 1 ? "behaviour" : "behaviours"} in {tour.chapters.length}{" "}
        {tour.chapters.length === 1 ? "chapter" : "chapters"}.
      </p>
      {unseen.length ? (
        <div className="walk-panel">
          <p className="font-semibold">
            {unseen.length}{" "}
            {unseen.length === 1
              ? "behaviour you haven’t looked at:"
              : "behaviours you haven’t looked at:"}
          </p>
          <div className="flex flex-wrap gap-1.5">
            {unseen.map((stop) => (
              <Button
                key={stop.id}
                variant="secondary"
                size="compact"
                onClick={() => go(tour.stops.indexOf(stop) + 1)}
              >
                {stop.title}
              </Button>
            ))}
          </div>
        </div>
      ) : null}
      {submit}
    </div>
  );
}

function Ledger({
  tour,
  document,
  visible,
  cur,
  open,
  filter,
  setFilter,
  progress,
  done,
  commented,
  toggle,
  expandAll,
  markAllSeen,
  setNote,
  comment,
  walk,
  ask,
}: {
  tour: Tour;
  document: ReviewDocument;
  visible: number[];
  cur: number;
  open: Set<string>;
  filter: "all" | "unseen" | "commented";
  setFilter: (filter: "all" | "unseen" | "commented") => void;
  progress: Progress;
  done: (stop: Stop) => boolean;
  commented: (stop: Stop) => boolean;
  toggle: (index: number) => void;
  expandAll: (expand: boolean) => void;
  markAllSeen: () => void;
  setNote: (stop: Stop, note: string) => void;
  comment: (index: number) => void;
  walk: (index: number) => void;
  ask?: (index: number) => void;
}) {
  const allOpen = visible.length > 0 && visible.every((at) => open.has(tour.stops[at]!.id));
  const counts = {
    all: tour.stops.length,
    unseen: tour.stops.filter((stop) => !done(stop)).length,
    commented: tour.stops.filter(commented).length,
  };
  const current = useRef<HTMLDivElement>(null);
  // A block body: Chrome's scrollIntoView returns a promise, which React
  // would take for the effect's cleanup.
  useEffect(() => {
    current.current?.scrollIntoView?.({ block: "nearest" });
  }, [cur]);
  return (
    <>
      <div className="flex flex-wrap items-center gap-1.5 border-b border-(--border-subtle) px-6 py-3">
        {(
          [
            ["all", "All"],
            ["unseen", "Not looked at"],
            ["commented", "With comments"],
          ] as const
        ).map(([value, label]) => (
          <button
            key={value}
            type="button"
            aria-pressed={filter === value}
            onClick={() => setFilter(value)}
            className="walk-filter"
          >
            {label} <span className="tabular-nums opacity-70">{counts[value]}</span>
          </button>
        ))}
        <span className="ml-auto flex gap-1.5">
          <Button variant="secondary" size="compact" onClick={markAllSeen}>
            Mark all looked at
          </Button>
          <Button variant="secondary" size="compact" onClick={() => expandAll(!allOpen)}>
            {allOpen ? "Collapse all details" : "Expand all details"}
          </Button>
        </span>
      </div>
      {tour.chapters.map((chapter, index) => {
        const rows = chapter.stops.filter((at) => visible.includes(at));
        if (rows.length === 0) return null;
        return (
          <section key={chapter.id} aria-label={chapter.title}>
            <h2 className="flex items-center gap-3 border-b border-(--border-subtle) bg-(--surface-wash) px-6 py-2.5">
              <span className="min-w-0 flex-1 font-semibold">
                {index + 1} · {chapter.title}
              </span>
              {chapter.level ? (
                <span className="text-xs text-(--text-secondary)">Risk {chapter.level}</span>
              ) : null}
              <StopBar tour={tour} stops={chapter.stops} done={done} />
              <span className="w-9 text-right text-xs tabular-nums">
                {chapter.stops.filter((at) => done(tour.stops[at]!)).length}/{chapter.stops.length}
              </span>
            </h2>
            {rows.map((at) => {
              const stop = tour.stops[at]!;
              const isOpen = open.has(stop.id);
              const isCommented = commented(stop);
              const isSeen = !isCommented && done(stop);
              return (
                <div
                  key={stop.id}
                  ref={at === cur ? current : undefined}
                  className={cn("walk-row", at === cur && "current", isCommented && "commented")}
                >
                  <div className="flex items-start gap-3.5 px-6 py-3">
                    <span
                      role="img"
                      aria-label={
                        isCommented
                          ? "Has your comment"
                          : isSeen
                            ? "Looked at"
                            : "Not looked at yet"
                      }
                      className={cn("walk-row-state", isCommented && "commented", isSeen && "seen")}
                    >
                      {isCommented ? <CommentsIcon size={13} /> : isSeen ? "✓" : null}
                    </span>
                    <div className="flex min-w-0 flex-1 flex-col gap-1.5">
                      <h3 className="font-semibold">{stop.title}</h3>
                      {echoes(stop) ? null : (
                        <p className="text-(--text-secondary)">{stop.after}</p>
                      )}
                      {isCommented && !isOpen ? (
                        <p className="walk-note whitespace-pre-line">{progress.notes[stop.id]}</p>
                      ) : null}
                      <div className="flex flex-wrap items-center gap-3 text-xs">
                        <button
                          type="button"
                          aria-expanded={isOpen}
                          onClick={() => toggle(at)}
                          className="walk-link flex items-center gap-1"
                        >
                          <ChevronRightIcon
                            size={12}
                            className={cn("transition-transform", isOpen && "rotate-90")}
                          />
                          Details and evidence
                        </button>
                        <button type="button" onClick={() => walk(at)} className="walk-link muted">
                          Walk me through it
                        </button>
                      </div>
                    </div>
                    <div className="flex flex-none gap-1.5">
                      {ask ? (
                        <Button
                          variant="secondary"
                          size="compact"
                          aria-label={`Ask about ${stop.title}`}
                          onClick={() => ask(at)}
                        >
                          Ask
                        </Button>
                      ) : null}
                      <Button variant="secondary" size="compact" onClick={() => comment(at)}>
                        {isCommented ? "Edit comment" : "Comment"}
                      </Button>
                    </div>
                  </div>
                  {isOpen ? (
                    <div className="flex flex-col gap-2.5 pr-6 pb-4 pl-15">
                      {stop.before ? (
                        <p>
                          <strong>Before:</strong> {stop.before}
                        </p>
                      ) : null}
                      {stop.question ? (
                        <p>
                          <strong>Ask yourself:</strong> {stop.question}
                        </p>
                      ) : null}
                      {stop.text && stop.text !== stop.after ? (
                        <p className="text-xs text-(--text-secondary)">
                          The review states: “{stop.text}”
                        </p>
                      ) : null}
                      {stop.anchors.length ? (
                        <HunkEvidence document={document} anchors={stop.anchors} />
                      ) : null}
                      <label className="flex flex-col gap-1 text-xs">
                        Comment{stop.where ? `, attached to ${stop.where}` : ""}. Leave it empty if
                        nothing looks off.
                        <textarea
                          id={`walk-note-${stop.id}`}
                          value={progress.notes[stop.id] ?? ""}
                          placeholder={stop.question ?? ""}
                          onChange={(event) => setNote(stop, event.target.value)}
                          className="walk-textarea"
                        />
                      </label>
                    </div>
                  ) : null}
                </div>
              );
            })}
          </section>
        );
      })}
      {visible.length === 0 ? (
        <p className="px-6 py-7 text-(--text-secondary)">No behaviours in this view.</p>
      ) : null}
      <p className="mt-auto flex flex-wrap gap-3.5 border-t border-(--border-subtle) bg-(--surface-wash) px-6 py-2.5 text-xs text-(--text-secondary)">
        <span>
          <kbd>j</kbd> <kbd>k</kbd> move
        </span>
        <span>
          <kbd>o</kbd> details
        </span>
        <span>
          <kbd>c</kbd> comment
        </span>
        <span>
          <kbd>t</kbd> walk me through it
        </span>
        <span>
          <kbd>e</kbd> back to the tour
        </span>
        <span>Opening details counts as looked at</span>
      </p>
    </>
  );
}

type SubmitState =
  | { state: "idle" }
  | { state: "sending" }
  | { state: "sent"; url: string }
  | { state: "failed"; message: string; code?: string };

/** The review's verdict and comments, sent to GitHub as one review. The
 *  verdict follows the comments unless the reader picked one that still
 *  applies: Approve only without comments, Request changes only with them,
 *  and neither on the reader's own pull request, which GitHub refuses. */
function Submit({
  entry,
  document,
  tour,
  progress,
  setProgress,
  seenCount,
  unseen,
  changed,
}: {
  entry: Entry;
  document: ReviewDocument;
  tour: Tour;
  progress: Progress;
  setProgress: (update: (current: Progress) => Progress) => void;
  seenCount: number;
  unseen: number;
  changed: () => Promise<void>;
}) {
  const [picked, setPicked] = useState<ReviewEvent | null>(null);
  const [sending, setSending] = useState<SubmitState>({ state: "idle" });
  const comments = commentedStops(tour, progress);
  const count = comments.length;
  const own = entry.pull_request.authored_by_me;
  const hasNote = Boolean(progress.note.trim());
  const events: { id: ReviewEvent; label: string; ok: boolean; hint: string }[] = [
    {
      id: "APPROVE",
      label: "Approve",
      ok: !own && count === 0,
      hint: own
        ? "GitHub doesn’t let you approve your own pull request"
        : count
          ? "Remove your comments to approve"
          : unseen
            ? `Nothing looked off · ${unseen} not looked at yet`
            : "Looked at everything, nothing looked off",
    },
    {
      id: "COMMENT",
      label: "Comment",
      ok: true,
      hint: "Send your comments without a verdict",
    },
    {
      id: "REQUEST_CHANGES",
      label: "Request changes",
      ok: !own && count > 0,
      hint: own
        ? "GitHub doesn’t let you request changes on your own pull request"
        : count
          ? `Ask the author to address ${count} ${count === 1 ? "comment" : "comments"}`
          : "Leave a comment first",
    },
  ];
  const event =
    events.find((e) => e.id === picked && e.ok)?.id ?? (count || own ? "COMMENT" : "APPROVE");
  const empty = event !== "APPROVE" && count === 0 && !hasNote;
  const send = async (inline: boolean) => {
    setSending({ state: "sending" });
    const sent = progress;
    try {
      const answer = await request<{ url: string }>(
        "api/prs/review",
        reviewSubmission(document, tour, sent, event, inline),
      );
      // What was said is on GitHub now; what was looked at stays, and so
      // does anything written while it was being sent.
      setProgress((current) => ({
        ...current,
        notes: Object.fromEntries(
          Object.entries(current.notes).filter(
            ([id, note]) => note.trim() && note !== sent.notes[id],
          ),
        ),
        note: current.note === sent.note ? "" : current.note,
      }));
      setPicked(null);
      setSending({ state: "sent", url: answer.url });
      void changed();
    } catch (error) {
      setSending({
        state: "failed",
        message: error instanceof Error ? error.message : "The review could not be sent.",
        code: error instanceof LocalRequestError ? error.code : undefined,
      });
    }
  };
  if (sending.state === "sent")
    return (
      <div role="status" className="walk-panel after">
        <p className="font-semibold">Your review is on GitHub.</p>
        <a href={sending.url} target="_blank" rel="noreferrer" className="flex items-center gap-1">
          Open it on GitHub <ExternalLinkIcon size={12} />
        </a>
        <Button
          variant="secondary"
          size="compact"
          className="self-start"
          onClick={() => setSending({ state: "idle" })}
        >
          Write another
        </Button>
      </div>
    );
  return (
    <div className="flex flex-col gap-3">
      <div role="radiogroup" aria-label="Review verdict" className="flex flex-col gap-1.5">
        {events.map((e) => (
          <button
            key={e.id}
            type="button"
            role="radio"
            aria-checked={event === e.id}
            aria-disabled={!e.ok}
            onClick={() => e.ok && setPicked(e.id)}
            className="walk-event"
          >
            <span aria-hidden className="walk-event-dot" />
            <span className="flex flex-col gap-0.5 text-left">
              <span className="font-semibold">{e.label}</span>
              <span className="text-xs text-(--text-secondary)">{e.hint}</span>
            </span>
          </button>
        ))}
      </div>
      <label className="flex flex-col gap-1 text-xs">
        Overall comment (optional)
        <textarea
          value={progress.note}
          onChange={(change) => {
            const note = change.target.value;
            setProgress((current) => ({ ...current, note }));
          }}
          className="walk-textarea"
        />
      </label>
      {count ? (
        <div className="flex flex-col gap-1.5">
          <p className="text-xs text-(--text-secondary)">
            {count} {count === 1 ? "comment" : "comments"}, each on its evidence line:
          </p>
          <ul className="flex flex-col gap-1.5">
            {comments.map((stop) => (
              <li key={stop.id} className="walk-note">
                <span className="text-xs text-(--text-secondary)">
                  {stop.title}
                  {stop.where ? ` · ${stop.where}` : " · in the review body"}
                </span>
                <p className="whitespace-pre-line">{progress.notes[stop.id]}</p>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {sending.state === "failed" ? (
        <Alert>
          {sending.message}
          {sending.code === "invalid_line" ? (
            <>
              {" "}
              <Button variant="secondary" size="compact" onClick={() => void send(false)}>
                Put the comments in the review body instead
              </Button>
            </>
          ) : null}
        </Alert>
      ) : null}
      <div className="flex flex-wrap items-center gap-2.5">
        <Button
          onClick={() => void send(true)}
          disabled={sending.state === "sending" || seenCount === 0 || empty}
        >
          {sending.state === "sending"
            ? "Sending…"
            : event === "APPROVE"
              ? "Approve"
              : event === "REQUEST_CHANGES"
                ? "Request changes"
                : "Send comments"}
        </Button>
        <span className="text-xs text-(--text-secondary)">
          {seenCount === 0
            ? "Look at a behaviour first"
            : empty
              ? "Write a comment first"
              : `Posts to ${entry.pull_request.repository} #${entry.pull_request.number} on GitHub`}
        </span>
      </div>
    </div>
  );
}
