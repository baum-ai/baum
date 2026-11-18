import pluralize from "pluralize";
import { useState } from "react";
import { HunkEvidence, HunkPatchProvider } from "@baum/viewer/components/hunk-evidence";
import { Lines, Part } from "@baum/viewer/components/review-parts";
import { humanize, sectionHunks, type HunkGroup } from "@baum/viewer/review-document";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import { ChevronRightIcon } from "@baum/viewer/components/icons";
import { LayersIcon } from "../icons";
import { Fill, plainText, Quiet } from "../parts";
import type { EverythingElseData } from "../tile-document";
import { isRecord, type TileContext, type TileFaceProps, type TileType } from "../types";
import { ChangeSection } from "./change";

const valid = (data: unknown) =>
  isRecord(data) &&
  (data.change === null || typeof data.change === "string") &&
  Array.isArray(data.unassigned);

const dataOf = (data: unknown) => data as EverythingElseData;

/** The hunks the tile covers: the mechanical change's and the unassigned. */
function covered(ctx: TileContext, data: EverythingElseData) {
  const mechanical = data.change
    ? ctx.review.sections.find((s) => s.id === data.change)
    : undefined;
  return { mechanical, hunks: [...(mechanical?.hunks ?? []), ...data.unassigned] };
}

/** How much is here and of what kind, and what the mechanical change does,
 *  so a reader can tell it can wait. */
function EverythingElseGlance({ tile, ctx }: TileFaceProps) {
  const data = dataOf(tile.data);
  const { hunks } = covered(ctx, data);
  const wanted = new Set(hunks);
  const indexed = ctx.review.index.hunks.filter((hunk) => wanted.has(hunk.id));
  const paths = [...new Set(indexed.map((hunk) => hunk.path))];
  const files = paths.length;
  const roles = [...new Set(indexed.map((hunk) => hunk.role))].map((role) => ({
    role,
    count: indexed.filter((hunk) => hunk.role === role).length,
  }));
  const summary = plainText(data.summary?.text, ctx.labels);
  if (indexed.length === 0) return <Quiet>Nothing outside the changes.</Quiet>;
  return (
    <div className="flex min-h-0 grow flex-col gap-2.5">
      <p className="text-base text-primary">
        <span className="font-semibold tabular-nums">
          {pluralize("hunk", indexed.length, true)}
        </span>{" "}
        <span className="text-secondary">in {pluralize("file", files, true)}</span>
      </p>
      <Fill min="3rem" className="space-y-1.5">
        {summary ? <p className="text-base leading-normal text-secondary">{summary}</p> : null}
        {/* The files say what the hunks no change describes are. */}
        <ul className="min-w-0 space-y-0.5">
          {paths.map((path) => (
            <li key={path} className="truncate font-mono text-xs text-secondary" title={path}>
              {path}
            </li>
          ))}
        </ul>
      </Fill>
      <p className="flex flex-wrap gap-x-2 text-xs text-tertiary">
        {roles
          .toSorted((a, b) => b.count - a.count)
          .map(({ role, count }) => (
            <span key={role}>
              {count} {humanize(role).toLowerCase()}
            </span>
          ))}
      </p>
    </div>
  );
}

/** The mechanical change as a section, then every hunk no change holds, a
 *  file to a fold, read when opened. */
function EverythingElseDetail({ tile, ctx }: TileFaceProps) {
  const data = dataOf(tile.data);
  const { mechanical } = covered(ctx, data);
  const files = sectionHunks(ctx.review, data.unassigned);
  return (
    <div className="space-y-8">
      {mechanical ? (
        <div className="flex flex-col">
          <ChangeSection document={ctx.review} section={mechanical} ctx={ctx} tile={tile} />
        </div>
      ) : null}
      {files.length > 0 ? (
        <Part
          label="Not in any change"
          aside={
            <span className="text-xs text-tertiary">
              {pluralize("hunk", data.unassigned.length, true)} in{" "}
              {pluralize("file", files.length, true)}
            </span>
          }
        >
          <HunkPatchProvider source={ctx.patches}>
            <ul className="divide-y divide-subtle rounded-md border border-subtle">
              {files.map((file) => (
                <FileFold key={file.path} file={file} ctx={ctx} />
              ))}
            </ul>
          </HunkPatchProvider>
        </Part>
      ) : null}
      {!mechanical && files.length === 0 ? <Quiet>Nothing outside the changes.</Quiet> : null}
    </div>
  );
}

function FileFold({ file, ctx }: { file: HunkGroup; ctx: TileContext }) {
  const [open, setOpen] = useState(false);
  return (
    <li data-review-block="" className="outline-none data-[review-block-active]:bg-surface-wash">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className={cn(
          "flex w-full cursor-pointer items-center gap-2 px-3 py-2 text-left text-base hover:bg-surface-hover",
          FOCUS_RING,
        )}
      >
        <ChevronRightIcon
          size={12}
          className={cn(
            "flex-none text-tertiary transition-transform motion-reduce:transition-none",
            open && "rotate-90",
          )}
        />
        <span className="min-w-0 flex-1 truncate font-mono text-xs text-primary" title={file.path}>
          {file.path}
        </span>
        <span className="flex-none text-xs text-tertiary">
          <Lines additions={file.additions} deletions={file.deletions} /> ·{" "}
          {pluralize("hunk", file.hunks.length, true)}
        </span>
      </button>
      {open ? (
        <div className="px-3 pb-3">
          <HunkEvidence
            document={ctx.review}
            anchors={file.hunks.map((hunk) => ({ type: "hunk" as const, id: hunk.id }))}
            loadVersion={ctx.loadVersion}
          />
        </div>
      ) : null}
    </li>
  );
}

export const everythingElseTile: TileType = {
  type: "everything_else",
  label: "Everything else",
  icon: LayersIcon,
  Glance: EverythingElseGlance,
  Detail: EverythingElseDetail,
  valid,
  commentAnchors: (_ctx, tile) =>
    (tile.covers ?? []).slice(0, 1).map((id) => ({ type: "hunk" as const, id })),
};
