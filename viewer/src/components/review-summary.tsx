import pluralize from "pluralize";
import { useState } from "react";

import { harnessName } from "../agent-names";
import {
  behaviourChanges,
  type BehaviourSection,
  type IntentLine,
  intentPrompts,
  type ReviewDocument,
  summaryIntent,
  riskScoreTone,
  riskScoreView,
  type Tone,
  type TriageRisk,
  triageRisks,
} from "../review-document";
import { QuotedPrompts } from "./chat";
import { StatusBlock, StatusLine } from "./editorial";
import { Markdown } from "./markdown";
import { formatRelative } from "./format";
import { SectionIcon } from "./icons";
import { Item, ItemMeta, JumpLink, Lines, None, Part, PROSE, RiskTags } from "./review-parts";
import { cn } from "./classnames";
import { Badge, CardHeader, SegmentedControl } from "./ui";

/** The document-level view: what the pull request does, why, and where the
 *  risk is. The sections themselves are one click away in the rail and in the
 *  list here; the pull request's standing facts (checks, author, size) and how
 *  it came about (the review exchange, sessions, decisions) are tabs of the
 *  conversation pane, so they stay in view whatever pane is open. */
export type SummaryDestination = "changes" | "risks" | "files" | "sessions" | "team";

export function ReviewSummary({
  document,
  onSelectSection,
}: {
  document: ReviewDocument;
  onSelectSection: (id: string) => void;
  /** Opens one of the review's other panes. The "view all" links and the
   *  other jump links go through it; without it they are not drawn. */
  onNavigate?: (destination: SummaryDestination) => void;
}) {
  const { summary } = document;
  const intent = summaryIntent(document);
  const prompts = intentPrompts(document, intent);
  // An agent with several sessions here numbers them, so they read apart.
  const sessionsOf = (vendor: string) => [
    ...new Set(prompts.filter((each) => each.vendor === vendor).map((each) => each.session)),
  ];
  const sessionLabel = (prompt: (typeof prompts)[number]) => {
    const agent = harnessName(prompt.vendor);
    const sessions = sessionsOf(prompt.vendor);
    return sessions.length > 1 ? `${agent} session ${sessions.indexOf(prompt.session) + 1}` : agent;
  };
  const triage = triageRisks(document);
  const behaviour = behaviourChanges(document);

  return (
    <>
      <div className="w-full flex-none">
        {/* The heading stays the one word, so it is the heading's name; the
            freshness sits on the line under it. */}
        <CardHeader
          size="pane"
          title="Summary"
          detail={
            <span className="inline-flex flex-wrap items-center gap-x-2.5 gap-y-1">
              <span className="inline-flex items-center gap-1.5">
                <span aria-hidden className="h-1.5 w-1.5 rounded-full bg-(--status-positive)" />
                Updated{" "}
                {formatRelative(
                  document.review.evidence_refreshed_at || document.review.generated_at,
                )}
              </span>
              <span>
                · Review v{document.review.version} · generated{" "}
                {formatRelative(document.review.generated_at)}
              </span>
            </span>
          }
        />
      </div>
      <div className="min-h-0 flex-1 xl:overflow-y-auto">
        <div className="min-w-0 space-y-8">
          {document.review.generator.review_md_changed && (
            <StatusLine tone="caution" label="Review policy changed" boxed>
              {document.review.generator.review_md
                ? "This pull request changes REVIEW.md. The review followed the base branch's REVIEW.md, not the version in this pull request."
                : "This pull request changes REVIEW.md. The base branch has no REVIEW.md, so the review used the default review policy."}
            </StatusLine>
          )}
          <div className="space-y-3">
            <Markdown className={PROSE}>{summary.text}</Markdown>
            {summary.breaking_change && (
              <StatusBlock tone="critical" label="Breaking change">
                <Markdown className="max-w-[90ch] text-base leading-[1.5] [overflow-wrap:anywhere] text-(--text-secondary)">
                  {summary.breaking_change}
                </Markdown>
              </StatusBlock>
            )}
            {summary.release_note && (
              <StatusBlock tone="neutral" label="Release note">
                <Markdown className="max-w-[90ch] text-base leading-[1.5] [overflow-wrap:anywhere] text-(--text-secondary)">
                  {summary.release_note}
                </Markdown>
              </StatusBlock>
            )}
          </div>

          {intent.length > 0 && (
            <Part label="Intent and instructions">
              <ul className="space-y-2.5">
                {intent.map((line, position) => (
                  <IntentRow
                    key={`${line.section}-${position}`}
                    line={line}
                    onSelectSection={onSelectSection}
                  />
                ))}
              </ul>
              {prompts.length > 0 && (
                <details className="mt-3">
                  <summary className="cursor-pointer text-base text-(--text-link) hover:underline">
                    From {pluralize("session prompt", prompts.length, true)}
                  </summary>
                  <QuotedPrompts
                    className="mt-2"
                    prompts={prompts.map((prompt) => ({
                      ...prompt,
                      session: { id: prompt.session, label: sessionLabel(prompt) },
                    }))}
                  />
                </details>
              )}
            </Part>
          )}

          {/* Always shown: the risk score is part of it, and says "not
           *  assessed" rather than disappearing. */}
          <div className="space-y-8">
            {/* The score says how closely to read before the changes say
             *  what to read, so it leads. */}
            <RiskScorePart document={document} onSelectSection={onSelectSection} />
            {behaviour.length > 0 && (
              <KeyChanges changes={behaviour} onSelectSection={onSelectSection} />
            )}
            {triage.lead.length > 0 && (
              <Part
                label="Risks"
                aside={
                  <>
                    <span className="text-xs text-(--text-tertiary)">
                      {triage.lead.length > 0
                        ? `${pluralize("risk", triage.lead.length, true)} to read first`
                        : "None to read first"}
                    </span>
                  </>
                }
              >
                {triage.lead.length > 0 ? (
                  <ul className="space-y-1.5">
                    {triage.lead.map((risk) => (
                      <RiskRow
                        key={`${risk.section}-${risk.id}`}
                        risk={risk}
                        onSelectSection={onSelectSection}
                        urgent
                      />
                    ))}
                  </ul>
                ) : (
                  <None>No critical or high risk was identified.</None>
                )}
                {triage.rest.length > 0 && (
                  <details className="mt-2.5">
                    <summary className="cursor-pointer text-base text-(--text-link) hover:underline">
                      {pluralize("lower-tier risk", triage.rest.length, true)}
                    </summary>
                    <ul className="mt-2 space-y-1.5">
                      {triage.rest.map((risk) => (
                        <RiskRow
                          key={`${risk.section}-${risk.id}`}
                          risk={risk}
                          onSelectSection={onSelectSection}
                        />
                      ))}
                    </ul>
                  </details>
                )}
              </Part>
            )}
          </div>
        </div>
      </div>
    </>
  );
}

/** The score's colour, as text and as a meter segment's fill. */
const RISK_TEXT: Record<Tone, string> = {
  neutral: "text-(--text-primary)",
  info: "text-(--status-info)",
  positive: "text-(--status-positive)",
  caution: "text-(--status-caution)",
  critical: "text-(--status-critical)",
};
const RISK_FILL: Record<Tone, string> = {
  neutral: "bg-(--text-secondary)",
  info: "bg-(--status-info)",
  positive: "bg-(--status-positive)",
  caution: "bg-(--status-caution)",
  critical: "bg-(--status-critical)",
};

/** Five segments filled up to the score. A lower bound outlines the
 *  segments above it, since the score may reach them. */
function RiskMeter({
  score,
  lowerBound,
  tone,
}: {
  score: number;
  lowerBound: boolean;
  tone: Tone;
}) {
  return (
    <span aria-hidden className="flex flex-none gap-1">
      {[1, 2, 3, 4, 5].map((step) => (
        <span
          key={step}
          className={cn(
            "h-2 w-5 rounded-[2px]",
            step <= score
              ? RISK_FILL[tone]
              : lowerBound
                ? "border border-dashed border-(--border-strong)"
                : "bg-(--border-default)",
          )}
        />
      ))}
    </span>
  );
}

/** The pull request's inherent risk on the rubric's 1–5 scale, read off the
 *  document rather than judged, with its strongest reasons and the way to
 *  their sections. A score that incomplete input may understate says so and
 *  says what was missing. It leads the risk parts, whose Risks part below carries
 *  the way to every risk, so it carries none of its own. */
function RiskScorePart({
  document,
  onSelectSection,
}: {
  document: ReviewDocument;
  onSelectSection: (id: string) => void;
}) {
  const view = riskScoreView(document);
  return (
    <Part label="Risk score">
      {view.assessed ? (
        <div className="space-y-2">
          <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-base">
            <RiskMeter
              score={view.score}
              lowerBound={view.lowerBound}
              tone={riskScoreTone(view.score)}
            />
            <span>
              <span
                className={cn("font-medium tabular-nums", RISK_TEXT[riskScoreTone(view.score)])}
              >
                {view.lowerBound ? `At least ${view.score}` : view.score} of 5
              </span>
              {/* A lower bound names no meaning: "negligible" would read as a
                  verdict on input the review could not rate. */}
              {!view.lowerBound && (
                <span className="text-(--text-secondary)"> · {view.meaning}</span>
              )}
            </span>
          </p>
          {view.reasons.length > 0 && (
            <ul className="space-y-1.5">
              {view.reasons.map((reason, index) => (
                <li
                  key={`${reason.section}-${index}`}
                  className="max-w-[90ch] text-base text-(--text-primary)"
                >
                  <Markdown className="inline [&>p]:inline">{reason.text}</Markdown>{" "}
                  <JumpLink onClick={() => onSelectSection(reason.section)}>
                    {reason.sectionTitle} →
                  </JumpLink>
                </li>
              ))}
            </ul>
          )}
        </div>
      ) : (
        <p className="flex flex-wrap items-baseline gap-x-2 gap-y-1 text-base">
          <span className="font-medium">Not assessed</span>
          <span className="text-(--text-secondary)">
            {view.legacy
              ? "This review was generated before risk scores. Regenerate it to assess."
              : "No current section of this review was rated."}
          </span>
        </p>
      )}
      {view.limitations.length > 0 && (
        <p className="mt-2 text-xs text-(--text-tertiary)">
          Incomplete input: {view.limitations.join(" · ")}
        </p>
      )}
    </Part>
  );
}

/** One stated reason, with the section that argued it. */
function IntentRow({
  line,
  onSelectSection,
}: {
  line: IntentLine;
  onSelectSection: (id: string) => void;
}) {
  return (
    <li className="flex gap-2.5">
      <span
        aria-hidden
        className="mt-[0.55rem] h-1 w-1 flex-none rounded-full bg-(--text-tertiary)"
      />
      <div className="max-w-[90ch] text-base leading-[1.6] text-(--text-primary)">
        <Markdown className="inline [&>p]:inline">{line.text}</Markdown>{" "}
        <JumpLink onClick={() => onSelectSection(line.section)}>{line.title} →</JumpLink>
      </div>
    </li>
  );
}

/** One triage risk. `urgent` is what the must-read list passes: there high
 *  risk earns the same red as critical, where in the expanded list below it
 *  keeps its own tone so the two groups stay tellable apart. */
function RiskRow({
  risk,
  onSelectSection,
  urgent = false,
}: {
  risk: TriageRisk;
  onSelectSection: (id: string) => void;
  urgent?: boolean;
}) {
  return (
    <Item className="space-y-1.5">
      <RiskTags
        tier={risk.tier}
        category={risk.category}
        sectionState={risk.sectionState}
        urgent={urgent}
      />
      <Markdown className="text-(--text-primary)">{risk.text}</Markdown>
      <ItemMeta>
        <JumpLink onClick={() => onSelectSection(risk.section)}>{risk.sectionTitle} →</JumpLink>
      </ItemMeta>
    </Item>
  );
}

/** What this revision changed, one row per section, folded to a line each:
 *  the title, its state, the surfaces it touches and its size, with the
 *  behaviour steps a click away. The first row starts open so the list is
 *  never a wall of closed drawers, and a strip of surface filters narrows the
 *  rows when the document names more than one surface. */
function KeyChanges({
  changes,
  onSelectSection,
}: {
  changes: BehaviourSection[];
  onSelectSection: (id: string) => void;
}) {
  const [surface, setSurface] = useState("all");
  const surfaces = [...new Set(changes.flatMap((change) => change.surfaces))];
  const shown = surface === "all" ? changes : changes.filter((c) => c.surfaces.includes(surface));
  return (
    <Part
      label="Key changes"
      aside={
        surfaces.length > 1 ? (
          <SegmentedControl
            label="Filter key changes by surface"
            value={surface}
            onChange={setSurface}
            items={[
              { value: "all", label: `All ${changes.length}` },
              ...surfaces.map((item) => ({
                value: item,
                label: `${item} ${changes.filter((c) => c.surfaces.includes(item)).length}`,
              })),
            ]}
          />
        ) : undefined
      }
    >
      <ul className="divide-y divide-(--border-subtle)">
        {shown.map((change) => (
          <KeyChangeRow key={change.section} change={change} onSelectSection={onSelectSection} />
        ))}
      </ul>
    </Part>
  );
}

function KeyChangeRow({
  change,
  onSelectSection,
}: {
  change: BehaviourSection;
  onSelectSection: (id: string) => void;
}) {
  return (
    <li>
      <div className="flex items-center gap-2.5 py-2">
        <SectionIcon size={14} className="flex-none text-(--action-accent)" />
        <span className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1">
          <JumpLink className="font-medium" onClick={() => onSelectSection(change.section)}>
            {change.title} →
          </JumpLink>
          {change.surfaces.map((item) => (
            <Badge key={item} size="compact" tone="info">
              {item}
            </Badge>
          ))}
        </span>
        <span className="flex-none text-base">
          <Lines additions={change.additions} deletions={change.deletions} />
        </span>
      </div>
    </li>
  );
}
