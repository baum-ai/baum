import {
  humanize,
  type ReviewDocument,
  type LoadReviewVersion,
  riskCategory,
  riskEvidenceReviewVersion,
  riskLifecycleLabel,
  riskLifecycleTone,
  sectionStateLabel,
  sectionStateTone,
  type TriageRisk,
} from "../review-document";
import { HunkEvidence } from "./hunk-evidence";
import { Markdown } from "./markdown";
import { Facts, JumpLink, Part, PROSE, TierBadge } from "./review-parts";
import { ReviewTiles } from "./review-tiles/review-tiles";
import { Badge, CardHeader } from "./ui";

/** One risk on its own, for a reader working down the rail's Risks list rather
 *  than reading the pull request section by section.
 *
 *  A risk has no title of its own in the schema, so the category is the
 *  heading: it is the part that says whether this risk is yours to answer, and
 *  it is short enough to be a heading where the risk's own sentence is not.
 *  The tier keeps the red the summary's must-read list gives it — this is the
 *  same set of risks, reached another way, and it would be strange for one of
 *  them to change colour on the way here. */
export function ReviewRisk({
  document,
  risk,
  onSelectSection,
  loadVersion,
  activeVersions,
  showTiles = false,
}: {
  document: ReviewDocument;
  risk: TriageRisk;
  onSelectSection: (id: string) => void;
  loadVersion?: LoadReviewVersion;
  /** The review versions in which the risk's section had hunks, once read. */
  activeVersions?: readonly number[];
  /** Tiles are held back from reviews until they are ready to show. */
  showTiles?: boolean;
}) {
  const lifecycle = riskLifecycleLabel(risk, document.review, risk.sectionState);
  return (
    <>
      <CardHeader
        size="pane"
        title={riskCategory(risk)}
        detail={`Risk in ${risk.sectionTitle}`}
        aside={
          <>
            <TierBadge
              tier={risk.tier}
              tone={risk.tier === "critical" || risk.tier === "high" ? "critical" : undefined}
            />
            {lifecycle && (
              <Badge size="compact" tone={riskLifecycleTone(risk, risk.sectionState)}>
                {lifecycle}
              </Badge>
            )}
            {/* An obsolete section already names itself in the risk's own badge. */}
            {risk.sectionState !== "unchanged" && risk.sectionState !== "stale" && (
              <Badge tone={sectionStateTone(risk.sectionState)}>
                {sectionStateLabel(risk.sectionState)}
              </Badge>
            )}
          </>
        }
      />
      <div className="min-h-0 flex-1 xl:overflow-y-auto">
        <div className="w-full min-w-0 space-y-7">
          <div className="space-y-2">
            <Markdown className={PROSE}>{risk.text}</Markdown>
            <HunkEvidence
              document={document}
              anchors={risk.anchors}
              historicalVersion={riskEvidenceReviewVersion(
                risk,
                risk.sectionState,
                document.review.version,
                activeVersions,
              )}
              loadVersion={loadVersion}
            />
          </div>

          {showTiles && <ReviewTiles document={document} tiles={risk.tiles} />}

          <Part label="Where it came from">
            <Facts
              items={[
                {
                  label: "Section",
                  value: (
                    <JumpLink onClick={() => onSelectSection(risk.section)}>
                      {risk.sectionTitle} →
                    </JumpLink>
                  ),
                },
                { label: "Raised by", value: humanize(risk.source) },
              ]}
            />
          </Part>
        </div>
      </div>
    </>
  );
}
