import {
  CATEGORIES,
  FEATURE_STATUSES,
  parseDecisionFilters,
  parseFeatureStatuses,
  personLabel,
  WHO_OPTIONS,
  withDecisionFilters,
  withFeatureStatuses,
  type AuthorCount,
  type DecisionCategory,
  type DecisionFile,
  type DecisionFilters,
  type DecisionWho,
  type FeatureStatus,
} from "@baum/viewer/decision-log";
import { humanize } from "@baum/viewer/review-document";
import {
  listed,
  parseQuery,
  quote,
  type Expr,
  type Field,
  type Op,
  type PageFilter,
} from "./pr-filter";

/** The Timeline's filter, written as the Inbox's and Pull requests' are:
 *  `repo=acme/app who=human category=architecture,testing date>=2026-10-01
 *  caching`. The address keeps the filters the shared timeline reads (`q`,
 *  `who`, `category`…); the query is how they are written and read back.
 *
 *  The server applies the filter to each page it reads, so every term must
 *  hold and each field is named once (`who=human,agent` for either): no OR,
 *  NOT, `-` or parentheses. `who!=agent` hides the agents' own decisions;
 *  any other word is looked for in the text. */
const DAY = /^\d{4}-\d{2}-\d{2}$/;
const never = () => false;
/** A day the calendar has, so `2026-02-30` is refused rather than dropped
 *  from the address without a word. */
function realDay(value: string): boolean {
  if (!DAY.test(value)) return false;
  const [year, month, day] = value.split("-").map(Number) as [number, number, number];
  const at = new Date(year, month - 1, day);
  return at.getFullYear() === year && at.getMonth() === month - 1 && at.getDate() === day;
}
/** The server only keeps what a field is: no field takes `!=` but `who`. */
const IS: Op[] = ["="];
const only = (field: Field): Field => ({ ...field, ops: IS });

/** The fields the open tab offers: Features leaves out those that pick
 *  decisions alone, and All decisions the feature status. */
export function timelineFields({
  features,
  repositories,
  people,
  files,
}: {
  features: boolean;
  repositories: string[];
  people: AuthorCount[];
  files: DecisionFile[];
}): Field[] {
  const repo: Field = {
    ops: IS,
    key: "repo",
    label: "Repository",
    values: () => repositories.map((name) => ({ value: name, label: name })),
    check: (value) =>
      repositories.some((name) => name.toLowerCase() === value.toLowerCase())
        ? ""
        : `repo takes ${repositories.join(", ")}.`,
    test: never,
  };
  const person: Field = {
    ops: IS,
    single: true,
    key: "person",
    label: "Author",
    values: () => people.map((one) => ({ value: one.email, label: personLabel(one) })),
    test: never,
  };
  const file: Field = {
    ops: IS,
    single: true,
    key: "file",
    label: "File or folder",
    values: () => files.map((one) => ({ value: one.path, label: one.path })),
    test: never,
  };
  const date: Field = {
    key: "date",
    label: "Date",
    values: () => [],
    ops: [">=", "<="],
    single: true,
    check: (value) => (realDay(value) ? "" : "date takes a day, as 2026-10-31."),
    test: never,
  };
  if (features)
    return [
      repo,
      only(
        listed(
          "status",
          "Status",
          FEATURE_STATUSES.map((one) => ({ value: one.value, label: one.label })),
          never,
        ),
      ),
      person,
      file,
      date,
    ];
  const who = listed(
    "who",
    "Decided by",
    WHO_OPTIONS.map((one) => ({ value: one.value, label: one.label })),
    never,
  );
  return [
    repo,
    {
      ...who,
      ops: ["=", "!="],
      check: (value, op) =>
        who.check?.(value, op) ||
        (op === "!=" && value.toLowerCase() !== "agent" ? "who!= takes agent alone." : ""),
    },
    only(
      listed(
        "category",
        "Category",
        CATEGORIES.map((one) => ({ value: one, label: humanize(one) })),
        never,
      ),
    ),
    person,
    file,
    date,
    only(
      listed("is", "Including", [{ value: "superseded", label: "Superseded decisions" }], never),
    ),
  ];
}

/** The Timeline's filter for `FilterButton` and `FilterBar`. */
export function timelineFilter(fields: readonly Field[]): PageFilter {
  return { label: "Filter the Timeline", fields, problem: (query) => problem(query, fields) };
}

/** Why the query cannot be applied, or "". */
function problem(query: string, fields: readonly Field[]): string {
  const { expr, error } = parseQuery(query, fields);
  if (error) return error;
  const items = !expr ? [] : expr.kind === "and" ? expr.items : [expr];
  const seen = new Set<string>();
  for (const item of items) {
    if (item.kind !== "term" && item.kind !== "text")
      return "The Timeline's filters must all hold: write no OR, NOT, - or parentheses.";
    if (item.kind !== "term") continue;
    const twice = `${item.key}${item.op}`;
    if (seen.has(twice))
      return `${item.key}${item.op} is written twice: list its values once, as ${item.key}${item.op}a,b.`;
    seen.add(twice);
    const one = { date: "day", file: "path", person: "author" }[item.key];
    if (one && item.values.length > 1) return `${item.key} takes one ${one}.`;
  }
  return "";
}

type Terms = Extract<Expr, { kind: "term" }>[];

function parts(query: string, fields: readonly Field[]) {
  const { expr } = parseQuery(query, fields);
  const items = !expr ? [] : expr.kind === "and" ? expr.items : [expr];
  const terms = items.filter((one): one is Terms[number] => one.kind === "term");
  const text = items
    .filter((one): one is Extract<Expr, { kind: "text" }> => one.kind === "text")
    .map((one) => one.text)
    .join(" ");
  const values = (key: string, op = "=") =>
    terms.filter((one) => one.key === key && one.op === op).flatMap((one) => one.values);
  return { text, values };
}

/** The address with the filters the query asks for, written into the open
 *  tab's: Features leaves the filters only All decisions has as they are. A
 *  query that cannot be applied changes nothing. */
export function withTimelineQuery(
  search: URLSearchParams,
  query: string,
  fields: readonly Field[],
  features: boolean,
): URLSearchParams {
  if (problem(query, fields)) return search;
  const { text, values } = parts(query, fields);
  // The search as the address has it when the query's text reads it back,
  // so its quotes and spaces are not rewritten by another filter's change.
  const same = text === parts(timelineQuery(search, features), fields).text;
  const lower = (list: string[]) => list.map((one) => one.toLowerCase());
  const changes: Partial<DecisionFilters> = {
    q: same ? parseDecisionFilters(search).q : text,
    file: values("file")[0] ?? "",
    person: values("person")[0] ?? "",
    from: values("date", ">=")[0] ?? null,
    to: values("date", "<=")[0] ?? null,
  };
  if (!features) {
    changes.who = lower(values("who")) as DecisionWho[];
    changes.categories = lower(values("category")) as DecisionCategory[];
    changes.superseded = lower(values("is")).includes("superseded");
    changes.hideAgent = values("who", "!=").length > 0;
  }
  let next = withDecisionFilters(search, changes);
  if (features) next = withFeatureStatuses(next, lower(values("status")) as FeatureStatus[]);
  const repos = values("repo");
  const before = search.getAll("repo");
  if (lower(repos).join("\n") !== lower(before).join("\n")) {
    // Another set of repositories: what was open was one of the old set's.
    next.delete("repo");
    next.delete("open");
    next.delete("feature");
    for (const name of repos) next.append("repo", name);
  }
  return next;
}

/** The query that reads back the open tab's filters in the address; given
 *  the repositories served, a `repo=` the Timeline does not serve, which it
 *  ignores, is left out. */
export function timelineQuery(
  search: URLSearchParams,
  features: boolean,
  served?: string[],
): string {
  const filters = parseDecisionFilters(search);
  const words: string[] = [];
  const term = (key: string, list: string[], op = "=") => {
    if (list.length > 0) words.push(`${key}${op}${list.map(quote).join(",")}`);
  };
  const known = served?.map((name) => name.toLowerCase());
  term(
    "repo",
    search.getAll("repo").filter((name) => !known || known.includes(name.toLowerCase())),
  );
  if (features) term("status", parseFeatureStatuses(search));
  else {
    term("who", filters.who);
    if (filters.hideAgent) term("who", ["agent"], "!=");
    term("category", filters.categories);
  }
  term("person", filters.person ? [filters.person] : []);
  term("file", filters.file ? [filters.file] : []);
  term("date", filters.from ? [filters.from] : [], ">=");
  term("date", filters.to ? [filters.to] : [], "<=");
  if (!features && filters.superseded) term("is", ["superseded"]);
  // Quoted where the words would read as syntax.
  const plain = !/[()":=<>]|(^|\s)(-|AND\s|OR\s|NOT\s|AND$|OR$|NOT$)/.test(filters.q);
  if (filters.q) words.push(plain ? filters.q : `"${filters.q.replaceAll('"', "")}"`);
  return words.join(" ");
}
