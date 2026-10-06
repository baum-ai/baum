import type { ReactNode } from "react";

import pluralize from "pluralize";

import {
  type ExchangeActor,
  type ExchangeEvent,
  findingStateTone,
  humanize,
  repositoryLink,
  type ReviewDocument,
  reviewExchange,
  reviewExchangeFacts,
  type ReviewExchangeFacts,
  severityTone,
  verdictTone,
} from "../review-document";
import { formatDateTime } from "./format";
import { ExternalLinkIcon } from "./icons";
import { cn } from "./classnames";
import { Markdown } from "./markdown";
import { CommitLink, Item, JumpLink, Lines, None, Part } from "./review-parts";
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
      {events.length === 0 ? (
        <None>No review tool assessed this pull request.</None>
      ) : (
        <Run facts={stated}>
          <ActorKey actors={actors} />
          <ol>
            {events.map((event, position) => (
              <li key={event.id} className="flex gap-3">
                <div className="flex w-2 flex-none flex-col items-center" aria-hidden>
                  <span
                    className="mt-1.5 h-2 w-2 flex-none rounded-full"
                    style={{ background: ACTOR_COLOR[event.actor] }}
                  />
                  {position < events.length - 1 && (
                    <span className="w-px flex-1 bg-(--border-subtle)" />
                  )}
                </div>
                <div className="min-w-0 flex-1 pb-4">
                  <ExchangeHeader document={document} event={event} />
                  {event.blockedOn && (
                    <p className="mt-1 text-base text-(--status-caution)">
                      Blocked on {event.blockedOn}
                    </p>
                  )}
                  {event.actor === "implementation" && (
                    <p className="mt-1 text-base text-(--text-tertiary)">
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
                  {event.actor === "review" && (
                    <ul className="mt-1.5 space-y-1.5">
                      {event.findings.length === 0 ? (
                        <li>
                          <None>This review raised no findings.</None>
                        </li>
                      ) : (
                        event.findings.map((finding) => (
                          <Item key={finding.id} className="space-y-1">
                            {/* The tags take their own line and the finding the
                                width under them: beside them, in the narrow
                                pane, it ran a word to a line. */}
                            <div className="flex flex-wrap items-start gap-x-2 gap-y-1">
                              <Badge tone={severityTone(finding.severity)} size="compact">
                                {humanize(finding.severity)}
                              </Badge>
                              <Badge tone={findingStateTone(finding.state)} size="compact">
                                {humanize(finding.state)}
                              </Badge>
                              <Markdown className="w-full min-w-0 text-(--text-primary)">
                                {finding.text}
                              </Markdown>
                            </div>
                            {finding.section && (
                              <JumpLink
                                className="text-xs"
                                onClick={() => onSelectSection(finding.section as string)}
                              >
                                {sectionTitle(finding.section)} →
                              </JumpLink>
                            )}
                          </Item>
                        ))
                      )}
                    </ul>
                  )}
                </div>
              </li>
            ))}
          </ol>
        </Run>
      )}
    </Part>
  );
}

/** The document's own account of the review: the figures first, the paragraph
 *  under them, and the run of turns they describe behind a disclosure.
 *
 *  Three readers are served in that order — one who wants to know whether the
 *  review found anything, one who wants to know how it went, and one who wants
 *  to know what was actually said, when, and against which head. A document
 *  with no account at all shows the run as it always did. */
function Run({ facts, children }: { facts: ReviewExchangeFacts | null; children: ReactNode }) {
  if (!facts) return children;
  const { changesAfterOpen: changes } = facts;
  const figures = [
    { label: "Reviews", value: facts.reviews },
    { label: "Back and forths", value: facts.backAndForths },
    { label: "Critical issues", value: facts.criticalIssues, alarming: true },
    { label: "Commits since opening", value: changes?.commits ?? null },
  ].filter((figure) => facts.counted || figure.value != null);

  return (
    <>
      {figures.length > 0 && (
        // Two up in the conversation pane, four across once it is wide enough
        // to give each label its line (the pane is the query container).
        <dl className="grid grid-cols-2 gap-2 @xl:grid-cols-4">
          {figures.map((figure) => (
            // A label that wraps in a narrow pane pushes its figure down; the
            // figures sit at the foot of their wells so a row still lines up.
            <div
              key={figure.label}
              className="flex flex-col justify-between gap-0.5 rounded-(--radius-md) bg-(--surface-sunken) px-3 py-2"
            >
              <dt className="text-xs leading-snug text-(--text-tertiary)">{figure.label}</dt>
              <dd
                className={cn(
                  "flex flex-wrap items-baseline gap-x-2 text-lg font-medium tabular-nums",
                  // A count of critical issues is the one figure here a reader
                  // must not skim past, and only when it is not zero.
                  figure.alarming && figure.value ? "text-(--status-critical)" : undefined,
                )}
              >
                {figure.value ?? "Unknown"}
                {figure.label === "Commits since opening" && changes ? (
                  <span className="text-xs font-normal">
                    <Lines additions={changes.additions} deletions={changes.deletions} />
                  </span>
                ) : null}
              </dd>
            </div>
          ))}
        </dl>
      )}
      {facts.summary && (
        <Markdown
          className={cn(
            "max-w-[90ch] text-base leading-[1.6] text-(--text-primary)",
            figures.length > 0 ? "mt-3" : undefined,
          )}
        >
          {facts.summary}
        </Markdown>
      )}
      <details className="mt-3">
        <summary className="cursor-pointer text-base text-(--text-link) hover:underline">
          Full exchange
        </summary>
        <div className="mt-3">{children}</div>
      </details>
    </>
  );
}

/** The one line that names the turn: who, what they concluded, against which
 *  head, and when. An addressed-in turn carries no time because the schema
 *  records which commit answered a finding, not when it landed. */
function ExchangeHeader({ document, event }: { document: ReviewDocument; event: ExchangeEvent }) {
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
      <span className="text-base font-medium text-(--text-primary)">{event.by}</span>
      {event.actor === "review" && (
        <span className="text-base text-(--text-secondary)">
          {event.verdict ? "reviewed" : "commented"}
        </span>
      )}
      {event.actor === "review" && event.verdict && (
        <Badge tone={verdictTone(event.verdict)} size="compact">
          {humanize(event.verdict)}
        </Badge>
      )}
      {event.actor === "implementation" && event.commit && (
        <span className="text-base text-(--text-secondary)">
          answered in <CommitLink document={document} sha={event.commit} />
        </span>
      )}
      {event.actor === "human" && (
        <span className="text-base text-(--text-secondary)">replied on the diff</span>
      )}
      <span className="ml-auto flex items-center gap-1.5 text-xs text-(--text-tertiary)">
        {event.actor === "review" && event.commit && (
          <>
            assessed <CommitLink document={document} sha={event.commit} />
            {event.at ? " · " : ""}
          </>
        )}
        {event.at ? formatDateTime(event.at) : ""}
        {repositoryLink(document, event.url) && (
          <a
            href={repositoryLink(document, event.url)}
            target="_blank"
            rel="noreferrer"
            aria-label={`Open ${event.by}'s comment on GitHub`}
            className="text-(--text-tertiary)"
          >
            <ExternalLinkIcon size={10} />
          </a>
        )}
      </span>
    </div>
  );
}
