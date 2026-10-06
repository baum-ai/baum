import type { ReactNode } from "react";

import {
  type Anchor,
  anchorFile,
  humanize,
  type ReviewDocument,
  resolveAnchor,
  RISK_SCORE_MEANING,
  riskCategory,
  type RiskScoreLevel,
  type RiskTier,
  reviewScoreTone,
  riskScoreTone,
  type RiskScoreView,
  type SectionTier,
  type SectionState,
  sectionStateTone,
  shortSha,
  TIER_COLOR,
  tierTone,
  type Tone,
} from "../review-document";
import { cn, FOCUS_RING } from "./classnames";
import { CodeFileLink } from "./code-file-link";
import { ExternalLinkIcon } from "./icons";
import { Badge } from "./ui";

/** The small pieces every pane of a review document is built from. */

export function TierDot({ tier, className }: { tier: RiskTier | "unrated"; className?: string }) {
  return (
    <span
      aria-hidden
      title={`${tier} risk`}
      className={cn("inline-block h-2 w-2 flex-none rounded-full", className)}
      style={{ background: TIER_COLOR[tier] }}
    />
  );
}

/** `tone` overrides the tier's usual colour. Triage passes it: in a list whose
 *  whole purpose is "read these first", high risk earns the same red as
 *  critical, where in the body of a section it would drown out critical.
 *  An unrated section is one with no open risk, which says nothing of how much
 *  it matters: beside a review score of 4, "Unrated" read as a contradiction.
 *  `risks` is the section's count including mitigated ones, which keep it
 *  unrated without meaning none were found. */
export function TierBadge({
  tier,
  tone,
  risks = 0,
}: {
  tier: SectionTier;
  tone?: Tone;
  risks?: number;
}) {
  const label =
    tier === "mechanical"
      ? "Mechanical"
      : tier === "unrated"
        ? risks > 0
          ? "No open risks"
          : "No risks found"
        : `${tier[0]!.toUpperCase()}${tier.slice(1)} risk`;
  return <Badge tone={tone ?? tierTone(tier)}>{label}</Badge>;
}

/** A section's review score on the risk score's 1–5 scale. `withMeaning`
 *  spells out what the level means where there is room; the rail has none,
 *  so it shows the number and says the meaning in the row's description. */
export function ReviewScoreBadge({
  score,
  withMeaning = false,
  size,
}: {
  score: RiskScoreLevel;
  withMeaning?: boolean;
  size?: "standard" | "compact";
}) {
  const meaning = RISK_SCORE_MEANING[score];
  const spoken = `Review score ${score} of 5: ${meaning}`;
  // One text node for what is seen: the badge is a flex box, which would trim
  // the space between the number and a separate meaning.
  return (
    <Badge size={size} tone={reviewScoreTone(score)} title={spoken}>
      <span aria-hidden>{withMeaning ? `${score} · ${meaning}` : score}</span>
      <span className="sr-only">{spoken}</span>
    </Badge>
  );
}

/** The pull request's risk score as a compact pill for the review head. A
 *  lower bound reads "Risk ≥4/5" and names no meaning, as in the summary. The
 *  visible text is decoration; the spoken name carries the words. */
export function RiskScoreBadge({
  view,
  className,
}: {
  view: Extract<RiskScoreView, { assessed: true }>;
  className?: string;
}) {
  const spoken = view.lowerBound
    ? `Risk score at least ${view.score} of 5`
    : `Risk score ${view.score} of 5: ${view.meaning}`;
  return (
    <Badge tone={riskScoreTone(view.score)} title={spoken} className={className}>
      <span aria-hidden className="tabular-nums">
        Risk {view.lowerBound ? "≥" : ""}
        {view.score}/5
      </span>
      <span className="sr-only">{spoken}</span>
    </Badge>
  );
}

/** What kind of risk this is — permissions, data loss, performance — beside
 *  the tier rather than in the meta line under it. The category is what tells
 *  a reader whether a risk is theirs to answer, so a reader scanning a list of
 *  them should not have to read the sentence to find out. Not a `Badge`: those
 *  tones are the reserved status scale, and a category is not a state. */
export function RiskCategoryTag({ category }: { category?: string | null }) {
  // A document is read without validation, and an empty tag is a worse answer
  // than no tag: it reads as a category whose name did not fit.
  const label = riskCategory({ category }, "");
  if (!label) return null;
  return (
    <span className="inline-flex items-center rounded-(--radius-sm) border border-(--border-default) bg-(--surface-sunken) px-1.5 py-0.5 text-xs font-semibold whitespace-nowrap text-(--text-primary)">
      {label}
    </span>
  );
}

/** The tags every rendering of a risk leads with: what tier it is, what kind of
 *  risk it is, and whether the section it came from has moved since the last
 *  review.
 *
 *  One component because the summary's triage list and the rail's Risks list
 *  show the same risks, and two copies of "what a risk looks like" would drift
 *  into telling a reader two different things about one risk. On their own row,
 *  so the sentence under them starts in the same place however many wrap.
 *
 *  `urgent` is what a must-read list passes: there high risk earns the same red
 *  as critical, where in the body of a section it would drown critical out. */
export function RiskTags({
  tier,
  category,
  sectionState,
  urgent = false,
}: {
  tier: RiskTier;
  category?: string | null;
  sectionState: SectionState;
  urgent?: boolean;
}) {
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
      <TierBadge tier={tier} tone={urgent ? "critical" : undefined} />
      <RiskCategoryTag category={category} />
      {sectionState !== "unchanged" && (
        <Badge size="compact" tone={sectionStateTone(sectionState)}>
          {humanize(sectionState)}
        </Badge>
      )}
    </div>
  );
}

/** What kind of change this is — feature, fix, refactor — as the first thing
 *  the summary says. Deliberately not a `Badge`: those tones are the reserved
 *  status scale, and a change kind is a category, not a state. */
export function ChangeKindTag({ kind }: { kind: string }) {
  return (
    <span className="inline-flex items-center rounded-(--radius-sm) border border-(--accent-200) bg-(--accent-50) px-2 py-1 text-xs font-semibold tracking-[0.16em] uppercase text-(--accent-700)">
      {humanize(kind)}
    </span>
  );
}

/** A commit, as the short sha, linked to it on GitHub. Every sha the review
 *  shows is a place a reader may want to go; none of them should be dead
 *  text they have to copy out by hand. */
export function CommitLink({
  document,
  sha,
  className,
}: {
  document: ReviewDocument;
  sha: string;
  className?: string;
}) {
  return (
    <a
      href={`https://github.com/${document.review.repository}/commit/${sha}`}
      target="_blank"
      rel="noreferrer"
      title={sha}
      className={cn("font-mono text-base", FOCUS_RING, className)}
    >
      {shortSha(sha)}
    </a>
  );
}

/** A diff stat with the two halves coloured the way every diff colours them:
 *  green for what arrived, red for what left.
 *
 *  These are the status tones rather than chart slots. Added and removed are
 *  not two series of one measure that a legend tells apart — they are the two
 *  directions a line can go, and the convention for them is older and better
 *  known than this palette. `formatLines` stays for the places that need the
 *  same figure as plain text, since a tooltip cannot carry colour. */
export function Lines({
  additions,
  deletions,
  format = (value: number) => value.toLocaleString("en-US"),
  separator = " ",
  className,
}: {
  additions: number;
  deletions: number;
  /** How each figure is written. The statistics strip abbreviates; a diff stat
   *  beside the code it describes does not. */
  format?: (value: number) => string;
  separator?: ReactNode;
  className?: string;
}) {
  return (
    <span className={cn("tabular-nums whitespace-nowrap", className)}>
      <span className="text-(--status-positive)">+{format(additions)}</span>
      {separator}
      <span className="text-(--status-critical)">−{format(deletions)}</span>
    </span>
  );
}

/** A titled run of content inside a pane, so a section reads as one card with
 *  several parts rather than a stack of cards.
 *
 *  The hairline closes the header rather than separating the parts: a rule
 *  drawn between them floats in the gutter with nothing on either side, which
 *  reads as a stray mark. Underlining the label ties it to what it names. */
export function Part({
  label,
  aside,
  as: Heading = "h3",
  children,
  className,
}: {
  label: string;
  aside?: ReactNode;
  /** A part nested under a group heading is a level below it. */
  as?: "h3" | "h4";
  children: ReactNode;
  className?: string;
}) {
  return (
    <section aria-label={label} className={cn("min-w-0", className)}>
      <div className="mb-3 flex items-center justify-between gap-3 border-b border-(--border-subtle) pb-1.5">
        <Heading className="text-base font-semibold tracking-[0.08em] text-(--text-primary) uppercase">
          {label}
        </Heading>
        {aside ? <div className="flex flex-none items-center gap-2">{aside}</div> : null}
      </div>
      {children}
    </section>
  );
}

/** Body copy for a generated paragraph: measured, a touch larger, secondary. */
export const PROSE = "max-w-[90ch] text-base leading-[1.7] text-(--text-primary)";

/** A dashed pale row: the shape of "nothing here", which for this document is
 *  itself a statement — an empty risk list means none were identified. */
export function None({ children }: { children: ReactNode }) {
  return (
    <p className="rounded-(--radius-md) border border-dashed border-(--border-default) px-3 py-2.5 text-base text-(--text-tertiary)">
      {children}
    </p>
  );
}

const chipClass = cn(
  "inline-flex max-w-full min-w-0 items-center gap-1 rounded-(--radius-sm) border border-(--border-subtle) bg-(--surface-sunken) px-1.5 py-0.5 text-xs leading-[1.4] text-(--text-secondary)",
);

/** The sources behind a judgment, as chips. A chip for code opens the file in
 *  the review's file view; one with a URL opens GitHub in a new tab at the
 *  comment or check; one without — a prompt, an issue key, a spec assertion —
 *  carries its detail in a tooltip and stays inline. */
export function AnchorChips({
  document,
  anchors,
  className,
}: {
  document: ReviewDocument;
  anchors: Anchor[] | undefined;
  className?: string;
}) {
  if (!anchors?.length) return null;
  return (
    <ul className={cn("flex flex-wrap gap-1.5", className)} aria-label="Sources">
      {anchors.map((anchor, position) => {
        const resolved = resolveAnchor(document, anchor);
        const title = resolved.title ? `${resolved.kind}: ${resolved.title}` : resolved.kind;
        const label = <span className="min-w-0 truncate">{resolved.label}</span>;
        const file = anchorFile(document, anchor);
        return (
          <li key={`${anchor.type}-${position}`} className="max-w-full min-w-0">
            {file ? (
              <CodeFileLink
                path={file.path}
                line={file.line}
                href={resolved.href}
                title={title}
                className={cn(
                  chipClass,
                  "hover:border-(--border-default) hover:text-(--text-primary) hover:no-underline",
                )}
              >
                {label}
              </CodeFileLink>
            ) : resolved.href ? (
              <a
                href={resolved.href}
                target="_blank"
                rel="noreferrer"
                title={title}
                className={cn(
                  chipClass,
                  "hover:border-(--border-default) hover:text-(--text-primary) hover:no-underline",
                  FOCUS_RING,
                )}
              >
                {label}
                <ExternalLinkIcon size={10} className="flex-none text-(--text-tertiary)" />
              </a>
            ) : (
              <span title={title} className={chipClass}>
                {label}
              </span>
            )}
          </li>
        );
      })}
    </ul>
  );
}

/** A row in a list of items — a risk, an evidence claim, a finding. The id is
 *  what an attention line's reference scrolls to. */
export function Item({
  id,
  children,
  className,
}: {
  id?: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <li
      id={id}
      className={cn(
        "rounded-(--radius-md) border border-(--border-subtle) bg-(--surface-card) px-3 py-2.5 text-base scroll-mt-4 target:border-(--border-focus)",
        className,
      )}
    >
      {children}
    </li>
  );
}

/** The quiet second line under an item: category, source, who said it. */
export function ItemMeta({ children }: { children: ReactNode }) {
  return (
    <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-(--text-tertiary)">
      {children}
    </p>
  );
}

/** A definition list laid out as label/value pairs. */
export function Facts({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-1.5 text-base">
      {items.map((item) => (
        <div key={item.label} className="contents">
          <dt className="text-(--text-tertiary)">{item.label}</dt>
          <dd className="min-w-0 text-(--text-primary) [overflow-wrap:anywhere]">{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}

const JUMP_LINK_TONE = {
  link: "text-(--text-link) hover:text-(--text-link-hover)",
  primary: "text-(--text-primary) hover:text-(--text-link)",
  secondary: "text-(--text-secondary) hover:text-(--text-link)",
} as const;

/** An in-pane jump — to another section, or to an item further down. Styled as
 *  a link rather than a button, because that is what it does. The colour is a
 *  variant rather than a class the caller appends, because `cn` does not
 *  resolve competing utilities. */
export function JumpLink({
  onClick,
  children,
  className,
  tone = "link",
}: {
  onClick: () => void;
  children: ReactNode;
  className?: string;
  tone?: keyof typeof JUMP_LINK_TONE;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "cursor-pointer rounded-(--radius-sm) text-left hover:underline",
        JUMP_LINK_TONE[tone],
        FOCUS_RING,
        className,
      )}
    >
      {children}
    </button>
  );
}
