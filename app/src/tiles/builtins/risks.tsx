import pluralize from "pluralize";
import { RiskIcon } from "@baum/viewer/components/icons";
import { HunkPatchProvider, HunkEvidence } from "@baum/viewer/components/hunk-evidence";
import { Markdown } from "@baum/viewer/components/markdown";
import {
  Item,
  ItemMeta,
  JumpLink,
  None,
  Part,
  RiskTags,
} from "@baum/viewer/components/review-parts";
import {
  humanize,
  riskEvidenceReviewVersion,
  riskLifecycleLabel,
  sectionBadgeState,
  TIER_ORDER,
  TIER_COLOR,
  type RiskTier,
} from "@baum/viewer/review-document";
import { AllClear, Clamp, plainText, TierCount } from "../parts";
import { changeById, changesInState, isCurrent, reviewIdOf, type TileRisk } from "../tile-document";
import { isRecord, type TileContext, type TileFaceProps, type TileType } from "../types";
import { StillWriting } from "./still-writing";

const valid = (data: unknown) =>
  isRecord(data) && Array.isArray(data.ranked) && data.ranked.every((id) => typeof id === "string");

/** The ranked risks the tile holds, as substrate risks. */
function ranked(ctx: Pick<TileContext, "doc">, data: unknown): TileRisk[] {
  const ids = isRecord(data) && Array.isArray(data.ranked) ? (data.ranked as string[]) : [];
  const byId = new Map(ctx.doc.risks.map((risk) => [risk.id, risk]));
  return ids.flatMap((id) => {
    const risk = byId.get(id);
    return risk ? [risk] : [];
  });
}

const URGENT: RiskTier[] = ["critical", "high"];
const MEDIUM_LINES = 3;

/** How many open risks of each tier, every critical and high one in full,
 *  then the first medium ones: the reader sees at once whether there is
 *  anything to fear and what it is. */
function RisksGlance({ tile, ctx }: TileFaceProps) {
  const risks = ranked(ctx, tile.data);
  const pending = changesInState(ctx.doc, "pending").length;
  if (risks.length === 0)
    return (
      <div className="space-y-2">
        <AllClear>No open risks</AllClear>
        <StillWriting count={pending} />
      </div>
    );
  const tiers = TIER_ORDER.filter((tier): tier is RiskTier => tier !== "unrated").flatMap(
    (tier) => {
      const count = risks.filter((risk) => risk.tier === tier).length;
      return count > 0 ? [{ tier, count }] : [];
    },
  );
  const urgent = risks.filter((risk) => URGENT.includes(risk.tier));
  const medium = risks.filter((risk) => risk.tier === "medium");
  const shownMedium = urgent.length >= 3 ? [] : medium.slice(0, MEDIUM_LINES - urgent.length);
  const rest = risks.length - urgent.length - shownMedium.length;
  return (
    <div className="space-y-3">
      <p className="flex flex-wrap items-center gap-x-3.5 gap-y-1">
        {tiers.map(({ tier, count }) => (
          <TierCount key={tier} tier={tier} count={count} />
        ))}
      </p>
      <ul className="space-y-2">
        {urgent.map((risk) => (
          <RiskLine key={risk.id} risk={risk} ctx={ctx} urgent />
        ))}
        {shownMedium.map((risk) => (
          <RiskLine key={risk.id} risk={risk} ctx={ctx} />
        ))}
      </ul>
      {rest > 0 ? (
        <p className="text-base text-tertiary">{pluralize("more risk", rest, true)}, lower tiers</p>
      ) : null}
      <StillWriting count={pending} />
    </div>
  );
}

function RiskLine({
  risk,
  ctx,
  urgent = false,
}: {
  risk: TileRisk;
  ctx: TileContext;
  urgent?: boolean;
}) {
  const change = changeById(ctx.doc, risk.change);
  return (
    <li className="flex min-w-0 gap-2">
      <span
        aria-hidden
        className="mt-[0.45rem] h-2 w-2 flex-none rounded-full"
        style={{ background: TIER_COLOR[risk.tier] }}
      />
      <div className="min-w-0 flex-1">
        <span className="sr-only">{humanize(risk.tier)} risk: </span>
        <Clamp
          lines={urgent ? 2 : 1}
          className={urgent ? "text-base text-primary" : "text-base text-secondary"}
        >
          {plainText(risk.text, ctx.labels)}
        </Clamp>
        {urgent && change ? (
          <p className="truncate text-xs text-tertiary">
            {humanize(risk.category)} · {change.title}
          </p>
        ) : null}
      </div>
    </li>
  );
}

/** Every open risk of the current changes, most severe first, each with the
 *  code it cites and the change it belongs to; mitigated ones are folded
 *  below. */
function RisksDetail({ tile, ctx }: TileFaceProps) {
  const risks = ranked(ctx, tile.data);
  const mitigated = ctx.doc.risks.filter(
    (risk) => risk.state === "mitigated" && isCurrent(changeById(ctx.doc, risk.change)),
  );
  const pending = changesInState(ctx.doc, "pending").length;
  return (
    <HunkPatchProvider source={ctx.patches}>
      <div className="space-y-7">
        <StillWriting count={pending} />
        {risks.length === 0 ? (
          <None>No open risk was identified in the current changes.</None>
        ) : (
          TIER_ORDER.flatMap((tier) => {
            const ofTier = risks.filter((risk) => risk.tier === tier);
            if (ofTier.length === 0) return [];
            return [
              <Part
                key={tier}
                label={`${humanize(tier)} · ${ofTier.length}`}
                aside={
                  URGENT.includes(tier as RiskTier) ? (
                    <span className="text-xs text-tertiary">Read before approving</span>
                  ) : undefined
                }
              >
                <ol className="space-y-2.5">
                  {ofTier.map((risk) => (
                    <RiskItem key={risk.id} risk={risk} ctx={ctx} />
                  ))}
                </ol>
              </Part>,
            ];
          })
        )}
        {mitigated.length > 0 ? (
          <details>
            <summary className="cursor-pointer text-base text-link hover:underline">
              {pluralize("mitigated risk", mitigated.length, true)}
            </summary>
            <ol className="mt-2.5 space-y-2.5">
              {mitigated.map((risk) => (
                <RiskItem key={risk.id} risk={risk} ctx={ctx} />
              ))}
            </ol>
          </details>
        ) : null}
      </div>
    </HunkPatchProvider>
  );
}

function RiskItem({ risk, ctx }: { risk: TileRisk; ctx: TileContext }) {
  const change = changeById(ctx.doc, risk.change);
  const sectionState = change ? change.derived.state : "unchanged";
  const lifecycle = riskLifecycleLabel(risk, ctx.review.review, sectionState);
  return (
    <Item id={`${ctx.idPrefix}tile-item-${risk.id}`} className="space-y-2">
      <RiskTags
        tier={risk.tier}
        category={risk.category}
        sectionState={sectionBadgeState(sectionState, ctx.review.review)}
        urgent={URGENT.includes(risk.tier)}
      />
      <Markdown className="text-primary">{risk.text}</Markdown>
      <HunkEvidence
        document={ctx.review}
        anchors={risk.anchors}
        historicalVersion={riskEvidenceReviewVersion(
          risk,
          sectionState,
          ctx.review.review.version,
          ctx.activeVersions(risk.change),
        )}
        loadVersion={ctx.loadVersion}
      />
      <ItemMeta>
        {change ? (
          <JumpLink onClick={() => ctx.openChange(change.id, reviewIdOf(risk))}>
            In {change.title} →
          </JumpLink>
        ) : null}
        <span>Raised by {humanize(risk.source).toLowerCase()}</span>
        {lifecycle ? <span>{lifecycle}</span> : null}
      </ItemMeta>
    </Item>
  );
}

export const risksTile: TileType = {
  type: "risks",
  label: "Risks",
  icon: RiskIcon,
  Glance: RisksGlance,
  Detail: RisksDetail,
  valid,
  commentAnchors: (ctx, _tile, item) => {
    if (!item) return [];
    const risk = ctx.doc.risks.find((each) => each.id === item);
    if (!risk) return [];
    const hunks = risk.anchors.filter((anchor) => anchor.type === "hunk");
    if (hunks.length > 0) return hunks;
    const change = changeById(ctx.doc, risk.change);
    return (change?.hunks ?? []).slice(0, 1).map((id) => ({ type: "hunk" as const, id }));
  },
  tall: (tile, doc) =>
    ranked({ doc }, tile.data).filter((risk) => URGENT.includes(risk.tier)).length > 2,
};
