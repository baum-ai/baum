import type { Entry } from "./api";
import { uiState, useUIState } from "./ui-state";

/** A filter over pull requests, written like a GitHub search:
 *  `repo:acme/app -is:draft (review:approved OR risk:>=4) flaky`.
 *
 *  - `key:value` keeps the pull requests whose field has the value, and
 *    `key:a,b` either value; `-key:value` or `NOT` leaves them out.
 *  - Terms next to each other must all hold (`AND` may be written); `OR`
 *    takes either side and binds looser than `AND`; parentheses group.
 *  - Any other word, or a "quoted phrase", is looked for in the title, the
 *    repository and the number.
 *
 *  Saved inboxes and each page's filter keep the text; it is parsed here. */
export type Expr =
  | { kind: "and" | "or"; items: Expr[] }
  | { kind: "not"; item: Expr }
  | { kind: "term"; key: string; values: string[]; start: number; end: number }
  | { kind: "text"; text: string };

export type Option = { value: string; label: string };

type Field = {
  key: string;
  label: string;
  /** The values to offer, given the page's pull requests. */
  values: (entries: Entry[]) => Option[];
  /** Why a typed value is not one the field takes, or "". */
  check?: (value: string) => string;
  test: (value: string, entry: Entry) => boolean;
};

const same = (a: string | undefined, b: string) => (a ?? "").toLowerCase() === b.toLowerCase();
const distinct = (values: (string | undefined)[]) =>
  [...new Map(values.filter(Boolean).map((v) => [v!.toLowerCase(), v!])).values()].sort((a, b) =>
    a.localeCompare(b),
  );
const plain = (values: (string | undefined)[]): Option[] =>
  distinct(values).map((value) => ({ value, label: value }));

/** The owner of `owner/name`, or of `host/owner/name` on GitHub Enterprise. */
export function ownerOf(repository: string): string {
  const parts = repository.split("/");
  return parts.length >= 2 ? (parts[parts.length - 2] ?? "") : "";
}

/** A field that takes only the listed values. */
function listed(
  key: string,
  label: string,
  options: Option[],
  test: (value: string, entry: Entry) => boolean,
): Field {
  return {
    key,
    label,
    values: () => options,
    check: (value) =>
      options.some((one) => same(one.value, value))
        ? ""
        : `${key}: takes ${options.map((one) => one.value).join(", ")}.`,
    test,
  };
}

const IS: Record<string, [string, (entry: Entry) => boolean]> = {
  draft: ["Draft", (e) => e.pull_request.draft],
  mergeable: ["Ready to merge", (e) => e.merge === "ready"],
  conflict: ["Has a conflict", (e) => e.merge === "conflict"],
  blocked: ["Blocked", (e) => e.merge === "blocked"],
  behind: ["Behind its base", (e) => e.pull_request.behind === true],
  bot: ["By a bot", (e) => e.pull_request.author_bot === true],
};
const REVIEW: Record<string, [string, (entry: Entry) => boolean]> = {
  approved: ["Approved", (e) => e.review === "approved"],
  changes_requested: ["Changes requested", (e) => e.review === "changes_requested"],
  none: ["No decision yet", (e) => e.review !== "approved" && e.review !== "changes_requested"],
};
const CHECKS: Record<string, [string, (entry: Entry) => boolean]> = {
  passing: ["Passing", (e) => e.checks === "passing"],
  failing: ["Failing", (e) => e.checks === "failing"],
  running: ["Running", (e) => e.checks === "running"],
  none: ["No checks", (e) => e.checks === "none" || e.checks === ""],
};
const fromTable = (key: string, label: string, table: typeof IS) =>
  listed(
    key,
    label,
    Object.entries(table).map(([value, [text]]) => ({ value, label: text })),
    (value, entry) => table[value.toLowerCase()]?.[1](entry) ?? false,
  );
const me = (key: string, label: string, test: (entry: Entry) => boolean) =>
  listed(key, label, [{ value: "@me", label: "Me" }], (_, entry) => test(entry));

const RISK = /^(>=|<=|>|<)?([1-5])$/;
/** Whether the score passes a risk value such as `>=3`; a pull request with
 *  no score yet passes only `none`. */
function riskTest(value: string, entry: Entry): boolean {
  const score = entry.risk_score?.score;
  value = value.toLowerCase();
  if (value === "none") return typeof score !== "number";
  const [, op = "", digit] = RISK.exec(value) ?? [];
  if (!digit || typeof score !== "number") return false;
  const n = Number(digit);
  return op === ">="
    ? score >= n
    : op === "<="
      ? score <= n
      : op === ">"
        ? score > n
        : op === "<"
          ? score < n
          : score === n;
}

export const FIELDS: Field[] = [
  {
    key: "repo",
    label: "Repository",
    values: (entries) => plain(entries.map((e) => e.pull_request.repository)),
    // The whole name, or the part after the owner.
    test: (value, entry) => {
      const repository = entry.pull_request.repository;
      return same(repository, value) || same(repository.split("/").pop(), value);
    },
  },
  {
    key: "org",
    label: "Organization",
    values: (entries) => plain(entries.map((e) => ownerOf(e.pull_request.repository))),
    test: (value, entry) => same(ownerOf(entry.pull_request.repository), value),
  },
  {
    key: "author",
    label: "Author",
    values: (entries) => [
      { value: "@me", label: "Me" },
      ...plain(entries.map((e) => (e.pull_request.authored_by_me ? "" : e.pull_request.author))),
    ],
    test: (value, entry) =>
      same(value, "@me")
        ? entry.pull_request.authored_by_me
        : same(entry.pull_request.author, value),
  },
  me("assignee", "Assignee", (e) => e.pull_request.assigned_to_me === true),
  me("review-requested", "Review requested", (e) => e.pull_request.review_requested_from_me),
  fromTable("review", "Review", REVIEW),
  fromTable("checks", "Checks", CHECKS),
  fromTable("is", "State", IS),
  {
    key: "risk",
    label: "Risk",
    values: () => [
      { value: ">=4", label: "4 or 5" },
      { value: ">=3", label: "3 or more" },
      ...[5, 4, 3, 2, 1].map((n) => ({ value: String(n), label: `Exactly ${n}` })),
      { value: "none", label: "Not scored yet" },
    ],
    check: (value) =>
      same(value, "none") || RISK.test(value)
        ? ""
        : "risk: takes 1 to 5, >=3, <4 and so on, or none.",
    test: riskTest,
  },
  {
    key: "base",
    label: "Base branch",
    values: (entries) => plain(entries.map((e) => e.pull_request.base_ref)),
    test: (value, entry) => same(entry.pull_request.base_ref, value),
  },
];
const fieldOf = (key: string) => FIELDS.find((one) => one.key === key.toLowerCase());

export const OPERATORS = ["AND", "OR", "NOT"] as const;

type Token =
  | { kind: "(" | ")" | "AND" | "OR" | "NOT"; start: number; end: number }
  | { kind: "word"; text: string; start: number; end: number; quoted: boolean };

/** Splits the query into parentheses, operators and words; a word keeps any
 *  "quoted" part whole. An unclosed quote runs to the end. */
function tokenize(query: string): Token[] {
  const out: Token[] = [];
  let i = 0;
  while (i < query.length) {
    const c = query[i]!;
    if (/\s/.test(c)) {
      i++;
    } else if (c === "(" || c === ")") {
      out.push({ kind: c, start: i, end: ++i });
    } else if (c === "-" && query[i + 1] === "(") {
      out.push({ kind: "NOT", start: i, end: ++i });
    } else {
      const start = i;
      let text = "";
      let quoted = false;
      while (i < query.length && !/[\s()]/.test(query[i]!)) {
        if (query[i] === '"') {
          quoted = true;
          const close = query.indexOf('"', i + 1);
          const stop = close < 0 ? query.length : close;
          text += query.slice(i + 1, stop);
          i = close < 0 ? stop : stop + 1;
        } else text += query[i++];
      }
      const raw = query.slice(start, i);
      if (raw === "AND" || raw === "OR" || raw === "NOT") out.push({ kind: raw, start, end: i });
      else out.push({ kind: "word", text, start, end: i, quoted });
    }
  }
  return out;
}

/** A word's key and values when it is `key:value,…` (before any quote). */
function splitTerm(query: string, token: Extract<Token, { kind: "word" }>) {
  const raw = query.slice(token.start, token.end).replace(/^-/, "");
  const colon = raw.indexOf(":");
  if (colon <= 0 || raw.slice(0, colon).includes('"')) return null;
  const values: string[] = [];
  let current = "";
  let inQuote = false;
  for (const c of raw.slice(colon + 1)) {
    if (c === '"') inQuote = !inQuote;
    else if (c === "," && !inQuote) {
      values.push(current);
      current = "";
    } else current += c;
  }
  values.push(current);
  return { key: raw.slice(0, colon).toLowerCase(), values };
}

export type Parsed = { expr: Expr | null; error: string };

function parse(query: string): Parsed {
  if (query.length > MAX_QUERY)
    return { expr: null, error: `A filter is at most ${MAX_QUERY} characters.` };
  const tokens = tokenize(query);
  let at = 0;
  const fail = (error: string): never => {
    throw new SyntaxError(error);
  };
  const operand = (): Expr => {
    const token = tokens[at];
    if (!token) return fail("The query ends where a filter should follow.");
    at++;
    if (token.kind === "NOT") return { kind: "not", item: operand() };
    if (token.kind === "(") {
      const inner = or();
      if (tokens[at]?.kind !== ")") fail("A parenthesis is not closed.");
      at++;
      return inner;
    }
    if (token.kind !== "word") return fail(`${token.kind} needs a filter before it.`);
    const negated = query[token.start] === "-" && token.end - token.start > 1;
    const term = splitTerm(query, token);
    let expr: Expr;
    if (term) {
      const field = fieldOf(term.key);
      if (!field)
        fail(`Unknown filter "${term.key}:". Try ${FIELDS.map((f) => `${f.key}:`).join(", ")}.`);
      if (term.values.some((v) => v.trim() === "")) fail(`${term.key}: needs a value.`);
      for (const value of term.values) {
        const problem = field!.check?.(value);
        if (problem) fail(problem);
      }
      expr = {
        kind: "term",
        key: field!.key,
        values: term.values,
        start: token.start,
        end: token.end,
      };
    } else {
      const text = negated ? token.text.slice(1) : token.text;
      if (!text) fail("A - needs a filter after it.");
      expr = { kind: "text", text };
    }
    return negated ? { kind: "not", item: expr } : expr;
  };
  const and = (): Expr => {
    const items = [operand()];
    while (at < tokens.length && tokens[at]!.kind !== "OR" && tokens[at]!.kind !== ")") {
      if (tokens[at]!.kind === "AND") at++;
      items.push(operand());
    }
    return items.length === 1 ? items[0]! : { kind: "and", items };
  };
  const or = (): Expr => {
    const items = [and()];
    while (tokens[at]?.kind === "OR") {
      at++;
      items.push(and());
    }
    return items.length === 1 ? items[0]! : { kind: "or", items };
  };
  try {
    if (tokens.length === 0) return { expr: null, error: "" };
    const expr = or();
    if (at < tokens.length) fail("A closing parenthesis has no opening one.");
    return { expr, error: "" };
  } catch (error) {
    return { expr: null, error: (error as Error).message };
  }
}

const parsed = new Map<string, Parsed>();
/** The query's expression, or why it cannot be read. Remembers recent ones,
 *  since every row of every list asks. */
export function parseQuery(query: string): Parsed {
  let out = parsed.get(query);
  if (!out) {
    if (parsed.size > 200) parsed.clear();
    out = parse(query);
    parsed.set(query, out);
  }
  return out;
}

function test(expr: Expr, entry: Entry): boolean {
  switch (expr.kind) {
    case "and":
      return expr.items.every((one) => test(one, entry));
    case "or":
      return expr.items.some((one) => test(one, entry));
    case "not":
      return !test(expr.item, entry);
    case "term": {
      const field = fieldOf(expr.key);
      return !!field && expr.values.some((value) => field.test(value, entry));
    }
    case "text": {
      const pr = entry.pull_request;
      const text = expr.text.toLowerCase();
      return (
        pr.title.toLowerCase().includes(text) ||
        pr.repository.toLowerCase().includes(text) ||
        `#${pr.number}` === text ||
        String(pr.number) === text
      );
    }
  }
}

/** Whether the pull request passes the query. A query that cannot be read
 *  narrows nothing; the filter bar says why. */
export function matchesQuery(query: string, entry: Entry): boolean {
  const { expr } = parseQuery(query);
  return !expr || test(expr, entry);
}

/** A value as the query writes it, quoted when it would not read back. */
export function quote(value: string): string {
  return /^[^\s,()"]+$/.test(value) ? value : `"${value.replaceAll('"', "")}"`;
}

/** The query's terms that must all hold, outside any OR or NOT. */
function topTerms(query: string) {
  const { expr } = parseQuery(query);
  const items = !expr ? [] : expr.kind === "and" ? expr.items : [expr];
  return items.filter((one): one is Extract<Expr, { kind: "term" }> => one.kind === "term");
}

/** Whether the query requires the value of the field, as the menu ticks it. */
export function hasValue(query: string, key: string, value: string): boolean {
  return topTerms(query).some(
    (term) => term.key === key && term.values.some((one) => same(one, value)),
  );
}

/** The query on one line, as the daemon keeps it: control characters and runs
 *  of space become one space. */
export const tidyQuery = (query: string) => query.replace(/[\p{Cc}\s]+/gu, " ").trim();
const tidy = tidyQuery;
/** The daemon's limit on a query. */
export const MAX_QUERY = 500;

/** The query with the value required, or no longer required: a term of the
 *  field that must hold gains or loses it as one more choice (`review:a,b`);
 *  otherwise a term is added, around a query with OR in parentheses. */
export function toggleValue(query: string, key: string, value: string): string {
  const terms = topTerms(query).filter((one) => one.key === key);
  const term = terms.find((one) => one.values.some((v) => same(v, value))) ?? terms[0];
  const { expr, error } = parseQuery(query);
  if (term) {
    const values = term.values.some((one) => same(one, value))
      ? term.values.filter((one) => !same(one, value))
      : [...term.values, value];
    const text = values.length ? `${key}:${values.map(quote).join(",")}` : "";
    let before = query.slice(0, term.start);
    let after = query.slice(term.end);
    if (!text) {
      // A term alone in parentheses takes them with it, and an AND beside it
      // goes too.
      while (/\(\s*$/.test(before) && /^\s*\)/.test(after)) {
        before = before.replace(/\(\s*$/, "");
        after = after.replace(/^\s*\)/, "");
      }
      if (/(^|\s)AND\s*$/.test(before)) before = before.replace(/AND\s*$/, "");
      else after = after.replace(/^\s*AND(?=\s|$)/, "");
    }
    return tidy(`${before}${text}${after}`);
  }
  const base = !error && expr?.kind === "or" ? `(${query.trim()})` : query.trim();
  return tidy(`${base} ${key}:${quote(value)}`);
}

/** The query with a phrase looked for, as typed in the menu's search. */
export function withText(query: string, text: string): string {
  // Quoted unless plain: an operator, a leading - or a quote would read as
  // syntax.
  const plain =
    /^[^\s()"-][^\s()"]*$/.test(text) && !OPERATORS.includes(text as (typeof OPERATORS)[number]);
  return tidy(`${query} ${plain ? text : `"${text.replaceAll('"', "")}"`}`);
}

export type Suggestion = {
  /** The query once it is taken, and where the caret goes. */
  query: string;
  caret: number;
  label: string;
  detail?: string;
  /** It names a field, and its values come next. */
  field?: boolean;
};

/** What to offer at the caret: the values of the field being typed, or the
 *  fields and operators that start with the word being typed. */
export function suggest(query: string, caret: number, entries: Entry[]): Suggestion[] {
  const token = tokenize(query).find(
    (one) => one.kind === "word" && one.start < caret && caret <= one.end,
  ) as Extract<Token, { kind: "word" }> | undefined;
  // Inside a word, or at its start, there is nothing to complete: taking a
  // suggestion would drop or join the rest of it.
  if (token && caret < token.end) return [];
  if (tokenize(query).some((one) => one.kind === "word" && one.start === caret)) return [];
  const start = token?.start ?? caret;
  const end = token?.end ?? caret;
  const typed = query.slice(start, caret);
  const take = (text: string, space = true): Pick<Suggestion, "query" | "caret"> => {
    const rest = query.slice(end);
    const gap = space && !rest.startsWith(" ") ? " " : "";
    return {
      query: `${query.slice(0, start)}${text}${gap}${rest}`,
      caret: start + text.length + (space ? 1 : 0),
    };
  };
  const negated = typed.startsWith("-");
  const body = negated ? typed.slice(1) : typed;
  const colon = body.indexOf(":");
  if (colon > 0) {
    const field = fieldOf(body.slice(0, colon));
    if (!field) return [];
    const head = body.slice(0, colon + 1);
    const listedValues = body.slice(colon + 1).split(",");
    const partial = (listedValues.pop() ?? "").replace(/^"/, "").toLowerCase();
    const prefix = `${negated ? "-" : ""}${head}${listedValues.map(quote).join(",")}${listedValues.length ? "," : ""}`;
    return field
      .values(entries)
      .filter(
        (one) =>
          !listedValues.some((v) => same(v, one.value)) &&
          // A value typed whole has nothing left to complete.
          !same(one.value, partial) &&
          (one.value.toLowerCase().includes(partial) || one.label.toLowerCase().includes(partial)),
      )
      .slice(0, 50)
      .map((one) => ({
        ...take(`${prefix}${quote(one.value)}`),
        label: `${negated ? "-" : ""}${field.key}:${one.value}`,
        detail: one.label === one.value ? undefined : one.label,
      }));
  }
  const word = body.toLowerCase();
  // Upper case starts an operator: the fields are written in lower case.
  const before = tokenize(query.slice(0, start)).at(-1);
  // After a filter or a closing parenthesis, AND and OR may follow.
  const afterOperand = before?.kind === "word" || before?.kind === ")";
  const operators = negated
    ? []
    : OPERATORS.filter(
        (op) =>
          (op === "NOT" || afterOperand) &&
          (word ? op.toLowerCase().startsWith(word) : afterOperand),
      ).map((op) => ({ ...take(op), label: op, detail: OPERATOR_HELP[op] }));
  if (operators.length > 0 && body !== word) return operators;
  const fields = FIELDS.filter(
    (field) =>
      field.key.startsWith(word) ||
      (word &&
        field.label
          .toLowerCase()
          .split(" ")
          .some((one) => one.startsWith(word))),
  )
    // The shortest key first, so the word typed whole is the one Tab takes.
    .sort((a, b) => (word ? a.key.length - b.key.length : 0))
    .map((field) => ({
      ...take(`${negated ? "-" : ""}${field.key}:`, false),
      label: `${negated ? "-" : ""}${field.key}:`,
      detail: negated ? `Not ${field.label.toLowerCase()}` : field.label,
      field: true,
    }));
  return [...fields, ...operators];
}

const OPERATOR_HELP: Record<(typeof OPERATORS)[number], string> = {
  AND: "Both sides hold (the default)",
  OR: "Either side holds",
  NOT: "Leaves out what follows",
};

/** The page a filter belongs to: `inbox`, `inbox:<id>` or `pulls`. */
export type FilterPage = string;
/** A Pull requests tab saved from a view and its filter. */
export type SavedTab = { id: string; name: string; view: string; query: string };
type Filters = { pages: Record<FilterPage, string>; tabs: SavedTab[] };
/** The daemon keeps at most 64 pages; the least recently changed go first. */
const KEEP_PAGES = 60;
export const MAX_TABS = 20;

const filters = () => uiState<Filters>("api/ui-state/filters", () => ({ pages: {}, tabs: [] }));
/** The document as the daemon takes it back: a page's filter is text. */
function readable(value: Partial<Filters>): Filters {
  const pages: Record<string, string> = {};
  for (const [page, query] of Object.entries(value.pages ?? {}))
    if (typeof query === "string") pages[page] = query;
  const tabs = (Array.isArray(value.tabs) ? value.tabs : []).filter(
    (tab): tab is SavedTab =>
      !!tab &&
      typeof tab.id === "string" &&
      typeof tab.name === "string" &&
      typeof tab.view === "string" &&
      typeof tab.query === "string",
  );
  return { pages, tabs };
}

/** Each page's filter, remembered in the daemon (`ui/filters`). */
export function usePageFilter(page: FilterPage) {
  const saved = useUIState(filters());
  const query = readable(saved.value).pages[page] ?? "";
  return {
    query,
    ready: saved.ready,
    error: saved.error,
    retry: saved.retry,
    set(next: string) {
      const text = tidy(next);
      if (text === query || parseQuery(text).error) return;
      saved.update((value) => {
        const current = readable(value);
        // Changed pages move to the end, so the oldest are dropped first.
        const { [page]: _, ...rest } = current.pages;
        const pages = text ? { ...rest, [page]: text } : rest;
        const keys = Object.keys(pages);
        for (const key of keys.slice(0, Math.max(0, keys.length - KEEP_PAGES))) delete pages[key];
        return { ...current, pages };
      });
    },
  };
}

/** The Pull requests tabs saved from a view and its filter, in order. */
export function useSavedTabs() {
  const saved = useUIState(filters());
  const change = (edit: (tabs: SavedTab[]) => SavedTab[]) =>
    saved.update((value) => {
      const current = readable(value);
      return { ...current, tabs: edit(current.tabs) };
    });
  return {
    tabs: readable(saved.value).tabs,
    ready: saved.ready,
    /** Adds the tab and, in the same save, clears the page whose filter
     *  moved into it. */
    add: (tab: SavedTab, from: FilterPage) =>
      saved.update((value) => {
        const current = readable(value);
        const { [from]: _, ...pages } = current.pages;
        return { pages, tabs: [...current.tabs, tab].slice(0, MAX_TABS) };
      }),
    remove: (id: string) => change((tabs) => tabs.filter((one) => one.id !== id)),
    setQuery: (id: string, query: string) => {
      const text = tidy(query);
      const tab = readable(saved.value).tabs.find((one) => one.id === id);
      if (!tab || tab.query === text || parseQuery(text).error) return;
      change((tabs) => tabs.map((one) => (one.id === id ? { ...one, query: text } : one)));
    },
  };
}
