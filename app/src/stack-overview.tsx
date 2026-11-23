import { useId, useState, type ComponentType, type ReactNode } from "react";
import { cn } from "@baum/viewer/components/classnames";
import { formatCount } from "@baum/viewer/components/format";
import { ChevronRightIcon, MergeIcon, StackIcon } from "@baum/viewer/components/icons";
import { Markdown } from "@baum/viewer/components/markdown";
import { Lines } from "@baum/viewer/components/review-parts";
import { Badge } from "@baum/viewer/components/ui";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import { RISK_SCORE_MEANING, type RiskScoreLevel } from "@baum/viewer/review-document";
import { PRIMARY } from "./action-classes";
import type { Entry, ReviewGroup } from "./api";

const plural = (n: number, one: string, many = `${one}s`) =>
  `${formatCount(n)} ${n === 1 ? one : many}`;

export function minutesText(minutes: number) {
  return minutes < 1 ? "<1 min" : `~${formatCount(Math.round(minutes))} min`;
}

/** The risk card of a pull request's, a group's or a stack's panel: its score
 *  as five blocks, in words, and what the review says of it below
 *  (`children`). */
export function RiskCard({
  score,
  text,
  children,
}: {
  score: number;
  /** The score in words: "3 of 5", or "at least 3 of 5". */
  text: string;
  children?: ReactNode;
}) {
  return (
    <div className="upnext-risk">
      <div className="upnext-risk-head">
        <span className="upnext-label">Risk</span>
        <span className="upnext-risk-blocks" aria-hidden="true">
          {[1, 2, 3, 4, 5].map((n) => (
            <span
              key={n}
              className={
                n <= score ? `upnext-risk-on upnext-risk-${Math.min(score, 5)}` : undefined
              }
            />
          ))}
        </span>
        <span className="upnext-risk-text">
          {text} · {RISK_SCORE_MEANING[score as RiskScoreLevel]}
        </span>
      </div>
      {children}
    </div>
  );
}

/** One pull request of a stack as its overview lists it. */
export type StackMember = {
  number: number;
  /** The pull request as the index read it; absent when baum has not. */
  entry?: Entry;
  /** Where it stands, in a few words, and the tone of its dot. */
  state: { text: string; tone: string };
  /** Opens it: in the Inbox's panel a preview, on the stack's page its own
   *  review page. Without either it is listed as read. */
  pick?: () => void;
  href?: string;
};

/** A word under What changed: why there is no summary yet, or that the one
 *  shown was written before the latest reviews. */
function summaryNote(overview: ReviewGroup["overview"]): string {
  if (!overview) return "";
  if (!overview.summary)
    return overview.reviewed < 2
      ? "Summary appears once two pull requests are reviewed."
      : "The summary appears here once it is written.";
  return overview.current || overview.reviewed < 2
    ? ""
    : "Written before the latest reviews. A new summary follows.";
}

/** A stack's overview: the stack and its size, its riskiest pull request, a
 *  summary of the whole stack written from the members' reviews, and its
 *  pull requests in order, each opening its own review. The Inbox shows it
 *  as the stack's panel (`href` is then the stack's page), and the stack's
 *  page shows it alone. */
export function StackOverview({
  group,
  members,
  entries,
  href,
  memberHref,
  Link,
  titled = true,
  actions,
}: {
  group: ReviewGroup;
  /** Every member in the stack's order, the landed ones first. */
  members: StackMember[];
  /** Every pull request read, for the open members' sizes. */
  entries: Entry[];
  /** The stack's own page, from the Inbox's panel. */
  href?: string;
  /** A member's review page. */
  memberHref: (entry: Entry) => string;
  Link: ComponentType<ViewerLinkProps>;
  /** False on the stack's page, whose bar already names it. */
  titled?: boolean;
  /** More actions beside Open the stack, as the Inbox's Snooze. */
  actions?: ReactNode;
}) {
  const [listOpen, setListOpen] = useState(true);
  const listId = useId();
  const landed = group.landed ?? [];
  const total = group.members.length + landed.length;
  const merged = landed.filter((member) => member.state === "merged").length;
  const unread = total - members.length;
  const read = (number: number) =>
    members.find((member) => member.number === number)?.entry ??
    entries.find(
      (entry) =>
        entry.pull_request.number === number &&
        entry.pull_request.repository.toLowerCase() === group.repository.toLowerCase(),
    );
  const open = group.members.flatMap((number) => {
    const entry = read(number);
    return entry ? [entry] : [];
  });
  const sum = (value: (entry: Entry) => number | null | undefined) =>
    open.reduce((total, entry) => total + (value(entry) ?? 0), 0);
  const additions = sum((entry) => entry.pull_request.additions);
  const deletions = sum((entry) => entry.pull_request.deletions);
  const files = sum((entry) => entry.pull_request.changed_files);
  const minutes = open.some((entry) => entry.review_minutes != null)
    ? sum((entry) => entry.review_minutes)
    : null;
  // Where the stack merges: the one base branch that is not a member's own.
  const heads = new Set(open.map((entry) => entry.pull_request.head_ref).filter(Boolean));
  const bases = new Set(
    open.map((entry) => entry.pull_request.base_ref).filter((base) => base && !heads.has(base)),
  );
  const base = bases.size === 1 ? [...bases][0] : "";
  const overview = group.overview;
  const score = group.risk_score?.score ?? null;
  const riskiest = overview?.riskiest ? read(overview.riskiest) : undefined;
  const reason = group.risk_score?.reasons?.[0]?.text;
  const note = summaryNote(overview);
  const title = (entry: Entry | undefined, number: number) =>
    entry ? entry.pull_request.title || "Untitled pull request" : `Pull request #${number}`;
  return (
    <article
      className="upnext-panel"
      aria-labelledby={titled ? "upnext-title" : undefined}
      aria-label={titled ? undefined : group.name}
    >
      <div className="upnext-panel-meta">
        <span className="upnext-chip upnext-chip-waiting">Stack</span>
        <span className="upnext-panel-where">
          {group.repository} · {plural(total, "pull request")}
        </span>
      </div>
      {titled ? (
        <h2 id="upnext-title" className="upnext-title">
          {href ? <Link href={href}>{group.name}</Link> : group.name}
        </h2>
      ) : null}
      <dl className="upnext-facts upnext-pr-facts">
        {base ? (
          <div className="upnext-branch-fact">
            <dt className="sr-only">Base</dt>
            <dd className="upnext-branches">
              <StackIcon size={14} />
              <span>Stacked on</span>
              <code title={base}>{base}</code>
            </dd>
          </div>
        ) : null}
        {additions + deletions + files > 0 ? (
          <div>
            <dt className="sr-only">Size</dt>
            <dd>
              <Lines additions={additions} deletions={deletions} format={formatCount} />
              {files ? <span className="upnext-fact-aside"> · {plural(files, "file")}</span> : null}
            </dd>
          </div>
        ) : null}
        {minutes != null ? (
          <div>
            <dt className="sr-only">Time to review</dt>
            <dd title="baum's estimates of the time to read each change, summed">
              {minutesText(minutes)} review
            </dd>
          </div>
        ) : null}
      </dl>
      {href ? (
        <div className="upnext-actions">
          <Link href={href} className={cn("upnext-primary", PRIMARY)}>
            Open the stack
            <kbd aria-hidden="true" className="key-hint">
              ↵
            </kbd>
          </Link>
          {actions}
        </div>
      ) : null}
      {score != null && overview?.riskiest ? (
        <RiskCard
          score={score}
          text={`${group.risk_score?.status === "lower_bound" ? "at least " : ""}${score} of 5`}
        >
          <div className="upnext-risk-copy">
            <strong>
              {riskiest ? (
                <Link href={memberHref(riskiest)}>{title(riskiest, overview.riskiest)}</Link>
              ) : (
                title(undefined, overview.riskiest)
              )}
            </strong>
            {reason ? (
              <Markdown html={false} className="upnext-prose">
                {reason}
              </Markdown>
            ) : null}
          </div>
        </RiskCard>
      ) : null}
      <section className="upnext-block" aria-label="What changed">
        <h3 className="upnext-label">What changed</h3>
        {overview?.summary ? (
          <Markdown html={false} className="upnext-lead upnext-prose">
            {overview.summary.text}
          </Markdown>
        ) : null}
        {note ? <p className="upnext-block-more">{note}</p> : null}
      </section>
      <section className="upnext-block" aria-label="Pull requests">
        <h3 className="upnext-label upnext-members-head">
          <button
            type="button"
            className="upnext-members-toggle"
            aria-expanded={listOpen}
            aria-controls={listId}
            onClick={() => setListOpen((open) => !open)}
          >
            <ChevronRightIcon size={12} className="upnext-chevron" />
            {plural(total, "pull request")}
          </button>
          {merged > 0 ? (
            <Badge tone="merged" size="compact" className="gap-1">
              <MergeIcon size={11} className="flex-none" />
              {formatCount(merged)} merged
            </Badge>
          ) : null}
        </h3>
        {listOpen ? (
          <ul id={listId} className="upnext-members upnext-members-stack">
            {members.map(({ number, entry, state, pick, href: memberPage }) => {
              const pr = entry?.pull_request;
              const text = (
                <span className="upnext-block-text">
                  <span className="upnext-member-title" title={pr?.title}>
                    {title(entry, number)}
                  </span>
                  <span className="upnext-block-sub">
                    #{number}
                    {state.text ? ` · ${state.text}` : ""}
                  </span>
                </span>
              );
              return (
                <li key={number} className={pick || memberPage ? undefined : "upnext-member-other"}>
                  <span
                    className={`upnext-member-dot upnext-member-${state.tone}`}
                    aria-hidden="true"
                  />
                  {pick ? (
                    <button type="button" className="upnext-member-pick" onClick={pick}>
                      {text}
                    </button>
                  ) : memberPage ? (
                    <Link href={memberPage} className="upnext-member-pick">
                      {text}
                    </Link>
                  ) : (
                    text
                  )}
                  {pr && (pr.additions || pr.deletions) ? (
                    <Lines
                      additions={pr.additions ?? 0}
                      deletions={pr.deletions ?? 0}
                      format={formatCount}
                      className="upnext-block-right"
                    />
                  ) : null}
                </li>
              );
            })}
          </ul>
        ) : null}
        {listOpen && unread > 0 ? (
          <p className="upnext-block-more">
            and {plural(unread, "more pull request")} baum has not read
          </p>
        ) : null}
      </section>
    </article>
  );
}
