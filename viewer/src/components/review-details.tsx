import pluralize from "pluralize";
import type { ReactNode } from "react";

import {
  humanize,
  implementsGroups,
  type IndexedSession,
  type ReviewDocument,
  reviewDecisions,
  riskSegments,
  TIER_COLOR,
} from "../review-document";
import { HarnessName, ModelName } from "./brand-logo";
import { EM_DASH, formatDate, formatDuration } from "./format";
import { Markdown } from "./markdown";
import { ChecksPart } from "./review-checks";
import { ReviewExchange } from "./review-exchange";
import { AnchorChips, Facts, Item, JumpLink, Lines, None, Part, TierDot } from "./review-parts";
import { Badge } from "./ui";
import { useScrollingFlag } from "./use-scrolling-flag";

/** The pull request's standing facts, for the conversation pane's Details
 *  tab: whether CI passed, who wrote it and with what, what it says it
 *  implements, and how big it is. Narrow by design — the pane is 320px at
 *  its smallest — so every part is a list or a two-up grid. */
export function ReviewDetails({ document }: { document: ReviewDocument }) {
  const { summary, stats } = document;
  const implemented = implementsGroups(summary.implements);
  const segments = riskSegments(stats.risk_loc);
  const roles = Object.entries(stats.role_loc).filter(([, lines]) => lines && lines > 0);

  return (
    <div className="space-y-7">
      <ChecksPart document={document} />

      <Part label="Author">
        <Facts
          items={[
            {
              label: "Author",
              value: `${summary.author.login} · ${humanize(summary.author.association.toLowerCase())}${
                summary.author.bot ? " · bot" : ""
              }`,
            },
            {
              label: "Agents",
              value:
                summary.authorship.observed_sessions === 0 ? (
                  "None"
                ) : (
                  <ObservedLine
                    vendors={summary.authorship.observed_vendors}
                    models={summary.authorship.observed_models}
                  />
                ),
            },
          ]}
        />
        <AnchorChips document={document} anchors={summary.authorship.anchors} className="mt-2" />
      </Part>

      {implemented.length > 0 && (
        <Part label="Implements">
          <Facts
            items={implemented.map((group) => ({
              label: group.label,
              value:
                group.items.length > 0 ? (
                  <span className="flex flex-wrap gap-1.5">
                    {group.items.map((item) => (
                      <Badge key={item} size="compact">
                        {item}
                      </Badge>
                    ))}
                  </span>
                ) : (
                  "Referenced without an assertion"
                ),
            }))}
          />
        </Part>
      )}

      <Part label="Change size">
        <Figures
          items={[
            { label: "Sections", value: stats.sections },
            { label: "Files", value: stats.files },
            { label: "Hunks", value: stats.hunks },
            {
              label: "Lines",
              value: <Lines additions={stats.additions} deletions={stats.deletions} />,
            },
          ]}
        />
        {segments.length > 0 && (
          <div className="mt-3 space-y-1.5">
            <div
              role="img"
              aria-label={`Lines by risk tier: ${segments
                .map((segment) => `${segment.tier} ${segment.lines}`)
                .join(", ")}`}
              className="flex h-2 w-full overflow-hidden rounded-full bg-(--surface-sunken)"
            >
              {segments.map((segment) => (
                <span
                  key={segment.tier}
                  style={{ width: `${segment.share * 100}%`, background: TIER_COLOR[segment.tier] }}
                />
              ))}
            </div>
            <ul className="flex flex-wrap gap-x-4 gap-y-1 text-base text-(--text-secondary)">
              {segments.map((segment) => (
                <li key={segment.tier} className="flex items-center gap-1.5">
                  <TierDot tier={segment.tier} />
                  {humanize(segment.tier)}{" "}
                  <span className="tabular-nums text-(--text-tertiary)">
                    {segment.lines.toLocaleString("en-US")}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}
        {roles.length > 0 && (
          <div className="mt-3 space-y-1.5">
            <p className="text-xs font-semibold tracking-[0.1em] text-(--text-tertiary) uppercase">
              By role
            </p>
            {/* The same list shape as the risk legend above it, so the two
                breakdowns of the same lines read as a pair. */}
            <ul className="flex flex-wrap gap-x-4 gap-y-1 text-base text-(--text-secondary)">
              {roles.map(([role, lines]) => (
                <li key={role} className="flex items-center gap-1.5">
                  {humanize(role)}
                  <span className="tabular-nums text-(--text-tertiary)">
                    {lines!.toLocaleString("en-US")}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}
      </Part>
    </div>
  );
}

/** How the change came about, for the conversation pane's Activity tab: the
 *  review tools' back and forth, the coding sessions behind it, and the
 *  decisions the review recorded. */
export function ReviewActivity({
  document,
  onSelectSection,
  onSelectSession,
  onViewSessions,
}: {
  document: ReviewDocument;
  onSelectSection: (id: string) => void;
  /** Opens one linked session in the Sessions pane. The document's session
   *  ids are the pull request's run ids, so the rail can name the same one. */
  onSelectSession?: (id: string) => void;
  /** Opens the Sessions pane on the whole list; without it the link is not drawn. */
  onViewSessions?: () => void;
}) {
  const decisions = reviewDecisions(document);
  return (
    <div className="space-y-7">
      <ReviewExchange document={document} onSelectSection={onSelectSection} />

      <Part
        label="Linked sessions"
        aside={
          onViewSessions && document.index.sessions.length > 0 ? (
            <JumpLink className="text-xs" onClick={onViewSessions}>
              View all sessions →
            </JumpLink>
          ) : undefined
        }
      >
        {document.index.sessions.length === 0 ? (
          <None>No coding session is linked to this pull request.</None>
        ) : (
          <ul className="space-y-2">
            {document.index.sessions.map((session) => (
              <SessionItem
                key={session.id}
                session={session}
                onOpen={
                  onSelectSession && !session.restricted
                    ? () => onSelectSession(session.id)
                    : undefined
                }
              />
            ))}
          </ul>
        )}
      </Part>

      {decisions.length > 0 && (
        <Part
          label="Decisions"
          aside={
            <span className="text-xs text-(--text-tertiary)">
              {decisions.filter((item) => item.by === "agent").length} agent ·{" "}
              {decisions.filter((item) => item.by === "human").length} person
            </span>
          }
        >
          {/* The tab is theirs, so the decisions are listed rather than
              folded behind a count. */}
          <ul className="divide-y divide-(--border-subtle)">
            {decisions.map((decision) => (
              <li
                key={`${decision.section}-${decision.id}`}
                className="space-y-1 py-2.5 text-base first:pt-0 last:pb-0"
              >
                <div className="flex items-start gap-2">
                  <Badge size="compact" tone={decision.by === "human" ? "info" : "neutral"}>
                    {humanize(decision.by)}
                  </Badge>
                  <Markdown className="min-w-0 flex-1 text-(--text-primary)">
                    {decision.text}
                  </Markdown>
                </div>
                {decision.rejected && (
                  <div className="text-(--text-tertiary)">
                    Instead of <Markdown className="inline-block">{decision.rejected}</Markdown>
                  </div>
                )}
                <JumpLink className="text-xs" onClick={() => onSelectSection(decision.section)}>
                  {decision.sectionTitle} →
                </JumpLink>
              </li>
            ))}
          </ul>
        </Part>
      )}
    </div>
  );
}

/** A few counts as a two-up grid of wells, four across once the pane is wide
 *  enough to give each its label on one line. */
function Figures({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <dl className="grid grid-cols-2 gap-2 @xl:grid-cols-4">
      {items.map((item) => (
        <div key={item.label} className="rounded-(--radius-md) bg-(--surface-sunken) px-3 py-2">
          <dt className="truncate text-xs text-(--text-tertiary)">{item.label}</dt>
          <dd className="text-lg font-medium tabular-nums">{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}

/** Each harness and model by name and mark, on one line. The pane is narrow,
 *  and a list that wraps there stacks one name per row; a line longer than the
 *  column scrolls sideways instead, its bar showing only while it moves. */
function ObservedLine({ vendors, models }: { vendors: string[]; models: string[] }) {
  const onScroll = useScrollingFlag();
  return (
    <span
      onScroll={onScroll}
      // Unlike a chart legend, nothing beside this line needs its height
      // matched, so the track is taken only when the line overflows.
      style={{ overflowX: "auto" }}
      className="scroll-on-demand flex min-w-0 items-center gap-x-2.5 whitespace-nowrap"
    >
      {vendors.map((vendor) => (
        <HarnessName key={vendor} vendor={vendor} className="flex-none" />
      ))}
      {models.map((model) => (
        <ModelName key={model} model={model} className="flex-none text-(--text-secondary)" />
      ))}
    </span>
  );
}

/** One linked session: its harness, models and outcome, then what it was
 *  about, with its prompts and events a click away. */
function SessionItem({
  session,
  onOpen,
}: {
  session: IndexedSession;
  /** Opens this session in the Sessions pane, where its full transcript and
   *  its unlink are. */
  onOpen?: () => void;
}) {
  if (session.restricted) {
    return (
      <Item className="flex flex-wrap items-center gap-2">
        <HarnessName vendor={session.vendor} className="font-medium" />
        <Badge size="compact">Details restricted</Badge>
        <span className="w-full text-base text-(--text-tertiary)">
          This repository is not a connected member of this session, so the review carries none of
          its prompts or events.
        </span>
      </Item>
    );
  }
  const duration =
    session.duration_minutes != null ? formatDuration(session.duration_minutes * 60) : EM_DASH;
  const meta = [
    session.started_at ? formatDate(session.started_at) : null,
    duration,
    session.prompt_count != null ? pluralize("prompt", session.prompt_count, true) : null,
    session.tool_calls != null ? pluralize("tool call", session.tool_calls, true) : null,
  ].filter(Boolean);
  return (
    <Item className="space-y-1.5">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        {onOpen ? (
          <JumpLink tone="primary" className="font-medium" onClick={onOpen}>
            <HarnessName vendor={session.vendor} /> →
          </JumpLink>
        ) : (
          <HarnessName vendor={session.vendor} className="font-medium" />
        )}
        {session.models.map((model) => (
          <Badge key={model} size="compact">
            <ModelName model={model} size={12} />
          </Badge>
        ))}
        {session.outcome && (
          <Badge size="compact" tone={session.outcome === "committed" ? "positive" : "neutral"}>
            {humanize(session.outcome)}
          </Badge>
        )}
      </div>
      {/* Its own line: beside the badges it wrapped wherever the pane's
          width happened to break it. */}
      <p className="text-xs text-(--text-tertiary)">{meta.join(" · ")}</p>
      {(session.title || session.git_branch) && (
        <p className="text-base text-(--text-primary)">
          {session.title}
          {session.title && session.git_branch ? " · " : ""}
          {session.git_branch && <code className="font-mono text-base">{session.git_branch}</code>}
        </p>
      )}
      {session.prompts && session.prompts.length > 0 && (
        <details className="group">
          <summary className="cursor-pointer text-base text-(--text-link) hover:underline">
            {pluralize("prompt", session.prompts.length, true)}
          </summary>
          <ol className="mt-2 space-y-2">
            {session.prompts.map((prompt) => (
              <li key={prompt.id} className="flex gap-2 text-base">
                <span className="w-6 flex-none text-right tabular-nums text-(--text-tertiary)">
                  {prompt.index}
                </span>
                <Markdown className="min-w-0 flex-1 text-(--text-primary)">{prompt.text}</Markdown>
              </li>
            ))}
          </ol>
        </details>
      )}
      {session.events && session.events.length > 0 && (
        <details>
          <summary className="cursor-pointer text-base text-(--text-link) hover:underline">
            {pluralize("event", session.events.length, true)}
          </summary>
          <ul className="mt-2 space-y-1">
            {session.events.map((event) => (
              <li key={event.id} className="flex gap-2 text-base">
                <span className="w-12 flex-none text-(--text-tertiary)" title={event.at}>
                  {event.at.slice(11, 16)}
                </span>
                <span className="w-16 flex-none text-(--text-tertiary)">
                  {humanize(event.verb)}
                </span>
                <Markdown className="min-w-0 flex-1 text-(--text-primary)">
                  {event.summary}
                </Markdown>
              </li>
            ))}
          </ul>
        </details>
      )}
    </Item>
  );
}
