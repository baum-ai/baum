import pluralize from "pluralize";
import type { ReactNode } from "react";
import { ViewerLink } from "../navigation";
import { harnessBrand, modelBrand } from "../brands";
import { withSelection } from "../review-selection";

import {
  humanize,
  decisionGroups,
  implementsGroups,
  type IndexedSession,
  type ReviewDocument,
  reviewDecisions,
  type SectionDecision,
  riskSegments,
  TIER_COLOR,
} from "../review-document";
import { HarnessName, ModelName } from "./brand-logo";
import { cn, FOCUS_RING } from "./classnames";
import { EM_DASH, formatDate, formatDuration } from "./format";
import { InlineMarkdown } from "./markdown";
import { ChecksPart } from "./review-checks";
import { ReviewExchange, StackReviewExchange } from "./review-exchange";
import { AnchorChips, Facts, Item, JumpLink, Lines, None, Part, TierDot } from "./review-parts";
import { Skeleton } from "./states";
import { Badge } from "./ui";

/** The pull request's standing facts, for the conversation pane's Details
 *  tab: who wrote it and with what, what it says it implements, and how big
 *  it is, after whether CI passed when the Activity tab does not show it. Narrow by design — the pane is 320px at
 *  its smallest — so every part is a list or a two-up grid. */
export function ReviewDetails({
  document,
  checks,
}: {
  document: ReviewDocument;
  /** Replaces the Checks part; null leaves it out. */
  checks?: ReactNode;
}) {
  const { summary, stats } = document;
  const implemented = implementsGroups(summary.implements);
  const segments = riskSegments(stats.risk_loc);
  const roles = Object.entries(stats.role_loc).filter(([, lines]) => lines && lines > 0);

  return (
    <div className="space-y-7">
      {checks === undefined ? <ChecksPart document={document} /> : checks}

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
                    models={summary.authorship.observed_models ?? []}
                    sessions={document.index.sessions}
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

/** A stack member's own review, for an Activity tab that gathers a stack's. */
export type ActivityMember = {
  number: number;
  document: ReviewDocument;
  /** Names the member beside what came from it, such as a link to its page. */
  tag: ReactNode;
  /** The member's review page, used for links to individual decisions. */
  href?: string;
  /** Decisions from the member version covered by the stack, when evidence is live. */
  decisions?: { version: number; items: SectionDecision[] };
};

/** The member reviews a stack's Activity tab gathers, as far as they are
 *  read. */
export type ActivityMembers = {
  members: ActivityMember[];
  /** Some members' reviews have not answered yet. */
  loading: boolean;
  /** Drawn under the checks, such as which members could not be read. */
  note?: ReactNode;
};

/** What still needs the author and how the change came about, for the
 *  conversation pane's Activity tab: whether CI passed, the review tools'
 *  back and forth, the coding sessions behind it, and the decisions the
 *  review recorded. */
export function ReviewActivity({
  document,
  checks,
  members,
  onSelectSection,
  onSelectSession,
  onViewSessions,
}: {
  document: ReviewDocument;
  /** Replaces the Checks part; null leaves it out. */
  checks?: ReactNode;
  /** A stack's member reviews: each part then gathers theirs rather than
   *  reading the document's own. */
  members?: ActivityMembers;
  onSelectSection: (id: string, decision?: string) => void;
  /** Opens one linked session in the Sessions pane. The document's session
   *  ids are the pull request's run ids, so the rail can name the same one. */
  onSelectSession?: (id: string) => void;
  /** Opens the Sessions pane on the whole list; without it the link is not drawn. */
  onViewSessions?: () => void;
}) {
  const checksPart = checks === undefined ? <ChecksPart document={document} /> : checks;
  if (members) {
    return (
      <StackActivity
        checks={checksPart}
        members={members}
        onSelectSession={onSelectSession}
        onViewSessions={onViewSessions}
      />
    );
  }
  const decisions: ListedDecision[] = reviewDecisions(document).map((decision) => ({
    ...decision,
    key: `${decision.section}-${decision.id}`,
    section: {
      title: decision.sectionTitle,
      select: () => onSelectSection(decision.section, decision.id),
    },
  }));
  return (
    <div className="space-y-7">
      {checksPart}

      <ReviewExchange document={document} onSelectSection={onSelectSection} />

      <Decisions decisions={decisions} />

      <LinkedSessions
        sessions={document.index.sessions}
        empty="No coding session is linked to this pull request."
        onSelectSession={onSelectSession}
        onViewSessions={onViewSessions}
      />
    </div>
  );
}

/** A stack's Activity tab: its checks, then each part gathered from the
 *  members' reviews, every thread and decision tagged with its member. */
function StackActivity({
  checks,
  members: { members, loading, note },
  onSelectSession,
  onViewSessions,
}: {
  checks: ReactNode;
  members: ActivityMembers;
  onSelectSession?: (id: string) => void;
  onViewSessions?: () => void;
}) {
  // A session can have produced commits in several members; it is listed
  // once, and in full when any member's review carries it in full.
  const sessions = new Map<string, IndexedSession>();
  for (const member of members) {
    for (const session of member.document.index.sessions) {
      const listed = sessions.get(session.id);
      if (!listed || (listed.restricted && !session.restricted)) sessions.set(session.id, session);
    }
  }
  const listed: ListedDecision[] = members.flatMap((member) =>
    (member.decisions?.items ?? reviewDecisions(member.document)).map((decision) => ({
      ...decision,
      key: `${member.number}:${decision.section}-${decision.id}`,
      section: undefined,
      memberNumber: member.number,
      href: member.href ? memberDecisionHref(member, decision) : undefined,
    })),
  );
  const unique = new Map<string, ListedDecision>();
  for (const decision of listed) {
    const sourceSessions = [
      ...new Set(
        decision.anchors.flatMap((anchor) => (anchor.type === "prompt" ? [anchor.session] : [])),
      ),
    ].toSorted();
    const identity = sourceSessions.length
      ? JSON.stringify([
          decision.text.trim(),
          sourceSessions,
          decision.by,
          !!decision.approved_by_human,
        ])
      : decision.key;
    if (!unique.has(identity)) unique.set(identity, decision);
  }
  const decisions = [...unique.values()];
  return (
    <div className="space-y-7">
      {checks}
      {note}
      {loading ? (
        <Skeleton height={200} />
      ) : (
        <>
          <StackReviewExchange members={members} />

          <Decisions decisions={decisions} />

          <LinkedSessions
            sessions={[...sessions.values()]}
            empty="No coding session is linked to these pull requests."
            onSelectSession={onSelectSession}
            onViewSessions={onViewSessions}
          />
        </>
      )}
    </div>
  );
}

function LinkedSessions({
  sessions,
  empty,
  onSelectSession,
  onViewSessions,
}: {
  sessions: IndexedSession[];
  empty: string;
  onSelectSession?: (id: string) => void;
  onViewSessions?: () => void;
}) {
  return (
    <Part
      label="Linked sessions"
      aside={
        onViewSessions && sessions.length > 0 ? (
          <JumpLink className="text-xs" onClick={onViewSessions}>
            View all sessions →
          </JumpLink>
        ) : undefined
      }
    >
      {sessions.length === 0 ? (
        <None>{empty}</None>
      ) : (
        <ul className="space-y-2">
          {sessions.map((session) => (
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
  );
}

/** A decision as listed: where it leads — its section in this review, or
 *  the stack member it came from. */
type ListedDecision = Omit<SectionDecision, "section"> & {
  key: string;
  section?: { title: string; select: () => void };
  memberNumber?: number;
  href?: string;
};

function memberDecisionHref(member: ActivityMember, decision: SectionDecision): string {
  const url = new URL(member.href!, "https://viewer.invalid");
  url.search = withSelection(url.searchParams, {
    kind: "section",
    id: decision.section,
    decision: decision.id,
  }).toString();
  url.searchParams.set(
    "review",
    String(member.decisions?.version ?? member.document.review.version),
  );
  return url.origin === "https://viewer.invalid"
    ? `${url.pathname}${url.search}${url.hash}`
    : url.href;
}

function Decisions({ decisions }: { decisions: ListedDecision[] }) {
  if (decisions.length === 0) return null;
  return (
    <Part label="Decisions">
      {/* Decisions made or approved by a person lead the list. */}
      <div className="space-y-3">
        {decisionGroups(decisions).map((group) => (
          <DecisionGroup key={group.label} label={group.label} decisions={group.items} />
        ))}
      </div>
    </Part>
  );
}

/** The same compact decision rows in a PR and a stack; only the destination differs. */
function DecisionGroup({ label, decisions }: { label: string; decisions: ListedDecision[] }) {
  if (decisions.length === 0) return null;
  return (
    <div>
      <p className="mb-1 text-base font-semibold text-(--text-secondary)">
        {label} · {decisions.length}
      </p>
      <ul className="divide-y divide-(--border-subtle)">
        {decisions.map((decision) => (
          <DecisionRow key={decision.key} decision={decision} />
        ))}
      </ul>
    </div>
  );
}

function DecisionRow({ decision }: { decision: ListedDecision }) {
  const className = cn(
    "flex w-full items-start gap-2 rounded-sm text-left hover:text-(--text-link) hover:no-underline",
    FOCUS_RING,
  );
  const content = (
    <>
      <InlineMarkdown className="min-w-0 flex-1">
        {decision.title?.trim() || decision.text}
      </InlineMarkdown>
      <span className="flex flex-none items-baseline gap-1 text-(--text-tertiary)">
        {decision.memberNumber && <span className="text-xs">#{decision.memberNumber}</span>}
        {(decision.href || decision.section) && <span aria-hidden>→</span>}
      </span>
    </>
  );
  return (
    <li className="flex items-start gap-1 py-1.5 text-base">
      <div className="min-w-0 flex-1">
        {decision.href ? (
          <ViewerLink
            href={decision.href}
            className={className}
            title={`Open decision in PR #${decision.memberNumber}`}
          >
            {content}
          </ViewerLink>
        ) : decision.section ? (
          <button
            type="button"
            onClick={decision.section.select}
            className={cn(className, "cursor-pointer")}
            title={`Open in ${decision.section.title}`}
          >
            {content}
          </button>
        ) : (
          <div className="flex items-start gap-2">{content}</div>
        )}
      </div>
    </li>
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

function modelLine(items: string[]) {
  return items.length > 0 ? (
    <span className="block truncate text-xs text-(--text-tertiary)" title={items.join(", ")}>
      {items[0]}
      {items.length > 1 ? ` + ${items.length - 1}` : ""}
    </span>
  ) : null;
}

/** Session metadata associates models with the harness that actually used them. */
function ObservedLine({
  vendors,
  models,
  sessions,
}: {
  vendors: string[];
  models: string[];
  sessions: IndexedSession[];
}) {
  const groups = vendors.map((vendor) => ({
    vendor,
    models: [
      ...new Set(
        sessions
          .filter((session) => session.vendor === vendor)
          .flatMap((session) => session.models ?? []),
      ),
    ].filter((model) => models.includes(model)),
  }));
  if (groups.length === 1 && groups[0]!.models.length === 0) groups[0]!.models = models;
  // Stack summaries may carry model names without indexed sessions. Associate
  // those only when the model family identifies one unambiguous harness.
  for (const model of models) {
    if (groups.some((group) => group.models.includes(model))) continue;
    const brand = modelBrand(model);
    const matching = groups.filter((group) => brand && harnessBrand(group.vendor) === brand);
    if (matching.length === 1) matching[0]!.models.push(model);
  }
  const assigned = new Set(groups.flatMap((group) => group.models));
  const unassigned = models.filter((model) => !assigned.has(model));
  return (
    <span className="flex min-w-0 flex-wrap items-start gap-x-4 gap-y-1.5">
      {groups.map((group) => (
        <span key={group.vendor} className="flex min-w-0 max-w-full flex-col">
          <HarnessName vendor={group.vendor} />
          <span className={cn("block", harnessBrand(group.vendor) && "pl-5")}>
            {modelLine(group.models)}
          </span>
        </span>
      ))}
      {modelLine(unassigned)}
    </span>
  );
}

/** One linked session: its harness, models and outcome, then what it was
 *  about, with its prompts and events a click away. */
export function SessionItem({
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
      <Item compact className="flex flex-wrap items-center gap-2">
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
  const [firstModel, ...otherModels] = session.models ?? [];
  return (
    <Item
      compact
      className={cn(
        "space-y-0.5",
        onOpen && "relative cursor-pointer transition-colors hover:bg-(--surface-hover)",
      )}
    >
      {/* One row: the first model, and how many more beside it, named in
          its tooltip, rather than a badge each that wraps the header. */}
      <div className="flex min-w-0 items-center gap-2">
        {onOpen ? (
          // Its hit area covers the card, so the whole card opens the session
          // while the harness stays the one control a keyboard reaches.
          <JumpLink
            tone="primary"
            className="flex-none font-medium after:absolute after:inset-0 after:content-['']"
            onClick={onOpen}
          >
            <HarnessName vendor={session.vendor} />
          </JumpLink>
        ) : (
          <HarnessName vendor={session.vendor} className="flex-none font-medium" />
        )}
        {firstModel && (
          <Badge size="compact" className="min-w-0">
            <ModelName model={firstModel} size={12} />
          </Badge>
        )}
        {otherModels.length > 0 && (
          // Above the card's hit area, so its tooltip still shows.
          <Badge size="compact" className="relative flex-none" title={otherModels.join(", ")}>
            +{otherModels.length}
          </Badge>
        )}
      </div>
      {/* Its own line: beside the badges it wrapped wherever the pane's
          width happened to break it. */}
      <p className="text-xs text-(--text-tertiary)">{meta.join(" · ")}</p>
      {(session.title || session.git_branch) && (
        <p
          className="truncate text-xs text-(--text-secondary)"
          title={[session.title, session.git_branch].filter(Boolean).join(" · ")}
        >
          {session.title}
          {session.title && session.git_branch ? " · " : ""}
          {session.git_branch && <code className="font-mono">{session.git_branch}</code>}
        </p>
      )}
    </Item>
  );
}
