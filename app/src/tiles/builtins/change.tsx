import pluralize from "pluralize";
import { useState } from "react";
import { Badge, Button } from "@baum/viewer/components/ui";
import { RiskIcon, SectionIcon, WarningIcon } from "@baum/viewer/components/icons";
import { JumpLink, Lines } from "@baum/viewer/components/review-parts";
import { HunkPatchProvider } from "@baum/viewer/components/hunk-evidence";
import { ReviewSection } from "@baum/viewer/components/review-section";
import {
  humanize,
  RISK_SCORE_MEANING,
  reviewScoreTone,
  sectionBadgeState,
  sectionHunks,
  sectionStateLabel,
  sectionStateTone,
  type EvidenceStatus,
  type ReviewDocument,
  type Section,
} from "@baum/viewer/review-document";
import { Count, Counts, Fill, plainSentence, plainText, Quiet, Writing } from "../parts";
import {
  changeById,
  isCurrent,
  mustShowRisks,
  riskInChange,
  risksShownInFull,
  tileOfChange,
  type ChangeData,
  type Tile,
  type TileChange,
  type TileDocument,
} from "../tile-document";
import { isRecord, type TileContext, type TileFaceProps, type TileType } from "../types";

const valid = (data: unknown) =>
  isRecord(data) && typeof data.change === "string" && Array.isArray(data.blocks);

/** The change a Change tile presents, and its section in the review view. */
function subject(tile: Tile, ctx: Pick<TileContext, "doc" | "review">) {
  const data = isRecord(tile.data) ? (tile.data as Partial<ChangeData>) : {};
  const id = typeof data.change === "string" ? data.change : tile.id;
  return {
    data,
    change: changeById(ctx.doc, id),
    section: ctx.review.sections.find((item) => item.id === id),
  };
}

/** Evidence of a change counted the way a reader weighs it: what holds, what
 *  is only claimed, and what is missing or failed. */
export function evidenceTally(summary: Partial<Record<EvidenceStatus, number>>) {
  const of = (...statuses: EvidenceStatus[]) =>
    statuses.reduce((sum, status) => sum + (summary[status] ?? 0), 0);
  return {
    verified: of("verified", "observed"),
    claimed: of("asserted", "reported"),
    missing: of("missing"),
    failed: of("failed"),
  };
}

/** The open risks of a change, and whether any is one the reader must read. */
function openRisks(doc: TileDocument, change: string) {
  const open = doc.risks.filter((risk) => risk.change === change && risk.state !== "mitigated");
  const urgent = mustShowRisks(doc).filter((risk) => risk.change === change);
  return { open, urgent };
}

/** What a Change glance says: what the change does in a sentence, how much
 *  it asks (its score and size), and whether it holds a risk or evidence
 *  gaps, so the reader knows whether to open it before they do. */
function ChangeGlance({ tile, ctx }: TileFaceProps) {
  const { data, change, section } = subject(tile, ctx);
  if (!change) return <Quiet>This change isn't in this version.</Quiet>;
  const files = sectionHunks(ctx.review, change.hunks).length;
  const steps = (data.blocks ?? []).reduce(
    (sum, block) =>
      sum + (block.kind === "behavior" && "steps" in block ? (block.steps?.length ?? 0) : 0),
    0,
  );
  const tally = evidenceTally(change.derived.evidence_summary);
  const { open, urgent } = openRisks(ctx.doc, change.id);
  const summary = plainText(data.summary?.text, ctx.labels);
  const stale = !isCurrent(change);
  return (
    <div className="flex min-h-0 grow flex-col gap-2.5">
      <ChangeBadges change={change} ctx={ctx} section={section} />
      {urgent.length > 0 ? (
        <p className="flex items-start gap-1.5 text-base text-status-critical">
          <RiskIcon size={14} className="mt-0.5 flex-none" />
          <span className="line-clamp-2 min-w-0 [overflow-wrap:anywhere]">
            {urgent.length > 1
              ? `${urgent.length} high or critical risks`
              : risksShownInFull(ctx.doc)
                ? // The Risks card beside it says the sentence; this names it.
                  `${humanize(urgent[0]!.tier)} risk · ${humanize(urgent[0]!.category)}`
                : plainSentence(urgent[0]!.text, ctx.labels)}
          </span>
        </p>
      ) : data.attention?.text ? (
        <p className="flex items-start gap-1.5 text-base text-secondary">
          <WarningIcon size={14} className="mt-0.5 flex-none text-status-caution" />
          <span className="line-clamp-1 min-w-0 [overflow-wrap:anywhere] @min-[36rem]/tiles:line-clamp-2">
            {plainSentence(data.attention.text, ctx.labels)}
          </span>
        </p>
      ) : null}
      {tile.state === "pending" ? (
        <Writing />
      ) : tile.state === "failed" ? (
        <DetailsUnavailable change={change} ctx={ctx} />
      ) : summary ? (
        // Three lines at least, and as much more as the row leaves room for.
        <Fill min="4.5rem" phoneMin="6rem">
          <p className="text-base leading-normal text-primary">{summary}</p>
        </Fill>
      ) : (
        <Quiet>No summary.</Quiet>
      )}
      <div className="space-y-1 text-xs text-tertiary">
        {/* An obsolete change has no code left to size. */}
        {stale ? null : (
          <p className="flex flex-wrap items-center gap-x-1.5">
            <Lines additions={change.derived.additions} deletions={change.derived.deletions} />
            <span aria-hidden>·</span>
            <span>{pluralize("file", files, true)}</span>
            {steps > 0 ? (
              <>
                <span aria-hidden>·</span>
                <span>{pluralize("behaviour", steps, true)}</span>
              </>
            ) : null}
          </p>
        )}
        {stale ? null : (
          <Counts className="text-xs">
            {[
              open.length > 0 ? (
                <Count
                  key="risks"
                  value={open.length}
                  label={pluralize("open risk", open.length)}
                  tone={urgent.length > 0 ? "critical" : "neutral"}
                />
              ) : (
                <span key="risks">No open risks</span>
              ),
              tally.verified > 0 ? (
                <Count key="verified" value={tally.verified} label="verified" tone="positive" />
              ) : null,
              tally.missing > 0 ? (
                <Count key="missing" value={tally.missing} label="missing" tone="caution" />
              ) : null,
              tally.failed > 0 ? (
                <Count key="failed" value={tally.failed} label="failed" tone="critical" />
              ) : null,
            ]}
          </Counts>
        )}
      </div>
    </div>
  );
}

/** A change's standing at a glance: its review score, its state against the
 *  last version (or the compared one), and the host's labels for it. */
function ChangeBadges({
  change,
  ctx,
  section,
}: {
  change: TileChange;
  ctx: TileContext;
  section: Section | undefined;
}) {
  const state = sectionBadgeState(change.derived.state, ctx.review.review);
  // Why it scored so, in the words of the risk the reason names, where one does.
  const reason = change.derived.review_reason;
  const named = reason?.risk ? riskInChange(ctx.doc, change.id, reason.risk) : undefined;
  const why = plainSentence(named?.text ?? reason?.text, ctx.labels);
  const score = change.derived.review_score;
  const added = ctx.compare?.added.has(change.id);
  const labels = section && ctx.renderLabels ? ctx.renderLabels(section) : null;
  if (score === undefined && state === "unchanged" && !added && !labels) return null;
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {score !== undefined && change.derived.state !== "stale" ? (
        <Badge
          size="compact"
          tone={reviewScoreTone(score)}
          title={`Review score ${score} of 5: ${RISK_SCORE_MEANING[score]}${why ? `. ${why}` : ""}`}
        >
          Score {score}
        </Badge>
      ) : null}
      {added ? (
        <Badge size="compact" tone="info">
          New since compared
        </Badge>
      ) : state !== "unchanged" ? (
        <Badge size="compact" tone={sectionStateTone(state)}>
          {sectionStateLabel(state)}
        </Badge>
      ) : null}
      {labels ? (
        // Above the card's own link, as the labels may link to the members.
        <span className="relative z-10 inline-flex flex-wrap gap-1">{labels}</span>
      ) : null}
    </div>
  );
}

/** A change whose details could not be written: says so, and offers to write
 *  them again on the current version. */
function DetailsUnavailable({ change, ctx }: { change: TileChange; ctx: TileContext }) {
  const [busy, setBusy] = useState(false);
  const retry =
    ctx.onRetryDetails && !ctx.historical && isCurrent(change) ? ctx.onRetryDetails : undefined;
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
      <p role="status" className="text-base text-secondary">
        Details unavailable
      </p>
      {retry ? (
        <Button
          variant="secondary"
          size="compact"
          disabled={busy}
          // Above the card's own link, so the click retries rather than opens.
          className="relative z-10"
          onClick={async () => {
            setBusy(true);
            try {
              await retry(change.id);
            } catch {
              // The host says why its request failed; Retry stays offered.
            } finally {
              setBusy(false);
            }
          }}
        >
          {busy ? "Retrying…" : "Retry"}
        </Button>
      ) : null}
    </div>
  );
}

/** Everything about a change: the section as the full review draws it, so
 *  nothing it shows is lost, then where its parts gathered in other tiles. */
function ChangeDetail({ tile, ctx }: TileFaceProps) {
  const { section } = subject(tile, ctx);
  if (!section)
    return (
      <p className="text-base text-secondary">
        This change did not exist in review v{ctx.review.review.version}.
      </p>
    );
  return <ChangeSection document={ctx.review} section={section} ctx={ctx} tile={tile} />;
}

export function ChangeSection({
  document,
  section,
  ctx,
  tile,
}: {
  document: ReviewDocument;
  section: Section;
  ctx: TileContext;
  tile: Tile;
}) {
  const elsewhere = gatheredElsewhere(ctx, section.id).filter((place) => place.tile !== tile.id);
  return (
    <>
      <HunkPatchProvider source={ctx.patches}>
        <ReviewSection
          document={document}
          section={section}
          onSelectSection={(id) => ctx.openChange(id)}
          loadVersion={ctx.loadVersion}
          activeVersions={ctx.activeVersions(section.id)}
          idPrefix={ctx.idPrefix}
          labels={ctx.renderLabels?.(section)}
          onRetryDetails={ctx.historical ? undefined : ctx.onRetryDetails}
        />
      </HunkPatchProvider>
      {elsewhere.length > 0 ? (
        <nav
          aria-label="Also on the tiles page"
          className="mt-8 flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-subtle pt-3 text-base"
        >
          <span className="text-tertiary">Also gathered in</span>
          {elsewhere.map((place) => (
            <JumpLink key={place.tile} onClick={() => ctx.open(place.tile)}>
              {place.label} →
            </JumpLink>
          ))}
        </nav>
      ) : null}
    </>
  );
}

/** The aggregate tiles that hold something of a change: its intent, its
 *  decisions, its images, its risks and its evidence. */
function gatheredElsewhere(ctx: TileContext, change: string) {
  const places: { tile: string; label: string }[] = [];
  const holds = (id: string, count: number, one: string, many = `${one}s`) => {
    if (count > 0 && ctx.doc.tiles.some((tile) => tile.id === id))
      places.push({ tile: id, label: `${count} ${count === 1 ? one : many}` });
  };
  const risks = ctx.doc.risks.filter(
    (risk) => risk.change === change && risk.state !== "mitigated",
  );
  holds("risks", isCurrent(changeById(ctx.doc, change)) ? risks.length : 0, "open risk");
  holds(
    "evidence",
    ctx.doc.evidence.filter((item) => item.change === change).length,
    "evidence item",
  );
  for (const tile of ctx.doc.tiles) {
    const data = isRecord(tile.data) ? tile.data : {};
    const ofChange = (list: unknown) =>
      Array.isArray(list)
        ? list.filter((item) => isRecord(item) && item.change === change).length
        : 0;
    if (tile.type === "intent") holds(tile.id, ofChange(data.lines), "intent line");
    if (tile.type === "decisions") holds(tile.id, ofChange(data.items), "decision");
    if (tile.type === "media") holds(tile.id, ofChange(data.items), "media block");
  }
  // The tiles made of the change: a block promoted out of it, and the
  // library and authored tiles that cite its code, by their titles.
  const own = new Set(tileOfChange(ctx.doc, change)?.covers ?? []);
  for (const tile of ctx.doc.tiles) {
    if (!GATHERING.has(tile.type) || places.some((place) => place.tile === tile.id)) continue;
    const origin = isRecord(tile.data) && isRecord(tile.data.origin) ? tile.data.origin : null;
    const cites = (tile.covers ?? []).some((hunk) => own.has(hunk));
    if (origin?.change === change || cites) places.push({ tile: tile.id, label: tile.title });
  }
  return places;
}

/** The tiles that may show part of a change: promoted blocks, authored
 *  tiles and library tiles. */
const GATHERING = new Set(["table", "code", "prose", "custom"]);

export const changeTile: TileType = {
  type: "change",
  label: "Change",
  icon: SectionIcon,
  Glance: ChangeGlance,
  Detail: ChangeDetail,
  valid,
};

export { ChangeBadges, DetailsUnavailable };
