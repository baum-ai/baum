import { useEffect, useRef } from "react";
import { ContextPart } from "@baum/viewer/components/review-context";
import { MapIcon } from "../icons";
import { Clamp, plainText, Quiet, Writing } from "../parts";
import type { ContextData } from "../tile-document";
import { isRecord, type TileFaceProps, type TileType } from "../types";

const valid = (data: unknown) =>
  isRecord(data) && isRecord(data.area) && typeof data.area.text === "string";

/** Where the change sits: the area's paragraph and the first files it names. */
function ContextGlance({ tile, ctx }: TileFaceProps) {
  if (tile.state === "pending") return <Writing>Writing context…</Writing>;
  const area = (tile.data as ContextData).area;
  const files = area.refs.slice(0, 3);
  return (
    <div className="flex h-full min-h-0 flex-col gap-2.5">
      <Clamp lines={5} className="text-base leading-normal text-primary">
        {plainText(area.text, ctx.labels)}
      </Clamp>
      {files.length > 0 ? (
        <ul aria-label="Files" className="flex flex-wrap gap-1.5">
          {files.map((ref, position) => (
            <li
              key={`${ref.path}-${position}`}
              className="max-w-full min-w-0 truncate rounded-sm bg-surface-wash px-1.5 py-0.5 font-mono text-xs text-secondary"
              title={ref.path}
            >
              {ref.symbol ?? ref.path.slice(ref.path.lastIndexOf("/") + 1)}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

/** The context in full, as the full review's Context part draws it, open. */
function ContextDetail({ tile, ctx }: TileFaceProps) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const fold = ref.current?.querySelector("details");
    if (fold) fold.open = true;
  });
  if (tile.state === "pending" || (!ctx.review.summary.context && ctx.contextPending))
    return <ContextPart document={ctx.review} pending />;
  if (!ctx.review.summary.context) return <Quiet>This review has no context.</Quiet>;
  return (
    <div ref={ref} className="@container">
      <ContextPart document={ctx.review} />
    </div>
  );
}

export const contextTile: TileType = {
  type: "context",
  label: "Context",
  icon: MapIcon,
  Glance: ContextGlance,
  Detail: ContextDetail,
  valid,
};
