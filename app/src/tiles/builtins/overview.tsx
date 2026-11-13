import pluralize from "pluralize";
import { StatusBlock, StatusLine } from "@baum/viewer/components/editorial";
import { SummaryIcon, WarningIcon } from "@baum/viewer/components/icons";
import { Markdown } from "@baum/viewer/components/markdown";
import { ChangeKindTag, Facts, Lines, Part, PROSE } from "@baum/viewer/components/review-parts";
import { humanize } from "@baum/viewer/review-document";
import { Clamp, Fade, plainText } from "../parts";
import type { OverviewData } from "../tile-document";
import { isRecord, type TileFaceProps, type TileType } from "../types";

const valid = (data: unknown) =>
  isRecord(data) && typeof data.text === "string" && typeof data.change_kind === "string";

const dataOf = (data: unknown) => data as OverviewData;

/** What the pull request does, in its first paragraph, what kind of change it
 *  is and how long it takes to read, with a breaking change and what needs
 *  attention called out under it. */
function OverviewGlance({ tile, ctx }: TileFaceProps) {
  const data = dataOf(tile.data);
  const minutes = ctx.doc.facts.review_minutes;
  const paragraph = plainText(data.text.split(/\n\s*\n/)[0], ctx.labels);
  const stats = ctx.doc.stats;
  return (
    <div className="flex h-full min-h-0 flex-col gap-3">
      <p className="flex flex-wrap items-center gap-x-2.5 gap-y-1 text-xs text-tertiary">
        <ChangeKindTag kind={data.change_kind} />
        {minutes ? <span>≈{pluralize("minute", minutes, true)} to read</span> : null}
      </p>
      <Fade className="space-y-2.5">
        <Clamp
          lines={data.breaking_change || data.attention?.text ? 3 : 4}
          className="text-base leading-normal text-primary"
        >
          {paragraph}
        </Clamp>
        {data.breaking_change ? (
          <p className="flex items-start gap-1.5 text-base text-status-critical">
            <WarningIcon size={14} className="mt-0.5 flex-none" />
            <span className="line-clamp-2 min-w-0">
              Breaking: {plainText(data.breaking_change, ctx.labels)}
            </span>
          </p>
        ) : null}
        {data.attention?.text ? (
          <p className="flex items-start gap-1.5 text-base text-secondary">
            <WarningIcon size={14} className="mt-0.5 flex-none text-status-caution" />
            <span className="line-clamp-2 min-w-0">
              {plainText(data.attention.text, ctx.labels)}
            </span>
          </p>
        ) : null}
      </Fade>
      {stats ? (
        <p className="flex flex-wrap items-center gap-x-1.5 text-xs text-tertiary">
          <Lines additions={stats.additions} deletions={stats.deletions} />
          <span aria-hidden>·</span>
          <span>{pluralize("file", stats.files, true)}</span>
          {ctx.doc.facts.author?.login ? (
            <>
              <span aria-hidden>·</span>
              <span className="truncate">by {ctx.doc.facts.author.login}</span>
            </>
          ) : null}
        </p>
      ) : null}
    </div>
  );
}

/** The overview in full: the text, a breaking change and the release note,
 *  what needs attention, and where the review's policy or the stack stand. */
function OverviewDetail({ tile, ctx }: TileFaceProps) {
  const data = dataOf(tile.data);
  const generator = ctx.review.review.generator;
  const stack = ctx.doc.stack;
  const minutes = ctx.doc.facts.review_minutes;
  return (
    <div className="space-y-7">
      {generator.review_md_changed ? (
        <StatusLine tone="caution" label="Review policy changed" boxed>
          {generator.review_md
            ? "This pull request changes REVIEW.md. The review followed the base branch's REVIEW.md, not the version in this pull request."
            : "This pull request changes REVIEW.md. The base branch has no REVIEW.md, so the review used the default review policy."}
        </StatusLine>
      ) : null}
      <div className="space-y-3">
        <p className="flex flex-wrap items-center gap-x-2.5 gap-y-1 text-xs text-tertiary">
          <ChangeKindTag kind={data.change_kind} />
          {minutes ? <span>≈{pluralize("minute", minutes, true)} to read</span> : null}
        </p>
        <Markdown className={PROSE}>{data.text}</Markdown>
      </div>
      {data.breaking_change ? (
        <StatusBlock tone="critical" label="Breaking change">
          <Markdown className="max-w-[90ch] text-base leading-[1.5] text-secondary">
            {data.breaking_change}
          </Markdown>
        </StatusBlock>
      ) : null}
      {data.attention?.text ? (
        <StatusBlock tone="caution" label="Needs attention" boxed>
          <Markdown className="max-w-[90ch] text-base leading-[1.5] text-primary">
            {data.attention.text}
          </Markdown>
          {data.attention.refs.length > 0 ? (
            // The references as chips: the markdown pass names each one.
            <Markdown className="mt-2 text-base">
              {data.attention.refs.filter((ref) => !data.attention!.text.includes(ref)).join(" ")}
            </Markdown>
          ) : null}
        </StatusBlock>
      ) : null}
      {data.release_note ? (
        <StatusBlock tone="neutral" label="Release note">
          <Markdown className="max-w-[90ch] text-base leading-[1.5] text-secondary">
            {data.release_note}
          </Markdown>
        </StatusBlock>
      ) : null}
      {stack && stack.members.length > 0 ? (
        <Part label={`Stack · ${stack.title}`}>
          <ul className="space-y-1 text-base">
            {stack.members.map((member) => (
              <li key={member.number} className="flex gap-2">
                <span className="tabular-nums text-primary">#{member.number}</span>
                <span className="text-tertiary">review v{member.review_version}</span>
              </li>
            ))}
          </ul>
        </Part>
      ) : null}
      <Part label="About the change">
        <Facts
          items={[
            ...(ctx.doc.facts.author?.login
              ? [
                  {
                    label: "Author",
                    value: `${ctx.doc.facts.author.login}${ctx.doc.facts.author.bot ? " (bot)" : ""}`,
                  },
                ]
              : []),
            {
              label: "Size",
              value: (
                <>
                  <Lines additions={ctx.doc.stats.additions} deletions={ctx.doc.stats.deletions} />
                  {` · ${pluralize("file", ctx.doc.stats.files, true)} · ${pluralize("hunk", ctx.doc.stats.hunks, true)}`}
                </>
              ),
            },
            { label: "Kind", value: humanize(data.change_kind) },
          ]}
        />
      </Part>
    </div>
  );
}

export const overviewTile: TileType = {
  type: "overview",
  label: "Overview",
  icon: SummaryIcon,
  Glance: OverviewGlance,
  Detail: OverviewDetail,
  valid,
};
