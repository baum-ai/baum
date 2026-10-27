import { type ReactNode, useEffect, useRef, useState } from "react";

import { ViewerLink } from "../navigation";
import {
  type GenerationState,
  generationTone,
  humanize,
  pullRequestStateTone,
  pullRequestUrl,
  type ReviewDocument,
  assessedRisk,
  reviewGeneration,
  shortSha,
} from "../review-document";
import { cn, FOCUS_RING } from "./classnames";
import { formatDate, formatLinesCompact, formatRelative } from "./format";
import { BranchIcon, ChevronLeftIcon, GitHubIcon } from "./icons";
import { ChangeKindTag, Lines, RiskScoreBadge } from "./review-parts";
import { Badge, LiveDot } from "./ui";

/** The review page's own gutter. Narrower than a dashboard's: the page is a
 *  workspace that fills the window, and forty pixels of margin either side
 *  is width the panes want more than the page does. */
export const REVIEW_GUTTER = "px-4 lg:px-6";

/** The height of the bar that stands in for the head once it has scrolled
 *  away, in pixels. The rail and the side pane stick beneath it, the rail's
 *  height (`3rem` less than the window) leaves room for it, and a host should
 *  pad the page's scroll by it (`scroll-padding-top` on a page holding
 *  `[data-review-page]`) so that a jump does not land a heading under the
 *  bar. */
export const REVIEW_BAR_PX = 48;

/** The pull request facts a host knows better than the document does. Each
 *  one is optional and falls back to what the document recorded when it was
 *  generated. */
export type PullRequestFacts = {
  number?: number;
  title?: string;
  /** The live state, "open", "merged" or "closed". The document's own is a
   *  snapshot of when it was generated. */
  state?: string;
  repository?: string;
  url?: string;
  branches?: { head: string; base: string };
  lines?: { additions: number; deletions: number };
};

/** What a host knows of the review's generation beyond the document: a
 *  newer state than the one the document carries, and whether the revision
 *  it describes is being written again. */
export type GenerationView = { state?: GenerationState; rewriting?: boolean };

/** The head's optional parts, each supplied by a host. Absent, the head
 *  draws only what the document says. */
export type ReviewHeaderSlots = {
  /** The head's title for a review of something other than one pull request,
   *  such as a stack's: it replaces the number and title, and the document's
   *  own pull request (its number, link and state) is not shown. */
  heading?: string;
  /** Additional status beside the full page title. */
  titleAside?: ReactNode;
  pullRequest?: PullRequestFacts;
  /** The way back to wherever the reader came from, drawn in the compact bar
   *  through the host's link component. */
  back?: { href: string; label: string };
  /** An approval state, shown beside the title and in the compact bar. */
  approval?: ReactNode;
  /** The pull request's figures, drawn as the head's last line. */
  statistics?: ReactNode;
  /** Drawn beside the version menu, or on its own line under it on a
   *  narrow screen, such as where the pull request sits in a stack. */
  beside?: ReactNode;
  generation?: GenerationView;
};

function Dot() {
  return (
    <span aria-hidden className="text-(--text-disabled)">
      •
    </span>
  );
}

/** A small bordered label: a branch, a count, a fact the header states in
 *  passing. */
function Chip({
  children,
  mono = false,
  className,
}: {
  children: ReactNode;
  mono?: boolean;
  className?: string;
}) {
  return (
    <span
      className={cn(
        "inline-flex h-7 items-center gap-1.5 rounded-(--radius-md) border border-(--border-default) bg-(--surface-card) px-2 text-base text-(--text-secondary)",
        mono && "font-mono text-xs",
        className,
      )}
    >
      {children}
    </span>
  );
}

/** A branch as a chip. `fit` lets a long name shrink to an ellipsis, for a
 *  row that must stay one line; the full name is the chip's title. */
function BranchChip({
  name,
  fit = false,
  className,
}: {
  name: string;
  fit?: boolean;
  className?: string;
}) {
  return (
    <Chip mono className={cn(fit && "min-w-0", className)}>
      <BranchIcon size={13} className="flex-none text-(--text-tertiary)" />
      <span className={fit ? "truncate" : undefined} title={name}>
        {name}
      </span>
    </Chip>
  );
}

/** The review's age and status beside the version menu: when its evidence
 *  was refreshed, a live dot while
 *  a new review is being written, and a badge only for a review that needs
 *  attention (outdated or failed). */
export function ReviewFreshness({
  document,
  generation,
  className,
  facts,
}: {
  document: ReviewDocument;
  generation?: GenerationView;
  className?: string;
  facts?: PullRequestFacts;
}) {
  const { state, generating, updated } = reviewGeneration(document, generation);
  const generatedFor = document.review.pr_state;
  const superseded = facts?.state && generatedFor && facts.state !== generatedFor;
  return (
    <span
      title={`Version ${document.review.version}, generated ${formatDate(document.review.generated_at)}${document.review.head ? ` against ${shortSha(document.review.head)}` : ""}`}
      className={cn(
        "inline-flex flex-wrap items-center gap-2 text-base text-(--text-tertiary)",
        className,
      )}
    >
      {generating ? (
        <span
          title="A new review is being generated"
          className="inline-flex items-center text-(--status-info)"
        >
          <LiveDot />
          <span className="sr-only">A new review is being generated</span>
        </span>
      ) : null}
      {updated ? (
        <span title={`Evidence refreshed ${formatDate(updated)}`}>{formatRelative(updated)}</span>
      ) : null}
      {superseded ? (
        <Badge tone="caution">Generated while {humanize(generatedFor).toLowerCase()}</Badge>
      ) : null}
      {state === "outdated" || state === "failed" ? (
        <Badge tone={generationTone(state)}>
          {humanize(state)}
          {generating ? " · Generating" : ""}
        </Badge>
      ) : generating ? (
        <span>Generating</span>
      ) : null}
    </span>
  );
}

/** The page's head: a bar with the way back and the pull request's standing
 *  facts, and under it the title with everything that says what state the
 *  review is in. Every figure here is one the page already holds — the
 *  branches and the size from the host, the version, state and refresh time
 *  from the review document. */
export function ReviewHeader({
  document,
  slots = {},
}: {
  document: ReviewDocument | null;
  slots?: ReviewHeaderSlots;
}) {
  const facts = slots.pullRequest ?? {};
  const heading = slots.heading;
  // A heading names something other than the document's pull request, so
  // none of that pull request's own facts stand in for the host's.
  const ofPullRequest = heading === undefined ? document : null;
  const number = facts.number ?? ofPullRequest?.review.pull_request;
  const title = facts.title ?? document?.summary.title;
  const repositoryFullName = facts.repository ?? document?.review.repository;
  const githubUrl = facts.url || (ofPullRequest ? pullRequestUrl(ofPullRequest) : undefined);
  // A generated review holds a snapshot of the pull request as it was when it
  // was written, so its `pr_state` is not news about the pull request: a review
  // generated while one was open still says "open" long after it merged. The
  // live state is what the badge states, and the snapshot only stands in when
  // nothing live has arrived.
  const state = facts.state ?? ofPullRequest?.review.pr_state;
  // The head's figures carry the score; the compact bar, which has no
  // figures, carries it as a pill. Not assessed shows nothing in either: the
  // Summary says why, and a "not assessed" mark would read as a finding.
  const risk = assessedRisk(document);
  // The estimate is the head's alone: it says what reading the review costs,
  // which is of use before the reader starts, not once the bar stands in. The
  // document is untrusted, so only what the schema allows shows: a whole
  // number of minutes from 1 to 240.
  const minutes = ofPullRequest?.summary.review_minutes;
  const reviewMinutes =
    typeof minutes === "number" && Number.isInteger(minutes) && minutes >= 1 && minutes <= 240
      ? minutes
      : null;
  // The page scrolls as one: the head goes with the content, and once it has
  // gone a single line keeps the pull request in view. The bar is always in
  // the document so that it can fade, and hidden from readers and the tab
  // order until the head is out of sight.
  const head = useRef<HTMLElement>(null);
  const [compact, setCompact] = useState(false);
  useEffect(() => {
    const element = head.current;
    if (!element) return;
    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry) setCompact(!entry.isIntersecting && entry.boundingClientRect.top < 0);
      },
      { rootMargin: `-${REVIEW_BAR_PX}px 0px 0px 0px` },
    );
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  return (
    <>
      <div className="sticky top-0 z-(--z-sticky) hidden h-0 lg:block">
        <div
          aria-hidden={!compact}
          style={{ height: REVIEW_BAR_PX }}
          className={cn(
            REVIEW_GUTTER,
            "absolute inset-x-0 top-0 flex items-center gap-3 border-b border-(--border-subtle) bg-(--surface-page) transition-[opacity,visibility] duration-150 motion-reduce:transition-none",
            compact ? "visible opacity-100" : "invisible opacity-0",
          )}
        >
          {slots.back ? (
            <ViewerLink
              href={slots.back.href}
              aria-label={slots.back.label}
              className={cn(
                "inline-flex h-7 w-7 flex-none items-center justify-center rounded-(--radius-sm) text-(--text-tertiary) hover:bg-(--surface-hover) hover:text-(--text-secondary)",
                FOCUS_RING,
              )}
            >
              <ChevronLeftIcon size={14} />
            </ViewerLink>
          ) : null}
          {state ? (
            <Badge tone={pullRequestStateTone(state)} className="flex-none">
              <span aria-hidden className="mr-1.5 h-1.5 w-1.5 rounded-full bg-current" />
              {humanize(state)}
            </Badge>
          ) : null}
          {risk ? <RiskScoreBadge view={risk} className="flex-none" /> : null}
          <button
            type="button"
            onClick={() => window.scrollTo({ top: 0 })}
            title="Back to the top"
            className={cn(
              "min-w-0 cursor-pointer truncate rounded-(--radius-sm) text-left text-base font-medium",
              FOCUS_RING,
            )}
          >
            {heading !== undefined ? (
              heading
            ) : number !== undefined ? (
              <>
                {title ? `${title} ` : ""}
                <span className="text-(--text-tertiary)">#{number}</span>
              </>
            ) : (
              "Review preview"
            )}
          </button>
          <div className="ml-auto flex flex-none items-center gap-3">
            {slots.approval ?? null}
            {facts.lines ? (
              <span className="text-base tabular-nums">
                <Lines
                  additions={facts.lines.additions}
                  deletions={facts.lines.deletions}
                  format={formatLinesCompact}
                />
              </span>
            ) : null}
          </div>
        </div>
      </div>
      <header ref={head} className={cn(REVIEW_GUTTER, "border-b border-(--border-subtle)")}>
        <div className="pb-4">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-2 pt-5">
            <h1 className="text-2xl font-medium tracking-[-0.015em]">
              {heading !== undefined ? (
                heading
              ) : number !== undefined ? (
                <>
                  <span className="text-(--text-tertiary)">#{number}</span>
                  {title ? ` ${title}` : ""}
                </>
              ) : (
                "Review preview"
              )}
            </h1>
            {document?.summary.change_kind ? (
              <ChangeKindTag kind={document.summary.change_kind} />
            ) : null}
            {slots.titleAside}
            {state ? (
              <Badge tone={pullRequestStateTone(state)}>
                <span aria-hidden className="mr-1.5 h-1.5 w-1.5 rounded-full bg-current" />
                {humanize(state)}
              </Badge>
            ) : null}
            {slots.approval ? (
              <span aria-label="Review approval state">{slots.approval}</span>
            ) : null}
            {!document && number !== undefined ? <Badge tone="info">Preview</Badge> : null}
          </div>
          <div className="flex flex-wrap items-center gap-2 pt-2 text-base text-(--text-secondary)">
            {repositoryFullName ? <span>{repositoryFullName}</span> : null}
            {/* Beside the repository from sm; on a phone, a row of their own. */}
            {facts.branches ? (
              <span className="hidden sm:contents">
                <Dot />
                <BranchChip name={facts.branches.head} />
                <span aria-hidden className="text-(--text-tertiary)">
                  →
                </span>
                <BranchChip name={facts.branches.base} />
              </span>
            ) : null}
            {reviewMinutes !== null ? (
              <>
                {repositoryFullName ? <Dot /> : null}
                <span
                  title={`Estimated time to read this review: about ${reviewMinutes} ${
                    reviewMinutes === 1 ? "minute" : "minutes"
                  }`}
                >
                  ~{reviewMinutes} min to review
                </span>
              </>
            ) : null}
            {githubUrl ? (
              <a
                href={githubUrl}
                target="_blank"
                rel="noopener noreferrer"
                aria-label="View on GitHub"
                title="View on GitHub"
                className={cn(
                  "inline-flex h-7 w-7 items-center justify-center rounded-(--radius-sm) text-(--text-tertiary) hover:bg-(--action-secondary-hover) hover:text-(--text-primary)",
                  FOCUS_RING,
                )}
              >
                <GitHubIcon size={16} />
              </a>
            ) : null}
          </div>
          {/* One line on a phone: the branches shrink to an ellipsis rather
          than wrapping onto a second line. */}
          {facts.branches ? (
            <div className="flex min-w-0 items-center gap-2 pt-2 sm:hidden">
              <BranchChip name={facts.branches.head} fit />
              <span aria-hidden className="flex-none text-(--text-tertiary)">
                →
              </span>
              {/* The branch it merges into is the one a reader checks, so it
                keeps up to half the row and the head branch gives way. */}
              <BranchChip name={facts.branches.base} fit className="max-w-1/2 flex-none" />
            </div>
          ) : null}
        </div>
        {slots.statistics ? (
          <div className="border-t border-(--border-subtle) py-3">{slots.statistics}</div>
        ) : null}
      </header>
    </>
  );
}
