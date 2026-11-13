import { LightbulbIcon } from "@baum/viewer/components/icons";
import { Markdown } from "@baum/viewer/components/markdown";
import {
  AnchorChips,
  DecisionByBadge,
  DecisionContext,
  Item,
  JumpLink,
  Part,
} from "@baum/viewer/components/review-parts";
import { decisionGroups } from "@baum/viewer/review-document";
import { Clamp, Count, Counts, Fill, plainText, Quiet } from "../parts";
import { changeById, type DecisionsData, type TileDecision } from "../tile-document";
import { isRecord, type TileContext, type TileFaceProps, type TileType } from "../types";

const valid = (data: unknown) => isRecord(data) && Array.isArray(data.items);
const itemsOf = (data: unknown) => (data as DecisionsData).items;

const titleOf = (decision: TileDecision, labels: ReadonlyMap<string, string>) =>
  plainText(decision.title?.trim() || decision.text, labels);

/** How many decisions and who made them, then the decisions, as many as the
 *  row has room for. */
function DecisionsGlance({ tile, ctx }: TileFaceProps) {
  const items = itemsOf(tile.data);
  if (items.length === 0) return <Quiet>No decisions recorded.</Quiet>;
  const human = items.filter((item) => item.by === "human").length;
  const approved = items.filter((item) => item.by === "agent" && item.approved_by_human).length;
  const agent = items.length - human - approved;
  return (
    <div className="flex min-h-0 grow flex-col gap-2.5">
      <Counts>
        {[
          human > 0 ? <Count key="h" value={human} label="by a person" /> : null,
          approved > 0 ? <Count key="a" value={approved} label="approved" /> : null,
          agent > 0 ? <Count key="g" value={agent} label="agent's own" /> : null,
        ]}
      </Counts>
      <Fill min="3rem">
        <ul className="space-y-2">
          {items.map((item) => (
            <li key={item.id} className="flex min-w-0 items-start gap-2">
              <span className="mt-px flex-none">
                <DecisionByBadge decision={item} />
              </span>
              <Clamp lines={2} className="text-base text-primary">
                {titleOf(item, ctx.labels)}
              </Clamp>
            </li>
          ))}
        </ul>
      </Fill>
    </div>
  );
}

/** Every decision, made by a person first, each with its change, what it
 *  replaced and why. */
function DecisionsDetail({ tile, ctx }: TileFaceProps) {
  const items = itemsOf(tile.data);
  if (items.length === 0) return <Quiet>No decisions recorded.</Quiet>;
  return (
    <div className="space-y-7">
      {decisionGroups(items).map((group) => (
        <Part key={group.label} label={`${group.label} · ${group.items.length}`}>
          <ul className="space-y-2.5">
            {group.items.map((item) => (
              <DecisionItem key={item.id} item={item} ctx={ctx} />
            ))}
          </ul>
        </Part>
      ))}
    </div>
  );
}

function DecisionItem({ item, ctx }: { item: TileDecision; ctx: TileContext }) {
  const change = item.change ? changeById(ctx.doc, item.change) : undefined;
  return (
    <Item id={`${ctx.idPrefix}tile-item-${item.id}`} className="space-y-1.5">
      <div className="flex flex-wrap items-center gap-2">
        <DecisionByBadge decision={item} />
        {item.title ? <span className="font-medium text-primary">{item.title}</span> : null}
      </div>
      <Markdown className="text-primary">{item.text}</Markdown>
      <DecisionContext decision={item} />
      <AnchorChips document={ctx.review} anchors={item.anchors} />
      {change ? (
        <p className="text-xs text-tertiary">
          <JumpLink tone="secondary" onClick={() => ctx.openChange(change.id)}>
            In {change.title} →
          </JumpLink>
        </p>
      ) : null}
    </Item>
  );
}

export const decisionsTile: TileType = {
  type: "decisions",
  label: "Decisions",
  icon: LightbulbIcon,
  Glance: DecisionsGlance,
  Detail: DecisionsDetail,
  valid,
  commentAnchors: (_ctx, tile, item) => {
    const found = item ? itemsOf(tile.data).find((each) => each.id === item) : undefined;
    return found ? found.anchors : [];
  },
};
