import { type ReactNode, useState } from "react";

import pluralize from "pluralize";

import {
  combineExchangeFacts,
  compareOpenThreads,
  type ExchangeActor,
  type ExchangeEvent,
  type OpenThread,
  humanize,
  repositoryLink,
  type ReviewDocument,
  reviewExchange,
  reviewExchangeFacts,
  type ReviewExchangeFacts,
  reviewThreadStatus,
  severityTone,
  verdictTone,
} from "../review-document";
import { formatDateTime, formatShortDateTime } from "./format";
import { CircleCheckIcon, ExternalLinkIcon, WarningIcon } from "./icons";
import { cn } from "./classnames";
import { Markdown } from "./markdown";
import { CommitLink, ItemMeta, JumpLink, Lines, None, Part } from "./review-parts";
import { Badge } from "./ui";

/** Who took the turn. Categorical, so these are palette colours rather than the
 *  reserved status tones — the verdict beside the dot is the status, the actor
 *  is not. A person is the deliberate absence of a tool identity, so they wear
 *  the muted slot.
 *
 *  Review uses the interaction accent, implementation uses its categorical
 *  identity and a person uses the neutral mark. Each follows the theme. */
const ACTOR_COLOR: Record<ExchangeActor, string> = {
  review: "var(--accent-600)",
  implementation: "var(--data-3)",
  human: "var(--data-muted)",
};

/** Colour is never the only channel: every turn also says in words which kind
 *  it is, and the key names the colours for a reader scanning the rail. */
const ACTOR_LABEL: Record<ExchangeActor, string> = {
  review: "Review",
  implementation: "Implementation",
  human: "Person",
};

const ACTOR_ORDER: ExchangeActor[] = ["review", "implementation", "human"];

function ActorKey({ actors }: { actors: Set<ExchangeActor> }) {
  const present = ACTOR_ORDER.filter((actor) => actors.has(actor));
  if (present.length < 2) return null;
  return (
    <ul className="mb-3 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-(--text-tertiary)">
      {present.map((actor) => (
        <li key={actor} className="flex items-center gap-1.5">
          <span
            aria-hidden
            className="h-2 w-2 flex-none rounded-full"
            style={{ background: ACTOR_COLOR[actor] }}
          />
          {ACTOR_LABEL[actor]}
        </li>
      ))}
    </ul>
  );
}

/** The back and forth over one pull request as a timeline: what each review
 *  tool said, the commit that answered it, and where a person stepped in.
 *  Reading down the rail is reading the argument in order. */
export function ReviewExchange({
  document,
  onSelectSection,
}: {
  document: ReviewDocument;
  onSelectSection: (id: string) => void;
}) {
  const events = reviewExchange(document);
  const actors = new Set(events.map((event) => event.actor));
  const stated = reviewExchangeFacts(document);
  const reviews = events.filter((event) => event.actor === "review");
  const rounds = reviews.length;
  const tools = new Set(reviews.map((event) => event.by));
  const threads = reviewThreadStatus(document);
  const criticalOpen = hasOpenCritical(document);
  const sectionTitle = (id: string) =>
    document.sections.find((section) => section.id === id)?.title ?? id;

  return (
    <Part
      label="Review exchange"
      aside={
        rounds > 0 ? (
          <span className="text-xs text-(--text-tertiary)">
            {pluralize("round", rounds, true)} · {pluralize("tool", tools.size, true)}
          </span>
        ) : undefined
      }
    >
      {threads && (
        <OpenThreads
          total={threads.total}
          entries={threads.open.map((item) => ({
            ...item,
            key: item.thread.id,
            document,
            section: item.finding?.section
              ? {
                  title: sectionTitle(item.finding.section),
                  select: () => onSelectSection(item.finding?.section as string),
                }
              : null,
          }))}
        />
      )}
      {events.length === 0 ? (
        <None>No review tool assessed this pull request.</None>
      ) : (
        <Run facts={stated} criticalOpen={criticalOpen}>
          <ActorKey actors={actors} />
          <ExchangeTimeline
            document={document}
            events={events}
            sectionTitle={sectionTitle}
            onSelectSection={onSelectSection}
          />
        </Run>
      )}
    </Part>
  );
}

/** A finding's state is its thread's, and stays open while threads are not
 *  synced, so a critical count resolved since is not raised as an alarm. */
const hasOpenCritical = (document: ReviewDocument) =>
  document.agent_reviews.some((review) =>
    review.findings.some((finding) => finding.severity === "must_fix" && finding.state === "open"),
  );

/** The review exchanges of a stack's members as one: every thread still open
 *  in any of them, worst first and tagged with its member, and their figures
 *  summed. Each member's run of turns is read on its own page, so there is no
 *  full exchange here. */
export function StackReviewExchange({
  members,
}: {
  members: { number: number; document: ReviewDocument; tag: ReactNode }[];
}) {
  const reviews = members.flatMap((member) =>
    reviewExchange(member.document).filter((event) => event.actor === "review"),
  );
  const tools = new Set(reviews.map((event) => event.by));
  const synced = members.flatMap((member) => {
    const status = reviewThreadStatus(member.document);
    return status ? [{ member, status }] : [];
  });
  const entries = synced
    .flatMap(({ member, status }) =>
      status.open.map((item) => ({
        ...item,
        key: `${member.number}:${item.thread.id}`,
        document: member.document,
        member: member.tag,
      })),
    )
    .toSorted(compareOpenThreads);
  const facts = combineExchangeFacts(
    members.flatMap((member) => reviewExchangeFacts(member.document) ?? []),
  );
  return (
    <Part
      label="Review exchange"
      aside={
        reviews.length > 0 ? (
          <span className="text-xs text-(--text-tertiary)">
            {pluralize("round", reviews.length, true)} · {pluralize("tool", tools.size, true)}
          </span>
        ) : undefined
      }
    >
      {synced.length > 0 && (
        <OpenThreads
          entries={entries}
          total={synced.reduce((total, { status }) => total + status.total, 0)}
        />
      )}
      {reviews.length === 0 && !facts ? (
        <None>No review tool assessed these pull requests.</None>
      ) : facts ? (
        <ExchangeFigures
          facts={facts}
          criticalOpen={members.some((member) => hasOpenCritical(member.document))}
        />
      ) : null}
    </Part>
  );
}

/** An open thread as listed: the review it is in, for its links, and where
 *  it leads — its section in this review, or the member it came from. */
type ThreadEntry = OpenThread & {
  key: string;
  document: ReviewDocument;
  member?: ReactNode;
  section?: { title: string; select: () => void } | null;
};

const SHOWN_THREADS = 5;

/** What the exchange left open, ahead of how it went: each review thread
 *  nobody has resolved, worst first, so a reader knows where to look before
 *  reading the history. An open must-fix finding makes the box critical. With
 *  every thread resolved it is one quiet line, and with none it is absent. */
function OpenThreads({ entries: open, total }: { entries: ThreadEntry[]; total: number }) {
  const [expanded, setExpanded] = useState(false);
  if (total === 0) return null;
  if (open.length === 0) {
    return (
      <p className="mb-4 flex items-center gap-1.5 text-base text-(--status-positive)">
        <CircleCheckIcon size={14} aria-hidden />
        {total === 1 ? "The review thread is resolved" : `All ${total} review threads resolved`}
      </p>
    );
  }
  const critical = open.some((item) => item.finding?.severity === "must_fix");
  const shown = expanded ? open : open.slice(0, SHOWN_THREADS);
  return (
    // A rule and a coloured heading rather than a filled box: it should be
    // the first thing read, not louder than the rest of the pane.
    <div
      className={cn(
        "mb-4 border-l-2 pl-3",
        critical ? "border-l-(--status-critical)" : "border-l-(--status-caution)",
      )}
    >
      <p
        className={cn(
          "flex items-center gap-1.5 text-base font-medium",
          critical ? "text-(--status-critical)" : "text-(--status-caution)",
        )}
      >
        <WarningIcon size={14} aria-hidden />
        {pluralize("unresolved thread", open.length, true)}{" "}
        <span className="font-normal text-(--text-tertiary)">of {total}</span>
      </p>
      <ul className="mt-1.5 divide-y divide-(--border-subtle)">
        {shown.map((entry) => (
          <OpenThreadRow key={entry.key} entry={entry} />
        ))}
      </ul>
      {open.length > SHOWN_THREADS && (
        <button
          type="button"
          onClick={() => setExpanded(!expanded)}
          className="mt-2 cursor-pointer text-base text-(--text-link) hover:underline"
        >
          {expanded ? "Show fewer" : `Show ${open.length - SHOWN_THREADS} more`}
        </button>
      )}
    </div>
  );
}

/** One open thread: the finding a tool raised on it, else where a person
 *  started it, with the way to GitHub and to the section it concerns. */
function OpenThreadRow({
  entry: { thread, finding, tool, document, member, section },
}: {
  entry: ThreadEntry;
}) {
  const where = thread.path
    ? `${thread.path}${thread.line ? `:${thread.line}` : ""}`
    : "the pull request";
  const href = repositoryLink(document, thread.url);
  const place = href ? (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      className="inline-flex items-center gap-1 font-mono text-(--text-link) hover:underline"
    >
      {where}
      <ExternalLinkIcon size={10} />
    </a>
  ) : (
    <span className="font-mono">{where}</span>
  );
  return (
    <li className="space-y-1 py-2 first:pt-0 last:pb-0">
      {finding ? (
        <div className="flex items-start gap-2">
          <Badge tone={severityTone(finding.severity)} size="compact">
            {humanize(finding.severity)}
          </Badge>
          <Markdown className="line-clamp-3 min-w-0 flex-1 text-(--text-primary)">
            {finding.text}
          </Markdown>
        </div>
      ) : (
        <p className="text-base text-(--text-primary)">Discussion on {place}</p>
      )}
      <ItemMeta>
        {member}
        {tool && <span>{tool}</span>}
        {finding && place}
        {thread.outdated && <span>Outdated</span>}
        {section && (
          <JumpLink className="text-xs" onClick={section.select}>
            {section.title} →
          </JumpLink>
        )}
      </ItemMeta>
    </li>
  );
}

const plural = (count: number | null, one: string, many: string) => (count === 1 ? one : many);

/** The document's own account of the review: how it went in one line of
 *  figures, then behind a disclosure the paragraph that tells it and the run
 *  of turns it describes. What is still open sits above all of it.
 *
 *  Three readers are served in that order — one who wants to know whether the
 *  review found anything, one who wants to know how it went, and one who wants
 *  to know what was actually said, when, and against which head. A document
 *  with no account at all shows the run as it always did. */
function Run({
  facts,
  criticalOpen,
  children,
}: {
  facts: ReviewExchangeFacts | null;
  criticalOpen: boolean;
  children: ReactNode;
}) {
  if (!facts) return children;
  return (
    <>
      <ExchangeFigures facts={facts} criticalOpen={criticalOpen} />
      {/* Spaced from the figures when there are any. */}
      <details className="[dl+&]:mt-3">
        <summary className="cursor-pointer text-base text-(--text-link) hover:underline">
          Full exchange
        </summary>
        {/* The account in prose introduces the run it describes, rather
            than standing between the figures and the way into it. */}
        {facts.summary && (
          <Markdown className="mt-3 max-w-[90ch] text-base leading-[1.6] text-(--text-secondary)">
            {facts.summary}
          </Markdown>
        )}
        <div className="mt-3">{children}</div>
      </details>
    </>
  );
}

/** How the review went in one line of figures; null when the account states
 *  none worth a place. */
function ExchangeFigures({
  facts,
  criticalOpen,
}: {
  facts: ReviewExchangeFacts;
  criticalOpen: boolean;
}) {
  const { changesAfterOpen: changes } = facts;
  const figures = [
    { key: "reviews", label: plural(facts.reviews, "review", "reviews"), value: facts.reviews },
    {
      key: "back-and-forths",
      label: plural(facts.backAndForths, "back and forth", "back and forths"),
      value: facts.backAndForths,
    },
    {
      key: "critical",
      label: plural(facts.criticalIssues, "critical issue found", "critical issues found"),
      value: facts.criticalIssues,
      alarming: criticalOpen,
    },
    {
      key: "commits",
      label: plural(changes?.commits ?? null, "commit since opening", "commits since opening"),
      value: changes?.commits ?? null,
    },
  ]
    .filter((figure) => facts.counted || figure.value != null)
    // No critical issue is not worth a line of its own.
    .filter((figure) => !(figure.key === "critical" && figure.value === 0));

  if (figures.length === 0) return null;
  return (
    // History, not a call to act, so one line of figures rather than a
    // grid of wells competing with the open threads above. The value
    // leads visually; the term comes first for a screen reader.
    <dl className="flex flex-wrap items-baseline gap-x-4 gap-y-1 text-base">
      {figures.map((figure) => (
        <div key={figure.key} className="flex items-baseline gap-1.5">
          <dt className="order-2 text-(--text-secondary)">{figure.label}</dt>
          <dd
            className={cn(
              "order-1 font-medium tabular-nums",
              // A critical issue still open must not be skimmed past.
              figure.alarming && figure.value
                ? "text-(--status-critical)"
                : "text-(--text-primary)",
            )}
          >
            {figure.value ?? "Unknown"}
          </dd>
          {figure.key === "commits" && changes ? (
            <dd className="order-3 text-xs">
              <Lines additions={changes.additions} deletions={changes.deletions} />
            </dd>
          ) : null}
        </div>
      ))}
    </dl>
  );
}

type ExchangeRow =
  | { kind: "turn"; event: ExchangeEvent; count: number }
  | { kind: "quiet"; id: string; events: ExchangeEvent[] };

/** A review turn that tells the reader nothing: no findings, nothing blocking
 *  and no verdict beyond a comment — a deploy preview or a tracker posting on
 *  the pull request, or a review that came back clean. */
const isQuiet = (event: ExchangeEvent) =>
  event.actor === "review" &&
  event.findings.length === 0 &&
  !event.blockedOn &&
  (!event.verdict || event.verdict === "comment");

/** The run as rows: consecutive quiet turns fold into one, and a person's
 *  replies in a row into one with a count, so the turns that carry findings,
 *  verdicts and answers are not lost between them. */
function exchangeRows(events: ExchangeEvent[]): ExchangeRow[] {
  const rows: ExchangeRow[] = [];
  for (const event of events) {
    const last = rows.at(-1);
    if (isQuiet(event)) {
      if (last?.kind === "quiet") last.events.push(event);
      else rows.push({ kind: "quiet", id: `quiet-${event.id}`, events: [event] });
    } else if (
      event.actor === "human" &&
      last?.kind === "turn" &&
      last.event.actor === "human" &&
      last.event.by === event.by
    ) {
      last.count++;
    } else {
      rows.push({ kind: "turn", event, count: 1 });
    }
  }
  return rows;
}

function ExchangeTimeline({
  document,
  events,
  sectionTitle,
  onSelectSection,
}: {
  document: ReviewDocument;
  events: ExchangeEvent[];
  sectionTitle: (id: string) => string;
  onSelectSection: (id: string) => void;
}) {
  const rows = exchangeRows(events);
  return (
    <ol>
      {rows.map((row, position) => {
        const actor = row.kind === "quiet" ? "review" : row.event.actor;
        return (
          <li key={row.kind === "quiet" ? row.id : row.event.id} className="flex gap-3">
            <div className="flex w-2 flex-none flex-col items-center" aria-hidden>
              <span
                className={cn(
                  "mt-1.5 h-2 w-2 flex-none rounded-full",
                  // A quiet row is hollow: a turn happened, nothing was said.
                  row.kind === "quiet" && "border-[1.5px] bg-transparent",
                )}
                style={
                  row.kind === "quiet"
                    ? { borderColor: ACTOR_COLOR[actor] }
                    : { background: ACTOR_COLOR[actor] }
                }
              />
              {position < rows.length - 1 && <span className="w-px flex-1 bg-(--border-subtle)" />}
            </div>
            <div className="min-w-0 flex-1 pb-3">
              {row.kind === "quiet" ? (
                <QuietTurns document={document} events={row.events} />
              ) : (
                <Turn
                  document={document}
                  event={row.event}
                  count={row.count}
                  sectionTitle={sectionTitle}
                  onSelectSection={onSelectSection}
                />
              )}
            </div>
          </li>
        );
      })}
    </ol>
  );
}

/** One or more tools that posted without a finding, as a single muted line;
 *  several name themselves under it rather than taking a row each. */
function QuietTurns({ document, events }: { document: ReviewDocument; events: ExchangeEvent[] }) {
  const [first] = events;
  if (!first) return null;
  const names = [...new Set(events.map((event) => event.by))];
  return (
    <>
      <TurnLine
        document={document}
        by={first.by}
        at={first.at}
        url={events.length === 1 ? first.url : undefined}
      >
        {events.length === 1 ? (
          <>
            <span className="font-medium text-(--text-secondary)">{first.by}</span>{" "}
            <span className="text-(--text-tertiary)">commented, no findings</span>
          </>
        ) : (
          <span className="text-(--text-tertiary)">
            {pluralize("tool", names.length, true)} commented, no findings
          </span>
        )}
      </TurnLine>
      {events.length > 1 && (
        <p className="mt-0.5 text-xs text-(--text-tertiary)">{names.join(", ")}</p>
      )}
    </>
  );
}

/** A turn that said something: a review's verdict and findings, the commit
 *  that answered findings, or a person replying on the diff. */
function Turn({
  document,
  event,
  count,
  sectionTitle,
  onSelectSection,
}: {
  document: ReviewDocument;
  event: ExchangeEvent;
  count: number;
  sectionTitle: (id: string) => string;
  onSelectSection: (id: string) => void;
}) {
  return (
    <>
      <TurnLine document={document} by={event.by} at={event.at} url={event.url}>
        <span className="font-medium text-(--text-primary)">{event.by}</span>{" "}
        {event.actor === "review" && (
          <>
            <span className="text-(--text-secondary)">
              {event.verdict ? "reviewed" : "commented"}
            </span>
            {/* A comment is what reviewed already says; only a verdict that
                approves or blocks earns a badge. */}
            {event.verdict && event.verdict !== "comment" && (
              <>
                {" "}
                <Badge tone={verdictTone(event.verdict)} size="compact" className="align-middle">
                  {humanize(event.verdict)}
                </Badge>
              </>
            )}
            {event.commit && (
              <span className="text-xs text-(--text-tertiary)">
                {" "}
                on <CommitLink document={document} sha={event.commit} />
              </span>
            )}
          </>
        )}
        {event.actor === "implementation" && event.commit && (
          <span className="text-(--text-secondary)">
            answered in <CommitLink document={document} sha={event.commit} />
          </span>
        )}
        {event.actor === "human" && (
          <span className="text-(--text-secondary)">
            replied on the diff{count > 1 ? ` · ${count} times` : ""}
          </span>
        )}
      </TurnLine>
      {event.blockedOn && (
        <p className="mt-0.5 text-base text-(--status-caution)">Blocked on {event.blockedOn}</p>
      )}
      {event.actor === "implementation" && (
        <p className="mt-0.5 text-xs text-(--text-tertiary)">
          Answers{" "}
          {event.findings
            .map(
              (finding) =>
                `${humanize(finding.severity).toLowerCase()}${
                  finding.section ? ` in ${sectionTitle(finding.section)}` : ""
                }`,
            )
            .join(", ")}
        </p>
      )}
      {event.actor === "review" && event.findings.length > 0 && (
        <ul className="mt-1.5 space-y-2">
          {event.findings.map((finding) => (
            <li key={finding.id} className="flex items-start gap-2">
              <Badge
                tone={finding.state === "open" ? severityTone(finding.severity) : "neutral"}
                size="compact"
                className="flex-none"
              >
                {humanize(finding.severity)}
              </Badge>
              <div className="min-w-0 flex-1 space-y-0.5">
                <Markdown
                  className={cn(
                    "line-clamp-2",
                    // What is settled steps back so what is open reads first.
                    finding.state === "open" ? "text-(--text-primary)" : "text-(--text-tertiary)",
                  )}
                >
                  {finding.text}
                </Markdown>
                <ItemMeta>
                  <span
                    className={cn(
                      finding.state === "open" && "text-(--status-caution)",
                      finding.state === "resolved" && "text-(--status-positive)",
                    )}
                  >
                    {humanize(finding.state)}
                  </span>
                  {finding.section && (
                    <JumpLink
                      className="text-xs"
                      onClick={() => onSelectSection(finding.section as string)}
                    >
                      {sectionTitle(finding.section)} →
                    </JumpLink>
                  )}
                </ItemMeta>
              </div>
            </li>
          ))}
        </ul>
      )}
    </>
  );
}

/** A turn's one line: what happened on the left, wrapping as it must, and when
 *  on the right, kept on the first line. An answering commit has no time: the
 *  schema records which commit answered a finding, not when it landed. */
function TurnLine({
  document,
  by,
  at,
  url,
  children,
}: {
  document: ReviewDocument;
  by: string;
  at: string | null;
  url?: string;
  children: ReactNode;
}) {
  const href = repositoryLink(document, url);
  return (
    <div className="flex items-baseline gap-2">
      <p className="min-w-0 flex-1 text-base break-words">{children}</p>
      {(at || href) && (
        <span className="flex flex-none items-center gap-1.5 text-xs text-(--text-tertiary)">
          {at && (
            <time dateTime={at} title={formatDateTime(at)}>
              {formatShortDateTime(at)}
            </time>
          )}
          {href && (
            <a
              href={href}
              target="_blank"
              rel="noreferrer"
              aria-label={`Open ${by}'s comment on GitHub`}
              className="text-(--text-tertiary)"
            >
              <ExternalLinkIcon size={10} />
            </a>
          )}
        </span>
      )}
    </div>
  );
}
