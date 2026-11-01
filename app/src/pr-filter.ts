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
 *  the parts with and. Owners and repositories form one tree: an owner alone
 *  is all of its repositories, an owner with some of its repositories named
 *  is only those, and a repository named without its owner is in as well. */
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

const ownerOf = (repository: string) => repository.split("/")[0]?.toLowerCase() ?? "";

/** Whether the repository is in the criteria's tree of owners and repositories. */
export function inWhere(criteria: Criteria, repository: string): boolean {
  if (criteria.owners.length === 0 && criteria.repositories.length === 0) return true;
  const name = repository.toLowerCase();
  const owner = ownerOf(name);
  if (criteria.repositories.some((r) => r.toLowerCase() === name)) return true;
  return (
    criteria.owners.some((o) => o.toLowerCase() === owner) &&
    !criteria.repositories.some((r) => ownerOf(r) === owner)
  );
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

/** One chip per part in force, each with the criteria it leaves when removed:
 *  an owner only while it stands for all of its repositories. Removing the
 *  last repository of an owner also removes the owner. */
export function chips(criteria: Criteria): Chip[] {
  const out: Chip[] = [];
  const named = (owner: string) =>
    criteria.repositories.some((r) => ownerOf(r) === owner.toLowerCase());
  for (const owner of criteria.owners.filter((one) => !named(one)))
    out.push({
      key: `owner:${owner}`,
      label: owner,
      without: {
        ...criteria,
        owners: criteria.owners.filter((one) => one !== owner),
        repositories: criteria.repositories,
      },
    });
  for (const repository of criteria.repositories)
    out.push({
      key: `repository:${repository}`,
      label: repository,
      without: (() => {
        const repositories = criteria.repositories.filter((one) => one !== repository);
        const owner = ownerOf(repository);
        return {
          ...criteria,
          owners: repositories.some((r) => ownerOf(r) === owner)
            ? criteria.owners
            : criteria.owners.filter((one) => one.toLowerCase() !== owner),
          repositories,
        };
      })(),
    });
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
