import type { ReactNode } from "react";
import pluralize from "pluralize";

import {
  humanize,
  type ReviewDocument,
  type Section,
  sectionBadgeState,
  sectionsAreScored,
  sectionsInReviewOrder,
  sectionStateTone,
  type TriageRisk,
} from "../review-document";
import { riskKey } from "../row-keys";
import { Item, ItemMeta, JumpLink, Lines, None, ReviewScoreBadge, RiskTags } from "./review-parts";
import { Markdown } from "./markdown";
import { Badge, CardHeader } from "./ui";

/** The two group panes behind the rail's Changes and Risks rows: everything
 *  the group holds in one place, so a reader can see how much is left before
 *  they start clicking into it.
 *
 *  Both take their checkbox from the caller rather than building one. The host
 *  decides whether a row is an approval it saves or a read mark, and a list
 *  that renders the same rows has no business deciding that again and
 *  differently. Without one, a row has no box. */

/** The worst open risk in a current section, as the row's meta line names it:
 *  none when the review found none, and none on an obsolete section, whose
 *  tier is always stored unrated. */
function worstRisk(section: Section): string | null {
  const tier = section.derived.risk_tier;
  if (section.risks.length === 0 || section.derived.state === "stale") return null;
  return tier === "unrated" || tier === "mechanical" ? null : tier;
}

export function ChangesOverview({
  document,
  onSelectSection,
  renderCheck,
}: {
  document: ReviewDocument;
  onSelectSection: (id: string) => void;
  /** The row-action slot: a host's read mark or approval beside each row. */
  renderCheck?: (section: Section) => ReactNode;
}) {
  const { stats } = document;
  const scored = sectionsAreScored(document.sections);
  return (
    <>
      <CardHeader
        size="pane"
        title="Changes"
        detail={
          <>
            {pluralize("section", document.sections.length, true)}
            {` · ${pluralize("file", stats.files, true)} · `}
            <Lines additions={stats.additions} deletions={stats.deletions} />
            {scored && " · most important to review first"}
          </>
        }
      />
      <div className="min-h-0 flex-1 xl:overflow-y-auto">
        {document.sections.length === 0 ? (
          <None>This review has no sections.</None>
        ) : (
          <ul className="space-y-1.5">
            {sectionsInReviewOrder(document.sections).map((section) => {
              const check = renderCheck?.(section);
              const badgeState = sectionBadgeState(section.derived.state, document.review);
              return (
                <Item
                  key={section.id}
                  className="relative flex items-start gap-2.5 hover:border-(--border-strong) hover:bg-(--surface-hover)"
                >
                  {/* Above the title's overlay, so the box is still a box. */}
                  {check ? <span className="relative z-10 flex-none">{check}</span> : null}
                  <div className="min-w-0 flex-1 space-y-1.5">
                    <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                      {/* The whole row opens the section, and does it without a
                       *  second control saying the same thing: the row is one
                       *  destination, so it stays one button, stretched over
                       *  the card it names. */}
                      <JumpLink
                        className="font-medium"
                        tone="primary"
                        onClick={() => onSelectSection(section.id)}
                      >
                        {section.title} →
                        <span aria-hidden className="absolute inset-0" />
                      </JumpLink>
                      {section.derived.review_score !== undefined && (
                        <ReviewScoreBadge score={section.derived.review_score} withMeaning />
                      )}
                      {badgeState !== "unchanged" && (
                        <Badge size="compact" tone={sectionStateTone(badgeState)}>
                          {humanize(badgeState)}
                        </Badge>
                      )}
                    </div>
                    <Markdown className="text-(--text-primary)">{section.summary.text}</Markdown>
                    {section.derived.review_reason && (
                      <Markdown className="text-(--text-secondary)">
                        {section.derived.review_reason.text}
                      </Markdown>
                    )}
                    <ItemMeta>
                      <Lines
                        additions={section.derived.additions}
                        deletions={section.derived.deletions}
                      />
                      <span>·</span>
                      <span>
                        {pluralize("risk", section.risks.length, true)}
                        {worstRisk(section) && `, worst ${worstRisk(section)}`}
                      </span>
                      {section.derived.open_findings > 0 && (
                        <>
                          <span>·</span>
                          <span>
                            {pluralize("open finding", section.derived.open_findings, true)}
                          </span>
                        </>
                      )}
                    </ItemMeta>
                  </div>
                </Item>
              );
            })}
          </ul>
        )}
      </div>
    </>
  );
}

export function RisksOverview({
  risks,
  onSelectRisk,
  onSelectSection,
  renderCheck,
}: {
  risks: TriageRisk[];
  onSelectRisk: (risk: TriageRisk) => void;
  onSelectSection: (id: string) => void;
  /** The row-action slot: a host's read mark beside each row. */
  renderCheck?: (risk: TriageRisk) => ReactNode;
}) {
  return (
    <>
      <CardHeader
        size="pane"
        title="Critical risks"
        detail={
          risks.length > 0
            ? `${pluralize("critical or high risk", risks.length, true)}, worst first. Medium and low risks read with the section they belong to.`
            : "Medium and low risks read with the section they belong to."
        }
      />
      <div className="min-h-0 flex-1 xl:overflow-y-auto">
        {risks.length === 0 ? (
          <None>No critical or high risk was identified in this review.</None>
        ) : (
          <ul className="space-y-1.5">
            {risks.map((risk) => {
              const check = renderCheck?.(risk);
              return (
                <Item
                  key={riskKey(risk.section, risk.id)}
                  className="relative flex items-start gap-2.5 hover:border-(--border-strong) hover:bg-(--surface-hover)"
                >
                  {/* Above the risk's overlay, so the box is still a box. */}
                  {check ? <span className="relative z-10 flex-none">{check}</span> : null}
                  <div className="min-w-0 flex-1 space-y-1.5">
                    <RiskTags
                      tier={risk.tier}
                      category={risk.category}
                      sectionState={risk.sectionState}
                      urgent
                    />
                    {/* All of the row opens the risk, the way the Changes rows
                     *  open a section: one destination, so one button, stretched
                     *  over the card it names. */}
                    <Markdown className="pointer-events-none relative z-10 text-(--text-secondary) [&_a]:pointer-events-auto">
                      {risk.text}
                    </Markdown>
                    <JumpLink
                      tone="secondary"
                      onClick={() => onSelectRisk(risk)}
                      className="absolute inset-0"
                    >
                      <span className="sr-only">Open risk: {risk.text}</span>
                    </JumpLink>
                    <span aria-hidden className="pointer-events-none text-(--text-secondary)">
                      Open risk →
                    </span>
                    <ItemMeta>
                      {/* The one part of the row that goes somewhere else, so it
                       *  sits above the overlay rather than under it. */}
                      <JumpLink
                        className="relative z-10"
                        onClick={() => onSelectSection(risk.section)}
                      >
                        In {risk.sectionTitle}
                      </JumpLink>
                    </ItemMeta>
                  </div>
                </Item>
              );
            })}
          </ul>
        )}
      </div>
    </>
  );
}
