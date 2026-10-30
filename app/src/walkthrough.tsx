import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { cn } from "@baum/viewer/components/classnames";
import {
  HunkEvidence,
  HunkPatchProvider,
  type HunkPatchSource,
} from "@baum/viewer/components/hunk-evidence";
import {
  CheckIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  CommentsIcon,
  ExternalLinkIcon,
} from "@baum/viewer/components/icons";
import { Markdown } from "@baum/viewer/components/markdown";
import { ViewerLink } from "@baum/viewer/navigation";
import { EvidenceBadge } from "@baum/viewer/components/review-section";
import {
  RISK_SCORE_MEANING,
  type ReviewDocument,
  type RiskScoreLevel,
} from "@baum/viewer/review-document";
import { LocalRequestError, request, type ChatFocus, type Entry } from "./api";
import { stopFocus } from "./chat";
import { PullRequestState } from "./inbox";
import { useShortcuts } from "./shortcuts";
import {
  buildTour,
  commentedStops,
  PROGRESS_EVENT,
  loadProgress,
  progressKey,
  reviewSubmission,
  saveProgress,
  type Chapter,
  type Progress,
  type ReviewEvent,
  type Stop,
  type Tour,
} from "./walkthrough-model";
import "./walkthrough.css";

/** The overview a pull request opens on, or the tour it leads into. */
export type WalkMode = "landing" | "tour";

const plural = (count: number, one: string, many = `${one}s`) =>
  `${count} ${count === 1 ? one : many}`;

/** Whether the statement only repeats the title, as a one-sentence step
 *  written before steps had titles does. */
const echoes = (stop: Stop) => stop.text.replace(/[.!?]\s*$/, "") === stop.title;

/** A pull request's overview, what matters about it on one page, and from
 *  there the review as a guided tour, one page per chapter, riskiest first: the
 *  chapter's intent and risk once at the top, then its behaviours, one of
 *  them picked out for the keys. Nobody has to sign off on every behaviour:
 *  moving past one, or leaving its chapter, counts as having looked at it,
 *  and a comment is the only way to say something looks off. The comments go
 *  to GitHub as one review, each on the line its behaviour cites. It is drawn
 *  as the inbox is: the chapters and their behaviours listed on the left, the
 *  page being read beside them. */
export function Walkthrough({
  entry,
  document,
  mode,
  setMode,
  close,
  back,
  patches,
  changed,
  actions,
  notices,
  ask,
}: {
  entry: Entry;
  document: ReviewDocument;
  mode: WalkMode;
  setMode: (mode: WalkMode) => void;
  /** To the full review. */
  close: () => void;
  /** Where the pull request was opened from. */
  back: { href: string; label: string };
  patches: HunkPatchSource;
  changed: () => Promise<void>;
  /** The pull request's own actions, such as a fix or a merge. */
  actions?: ReactNode;
  /** What is happening on it now, such as agents on its branch. */
  notices?: ReactNode;
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
  // The stop whose comment is being edited and its note as it was when the
  // editing began, to keep what the Ask panel adds to it meanwhile.
  const editingRef = useRef<string | null>(null);
  const editBase = useRef("");
  // The Ask panel adds an answer to the draft from outside this page.
  useEffect(() => {
    const changed = (event: Event) => {
      if ((event as CustomEvent<string>).detail !== key) return;
      const next = loadProgress(key);
      setProgress(next);
      const id = editingRef.current;
      if (id === null) return;
      // Text appended to the stop's note since the editing began joins the
      // text being edited, and text the panel takes back (Undo) leaves it.
      const stored = next.notes[id] ?? "";
      const base = editBase.current;
      if (stored === base) return;
      if (stored.startsWith(base)) {
        editBase.current = stored;
        const added = stored.slice(base.length).replace(/^\s+/, "");
        setDraft((current) =>
          current.trim() ? `${current.replace(/\s+$/, "")}\n\n${added}` : added,
        );
      } else if (base.startsWith(stored)) {
        editBase.current = stored;
        const removed = base.slice(stored.length).trim();
        if (removed) setDraft((current) => withoutLast(current, removed));
      }
    };
    window.addEventListener(PROGRESS_EVENT, changed);
    return () => window.removeEventListener(PROGRESS_EVENT, changed);
  }, [key]);
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

  const last = tour.chapters.length + 1;
  // 1… the chapters, `last` the wrap-up; before the first is the overview.
  const [page, setPage] = useState(1);
  // The behaviour the keys act on, an index into `tour.stops`.
  const [cur, setCur] = useState(0);
  const [editing, setEditing] = useState<string | null>(null);
  editingRef.current = editing;
  const [draft, setDraft] = useState("");
  const detail = useRef<HTMLDivElement>(null);
  // Each page starts at its top, as a new page would.
  useEffect(() => {
    detail.current?.scrollTo?.({ top: 0 });
  }, [page]);
  const chapter = page >= 1 && page < last ? tour.chapters[page - 1]! : null;
  // A picked behaviour is brought into view; a chapter opened whole is read
  // from its top.
  const reveal = useRef(false);
  useEffect(() => {
    if (!chapter || !reveal.current) return;
    window.document
      .getElementById(`walk-behaviour-${tour.stops[cur]?.id}`)
      ?.scrollIntoView?.({ block: "nearest" });
  }, [cur, page]);

  /** Opens a page; on a chapter, picks `at` or the chapter's first
   *  behaviour. Leaving a chapter counts all of it as looked at. */
  const open = (next: number, at?: number) => {
    next = Math.min(last, next);
    if (mode === "tour" && chapter && next !== page)
      markSeen(...chapter.stops.map((index) => tour.stops[index]!));
    if (next < 1) {
      setEditing(null);
      return setMode("landing");
    }
    if (mode !== "tour") setMode("tour");
    const target = tour.chapters[next - 1];
    if (target) setCur(at ?? target.stops[0]!);
    reveal.current = at !== undefined;
    setPage(next);
    setEditing(null);
  };
  /** Picks a behaviour, opening its chapter; the one left counts as looked at. */
  const pick = (at: number) => {
    const stop = tour.stops[at];
    if (!stop) return;
    if (chapter && at !== cur) markSeen(tour.stops[cur]!);
    if (stop.chapter + 1 !== page) open(stop.chapter + 1, at);
    else setCur(at);
    reveal.current = true;
    if (editing && editing !== stop.id) setEditing(null);
  };
  // j and k walk the behaviours, across chapters; → and ← whole pages.
  const down = () => {
    if (mode === "landing") return open(1);
    if (!chapter) return open(page + 1);
    const index = chapter.stops.indexOf(cur);
    if (index < chapter.stops.length - 1) pick(chapter.stops[index + 1]!);
    else open(page + 1);
  };
  const up = () => {
    if (page === last) {
      const final = tour.chapters[last - 2]!;
      return open(last - 1, final.stops[final.stops.length - 1]);
    }
    if (!chapter) return;
    const index = chapter.stops.indexOf(cur);
    if (index > 0) return pick(chapter.stops[index - 1]!);
    const previous = tour.chapters[page - 2];
    open(page - 1, previous ? previous.stops[previous.stops.length - 1] : undefined);
  };
  const startComment = (stop: Stop) => {
    editBase.current = progress.notes[stop.id] ?? "";
    setDraft(editBase.current);
    setEditing(stop.id);
  };
  const toggleCode = (stop: Stop) => {
    const details = window.document
      .getElementById(`walk-behaviour-${stop.id}`)
      ?.querySelector("details");
    if (details) details.open = !details.open;
  };

  useShortcuts((event) => {
    const key = event.key;
    if (key === "v") {
      event.preventDefault();
      return close();
    }
    if (mode === "landing") {
      // The overview's one move is into the tour; the page's own keys, such
      // as m to merge, stay with the page.
      if (key === "ArrowRight" || key === "j" || key === "Enter") {
        if (
          key === "Enter" &&
          event.target instanceof HTMLElement &&
          event.target.closest("a, button")
        )
          return;
        event.preventDefault();
        open(1);
      }
      return;
    }
    const stop = chapter ? tour.stops[cur] : undefined;
    if (key === "j" || key === "ArrowDown") {
      event.preventDefault();
      down();
    } else if (key === "k" || key === "ArrowUp") {
      event.preventDefault();
      up();
    } else if (key === "ArrowRight") {
      event.preventDefault();
      open(page + 1);
    } else if (key === "ArrowLeft") {
      event.preventDefault();
      open(page - 1);
    } else if (key === "c" && stop) {
      event.preventDefault();
      startComment(stop);
    } else if (key === "o" && stop) {
      event.preventDefault();
      toggleCode(stop);
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
      <div className="walkthrough">
        <div className="local-review-empty">
          <h2>Nothing to walk through</h2>
          <p>This review has no logical sections with changes to look at.</p>
          <button type="button" className="walk-secondary" onClick={close}>
            Back to the full review
          </button>
        </div>
      </div>
    );

  if (mode === "landing")
    return (
      <section aria-label="Walkthrough" className="walkthrough">
        <div className="walk-landing">
          <Landing
            entry={entry}
            document={document}
            tour={tour}
            done={done}
            open={open}
            close={close}
            back={back}
            actions={actions}
            notices={notices}
            ask={ask}
          />
        </div>
      </section>
    );

  return (
    <section aria-label="Walkthrough" className="walkthrough">
      <header className="walk-head">
        <div className="walk-head-inner">
          <div className="walk-head-text">
            <h1 title={entry.pull_request.title}>
              <span className="walk-head-number">#{entry.pull_request.number}</span>{" "}
              {entry.pull_request.title}
            </h1>
            <p>
              {plural(total, "behaviour")} · {left} ·{" "}
              <strong>
                {seenCount} of {total} looked at
              </strong>{" "}
              ·{" "}
              <span className={commentCount ? "walk-head-comments" : undefined}>
                {commentCount ? plural(commentCount, "comment") : "no comments"}
              </span>
            </p>
          </div>
          <div className="walk-head-actions">
            {ask ? <AskButton ask={ask} /> : null}
            <button type="button" className="walk-secondary" onClick={close}>
              Full review
              <kbd aria-hidden>v</kbd>
            </button>
          </div>
          <ChapterSteps tour={tour} page={page} done={done} commented={commented} open={open} />
        </div>
        <div aria-hidden className="walk-head-bar">
          <span
            className="walk-head-bar-seen"
            style={{ width: `${(100 * (seenCount - commentCount)) / total}%` }}
          />
          <span
            className="walk-head-bar-commented"
            style={{ width: `${(100 * commentCount) / total}%` }}
          />
        </div>
      </header>
      {entry.pull_request.head_sha && entry.pull_request.head_sha !== document.review.head ? (
        <p className="walk-stale">
          The pull request has new commits since this review was generated. Comments attach to the
          reviewed commit and may show as outdated on GitHub.
        </p>
      ) : null}
      <HunkPatchProvider source={patches}>
        <div ref={detail} className="walk-landing">
          {chapter ? (
            <ChapterPage
              key={chapter.id}
              tour={tour}
              chapter={chapter}
              page={page}
              document={document}
              cur={cur}
              pick={pick}
              done={done}
              notes={progress.notes}
              editing={editing}
              draft={draft}
              setDraft={setDraft}
              startComment={startComment}
              saveComment={(stop) => {
                setNote(stop, draft.trim());
                markSeen(stop);
                setEditing(null);
              }}
              cancelComment={() => setEditing(null)}
              removeComment={(stop) => setNote(stop, "")}
              ask={ask ? (stop) => ask.about(stopFocus(document, stop)) : undefined}
              next={() => open(page + 1)}
              back={() => open(page - 1)}
            />
          ) : (
            <WrapUp
              tour={tour}
              unseen={unseen}
              seenCount={seenCount}
              commentCount={commentCount}
              pick={pick}
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
        </div>
      </HunkPatchProvider>
    </section>
  );
}

/** A behaviour's state as the inbox marks a row: an empty ring, a tick once
 *  looked at, or the comment mark once something looked off. */
function Mark({ state }: { state: "seen" | "commented" | null }) {
  return (
    <span aria-hidden className={cn("walk-mark", state)}>
      {state === "commented" ? (
        <CommentsIcon size={9} />
      ) : state === "seen" ? (
        <CheckIcon size={10} />
      ) : null}
    </span>
  );
}

/** Opens or closes the Ask panel, which hands the keyboard back to it. */
function AskButton({ ask }: { ask: { open: boolean; toggle: () => void } }) {
  return (
    <button
      type="button"
      className="walk-secondary"
      aria-pressed={ask.open}
      aria-keyshortcuts="a"
      data-ask-button=""
      onClick={ask.toggle}
    >
      Ask
      <kbd aria-hidden>a</kbd>
    </button>
  );
}

function RiskBadge({ level }: { level: RiskScoreLevel | null }) {
  if (!level) return null;
  return (
    <span
      role="img"
      aria-label={`Risk ${level} of 5`}
      className={cn("walk-badge", `risk-${level}`)}
    >
      {level}
    </span>
  );
}

function RiskCard({
  level,
  meaning,
  children,
}: {
  level: RiskScoreLevel | null;
  meaning: string | null;
  children: ReactNode;
}) {
  return (
    <div className="walk-risk">
      {level ? (
        <div className="walk-risk-head">
          <span className="walk-label">Risk</span>
          <span className="walk-risk-blocks" aria-hidden>
            {[1, 2, 3, 4, 5].map((n) => (
              <span key={n} className={n <= level ? `on risk-${level}` : undefined} />
            ))}
          </span>
          <span className="walk-risk-text">
            {level} of 5{meaning ? ` · ${meaning}` : ""}
          </span>
        </div>
      ) : null}
      {children}
    </div>
  );
}

/** The tour's pages in a row: the way back to the overview, each chapter
 *  by name, shortened to fit, with a dot per behaviour, and the wrap-up. */
function ChapterSteps({
  tour,
  page,
  done,
  commented,
  open,
}: {
  tour: Tour;
  page: number;
  done: (stop: Stop) => boolean;
  commented: (stop: Stop) => boolean;
  open: (page: number) => void;
}) {
  const last = tour.chapters.length + 1;
  const steps = useRef<HTMLElement>(null);
  useEffect(() => {
    steps.current
      ?.querySelector("[aria-current]")
      ?.scrollIntoView?.({ inline: "nearest", block: "nearest" });
  }, [page]);
  return (
    <nav ref={steps} aria-label="Tour stops" className="walk-steps">
      <button type="button" className="walk-step walk-step-end" onClick={() => open(0)}>
        <ChevronLeftIcon size={13} />
        Overview
      </button>
      {tour.chapters.map((chapter, index) => {
        const stops = chapter.stops.map((at) => tour.stops[at]!);
        const looked = stops.filter(done).length;
        return (
          <button
            key={chapter.id}
            type="button"
            title={chapter.title}
            aria-label={`${chapter.title}, ${looked} of ${stops.length} looked at`}
            aria-current={page === index + 1 ? "step" : undefined}
            className="walk-step"
            onClick={() => open(index + 1)}
          >
            <span className="walk-step-name">
              <RiskBadge level={chapter.level} />
              <span className="walk-step-title">{chapter.title}</span>
            </span>
            <span aria-hidden className="walk-step-dots">
              {stops.map((stop) => (
                <span
                  key={stop.id}
                  className={commented(stop) ? "commented" : done(stop) ? "seen" : undefined}
                />
              ))}
            </span>
          </button>
        );
      })}
      <button
        type="button"
        aria-current={page === last ? "step" : undefined}
        className="walk-step walk-step-end"
        onClick={() => open(last)}
      >
        Send review
        <ChevronRightIcon size={13} />
      </button>
    </nav>
  );
}

/** What matters about the pull request before reading it: where it stands
 *  on GitHub, what it does, what to look at closely and how risky it is,
 *  and its chapters; then the way into the tour. Only what says something
 *  is shown: a state with nothing to report is left out. */
function Landing({
  entry,
  document,
  tour,
  done,
  open,
  close,
  back,
  actions,
  notices,
  ask,
}: {
  entry: Entry;
  document: ReviewDocument;
  tour: Tour;
  done: (stop: Stop) => boolean;
  open: (page: number) => void;
  close: () => void;
  back: { href: string; label: string };
  actions?: ReactNode;
  notices?: ReactNode;
  ask?: { open: boolean; toggle: () => void };
}) {
  const pr = entry.pull_request;
  const level = tour.level;
  const unresolved = entry.unresolved ?? 0;
  const stats = document.stats;
  const started = tour.stops.some(done);
  const attention = document.summary.attention?.text?.trim();
  const attentionChapter = tour.chapters.findIndex(
    (chapter) => chapter.id === document.summary.attention?.refs?.[0],
  );
  const breaking = document.summary.breaking_change?.trim();
  return (
    <article aria-labelledby="walk-landing-title" className="walk-panel">
      <ViewerLink href={back.href} className="walk-back">
        <ChevronLeftIcon size={13} />
        {back.label}
      </ViewerLink>
      <div className="walk-meta">
        {pr.review_requested_from_me ? (
          <span className="walk-chip risk-3">Review requested</span>
        ) : pr.authored_by_me ? (
          <span className="walk-chip">Yours</span>
        ) : null}
        <span className="walk-where">
          {pr.repository} · #{pr.number}
          {pr.author && !pr.authored_by_me ? ` · ${pr.author}` : ""}
          {pr.draft ? " · Draft" : ""}
        </span>
        {tour.minutes ? <span className="walk-minutes">≈ {tour.minutes} min to review</span> : null}
      </div>
      <h1 id="walk-landing-title" className="walk-title">
        {pr.title}
      </h1>
      <div className="walk-facts">
        <PullRequestState entry={entry} />
        {/* The state above names one blocker; failing checks behind a
            conflict still need saying. */}
        {entry.checks === "failing" && entry.merge === "conflict" ? (
          <span className="walk-fact failing">Checks failing</span>
        ) : null}
        {unresolved > 0 ? (
          <span className="walk-fact caution">
            <CommentsIcon size={13} />
            {plural(unresolved, "unresolved thread")}
            {entry.unresolved_capped ? " or more" : ""}
          </span>
        ) : null}
        <span className="walk-fact">
          <span className="walk-add">+{stats.additions.toLocaleString("en-US")}</span>{" "}
          <span className="walk-del">−{stats.deletions.toLocaleString("en-US")}</span> in{" "}
          {plural(stats.files, "file")}
        </span>
      </div>
      {tour.intro ? <p className="walk-why">{tour.intro}</p> : null}
      <div className="walk-actions">
        <button type="button" className="walk-primary" onClick={() => open(1)}>
          {started ? "Continue the review" : "Start the review"}
          <kbd aria-hidden>→</kbd>
        </button>
        {actions}
        {ask ? <AskButton ask={ask} /> : null}
        <button type="button" className="walk-secondary" onClick={close}>
          Full review
          <kbd aria-hidden>v</kbd>
        </button>
        <a href={pr.url} target="_blank" rel="noreferrer" className="walk-secondary">
          GitHub
          <ExternalLinkIcon size={12} />
        </a>
      </div>
      {notices ? <div className="walk-notices">{notices}</div> : null}
      {document.review.head && pr.head_sha && pr.head_sha !== document.review.head ? (
        <p className="walk-flag">
          The pull request has new commits since this review was generated.
        </p>
      ) : null}
      {attention ? (
        <section className="walk-flag" aria-label="Look closely at">
          <h2 className="walk-label">Look closely at</h2>
          <p>
            {attention}{" "}
            {attentionChapter >= 0 ? (
              <button
                type="button"
                className="walk-link"
                onClick={() => open(attentionChapter + 1)}
              >
                Go to {tour.chapters[attentionChapter]!.title} →
              </button>
            ) : null}
          </p>
        </section>
      ) : null}
      {breaking ? (
        <section className="walk-flag breaking" aria-label="Breaking change">
          <h2 className="walk-label">Breaking change</h2>
          <p>{breaking}</p>
        </section>
      ) : null}
      {tour.risks.length || level ? (
        <RiskCard level={level} meaning={level ? RISK_SCORE_MEANING[level] : null}>
          {tour.risks.map((risk, index) => {
            // Several reasons can name one chapter: the first carries its link.
            const linked =
              risk.stop !== null &&
              tour.risks.findIndex((other) => other.stop === risk.stop) === index;
            const chapter = risk.stop === null ? null : tour.stops[risk.stop]!.chapter;
            return (
              <p key={risk.text}>
                {risk.text}{" "}
                {linked && chapter !== null ? (
                  <button type="button" className="walk-link" onClick={() => open(chapter + 1)}>
                    Go to {tour.chapters[chapter]!.title} →
                  </button>
                ) : null}
              </p>
            );
          })}
        </RiskCard>
      ) : null}
      <section className="walk-block" aria-label="The route">
        <h2 className="walk-label">The tour · riskiest first</h2>
        <ul>
          {tour.chapters.map((chapter, index) => (
            <li key={chapter.id}>
              <button type="button" className="walk-block-row" onClick={() => open(index + 1)}>
                <RiskBadge level={chapter.level} />
                <span className="walk-block-text">
                  <span>{chapter.title}</span>
                  <span className="walk-block-sub">
                    {plural(chapter.stops.length, "behaviour")}
                    {chapter.meaning ? ` · ${chapter.meaning.toLowerCase()}` : ""}
                    {` · ${chapter.stops.filter((at) => done(tour.stops[at]!)).length} looked at`}
                  </span>
                </span>
                <span className="walk-block-right">
                  <span className="walk-add">
                    +{chapter.lines.additions.toLocaleString("en-US")}
                  </span>{" "}
                  <span className="walk-del">
                    −{chapter.lines.deletions.toLocaleString("en-US")}
                  </span>
                </span>
              </button>
            </li>
          ))}
        </ul>
      </section>
      {tour.releaseNote ? (
        <section className="walk-block" aria-label="Release note">
          <h2 className="walk-label">Release note</h2>
          <p className="walk-lead">{tour.releaseNote}</p>
        </section>
      ) : null}
    </article>
  );
}

/** One chapter: its intent and risk once, then its behaviours, as the full
 *  review states them, the picked one with its actions. */
function ChapterPage({
  tour,
  chapter,
  page,
  document,
  cur,
  pick,
  done,
  notes,
  editing,
  draft,
  setDraft,
  startComment,
  saveComment,
  cancelComment,
  removeComment,
  ask,
  next,
  back,
}: {
  tour: Tour;
  chapter: Chapter;
  page: number;
  document: ReviewDocument;
  cur: number;
  pick: (at: number) => void;
  done: (stop: Stop) => boolean;
  notes: Record<string, string>;
  editing: string | null;
  draft: string;
  setDraft: (draft: string) => void;
  startComment: (stop: Stop) => void;
  saveComment: (stop: Stop) => void;
  cancelComment: () => void;
  removeComment: (stop: Stop) => void;
  /** Asks the review's agent about a behaviour. */
  ask?: (stop: Stop) => void;
  next: () => void;
  back: () => void;
}) {
  const following = tour.chapters[page];
  const looked = chapter.stops.filter((at) => done(tour.stops[at]!)).length;
  return (
    <article aria-labelledby={`walk-title-${chapter.id}`} className="walk-panel">
      <div className="walk-meta">
        <span className={cn("walk-chip", chapter.level && `risk-${chapter.level}`)}>
          Chapter {page} of {tour.chapters.length}
        </span>
        <span className="walk-where">
          {plural(chapter.stops.length, "behaviour")} · {looked} looked at
        </span>
      </div>
      <h2 id={`walk-title-${chapter.id}`} className="walk-title">
        {chapter.title}
      </h2>
      {chapter.summary ? <Markdown className="walk-why">{chapter.summary}</Markdown> : null}
      {chapter.intent ? (
        <section className="walk-block" aria-label="Intent">
          <h3 className="walk-label">Intent</h3>
          <Markdown className="walk-lead">{chapter.intent}</Markdown>
        </section>
      ) : null}
      {chapter.why || chapter.level || chapter.risks.length ? (
        <RiskCard level={chapter.level} meaning={chapter.meaning}>
          {chapter.why ? <p>{chapter.why}</p> : null}
          {chapter.risks.length ? (
            <ul className="walk-risks">
              {chapter.risks.map((risk) => (
                <li key={risk.text}>
                  <span className={cn("walk-tier", risk.tier)}>{risk.tier}</span>
                  <span>{risk.text}</span>
                </li>
              ))}
            </ul>
          ) : null}
        </RiskCard>
      ) : null}
      <section className="walk-block" aria-label="Behaviour">
        <h3 className="walk-label">Behaviour</h3>
        <ol className="walk-behaviours">
          {chapter.stops.map((at, index) => {
            const stop = tour.stops[at]!;
            return (
              <Behaviour
                key={stop.id}
                number={index + 1}
                stop={stop}
                document={document}
                picked={at === cur}
                pick={() => pick(at)}
                seen={done(stop)}
                note={notes[stop.id] ?? ""}
                editing={editing === stop.id}
                draft={draft}
                setDraft={setDraft}
                startComment={() => startComment(stop)}
                saveComment={() => saveComment(stop)}
                cancelComment={cancelComment}
                removeComment={() => removeComment(stop)}
                ask={ask ? () => ask(stop) : undefined}
              />
            );
          })}
        </ol>
      </section>
      <div className="walk-actions walk-page-foot">
        <button type="button" className="walk-primary" onClick={next}>
          {following ? `Next: ${following.title}` : "Wrap up"}
          <kbd aria-hidden>→</kbd>
        </button>
        <button type="button" className="walk-secondary" onClick={back}>
          <ChevronLeftIcon size={14} />
          Back
        </button>
      </div>
    </article>
  );
}

function Behaviour({
  number,
  stop,
  document,
  picked,
  pick,
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
  number: number;
  stop: Stop;
  document: ReviewDocument;
  picked: boolean;
  pick: () => void;
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
  const hasNote = Boolean(note.trim());
  const target = stop.where || "the review";
  return (
    // Picking is also on the keys and in the list; the click is a shortcut.
    <li
      id={`walk-behaviour-${stop.id}`}
      aria-current={picked ? "true" : undefined}
      className={cn("walk-behaviour", picked && "picked", hasNote && "commented")}
      onClick={pick}
    >
      <span className={cn("walk-behaviour-number", seen && "seen")}>{number}</span>
      <div className="walk-behaviour-body">
        <div className="walk-behaviour-head">
          <h4>{stop.title}</h4>
          {stop.evidence.length ? (
            <ul className="walk-evidence" aria-label="Supporting evidence">
              {stop.evidence.map((item) => (
                <li key={item.id}>
                  <EvidenceBadge evidence={item} compact />
                </li>
              ))}
            </ul>
          ) : null}
        </div>
        {stop.text && !echoes(stop) ? (
          <Markdown className="walk-behaviour-text">{stop.text}</Markdown>
        ) : null}
        <div className="walk-behaviour-foot">
          {stop.anchors.length ? (
            <div className="walk-code">
              <HunkEvidence document={document} anchors={stop.anchors} />
            </div>
          ) : null}
          {picked && !editing && !hasNote ? (
            <button
              type="button"
              className="walk-small"
              onClick={(event) => {
                event.stopPropagation();
                startComment();
              }}
            >
              <CommentsIcon size={12} />
              Something looks off
              <kbd aria-hidden>c</kbd>
            </button>
          ) : null}
          {picked && ask ? (
            <button
              type="button"
              className="walk-small"
              onClick={(event) => {
                event.stopPropagation();
                ask();
              }}
            >
              Ask about this
            </button>
          ) : null}
        </div>
        {editing ? (
          <div className="walk-comment">
            <label htmlFor={`walk-draft-${stop.id}`}>
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
            <div className="walk-actions">
              <button
                type="button"
                className="walk-primary"
                onClick={saveComment}
                disabled={!draft.trim()}
              >
                Save comment
              </button>
              <button type="button" className="walk-secondary" onClick={cancelComment}>
                {hasNote ? "Cancel" : "Never mind"}
              </button>
            </div>
          </div>
        ) : hasNote ? (
          <div className="walk-comment saved">
            <CommentsIcon size={16} className="walk-comment-icon" />
            <div className="walk-comment-body">
              <span className="walk-comment-where">Your comment on {target}</span>
              <p>{note}</p>
            </div>
            <button type="button" className="walk-small" onClick={startComment}>
              Edit
            </button>
            <button type="button" className="walk-small" onClick={removeComment}>
              Remove
            </button>
          </div>
        ) : null}
      </div>
    </li>
  );
}

function WrapUp({
  tour,
  unseen,
  seenCount,
  commentCount,
  pick,
  submit,
}: {
  tour: Tour;
  unseen: Stop[];
  seenCount: number;
  commentCount: number;
  pick: (at: number) => void;
  submit: ReactNode;
}) {
  return (
    <article aria-labelledby="walk-wrap-title" className="walk-panel">
      <div className="walk-meta">
        <span className="walk-chip">Submit</span>
        <span className="walk-where">
          {seenCount} of {tour.stops.length} looked at ·{" "}
          {commentCount ? plural(commentCount, "comment") : "no comments"}
        </span>
      </div>
      <h2 id="walk-wrap-title" className="walk-title">
        Send your review
      </h2>
      <p className="walk-why">
        That’s the whole change: {plural(tour.stops.length, "behaviour")} in{" "}
        {plural(tour.chapters.length, "chapter")}.
      </p>
      {unseen.length ? (
        <section className="walk-block" aria-label="Not looked at yet">
          <h3 className="walk-label">
            {unseen.length === 1
              ? "1 behaviour you haven’t looked at"
              : `${unseen.length} behaviours you haven’t looked at`}
          </h3>
          <ul>
            {unseen.map((stop) => (
              <li key={stop.id}>
                <button
                  type="button"
                  className="walk-block-row"
                  onClick={() => pick(tour.stops.indexOf(stop))}
                >
                  <Mark state={null} />
                  <span className="walk-block-text">
                    <span>{stop.title}</span>
                    <span className="walk-block-sub">
                      {tour.chapters[stop.chapter]!.title}
                      {stop.where ? ` · ${stop.where}` : ""}
                    </span>
                  </span>
                  <ChevronRightIcon size={14} className="walk-block-right" />
                </button>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
      <section className="walk-block" aria-label="Your review">
        <h3 className="walk-label">Your review</h3>
        {submit}
      </section>
    </article>
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
          ? `Ask the author to address ${plural(count, "comment")}`
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
      <div role="status" className="walk-sent">
        <p>
          <CheckIcon size={14} /> Your review is on GitHub.
        </p>
        <a href={sending.url} target="_blank" rel="noreferrer">
          Open it on GitHub <ExternalLinkIcon size={12} />
        </a>
        <button type="button" className="walk-small" onClick={() => setSending({ state: "idle" })}>
          Write another
        </button>
      </div>
    );
  return (
    <div className="walk-submit">
      <div role="radiogroup" aria-label="Review verdict" className="walk-events">
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
            <span className="walk-event-text">
              <span className="walk-event-label">{e.label}</span>
              <span className="walk-event-hint">{e.hint}</span>
            </span>
          </button>
        ))}
      </div>
      <label className="walk-field">
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
        <div className="walk-field">
          <p>{plural(count, "comment")}, each on its evidence line:</p>
          <ul className="walk-notes">
            {comments.map((stop) => (
              <li key={stop.id}>
                <span>
                  {stop.title}
                  {stop.where ? ` · ${stop.where}` : " · in the review body"}
                </span>
                <p>{progress.notes[stop.id]}</p>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {sending.state === "failed" ? (
        <p role="alert" className="walk-failed">
          {sending.message}
          {sending.code === "invalid_line" ? (
            <>
              {" "}
              <button type="button" className="walk-small" onClick={() => void send(false)}>
                Put the comments in the review body instead
              </button>
            </>
          ) : null}
        </p>
      ) : null}
      <div className="walk-actions">
        <button
          type="button"
          className="walk-primary"
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
        </button>
        <span className="walk-skipped">
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

/** The text with the last occurrence of a part taken out, along with the blank
 *  line that joined it; the text unchanged when the part is no longer in it. */
function withoutLast(text: string, part: string): string {
  const at = text.lastIndexOf(part);
  if (at < 0) return text;
  const before = text.slice(0, at).replace(/\s+$/, "");
  const after = text.slice(at + part.length).replace(/^\s+/, "");
  return before && after ? `${before}\n\n${after}` : before || after;
}
