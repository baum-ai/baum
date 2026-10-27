import pluralize from "pluralize";
import type { ReactNode } from "react";
import { useUrlState, ViewerLink } from "../navigation";
import { withSelection } from "../review-selection";

import {
  type EntityContentGroup,
  entityGroupStroke,
  entityGroupWash,
  groupApiContent,
} from "../review-entity-groups";
import {
  type Anchor,
  type Block,
  type Evidence,
  decisionGroups,
  evidenceTone,
  findingStateTone,
  humanize,
  type ReviewDocument,
  type LoadReviewVersion,
  repositoryLink,
  riskEvidenceReviewVersion,
  riskLifecycleLabel,
  riskLifecycleTone,
  type Section,
  sectionFindings,
  sectionHunks,
  sectionBadgeState,
  sectionStateLabel,
  sectionStateTone,
  severityTone,
} from "../review-document";
import { cn } from "./classnames";
import { HunkEvidence } from "./hunk-evidence";
import { InlineMarkdown, Markdown } from "./markdown";
import { CheckIcon, ExternalLinkIcon, RiskIcon } from "./icons";
import {
  AnchorChips,
  CommitLink,
  DecisionContext,
  Item,
  ItemMeta,
  JumpLink,
  Lines,
  Part,
  PROSE,
  ReviewScoreBadge,
  TierBadge,
} from "./review-parts";
import { ReviewImages } from "./review-images";
import { ReviewTiles } from "./review-tiles/review-tiles";
import { type Column, Table } from "./table";
import { Badge, CardHeader } from "./ui";

/** A section always resolves its content and anchors against the supplied document. */
export function ReviewSection({
  document,
  section,
  onSelectSection,
  loadVersion,
  activeVersions,
  idPrefix = "",
  showTiles = false,
  labels,
}: {
  document: ReviewDocument;
  section: Section;
  onSelectSection: (id: string) => void;
  loadVersion?: LoadReviewVersion;
  /** The review versions in which this section had hunks, once read. */
  activeVersions?: readonly number[];
  idPrefix?: string;
  /** Tiles are held back from reviews until they are ready to show. */
  showTiles?: boolean;
  /** The host's labels for the section, at the end of its badges. */
  labels?: ReactNode;
}) {
  const { derived } = section;
  const badgeState = sectionBadgeState(derived.state, document.review);
  const files = sectionHunks(document, section.hunks);
  const findings = sectionFindings(document, section.id);
  const roles = Object.entries(derived.role_loc).filter(([, lines]) => lines && lines > 0);
  // A "none" entry records that no source named a reviewer; it is not a person.
  const reviewers = derived.reviewers.filter(
    (reviewer) => reviewer.login && reviewer.source !== "none",
  );
  const itemId = (ref: string) => `${idPrefix}${section.id}-${ref}`;
  // Tiles zoom into what the change does, so they belong beside the behaviour
  // steps. A section that states no behaviour has no such place, and dropping
  // its tiles would lose them, so there they stay where they used to sit.
  const behaviorStatesStep = section.blocks.some((block) => block.kind === "behavior");
  const tiles = showTiles ? <SectionTiles document={document} section={section} /> : null;

  const attention = section.attention && {
    ...section.attention,
    refs: section.attention.refs.filter((ref) => {
      const risk = section.risks.find((item) => item.id === ref);
      return !risk || risk.tier === "critical" || risk.tier === "high";
    }),
  };
  const showAttention =
    attention && (section.attention!.refs.length === 0 || attention.refs.length > 0);
  const attentionRiskIds = new Set(attention?.refs ?? []);
  const attentionRisks = section.risks.filter((risk) => attentionRiskIds.has(risk.id));
  const otherRisks = section.risks.filter((risk) => !attentionRiskIds.has(risk.id));
  const renderRisk = (risk: Section["risks"][number], position: number, inAttention = false) => {
    const Container = inAttention ? "li" : Item;
    return (
      <Container key={risk.id} id={itemId(risk.id)} className="flex gap-3">
        {!inAttention && (
          <span className="w-5 flex-none pt-px text-right text-base tabular-nums text-(--text-tertiary)">
            {position + 1}
          </span>
        )}
        <div className="min-w-0 flex-1 space-y-1.5">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <TierBadge tier={risk.tier} />
            {riskLifecycleLabel(risk, document.review, section.derived.state) &&
              (inAttention ? (
                <span className="text-xs text-(--text-tertiary)">
                  {riskLifecycleLabel(risk, document.review, section.derived.state)}
                </span>
              ) : (
                <Badge size="compact" tone={riskLifecycleTone(risk, section.derived.state)}>
                  {riskLifecycleLabel(risk, document.review, section.derived.state)}
                </Badge>
              ))}
          </div>
          <Markdown className="text-(--text-primary)">{risk.text}</Markdown>
          {showTiles && <ReviewTiles document={document} tiles={risk.tiles} />}
          <HunkEvidence
            document={document}
            anchors={risk.anchors}
            diffClassName={inAttention ? undefined : "-ml-8 sm:ml-0"}
            historicalVersion={riskEvidenceReviewVersion(
              risk,
              derived.state,
              document.review.version,
              activeVersions,
            )}
            loadVersion={loadVersion}
          />
        </div>
      </Container>
    );
  };

  return (
    <>
      {/* The pills sit above the title rather than beside it, so a long title
          keeps the pane's width and the pills read as a line of their own. */}
      <div className="mb-2 flex flex-wrap items-center gap-2">
        {/* The score that ranked the section, so a reader who lands here
         *  sees why, beside what the review found in it. */}
        {derived.review_score !== undefined && (
          <ReviewScoreBadge score={derived.review_score} withMeaning />
        )}
        {/* An obsolete section is always stored unrated, whatever risks it
         *  kept, so its tier would claim none were found. */}
        {derived.state !== "stale" && (
          <TierBadge tier={derived.risk_tier} risks={section.risks.length} />
        )}
        {/* "Changed" is what nearly every section is, so only the states that
         *  say something else are named. */}
        {badgeState !== "unchanged" && badgeState !== "changed" && (
          <Badge tone={sectionStateTone(badgeState)}>{sectionStateLabel(badgeState)}</Badge>
        )}
        {labels}
      </div>
      <CardHeader
        size="pane"
        title={section.title}
        detail={
          <>
            <Lines additions={derived.additions} deletions={derived.deletions} />
            {roles.length > 0 &&
              ` · ${roles.map(([role, lines]) => `${humanize(role)} ${lines!.toLocaleString("en-US")}`).join(" · ")}`}
            {` · ${pluralize("hunk", section.hunks.length, true)} in ${pluralize("file", files.length, true)}`}
            {reviewers.length > 0 &&
              ` · Reviewer ${reviewers
                .map((reviewer) => `${reviewer.login} (${humanize(reviewer.source).toLowerCase()})`)
                .join(", ")}`}
            {derived.state === "unchanged" && document.review.previous_version
              ? ` · Unchanged since v${document.review.previous_version}`
              : ""}
          </>
        }
      />
      <div className="min-h-0 flex-1 space-y-7 xl:overflow-y-auto">
        <div className="space-y-2">
          <Markdown className={PROSE}>{section.summary.text}</Markdown>
          <AnchorChips document={document} anchors={withoutHunks(section.summary.anchors)} />
        </div>

        {showAttention && (
          <AttentionCallout
            attention={attention}
            section={section}
            onJump={(ref) => scrollToItem(itemId(ref))}
            risks={attentionRisks.map((risk, position) => renderRisk(risk, position, true))}
          />
        )}

        {!behaviorStatesStep && tiles}

        {section.blocks.map((block, position) => (
          <BlockView
            key={`${block.kind}-${position}`}
            document={document}
            section={section}
            block={block}
            onSelectSection={onSelectSection}
            idPrefix={idPrefix}
            tiles={block.kind === "behavior" ? tiles : null}
          />
        ))}

        {otherRisks.length > 0 && (
          <Part label="Risks">
            <ol className="space-y-2">
              {otherRisks.map((risk, position) => renderRisk(risk, position))}
            </ol>
          </Part>
        )}

        {findings.length > 0 && (
          <Part label="Agent findings">
            <ul className="space-y-1.5">
              {findings.map((finding) => (
                <Item key={finding.id} id={itemId(finding.id)} className="space-y-1.5">
                  <div className="flex flex-wrap items-start gap-x-2 gap-y-1">
                    <Badge tone={severityTone(finding.severity)} size="compact">
                      {humanize(finding.severity)}
                    </Badge>
                    <Badge tone={findingStateTone(finding.state)} size="compact">
                      {humanize(finding.state)}
                    </Badge>
                    <Markdown className="min-w-0 flex-1 text-(--text-primary)">
                      {finding.text}
                    </Markdown>
                  </div>
                  <ItemMeta>
                    <span>{finding.tool}</span>
                    {finding.stale && (
                      <>
                        <span>·</span>
                        <span>
                          Assessed <CommitLink document={document} sha={finding.assessed_head} />
                        </span>
                      </>
                    )}
                    {finding.addressed_by && (
                      <>
                        <span>·</span>
                        <span>
                          Addressed in <CommitLink document={document} sha={finding.addressed_by} />
                        </span>
                      </>
                    )}
                  </ItemMeta>
                  <AnchorChips document={document} anchors={finding.anchors} />
                </Item>
              ))}
            </ul>
          </Part>
        )}
      </div>
    </>
  );
}

/** A section's tiles, with the contract tiles framed under the endpoints they
 *  describe where the section names any. */
function SectionTiles({ document, section }: { document: ReviewDocument; section: Section }) {
  const apiGroups = groupApiContent(section, section.tiles);
  if (!apiGroups) return <ReviewTiles document={document} tiles={section.tiles} />;
  return (
    <>
      {apiGroups.groups.map((group) => (
        <EntityGroupFrame key={group.id} group={group}>
          <ReviewTiles document={document} tiles={group.tiles} />
        </EntityGroupFrame>
      ))}
      <ReviewTiles document={document} tiles={apiGroups.remainderTiles} />
    </>
  );
}

/** The summary and intent lines read as prose: their diff hunks are left to the
 *  code evidence below, where the change is shown rather than pointed at. */
function withoutHunks(anchors: Anchor[] | undefined): Anchor[] | undefined {
  return anchors?.filter((anchor) => anchor.type !== "hunk");
}

function scrollToItem(id: string) {
  document.getElementById(id)?.scrollIntoView({ behavior: "smooth", block: "start" });
}

type AttentionRef = {
  ref: string;
  kind: "Risk" | "Decision" | "Evidence" | "Reference";
  text: string;
  /** A chip that says the same about the item as it says where it is listed. */
  badge?: ReactNode;
  /** Whether the item is listed on the section, so the row can jump to it. */
  listed: boolean;
};

/** What an attention reference points at: the risk, the decision or the
 *  evidence claim it names within the section. A reference that names nothing
 *  is kept as its id, not dropped, and does not pretend to link anywhere. */
function resolveRef(section: Section, ref: string): AttentionRef {
  const risk = section.risks.find((item) => item.id === ref);
  if (risk) {
    return {
      ref,
      kind: "Risk",
      text: risk.text,
      badge: <TierBadge tier={risk.tier} />,
      listed: true,
    };
  }
  for (const block of section.blocks) {
    if (block.kind !== "decisions") continue;
    const decisions = block as Extract<Block, { kind: "decisions" }>;
    const decision = decisions.items.find((item) => item.id === ref);
    if (decision) return { ref, kind: "Decision", text: decision.text, listed: true };
  }
  const evidence = section.evidence.find((item) => item.id === ref);
  if (evidence) {
    // Evidence is not listed on the section, so the row carries its status.
    return {
      ref,
      kind: "Evidence",
      text: evidence.claim,
      badge: <EvidenceBadge evidence={evidence} compact />,
      listed: false,
    };
  }
  return { ref, kind: "Reference", text: ref, listed: false };
}

/** One flat callout: a single risk supplies its own explanation instead of
 *  repeating the attention summary, with evidence directly below it. */
function AttentionCallout({
  attention,
  section,
  onJump,
  risks,
}: {
  attention: NonNullable<Section["attention"]>;
  section: Section;
  onJump: (ref: string) => void;
  risks: ReactNode[];
}) {
  const refs = attention.refs
    .filter((ref) => !section.risks.some((risk) => risk.id === ref))
    .map((ref) => resolveRef(section, ref));
  return (
    <section
      aria-label="Attention"
      className="min-w-0 border-l-2 border-(--status-caution) py-1 pl-4"
    >
      <div className="mb-2 flex items-center gap-2">
        <span aria-hidden className="flex text-(--status-critical)">
          <RiskIcon size={14} />
        </span>
        <h3 className="text-xs font-semibold tracking-[0.12em] uppercase text-(--text-tertiary)">
          Attention
        </h3>
      </div>
      {(risks.length !== 1 || refs.length > 0) && (
        <Markdown className="mb-2 max-w-[62rem] text-base leading-normal text-(--text-primary)">
          {attention.text}
        </Markdown>
      )}
      {risks.length > 0 && <ol className="space-y-3">{risks}</ol>}
      {refs.length > 0 && (
        <ul
          aria-label="What to look at"
          className="divide-y divide-(--border-subtle) border-t border-(--border-subtle)"
        >
          {refs.map((item, index) => (
            <li
              key={`${index}:${item.ref}`}
              className="flex items-start gap-3 py-2 text-base leading-[1.45]"
            >
              <span className="w-16 flex-none pt-0.5 text-3xs font-semibold tracking-[0.08em] uppercase text-(--text-tertiary)">
                {item.kind}
              </span>
              {item.badge ? <span className="flex-none">{item.badge}</span> : null}
              <Markdown
                title={item.text}
                className={cn(
                  "line-clamp-2 min-w-0 flex-1 text-(--text-primary)",
                  item.kind === "Reference" && "font-mono",
                )}
              >
                {item.text}
              </Markdown>
              {item.listed ? (
                <JumpLink onClick={() => onJump(item.ref)} className="flex-none">
                  View ↓
                </JumpLink>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

/** A status pill that reads as a check mark for verified claims: the spec asks
 *  that a claim with supporting test evidence carry a visible indication. */
function EvidenceBadge({ evidence, compact = false }: { evidence: Evidence; compact?: boolean }) {
  return (
    <Badge
      tone={evidenceTone(evidence.status)}
      size={compact ? "compact" : "standard"}
      title={compact ? evidence.claim : undefined}
      className="gap-1"
    >
      {evidence.status === "verified" && <CheckIcon size={11} />}
      {humanize(evidence.status)}
    </Badge>
  );
}

/** A coloured panel around one changed endpoint and the contract tile that
 *  describes it, so a reader sees which surface the contract belongs to before
 *  reading the fields. The endpoint's name rides in the heading: two changed
 *  endpoints would otherwise give a section two panels both called "API". */
function EntityGroupFrame({ group, children }: { group: EntityContentGroup; children: ReactNode }) {
  const name = `${group.label} · ${group.entity.name}`;
  return (
    <section
      aria-label={name}
      data-entity-group={group.kind}
      className="min-w-0 space-y-4 rounded-(--radius-lg) border p-5 shadow-(--shadow-card)"
      style={{
        background: entityGroupWash(group.color),
        borderColor: entityGroupStroke(group.color),
        boxShadow: `inset 4px 0 0 ${group.color}, var(--shadow-card)`,
      }}
    >
      <div className="flex min-w-0 items-center gap-2">
        <span
          className="h-2.5 w-2.5 flex-none rounded-full"
          style={{ background: group.color }}
          aria-hidden
        />
        <h3 className="min-w-0 text-base font-medium tracking-[-0.01em] text-(--text-primary)">
          {group.label}
          <span className="ml-2 font-mono text-xs text-(--text-tertiary)">{group.entity.name}</span>
        </h3>
      </div>
      {children}
    </section>
  );
}

function BlockView({
  document,
  section,
  block,
  onSelectSection,
  idPrefix,
  tiles = null,
}: {
  document: ReviewDocument;
  section: Section;
  block: Block;
  onSelectSection: (id: string) => void;
  idPrefix: string;
  tiles?: ReactNode;
}) {
  const itemId = (ref: string) => `${idPrefix}${section.id}-${ref}`;
  const hunkChips = (hunks: string[]) => (
    <AnchorChips document={document} anchors={hunks.map((id) => ({ type: "hunk" as const, id }))} />
  );

  switch (block.kind) {
    case "intent": {
      const intent = block as Extract<Block, { kind: "intent" }>;
      return (
        <Part label="Intent">
          <Markdown className={PROSE}>{intent.text}</Markdown>
          <AnchorChips
            document={document}
            anchors={withoutHunks(intent.anchors)}
            className="mt-2"
          />
        </Part>
      );
    }
    case "behavior": {
      const behavior = block as Extract<Block, { kind: "behavior" }>;
      return (
        <Part label="Behavior">
          <ol className="space-y-2">
            {behavior.steps.map((step, position) => (
              <Item key={step.id} id={itemId(step.id)} className="flex gap-3">
                <span className="w-5 flex-none pt-px text-right text-base tabular-nums text-(--text-tertiary)">
                  {position + 1}
                </span>
                <div className="min-w-0 flex-1 space-y-1.5">
                  <Markdown className="text-(--text-primary)">{step.text}</Markdown>
                  {step.evidence.length > 0 && (
                    <ul className="flex flex-wrap gap-1.5" aria-label="Supporting evidence">
                      {step.evidence.map((ref) => {
                        const evidence = section.evidence.find((item) => item.id === ref);
                        return (
                          <li key={ref}>
                            {evidence ? (
                              <EvidenceBadge evidence={evidence} compact />
                            ) : (
                              <Badge size="compact" tone="caution">
                                Evidence {ref} missing
                              </Badge>
                            )}
                          </li>
                        );
                      })}
                    </ul>
                  )}
                  {/* Out under the step number on a phone, where its column is
                      width the code cannot spare. */}
                  <HunkEvidence
                    document={document}
                    anchors={step.anchors}
                    diffClassName="-ml-8 sm:ml-0"
                  />
                </div>
              </Item>
            ))}
          </ol>
          {tiles && <div className="mt-7 space-y-7">{tiles}</div>}
        </Part>
      );
    }
    // Entities are left out for now: a list of changed names repeated what
    // the hunks show without saying more. Stored reviews may still carry one.
    case "entities":
      return null;
    case "groups": {
      const groups = block as Extract<Block, { kind: "groups" }>;
      return (
        <Part label="Grouped changes">
          <ul className="space-y-1.5">
            {groups.items.map((group) => (
              <Item key={group.id} id={itemId(group.id)} className="space-y-1.5">
                <Markdown className="text-(--text-primary)">{group.rule}</Markdown>
                <ItemMeta>
                  <span>{pluralize("hunk", group.count, true)}</span>
                  {group.derived_from && (
                    <>
                      <span>·</span>
                      <span>
                        Produced by{" "}
                        <JumpLink onClick={() => onSelectSection(group.derived_from!)}>
                          {document.sections.find(
                            (candidate) => candidate.id === group.derived_from,
                          )?.title ?? group.derived_from}
                        </JumpLink>
                      </span>
                    </>
                  )}
                </ItemMeta>
                <details>
                  <summary className="cursor-pointer text-base text-(--text-link) hover:underline">
                    Show hunks
                  </summary>
                  <div className="mt-1.5">{hunkChips(group.hunks)}</div>
                </details>
              </Item>
            ))}
          </ul>
        </Part>
      );
    }
    case "decisions": {
      const decisions = block as Extract<Block, { kind: "decisions" }>;
      return (
        <Part label="Key decisions">
          <div className="space-y-4">
            {decisionGroups(decisions.items).map((group) => (
              <div key={group.label}>
                <p className="mb-2 text-base font-semibold text-(--text-secondary)">
                  {group.label} · {group.items.length}
                </p>
                <ul className="divide-y divide-(--border-subtle)">
                  {group.items.map((decision) => (
                    <li
                      key={decision.id}
                      id={itemId(decision.id)}
                      className="rounded-(--radius-sm) py-3 first:pt-0 last:pb-0"
                    >
                      <DecisionDisclosure
                        single={decisions.items.length === 1}
                        title={
                          <InlineMarkdown>{decision.title?.trim() || decision.text}</InlineMarkdown>
                        }
                      >
                        <div className="mt-2 space-y-1">
                          {decision.title?.trim() && (
                            <Markdown className="max-w-[90ch] text-base text-(--text-secondary)">
                              {decision.text}
                            </Markdown>
                          )}
                          {decision.rationale && (
                            <p className="max-w-[90ch] text-xs leading-normal text-(--text-tertiary)">
                              {decision.rationale}
                            </p>
                          )}
                          <DecisionContext
                            decision={{ evidence: decision.evidence }}
                            quoteAside={
                              decision.evidence &&
                              (() => {
                                const prompt = decision.anchors.find(
                                  (anchor) => anchor.type === "prompt",
                                );
                                return prompt?.type === "prompt" &&
                                  document.index.sessions.some(
                                    (session) =>
                                      session.id === prompt.session && !session.restricted,
                                  ) ? (
                                  <DecisionSessionLink
                                    session={prompt.session}
                                    turn={prompt.index}
                                  />
                                ) : null;
                              })()
                            }
                          />
                        </div>
                      </DecisionDisclosure>
                    </li>
                  ))}
                </ul>
              </div>
            ))}
          </div>
        </Part>
      );
    }
    case "dependency": {
      const dependency = block as Extract<Block, { kind: "dependency" }>;
      return (
        <Part label="Dependencies">
          <ul className="space-y-1.5">
            {dependency.items.map((item) => (
              <Item
                key={`${item.ecosystem}-${item.name}`}
                className="flex flex-wrap items-center gap-x-2 gap-y-1"
              >
                <code className="font-mono text-base">{item.name}</code>
                <Badge size="compact">{item.ecosystem}</Badge>
                <span className="tabular-nums text-(--text-secondary)">
                  {item.from ?? "—"} → {item.to ?? "removed"}
                </span>
                <span className="w-full">
                  <AnchorChips
                    document={document}
                    anchors={[
                      ...item.hunks.map((id) => ({ type: "hunk" as const, id })),
                      ...(item.changelog ? [item.changelog] : []),
                    ]}
                  />
                </span>
              </Item>
            ))}
          </ul>
        </Part>
      );
    }
    case "included_prs": {
      const included = block as Extract<Block, { kind: "included_prs" }>;
      return (
        <Part label="Included pull requests">
          <ul className="space-y-1.5">
            {included.items.map((item) => (
              <Item key={item.pr} className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <AnchorChips document={document} anchors={[{ type: "pr", number: item.pr }]} />
                {item.note && <span className="text-(--text-secondary)">{item.note}</span>}
              </Item>
            ))}
          </ul>
        </Part>
      );
    }
    case "diagram":
    case "code": {
      const content = block as Extract<Block, { kind: "diagram" | "code" }>;
      const label =
        content.kind === "diagram"
          ? `Diagram (${(content as Extract<Block, { kind: "diagram" }>).format})`
          : "Code";
      const caption =
        content.kind === "code" ? (content as Extract<Block, { kind: "code" }>).caption : undefined;
      return (
        <Part label={label}>
          <pre className="overflow-x-auto rounded-(--radius-md) bg-(--surface-sunken) p-3 font-mono text-base leading-[1.5] text-(--text-primary)">
            <code>{content.content}</code>
          </pre>
          {caption && <p className="mt-1.5 text-base text-(--text-tertiary)">{caption}</p>}
          <AnchorChips document={document} anchors={content.source} className="mt-2" />
        </Part>
      );
    }
    case "media": {
      const media = block as Extract<Block, { kind: "media" }>;
      const href = repositoryLink(document, media.url);
      return (
        <Part label="Media">
          {href ? (
            <a
              href={href}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-base"
            >
              {media.caption ?? "Open attachment"}
              <ExternalLinkIcon size={10} />
            </a>
          ) : (
            <p className="text-base text-(--text-primary)">{media.caption ?? "Attachment"}</p>
          )}
          <AnchorChips document={document} anchors={media.source} className="mt-2" />
        </Part>
      );
    }
    case "images":
      return (
        <ReviewImages document={document} block={block as Extract<Block, { kind: "images" }>} />
      );
    case "repro": {
      const repro = block as Extract<Block, { kind: "repro" }>;
      return (
        <Part label="Reproduction steps">
          <ol className="list-decimal space-y-1 pl-6 text-base text-(--text-primary)">
            {repro.steps.map((step, position) => (
              <li key={`${position}-${step}`}>{step}</li>
            ))}
          </ol>
          <AnchorChips document={document} anchors={repro.source} className="mt-2" />
        </Part>
      );
    }
    case "table": {
      const table = block as Extract<Block, { kind: "table" }>;
      type Row = { key: string; cells: string[] };
      const rows: Row[] = table.rows.map((cells, position) => ({ key: String(position), cells }));
      const columns: Column<Row>[] = table.columns.map((column, position) => ({
        key: `${position}-${column}`,
        header: column,
        cell: (row) => row.cells[position] ?? "",
      }));
      return (
        <Part label="Table">
          <Table
            columns={columns}
            rows={rows}
            rowKey={(row) => row.key}
            density="compact"
            minWidth={360}
          />
          <AnchorChips document={document} anchors={table.source} className="mt-2" />
        </Part>
      );
    }
    default:
      return (
        <Part label="Unsupported content">
          <details className="rounded-(--radius-md) border border-dashed border-(--border-default) px-3 py-2 text-base text-(--text-tertiary)">
            <summary className="cursor-pointer">
              This review uses a “{humanize(block.kind)}” block this page cannot render yet.
            </summary>
            <AnchorChips
              document={document}
              anchors={(block as { anchors?: Anchor[] }).anchors}
              className="mt-2"
            />
          </details>
        </Part>
      );
  }
}

function DecisionDisclosure({
  single,
  title,
  children,
}: {
  single: boolean;
  title: ReactNode;
  children: ReactNode;
}) {
  if (single)
    return (
      <div>
        <h4 className="text-base font-medium leading-snug text-(--text-primary)">{title}</h4>
        {children}
      </div>
    );
  return (
    <details>
      <summary className="cursor-pointer text-base text-(--text-secondary) hover:text-(--text-primary)">
        {title}
      </summary>
      {children}
    </details>
  );
}

function DecisionSessionLink({ session, turn }: { session: string; turn: number }) {
  const { search } = useUrlState();
  const next = withSelection(search, { kind: "session", id: session });
  next.set("turn", String(turn));
  return (
    <ViewerLink
      href={`?${next}`}
      className="inline-block text-xs text-(--text-link) hover:text-(--text-link-hover) hover:underline"
    >
      View in session →
    </ViewerLink>
  );
}
