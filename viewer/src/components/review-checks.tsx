import pluralize from "pluralize";

import {
  type CheckStatus,
  checkStatus,
  humanize,
  type IndexedCheck,
  repositoryLink,
  type ReviewDocument,
} from "../review-document";
import { StatusBlock } from "./editorial";
import { formatDate, formatRelative } from "./format";
import { ExternalLinkIcon } from "./icons";
import { Part } from "./review-parts";
import { Badge, type BadgeTone } from "./ui";

/** How old a check snapshot may be before the line says when it was taken. */
const STALE_AFTER_MS = 60 * 60 * 1000;

/** "as of 2 hours ago", once a snapshot is old enough that a reader could
 *  take a running check for a live one. */
function AsOf({ at }: { at?: string }) {
  if (!at || Date.now() - new Date(at).getTime() < STALE_AFTER_MS) return null;
  return (
    <span className="text-xs text-(--text-tertiary)" title={`Checks as of ${formatDate(at)}`}>
      as of {formatRelative(at)}
    </span>
  );
}

/** The CI state of the head under review in one line: the state and the
 *  count that decides it, then whatever is left over.
 *
 *  `asOf` is when the document's check index was last refreshed. The index
 *  is a snapshot, and one that is hours old says so beside the state, so a
 *  check it shows running is read as "was running then", not "is running". */
export function CheckState({ status, asOf }: { status: CheckStatus; asOf?: string }) {
  if (status.state === "none") {
    return (
      <span className="flex flex-wrap items-center gap-2 text-base text-(--text-tertiary)">
        No checks ran on this head
        <AsOf at={asOf} />
      </span>
    );
  }
  const { tone, label } = {
    passing: { tone: "positive" as const, label: "CI green" },
    failing: { tone: "critical" as const, label: "CI failing" },
    pending: { tone: "neutral" as const, label: "CI pending" },
    inconclusive: { tone: "neutral" as const, label: "CI inconclusive" },
  }[status.state];
  const counted =
    status.state === "failing"
      ? `${pluralize("check", status.failed.length, true)} failed`
      : status.state === "pending"
        ? `${status.pending} running`
        : status.state === "inconclusive"
          ? `${pluralize("check", status.skipped, true)} skipped`
          : `${status.passed} of ${status.total} passed`;
  const rest = [
    status.state === "passing" || status.state === "inconclusive"
      ? null
      : `${status.passed} of ${status.total} passed`,
    status.state === "failing" && status.pending > 0 ? `${status.pending} running` : null,
    status.state === "passing" && status.skipped > 0 ? `${status.skipped} skipped` : null,
  ].filter(Boolean);
  return (
    <span className="flex flex-wrap items-center gap-2">
      <Badge tone={tone} role="status">
        {label} · {counted}
      </Badge>
      {rest.length > 0 && (
        <span className="text-base text-(--text-tertiary)">{rest.join(" · ")}</span>
      )}
      <AsOf at={asOf} />
    </span>
  );
}

/** A check links to its run only when the link belongs to this pull
 *  request's repository (see `repositoryLink`); otherwise it reads as text. */
function CheckLink({ check }: { check: IndexedCheck }) {
  if (!check.url) {
    return <span className="min-w-0 truncate text-base text-(--text-primary)">{check.name}</span>;
  }
  return (
    <a
      href={check.url}
      target="_blank"
      rel="noreferrer"
      className="inline-flex min-w-0 items-center gap-1 text-base text-(--text-primary)"
    >
      <span className="truncate">{check.name}</span>
      <ExternalLinkIcon size={10} className="flex-none text-(--text-tertiary)" />
    </a>
  );
}

/** The checks a reader has to look at, under the rule the Summary uses for
 *  every other thing that needs attention. Renders nothing when nothing failed. */
export function FailingChecks({ status }: { status: CheckStatus }) {
  if (status.failed.length === 0) return null;
  return (
    <StatusBlock tone="critical" label={status.blocking ? "Checks blocking" : "Checks failing"}>
      <ul className="space-y-1">
        {status.failed.map((check) => (
          <li key={check.id} className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <Badge tone="critical" size="compact">
              {humanize(check.conclusion)}
            </Badge>
            <CheckLink check={check} />
            {check.required && <Badge size="compact">Required</Badge>}
          </li>
        ))}
      </ul>
    </StatusBlock>
  );
}

const CONCLUSION_TONES: Record<string, BadgeTone> = {
  success: "positive",
  failure: "critical",
  cancelled: "critical",
  pending: "neutral",
  skipped: "neutral",
  neutral: "neutral",
};

/** The Details tab's Checks part: the state of CI on this head in one line, the
 *  failures in the open, and every run behind a disclosure, so a reader who
 *  wants to know what else ran does not have to leave the review. */
export function ChecksPart({ document: supplied }: { document: ReviewDocument }) {
  // Check links from another repository are dropped once, here, so the
  // failing-checks list and the full list cannot disagree about them.
  const document = {
    ...supplied,
    index: {
      ...supplied.index,
      checks: supplied.index.checks.map((check) => ({
        ...check,
        url: repositoryLink(supplied, check.url),
      })),
    },
  };
  const status = checkStatus(document);
  const checks = document.index.checks
    .filter((check) => check.head === document.review.head)
    .toSorted((left, right) => Number(right.required) - Number(left.required));
  return (
    <Part label="Checks">
      <div className="space-y-3">
        <CheckState
          status={status}
          asOf={document.review.evidence_refreshed_at || document.review.generated_at}
        />
        <FailingChecks status={status} />
        {checks.length > 0 && (
          <details>
            <summary className="cursor-pointer text-base text-(--text-link) hover:underline">
              {pluralize("check", checks.length, true)} on this head
            </summary>
            <ul aria-label="Checks on this head" className="mt-2 divide-y divide-(--border-subtle)">
              {checks.map((check) => (
                <li key={check.id} className="flex flex-wrap items-center gap-x-2 gap-y-1 py-2">
                  <Badge tone={CONCLUSION_TONES[check.conclusion] ?? "neutral"} size="compact">
                    {humanize(check.conclusion)}
                  </Badge>
                  <CheckLink check={check} />
                  {check.required && <Badge size="compact">Required</Badge>}
                </li>
              ))}
            </ul>
          </details>
        )}
      </div>
    </Part>
  );
}
