import pluralize from "pluralize";
import { QuotedPrompts } from "@baum/viewer/components/chat";
import { Markdown } from "@baum/viewer/components/markdown";
import { AnchorChips, Item, JumpLink, Part } from "@baum/viewer/components/review-parts";
import { harnessName } from "@baum/viewer/agent-names";
import { implementsGroups, intentPrompts, type Implements } from "@baum/viewer/review-document";
import { QuoteIcon } from "../icons";
import { Clamp, plainText, Quiet } from "../parts";
import { changeById, type IntentData } from "../tile-document";
import { isRecord, type TileContext, type TileFaceProps, type TileType } from "../types";

const valid = (data: unknown) =>
  isRecord(data) && Array.isArray(data.implements) && Array.isArray(data.lines);

const dataOf = (data: unknown) => data as IntentData;

/** The intent lines as the viewer's prompt helpers read them. */
function intentLines(ctx: TileContext, data: IntentData) {
  return data.lines.map((line) => ({
    section: line.change ?? "",
    title: line.change ? (changeById(ctx.doc, line.change)?.title ?? "") : "",
    text: line.text,
    anchors: line.anchors ?? [],
  }));
}

function ImplementsChips({ items }: { items: Implements[] }) {
  const groups = implementsGroups(items);
  if (groups.length === 0) return null;
  return (
    <ul aria-label="Implements" className="flex flex-wrap gap-1.5">
      {groups.flatMap((group) =>
        (group.items.length > 0 ? group.items : [group.label]).map((item) => (
          <li
            key={`${group.kind}-${group.label}-${item}`}
            className="max-w-full min-w-0 truncate rounded-sm bg-surface-wash px-1.5 py-0.5 font-mono text-xs text-secondary"
            title={`${group.label} ${item}`}
          >
            {item}
          </li>
        )),
      )}
    </ul>
  );
}

/** What was asked for: the issues it implements, the first stated reason,
 *  and how many prompts it came from. */
function IntentGlance({ tile, ctx }: TileFaceProps) {
  const data = dataOf(tile.data);
  const prompts = intentPrompts(ctx.review, intentLines(ctx, data));
  const first = data.lines[0];
  if (!first && data.implements.length === 0) return <Quiet>No stated intent.</Quiet>;
  return (
    <div className="space-y-2.5">
      <ImplementsChips items={data.implements} />
      {first ? (
        <Clamp lines={4} className="text-base leading-normal text-primary">
          {plainText(first.text, ctx.labels)}
        </Clamp>
      ) : null}
      <p className="text-xs text-tertiary">
        {[
          data.lines.length > 1 ? pluralize("stated reason", data.lines.length, true) : "",
          prompts.length > 0 ? `from ${pluralize("prompt", prompts.length, true)}` : "",
        ]
          .filter(Boolean)
          .join(" · ")}
      </p>
    </div>
  );
}

/** Every stated reason, grouped by the change that argued it, and the prompts
 *  they cite, quoted. */
function IntentDetail({ tile, ctx }: TileFaceProps) {
  const data = dataOf(tile.data);
  const prompts = intentPrompts(ctx.review, intentLines(ctx, data));
  const sessions = (vendor: string) => [
    ...new Set(prompts.filter((each) => each.vendor === vendor).map((each) => each.session)),
  ];
  const groups = [...new Set(data.lines.map((line) => line.change ?? ""))].map((change) => ({
    change,
    lines: data.lines.filter((line) => (line.change ?? "") === change),
  }));
  return (
    <div className="space-y-7">
      {data.implements.length > 0 ? (
        <Part label="Implements">
          <ImplementsChips items={data.implements} />
        </Part>
      ) : null}
      {groups.map(({ change, lines }) => {
        const title = change ? changeById(ctx.doc, change)?.title : undefined;
        return (
          <Part
            key={change || "document"}
            label={title ?? "The pull request"}
            aside={
              change ? (
                <JumpLink onClick={() => ctx.openChange(change)}>Open the change →</JumpLink>
              ) : undefined
            }
          >
            <ul className="space-y-2.5">
              {lines.map((line, position) => (
                <Item key={position} className="space-y-1.5">
                  <Markdown className="text-primary">{line.text}</Markdown>
                  <AnchorChips document={ctx.review} anchors={line.anchors} />
                </Item>
              ))}
            </ul>
          </Part>
        );
      })}
      {prompts.length > 0 ? (
        <Part label={`From ${pluralize("session prompt", prompts.length, true)}`}>
          <QuotedPrompts
            prompts={prompts.map((prompt) => {
              const own = sessions(prompt.vendor);
              const agent = harnessName(prompt.vendor);
              return {
                ...prompt,
                session: {
                  id: prompt.session,
                  label:
                    own.length > 1 ? `${agent} session ${own.indexOf(prompt.session) + 1}` : agent,
                },
              };
            })}
          />
        </Part>
      ) : null}
    </div>
  );
}

export const intentTile: TileType = {
  type: "intent",
  label: "Intent",
  icon: QuoteIcon,
  Glance: IntentGlance,
  Detail: IntentDetail,
  valid,
};
