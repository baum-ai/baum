import type { Entry } from "./api";
import { uiState, useUIState } from "./ui-state";

export type Involvement = "assigned" | "requested" | "authored";
export type ReviewState = "approved" | "changes_requested" | "none";
export type ChecksState = "passing" | "failing" | "running";
export type MergeState = "ready" | "conflict";
export type Risk = "any" | "3" | "4";

/** What narrows a list of pull requests, as the daemon stores it for saved
 *  inboxes and for each page's quick filter (see its `internal/inboxes`
 *  README). Every list is empty for any; a list's values combine with or, and
 *  the parts with and, so with both owners and repositories named a pull
 *  request's repository must be in both. */
export type Criteria = {
  owners: string[];
  risk: Risk;
  repositories: string[];
  involvement: Involvement[];
  no_drafts: boolean;
  review: ReviewState[];
  checks: ChecksState[];
  merge: MergeState[];
};

export const NO_CRITERIA: Criteria = {
  owners: [],
  risk: "any",
  repositories: [],
  involvement: [],
  no_drafts: false,
  review: [],
  checks: [],
  merge: [],
};

export const INVOLVEMENT: [Involvement, string][] = [
  ["assigned", "Assigned to me"],
  ["requested", "Review requested"],
  ["authored", "Created by me"],
];
export const REVIEWS: [ReviewState, string][] = [
  ["approved", "Approved"],
  ["changes_requested", "Changes requested"],
  ["none", "No decision yet"],
];
export const CHECKS: [ChecksState, string][] = [
  ["passing", "Checks passing"],
  ["failing", "Checks failing"],
  ["running", "Checks running"],
];
export const MERGES: [MergeState, string][] = [
  ["ready", "Ready to merge"],
  ["conflict", "Has a conflict"],
];
export const RISKS: [Risk, string][] = [
  ["any", "Any risk"],
  ["3", "Risk 3 or more"],
  ["4", "Risk 4 or 5"],
];

const isString = (value: unknown): value is string => typeof value === "string";
const strings = (value: unknown) => (Array.isArray(value) ? value.filter(isString) : []);
const known = <T extends string>(value: unknown, options: [T, string][]): T[] =>
  strings(value).filter((one): one is T => options.some(([key]) => key === one));

/** Criteria as the page can rely on them: missing lists are empty and
 *  unknown values are left out. */
export function parseCriteria(raw: unknown): Criteria {
  const value = (raw && typeof raw === "object" ? raw : {}) as Record<string, unknown>;
  return {
    owners: strings(value.owners),
    risk: value.risk === "3" || value.risk === "4" ? value.risk : "any",
    repositories: strings(value.repositories),
    involvement: known(value.involvement, INVOLVEMENT),
    no_drafts: value.no_drafts === true,
    review: known(value.review, REVIEWS),
    checks: known(value.checks, CHECKS),
    merge: known(value.merge, MERGES),
  };
}

const INVOLVED: Record<Involvement, (pr: Entry["pull_request"]) => boolean> = {
  assigned: (pr) => pr.assigned_to_me === true,
  requested: (pr) => pr.review_requested_from_me,
  authored: (pr) => pr.authored_by_me,
};

/** The owner of `owner/name`, or of `host/owner/name` on GitHub Enterprise. */
export function ownerOf(repository: string): string {
  const parts = repository.split("/");
  return parts.length >= 2 ? (parts[parts.length - 2] ?? "") : "";
}
const same = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

/** Whether the repository is in the criteria's owners and repositories. */
export function inWhere(criteria: Criteria, repository: string): boolean {
  if (criteria.owners.length > 0 && !criteria.owners.some((o) => same(o, ownerOf(repository))))
    return false;
  return !(
    criteria.repositories.length > 0 && !criteria.repositories.some((r) => same(r, repository))
  );
}

/** The daemon's limits on a list of owners or repositories. */
export const MAX_NAMED = 50;
// The shapes the daemon accepts (`internal/inboxes`).
const OWNER = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$/;
const REPOSITORY =
  /^(?:[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?(?::[0-9]{1,5})?\/)?[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?\/[A-Za-z0-9._-]{1,100}$/;

/** Whether the daemon would keep these owners and repositories. A value it
 *  refuses would stay pending and fail every later save of the filters. */
export function savable(criteria: Pick<Criteria, "owners" | "repositories">): boolean {
  return (
    criteria.owners.length <= MAX_NAMED &&
    criteria.repositories.length <= MAX_NAMED &&
    criteria.owners.every((one) => OWNER.test(one)) &&
    criteria.repositories.every(
      (one) => REPOSITORY.test(one) && !one.endsWith("/.") && !one.endsWith("/.."),
    )
  );
}

/** The owners and repositories that hold exactly `picked` out of `known`:
 *  owners alone while every owner touched is picked whole, so its later
 *  repositories come in too; else the picked repositories by name. Null when
 *  that needs more than the daemon keeps. */
export function whereFor(
  picked: string[],
  known: string[],
): Pick<Criteria, "owners" | "repositories"> | null {
  const all = [...known, ...picked.filter((one) => !known.some((k) => same(k, one)))];
  if (picked.length === 0 || all.every((one) => picked.some((p) => same(p, one))))
    return { owners: [], repositories: [] };
  const owners: string[] = [];
  for (const one of picked)
    if (!owners.some((o) => same(o, ownerOf(one)))) owners.push(ownerOf(one));
  const whole = owners.every((owner) =>
    all.filter((one) => same(ownerOf(one), owner)).every((one) => picked.some((p) => same(p, one))),
  );
  if (whole) return owners.length > MAX_NAMED ? null : { owners, repositories: [] };
  return owners.length > MAX_NAMED || picked.length > MAX_NAMED
    ? null
    : { owners, repositories: picked };
}

export function matchesCriteria(criteria: Criteria, entry: Entry): boolean {
  const pr = entry.pull_request;
  if (!inWhere(criteria, pr.repository)) return false;
  if (criteria.risk !== "any") {
    // A pull request with no score yet is never riskier than a threshold.
    const score = entry.risk_score?.score;
    if (typeof score !== "number" || score < Number(criteria.risk)) return false;
  }
  if (criteria.involvement.length > 0 && !criteria.involvement.some((one) => INVOLVED[one](pr)))
    return false;
  if (criteria.no_drafts && pr.draft) return false;
  const review: ReviewState =
    entry.review === "approved" || entry.review === "changes_requested" ? entry.review : "none";
  if (criteria.review.length > 0 && !criteria.review.includes(review)) return false;
  if (criteria.checks.length > 0 && !criteria.checks.some((one) => one === entry.checks))
    return false;
  return !(criteria.merge.length > 0 && !criteria.merge.some((one) => one === entry.merge));
}

export type Chip = { key: string; label: string; without: Criteria };

const label = <T extends string>(options: [T, string][], key: T) =>
  options.find(([one]) => one === key)?.[1] ?? key;

/** One chip per part in force, each with the criteria it leaves when removed.
 *  An owner whose `known` repositories are all named is one chip; removing a
 *  repository that was its owner's last takes the owner out too. */
export function chips(criteria: Criteria, known: string[] = []): Chip[] {
  const out: Chip[] = [];
  const named = (owner: string) => criteria.repositories.filter((r) => same(ownerOf(r), owner));
  const whole = (owner: string) => {
    const own = known.filter((one) => same(ownerOf(one), owner));
    return (
      criteria.repositories.length === 0 ||
      (own.length > 0 && own.every((one) => named(owner).some((r) => same(r, one))))
    );
  };
  const wholeOwners = criteria.owners.filter(whole);
  for (const owner of wholeOwners)
    out.push({
      key: `owner:${owner}`,
      label: owner,
      without: {
        ...criteria,
        owners: criteria.owners.filter((one) => one !== owner),
        repositories: criteria.repositories.filter((r) => !same(ownerOf(r), owner)),
      },
    });
  for (const repository of criteria.repositories) {
    if (wholeOwners.some((owner) => same(owner, ownerOf(repository)))) continue;
    const repositories = criteria.repositories.filter((one) => one !== repository);
    const owner = ownerOf(repository);
    out.push({
      key: `repository:${repository}`,
      label: repository,
      without: {
        ...criteria,
        owners: repositories.some((r) => same(ownerOf(r), owner))
          ? criteria.owners
          : criteria.owners.filter((one) => !same(one, owner)),
        repositories,
      },
    });
  }
  const lists = [
    ["involvement", INVOLVEMENT],
    ["review", REVIEWS],
    ["checks", CHECKS],
    ["merge", MERGES],
  ] as const;
  for (const [field, options] of lists)
    for (const value of criteria[field] as string[])
      out.push({
        key: `${field}:${value}`,
        label: label(options as [string, string][], value),
        without: { ...criteria, [field]: (criteria[field] as string[]).filter((v) => v !== value) },
      });
  if (criteria.no_drafts)
    out.push({ key: "no_drafts", label: "No drafts", without: { ...criteria, no_drafts: false } });
  if (criteria.risk !== "any")
    out.push({
      key: "risk",
      label: label(RISKS, criteria.risk),
      without: { ...criteria, risk: "any" },
    });
  return out;
}

/** Adds `value` to the list, or takes it out. */
export function toggled<T extends string>(list: T[], value: T): T[] {
  return list.includes(value) ? list.filter((one) => one !== value) : [...list, value];
}

/** The page a quick filter belongs to: `inbox`, `inbox:<id>` or `pulls`. */
export type FilterPage = string;
type Filters = { pages: Record<FilterPage, Criteria> };
/** The daemon keeps at most 64 pages; the least recently changed go first. */
const KEEP_PAGES = 60;

/** Each page's quick filter, remembered in the daemon (`ui/filters`). */
export function usePageFilter(page: FilterPage) {
  const saved = useUIState(uiState<Filters>("api/ui-state/filters", () => ({ pages: {} })));
  const criteria = parseCriteria(saved.value.pages?.[page] ?? NO_CRITERIA);
  return {
    criteria,
    ready: saved.ready,
    error: saved.error,
    retry: saved.retry,
    saving: saved.saving,
    set(next: Criteria) {
      if (JSON.stringify(next) === JSON.stringify(criteria)) return;
      if (!savable(next)) return;
      saved.update((value) => {
        // Changed pages move to the end, so the oldest are dropped first.
        const { [page]: _, ...rest } = value.pages ?? {};
        const pages = chips(next).length > 0 ? { ...rest, [page]: next } : rest;
        const keys = Object.keys(pages);
        for (const key of keys.slice(0, Math.max(0, keys.length - KEEP_PAGES))) delete pages[key];
        return { pages };
      });
    },
  };
}
