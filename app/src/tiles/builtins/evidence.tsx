import pluralize from "pluralize";
import { ChecksIcon } from "@baum/viewer/components/icons";
import { HunkEvidence, HunkPatchProvider } from "@baum/viewer/components/hunk-evidence";
import { Markdown } from "@baum/viewer/components/markdown";
import { Item, JumpLink, None, Part } from "@baum/viewer/components/review-parts";
import { EvidenceBadge } from "@baum/viewer/components/review-section";
import { checkStatus, humanize } from "@baum/viewer/review-document";
import { Clamp, Count, Counts, Fill, plainSentence, Quiet } from "../parts";
import { changeById, changesInState, isCurrent, type TileEvidence } from "../tile-document";
import { type TileContext, type TileFaceProps, type TileType } from "../types";
import { StillWriting } from "./still-writing";

/** The evidence of the current changes, in document order. */
function currentEvidence(ctx: Pick<TileContext, "doc">) {
  return ctx.doc.evidence.filter((item) => isCurrent(changeById(ctx.doc, item.change)));
}

const GAPS = new Set(["missing", "failed"]);

/** What holds, what is only claimed and what is missing or failing, then the
 *  gaps themselves, and the head's checks as the review saw them. */
function EvidenceGlance({ ctx }: TileFaceProps) {
  const items = currentEvidence(ctx);
  const count = (...statuses: string[]) =>
    items.filter((item) => statuses.includes(item.status)).length;
  const verified = count("verified", "observed");
  const claimed = count("asserted", "reported");
  const missing = count("missing");
  const failed = count("failed");
  const gaps = items.filter((item) => GAPS.has(item.status));
  const checks = checkStatus(ctx.review);
  const pending = changesInState(ctx.doc, "pending").length;
  if (items.length === 0)
    return (
      <div className="space-y-2">
        <Quiet>No evidence was recorded for the current changes.</Quiet>
        <CheckLine checks={checks} />
        <StillWriting count={pending} />
      </div>
    );
  // The gaps lead; what holds follows, as far as the row has room.
  const shown = [...gaps, ...items.filter((item) => !GAPS.has(item.status))];
  return (
    <div className="flex min-h-0 grow flex-col gap-2.5">
      <Counts>
        {[
          verified > 0 ? <Count key="v" value={verified} label="verified" tone="positive" /> : null,
          claimed > 0 ? <Count key="c" value={claimed} label="claimed" /> : null,
          missing > 0 ? <Count key="m" value={missing} label="missing" tone="caution" /> : null,
          failed > 0 ? <Count key="f" value={failed} label="failed" tone="critical" /> : null,
        ]}
      </Counts>
      {gaps.length === 0 ? (
        <p className="text-base text-secondary">Nothing missing or failing.</p>
      ) : null}
      <Fill min={gaps.length > 0 ? "3rem" : "0"}>
        <ul className="space-y-1.5">
          {shown.map((item) => (
            <li key={item.id} className="flex min-w-0 items-baseline gap-2">
              <span className="flex-none">
                <EvidenceBadge evidence={item} compact />
              </span>
              <Clamp lines={2} className="text-base text-secondary">
                {plainSentence(
                  GAPS.has(item.status) ? item.expects || item.claim : item.claim,
                  ctx.labels,
                )}
              </Clamp>
            </li>
          ))}
        </ul>
      </Fill>
      <CheckLine checks={checks} />
      <StillWriting count={pending} />
    </div>
  );
}

function CheckLine({ checks }: { checks: ReturnType<typeof checkStatus> }) {
  if (checks.state === "none") return null;
  const text =
    checks.state === "failing"
      ? `${pluralize("check", checks.failed.length, true)} failing${checks.blocking ? ", required" : ""}`
      : checks.state === "pending"
        ? `${pluralize("check", checks.pending, true)} running`
        : checks.state === "passing"
          ? `All ${pluralize("check", checks.total, true)} passing`
          : "Checks inconclusive";
  return (
    <p
      className={
        checks.state === "failing" ? "text-xs text-status-critical" : "text-xs text-tertiary"
      }
    >
      CI when reviewed: {text}
    </p>
  );
}

/** Every evidence item, grouped by the change it backs, current changes first. */
function EvidenceDetail({ ctx }: TileFaceProps) {
  const pending = changesInState(ctx.doc, "pending").length;
  const groups = ctx.doc.changes
    .map((change) => ({
      change,
      items: ctx.doc.evidence.filter((item) => item.change === change.id),
    }))
    .filter((group) => group.items.length > 0)
    .toSorted((a, b) => Number(!isCurrent(a.change)) - Number(!isCurrent(b.change)));
  return (
    <HunkPatchProvider source={ctx.patches}>
      <div className="space-y-7">
        <StillWriting count={pending} />
        {groups.length === 0 ? <None>No evidence was recorded.</None> : null}
        {groups.map(({ change, items }) => (
          <Part
            key={change.id}
            label={change.title}
            aside={
              <JumpLink onClick={() => ctx.openChange(change.id)}>
                {isCurrent(change) ? "Open the change →" : "Obsolete change →"}
              </JumpLink>
            }
          >
            <ul className="space-y-2.5">
              {items.map((item) => (
                <EvidenceItem key={item.id} item={item} ctx={ctx} />
              ))}
            </ul>
          </Part>
        ))}
      </div>
    </HunkPatchProvider>
  );
}

function EvidenceItem({ item, ctx }: { item: TileEvidence; ctx: TileContext }) {
  return (
    <Item id={`${ctx.idPrefix}tile-item-${item.id}`} className="space-y-1.5">
      <div className="flex flex-wrap items-center gap-2">
        <EvidenceBadge evidence={item} compact />
        <span className="text-xs text-tertiary">{humanize(item.kind)}</span>
      </div>
      <Markdown className="text-primary">{item.claim}</Markdown>
      {item.detail ? <Markdown className="text-secondary">{item.detail}</Markdown> : null}
      {item.expects ? (
        <div className="space-y-1">
          <p className="text-xs font-medium text-tertiary">Expected coverage</p>
          <Markdown className="text-secondary">{item.expects}</Markdown>
        </div>
      ) : null}
      <HunkEvidence document={ctx.review} anchors={item.anchors} loadVersion={ctx.loadVersion} />
    </Item>
  );
}

export const evidenceTile: TileType = {
  type: "evidence",
  label: "Evidence",
  icon: ChecksIcon,
  Glance: EvidenceGlance,
  Detail: EvidenceDetail,
  valid: () => true,
  commentAnchors: (ctx, _tile, item) => {
    const found = item ? ctx.doc.evidence.find((each) => each.id === item) : undefined;
    return found ? found.anchors : [];
  },
};
