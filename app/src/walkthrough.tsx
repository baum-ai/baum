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
  SearchIcon,
} from "@baum/viewer/components/icons";
import { Markdown } from "@baum/viewer/components/markdown";
import { EvidenceBadge } from "@baum/viewer/components/review-section";
import {
  RISK_SCORE_MEANING,
  type ReviewDocument,
  type RiskScoreLevel,
} from "@baum/viewer/review-document";
import { LocalRequestError, request, type Entry } from "./api";
import { useShortcuts } from "./shortcuts";
import {
  buildTour,
  commentedStops,
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

const plural = (count: number, one: string, many = `${one}s`) =>
  `${count} ${count === 1 ? one : many}`;

/** Whether the statement only repeats the title, as a one-sentence step
 *  written before steps had titles does. */
const echoes = (stop: Stop) => stop.text.replace(/[.!?]\s*$/, "") === stop.title;

/** The review as a guided tour of its behaviours, riskiest chapter first.
 *  Nobody has to sign off on every behaviour:
 *  moving past a stop counts as having looked at it,
 *  and a comment is the only way to say something looks off. The comments go
 *  to GitHub as one review, each on the line its behaviour cites. It is drawn
 *  as the inbox is: the stops listed on the left, the one being read beside
 *  them. */
export function Walkthrough({
  entry,
  document,
  close,
  patches,
  changed,
}: {
  entry: Entry;
  document: ReviewDocument;
  /** Back to the full review. */
  close: () => void;
  patches: HunkPatchSource;
  changed: () => Promise<void>;
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
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const detail = useRef<HTMLDivElement>(null);
  // Each stop starts at its top, as a new page would.
  useEffect(() => {
    detail.current?.scrollTo?.({ top: 0 });
  }, [step]);

  const go = (next: number) => {
    next = Math.max(0, Math.min(last, next));
    const leaving = tour.stops[step - 1];
    if (leaving && next !== step) markSeen(leaving);
    setStep(next);
    setEditing(null);
  };
  const forward = () => go(step + 1);
  const startComment = (stop: Stop) => {
    setDraft(progress.notes[stop.id] ?? "");
    setEditing(stop.id);
  };

  const claim = step >= 1 && step < last ? tour.stops[step - 1]! : null;
  useShortcuts((event) => {
    const key = event.key;
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

  return (
    <section aria-label="Walkthrough" className="walkthrough">
      <header className="walk-head">
        <div className="walk-head-text">
          <h1>
            <span className="walk-head-number">#{entry.pull_request.number}</span>{" "}
            {entry.pull_request.title}
          </h1>
          <p>
            {entry.pull_request.repository} · Review v{document.review.version} ·{" "}
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
          <button type="button" className="walk-secondary" onClick={close}>
            Full review
          </button>
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
        <div className="walk-split">
          <StopList
            tour={tour}
            step={step}
            done={done}
            commented={commented}
            seenCount={seenCount}
            commentCount={commentCount}
            go={go}
          />
          <div ref={detail} className="walk-detail">
            {step === 0 ? (
              <Intro
                entry={entry}
                document={document}
                tour={tour}
                done={done}
                go={go}
                start={forward}
              />
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
                next={forward}
                back={() => go(step - 1)}
                nextLabel={step === tour.stops.length ? "Wrap up" : "Next"}
              />
            ) : (
              <WrapUp
                tour={tour}
                unseen={unseen}
                seenCount={seenCount}
                commentCount={commentCount}
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
          </div>
        </div>
      </HunkPatchProvider>
    </section>
  );
}

/** A stop's state as the inbox marks a row: an empty ring, a tick once
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

/** The tour's stops, grouped by chapter as the inbox groups its queue. */
function StopList({
  tour,
  step,
  done,
  commented,
  seenCount,
  commentCount,
  go,
}: {
  tour: Tour;
  step: number;
  done: (stop: Stop) => boolean;
  commented: (stop: Stop) => boolean;
  seenCount: number;
  commentCount: number;
  go: (step: number) => void;
}) {
  const last = tour.stops.length + 1;
  const list = useRef<HTMLElement>(null);
  useEffect(() => {
    list.current?.querySelector("[aria-current]")?.scrollIntoView?.({ block: "nearest" });
  }, [step]);
  const row = (at: number, title: string, sub: ReactNode, mark: ReactNode, label = title) => (
    <li key={at}>
      <button
        type="button"
        onClick={() => go(at)}
        aria-label={label}
        aria-current={at === step ? "step" : undefined}
        className="walk-row"
      >
        {mark}
        <span className="walk-row-text">
          <span className="walk-row-title">{title}</span>
          {sub ? <span className="walk-row-sub">{sub}</span> : null}
        </span>
      </button>
    </li>
  );
  return (
    <nav ref={list} aria-label="Tour stops" className="walk-list">
      <div className="walk-list-body">
        <ul>
          {row(
            0,
            "Overview",
            "What it does, and the route",
            <span aria-hidden className="walk-mark walk-mark-end">
              <SearchIcon size={10} />
            </span>,
          )}
        </ul>
        {tour.chapters.map((chapter, index) => (
          <section key={chapter.id} aria-label={chapter.title} className="walk-group">
            <div className="walk-group-head">
              <h2>
                {index + 1} · {chapter.title}
              </h2>
              <RiskBadge level={chapter.level} />
            </div>
            <ul>
              {chapter.stops.map((at) => {
                const stop = tour.stops[at]!;
                const state = commented(stop) ? "commented" : done(stop) ? "seen" : null;
                return row(
                  at + 1,
                  stop.title,
                  state === "commented" ? (
                    <span className="walk-row-commented">Your comment</span>
                  ) : (
                    stop.where
                  ),
                  <Mark state={state} />,
                  stop.title +
                    (state === "commented"
                      ? ", has your comment"
                      : state === "seen"
                        ? ", looked at"
                        : ""),
                );
              })}
            </ul>
          </section>
        ))}
        <section aria-label="Submit" className="walk-group">
          <ul>
            {row(
              last,
              "Send your review",
              `${seenCount} of ${tour.stops.length} looked at · ${commentCount ? plural(commentCount, "comment") : "no comments"}`,
              <span aria-hidden className="walk-mark walk-mark-end">
                <ChevronRightIcon size={10} />
              </span>,
            )}
          </ul>
        </section>
      </div>
      <footer className="walk-keys" aria-hidden>
        <span>
          <kbd>←</kbd> <kbd>→</kbd> move
        </span>
        <span>
          <kbd>c</kbd> comment
        </span>
      </footer>
    </nav>
  );
}

function Intro({
  entry,
  document,
  tour,
  done,
  go,
  start,
}: {
  entry: Entry;
  document: ReviewDocument;
  tour: Tour;
  done: (stop: Stop) => boolean;
  go: (step: number) => void;
  start: () => void;
}) {
  const level = tour.level;
  return (
    <article aria-labelledby="walk-intro-title" className="walk-panel">
      <div className="walk-meta">
        <span className="walk-chip">Overview</span>
        <span className="walk-where">
          {entry.pull_request.repository} · #{entry.pull_request.number} · Review v
          {document.review.version}
        </span>
        {tour.minutes ? <span className="walk-minutes">≈ {tour.minutes} min</span> : null}
      </div>
      <h2 id="walk-intro-title" className="walk-title">
        What this pull request does
      </h2>
      {tour.intro ? <p className="walk-why">{tour.intro}</p> : null}
      <div className="walk-actions">
        <button type="button" className="walk-primary" onClick={start}>
          Start the tour
          <kbd aria-hidden>→</kbd>
        </button>
      </div>
      {tour.risks.length || level ? (
        <RiskCard level={level} meaning={level ? RISK_SCORE_MEANING[level] : null}>
          {tour.risks.map((risk, index) => {
            // Several reasons can name one chapter: the first carries its link.
            const linked =
              risk.stop !== null &&
              tour.risks.findIndex((other) => other.stop === risk.stop) === index;
            return (
              <p key={risk.text}>
                {risk.text}{" "}
                {linked ? (
                  <button type="button" className="walk-link" onClick={() => go(risk.stop! + 1)}>
                    Go to {tour.chapters[tour.stops[risk.stop!]!.chapter]!.title} →
                  </button>
                ) : null}
              </p>
            );
          })}
        </RiskCard>
      ) : null}
      <section className="walk-block" aria-label="The route">
        <h3 className="walk-label">The route · riskiest first</h3>
        <ul>
          {tour.chapters.map((chapter, index) => (
            <li key={chapter.id}>
              <button
                type="button"
                className="walk-block-row"
                onClick={() => go(chapter.stops[0]! + 1)}
              >
                <span className="walk-number">{index + 1}</span>
                <span className="walk-block-text">
                  <span>{chapter.title}</span>
                  <span className="walk-block-sub">
                    {plural(chapter.stops.length, "behaviour")}
                    {chapter.meaning ? ` · ${chapter.meaning.toLowerCase()}` : ""}
                  </span>
                </span>
                <ChapterProgress tour={tour} chapter={chapter} done={done} />
                <RiskBadge level={chapter.level} />
              </button>
            </li>
          ))}
        </ul>
      </section>
      {tour.releaseNote ? (
        <section className="walk-block" aria-label="Release note">
          <h3 className="walk-label">Release note</h3>
          <p className="walk-lead">{tour.releaseNote}</p>
        </section>
      ) : null}
      <p className="walk-note">
        Read each stop and move on. You only need to act when something looks off: leave a comment
        and it goes into the review. The code is one click away when you want it.
      </p>
    </article>
  );
}

function ChapterProgress({
  tour,
  chapter,
  done,
}: {
  tour: Tour;
  chapter: Chapter;
  done: (stop: Stop) => boolean;
}) {
  const count = chapter.stops.filter((at) => done(tour.stops[at]!)).length;
  return (
    <span className="walk-block-right">
      {count}/{chapter.stops.length}
    </span>
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
  next,
  back,
  nextLabel,
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
  next: () => void;
  back: () => void;
  nextLabel: string;
}) {
  const chapter = tour.chapters[stop.chapter]!;
  const hasNote = Boolean(note.trim());
  const target = stop.where || "the review";
  const position = chapter.stops.indexOf(tour.stops.indexOf(stop)) + 1;
  return (
    <article aria-labelledby={`walk-title-${stop.id}`} className="walk-panel">
      <div className="walk-meta">
        <span className={cn("walk-chip", chapter.level && `risk-${chapter.level}`)}>
          Chapter {stop.chapter + 1} · {chapter.title}
        </span>
        <span className="walk-where">
          Behaviour {position} of {chapter.stops.length}
        </span>
        {hasNote ? (
          <span className="walk-state commented">Your comment</span>
        ) : seen ? (
          <span className="walk-state">
            <CheckIcon size={12} /> Looked at
          </span>
        ) : null}
      </div>
      <h2
        id={`walk-title-${stop.id}`}
        className={cn("walk-title", stop.title.length > 90 && "long")}
      >
        {stop.title}
      </h2>
      {stop.text && !echoes(stop) ? <Markdown className="walk-why">{stop.text}</Markdown> : null}
      {stop.evidence.length ? (
        <ul className="walk-evidence" aria-label="Supporting evidence">
          {stop.evidence.map((item) => (
            <li key={item.id}>
              <EvidenceBadge evidence={item} compact />
            </li>
          ))}
        </ul>
      ) : null}
      {editing ? null : (
        <div className="walk-actions">
          <button type="button" className="walk-primary" onClick={next}>
            {nextLabel}
            <kbd aria-hidden>→</kbd>
          </button>
          {hasNote ? null : (
            <button type="button" className="walk-secondary" onClick={startComment}>
              <CommentsIcon size={14} />
              Something looks off
              <kbd aria-hidden>c</kbd>
            </button>
          )}
          <button type="button" className="walk-secondary" onClick={back}>
            <ChevronLeftIcon size={14} />
            Back
          </button>
        </div>
      )}
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
      {chapter.intent ? (
        <section className="walk-block" aria-label="Intent">
          <h3 className="walk-label">Intent</h3>
          <Markdown className="walk-lead">{chapter.intent}</Markdown>
        </section>
      ) : null}
      {stop.anchors.length ? (
        <section className="walk-block walk-code" aria-label="The code">
          <h3 className="walk-label">
            The code{stop.where ? <span className="walk-label-where"> · {stop.where}</span> : null}
          </h3>
          <HunkEvidence document={document} anchors={stop.anchors} />
        </section>
      ) : null}
      {chapter.why || chapter.level ? (
        <RiskCard level={chapter.level} meaning={chapter.meaning}>
          {chapter.why ? (
            <p>
              <strong>{chapter.title}. </strong>
              {chapter.why}
            </p>
          ) : null}
        </RiskCard>
      ) : null}
    </article>
  );
}

function WrapUp({
  tour,
  unseen,
  seenCount,
  commentCount,
  go,
  submit,
}: {
  tour: Tour;
  unseen: Stop[];
  seenCount: number;
  commentCount: number;
  go: (step: number) => void;
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
                  onClick={() => go(tour.stops.indexOf(stop) + 1)}
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
