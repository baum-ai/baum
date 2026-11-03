import type { Entry } from "./api";
import { useEffect } from "react";
import { uiState, useUIState } from "./ui-state";

/** A filter over pull requests, written like a GitHub search:
 *  `repo=acme/app is!=draft (review=approved OR risk>=4) flaky`.
 *
 *  - A term is a field, an operator and a value: `key=value` keeps the pull
 *    requests whose field has the value (`key:value` reads the same), and
 *    `key=a,b` either value; `key!=value` leaves them out, as do `-` and
 *    `NOT` before any term; risk also compares (`risk>=3`, `<`, `<=`, `>`).
 *  - Terms next to each other must all hold (`AND` may be written); `OR`
 *    takes either side and binds looser than `AND`; parentheses group.
 *  - Any other word, or a "quoted phrase", is looked for in the title, the
 *    repository and the number.
 *
 *  Saved inboxes and each page's filter keep the text; it is parsed here. */
export type Expr =
  | { kind: "and" | "or"; items: Expr[] }
  | { kind: "not"; item: Expr }
  | { kind: "term"; key: string; op: Op; values: string[]; start: number; end: number }
  | { kind: "text"; text: string };

export type Option = { value: string; label: string };

/** How a term compares its field with its values. */
export type Op = "=" | "!=" | ">=" | "<=" | ">" | "<";
/** Each operator in words, as the menu and the suggestions name it. */
export const OP_LABELS: Record<Op, string> = {
  "=": "is",
  "!=": "is not",
  ">=": "≥",
  "<=": "≤",
  ">": ">",
  "<": "<",
};
const EQUALITY: Op[] = ["=", "!="];
const COMPARISONS: Op[] = ["=", "!=", ">=", "<=", ">", "<"];

type Field = {
  key: string;
  label: string;
  /** The values to offer, given the page's pull requests. */
  values: (entries: Entry[]) => Option[];
  /** The operators it takes, `=` and `!=` unless it says. */
  ops?: Op[];
  /** Why a typed value is not one the field takes with the operator, or "". */
  check?: (value: string, op: Op) => string;
  /** Whether the pull request's field is the value, or for a comparison
   *  passes it; `kind` is what it waits for in an inbox's queue. */
  test: (value: string, entry: Entry, op: Op, kind?: string) => boolean;
  /** Only an inbox's queue knows it (`kind`): offered there alone. */
  inboxOnly?: boolean;
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
function listed(key: string, label: string, options: Option[], test: Field["test"]): Field {
  return {
    key,
    label,
    values: () => options,
    check: (value) =>
      options.some((one) => same(one.value, value))
        ? ""
        : `${key} takes ${options.map((one) => one.value).join(", ")}.`,
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
/** What a queue item waits for, by its kind: the inbox's "what". */
const WAITING: Record<string, [string, (kind: string) => boolean]> = {
  review: ["My review", (kind) => kind === "review" || kind === "clone"],
  merge: ["Merging", (kind) => kind === "merge"],
  agent: ["An agent's fix", (kind) => kind === "fix" || kind === "update"],
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

/** Whether the score compares with a risk value as the operator asks; a pull
 *  request with no score yet is only `none`, and passes no comparison. */
function riskTest(value: string, entry: Entry, op: Op): boolean {
  const score = entry.risk_score?.score;
  if (same(value, "none")) return typeof score !== "number";
  if (!/^[1-5]$/.test(value) || typeof score !== "number") return false;
  const n = Number(value);
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

const FIELDS: Field[] = [
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
    ...listed(
      "waiting",
      "Waiting for",
      Object.entries(WAITING).map(([value, [text]]) => ({ value, label: text })),
      (value, _entry, _op, kind) => !!kind && WAITING[value.toLowerCase()]?.[1](kind) === true,
    ),
    inboxOnly: true,
  },
  {
    key: "risk",
    label: "Risk",
    ops: COMPARISONS,
    values: () => [
      ...[5, 4, 3, 2, 1].map((n) => ({ value: String(n), label: String(n) })),
      { value: "none", label: "Not scored yet" },
    ],
    check: (value, op) =>
      /^[1-5]$/.test(value) || (same(value, "none") && EQUALITY.includes(op))
        ? ""
        : same(value, "none")
          ? "risk compares only scores: none takes = or !=."
          : "risk takes 1 to 5, or none.",
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

const OPERATORS = ["AND", "OR", "NOT"] as const;

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

/** A field, an operator and what follows: `review!=a,b`. `:` is `=`, and
 *  `risk:>=3` reads as `risk>=3`. */
const TERM = /^([A-Za-z][A-Za-z-]*)(!=|>=|<=|=|:|>|<)(.*)$/s;

/** A word's key, operator and values when it is a term (before any quote). */
function splitTerm(query: string, token: Extract<Token, { kind: "word" }>) {
  const raw = query.slice(token.start, token.end).replace(/^-/, "");
  const match = TERM.exec(raw);
  if (!match) return null;
  let op = (match[2] === ":" ? "=" : match[2]) as Op;
  let rest = match[3]!;
  const compared = /^(>=|<=|>|<)/.exec(rest);
  if (match[2] === ":" && compared) {
    op = compared[1] as Op;
    rest = rest.slice(op.length);
  }
  const values: string[] = [];
  let current = "";
  let inQuote = false;
  for (const c of rest) {
    if (c === '"') inQuote = !inQuote;
    else if (c === "," && !inQuote) {
      values.push(current);
      current = "";
    } else current += c;
  }
  values.push(current);
  return { key: match[1]!.toLowerCase(), op, values };
}

export type Parsed = { expr: Expr | null; error: string };

function parse(query: string): Parsed {
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
      if (!field) fail(`Unknown filter "${term.key}". Try ${FIELDS.map((f) => f.key).join(", ")}.`);
      const ops = field!.ops ?? EQUALITY;
      if (!ops.includes(term.op)) fail(`${field!.key} takes ${ops.join(" or ")}.`);
      if (term.values.some((v) => v.trim() === "")) fail(`${field!.key}${term.op} needs a value.`);
      for (const value of term.values) {
        const problem = field!.check?.(value, term.op);
        if (problem) fail(problem);
      }
      expr = {
        kind: "term",
        key: field!.key,
        op: term.op,
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

function test(expr: Expr, entry: Entry, kind?: string): boolean {
  switch (expr.kind) {
    case "and":
      return expr.items.every((one) => test(one, entry, kind));
    case "or":
      return expr.items.some((one) => test(one, entry, kind));
    case "not":
      return !test(expr.item, entry, kind);
    case "term": {
      const field = fieldOf(expr.key);
      if (!field) return false;
      if (expr.op === "!=")
        return !expr.values.some((value) => field.test(value, entry, "=", kind));
      return expr.values.some((value) => field.test(value, entry, expr.op, kind));
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

/** Why the query cannot be saved: it does not read, or is longer than the
 *  daemon keeps. "" when it can. */
export function queryProblem(query: string, inbox = true): string {
  return (
    parseQuery(query).error ||
    (query.length > MAX_QUERY ? `A filter is at most ${MAX_QUERY} characters.` : "") ||
    (!inbox && usesInboxOnly(query) ? "waiting works only in an inbox's queue." : "")
  );
}

/** Whether the query names a field only an inbox's queue knows. */
function usesInboxOnly(query: string): boolean {
  const walk = (expr: Expr | null): boolean =>
    !!expr &&
    (expr.kind === "and" || expr.kind === "or"
      ? expr.items.some(walk)
      : expr.kind === "not"
        ? walk(expr.item)
        : expr.kind === "term" && !!fieldOf(expr.key)?.inboxOnly);
  return walk(parseQuery(query).expr);
}

/** Whether the pull request passes the query; `kind` is what it waits for
 *  in an inbox's queue (none elsewhere, where `waiting` holds for nothing). A
 *  query that cannot be read narrows nothing; the filter bar says why. */
export function matchesQuery(query: string, entry: Entry, kind?: string): boolean {
  const { expr } = parseQuery(query);
  return !expr || test(expr, entry, kind);
}

/** The fields a page offers: `waiting` only where queue items have a kind. */
export function fieldsFor(inbox = false) {
  return FIELDS.filter((field) => inbox || !field.inboxOnly);
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

/** Whether the query requires the field to compare so with the value, as
 *  the menu ticks it. */
export function hasValue(query: string, key: string, value: string, op: Op = "="): boolean {
  return topTerms(query).some(
    (term) => term.key === key && term.op === op && term.values.some((one) => same(one, value)),
  );
}

/** The query on one line, as the daemon keeps it: control characters and runs
 *  of space become one space. */
export const tidyQuery = (query: string) => query.replace(/[\p{Cc}\s]+/gu, " ").trim();
const tidy = tidyQuery;
/** The daemon's limit on a query. */
const MAX_QUERY = 500;

/** The query with the value required, or no longer required: a term of the
 *  field and operator that must hold gains or loses it as one more choice
 *  (`review=a,b`); otherwise a term is added, around a query with OR in
 *  parentheses. */
export function toggleValue(query: string, key: string, value: string, op: Op = "="): string {
  const terms = topTerms(query).filter((one) => one.key === key && one.op === op);
  const term = terms.find((one) => one.values.some((v) => same(v, value))) ?? terms[0];
  const { expr, error } = parseQuery(query);
  if (term) {
    const values = term.values.some((one) => same(one, value))
      ? term.values.filter((one) => !same(one, value))
      : [...term.values, value];
    const text = values.length ? `${key}${op}${values.map(quote).join(",")}` : "";
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
  return tidy(`${base} ${key}${op}${quote(value)}`);
}

/** Both queries must hold: the second added to the first, each in
 *  parentheses when it has an OR of its own. */
export function andQuery(first: string, second: string): string {
  const wrap = (query: string) => {
    const { expr, error } = parseQuery(query);
    return !error && expr?.kind === "or" ? `(${query.trim()})` : query.trim();
  };
  const parts = [first, second].filter((one) => one.trim());
  // One alone needs no parentheses.
  return tidy(parts.length > 1 ? parts.map(wrap).join(" ") : (parts[0] ?? ""));
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
export function suggest(
  query: string,
  caret: number,
  entries: Entry[],
  inbox = false,
): Suggestion[] {
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
  const sign = negated ? "-" : "";
  const term = TERM.exec(body);
  // `risk:` waits for its comparison like `risk`, and `review!` for its `=`.
  const field = fieldOf((term?.[1] ?? /^([A-Za-z][A-Za-z-]*)!?$/.exec(body)?.[1]) || "");
  const ops = field?.ops ?? EQUALITY;
  const opRow = (op: Op): Suggestion => ({
    ...take(`${sign}${field!.key}${op}`, false),
    label: `${sign}${field!.key}${op}`,
    detail: OP_LABELS[op],
    field: true,
  });
  // A field with `!` or `:` alone: its operators come next, as they do above
  // the other fields when it is typed whole.
  if (field && /^[^!:=<>]+(!|:)$/.test(body))
    return (body.endsWith("!") ? ops.filter((op) => op === "!=") : ops).map(opRow);
  const whole = field && body.toLowerCase() === field.key ? ops.map(opRow) : [];
  if (term && field) {
    let op = (term[2] === ":" ? "=" : term[2]) as Op;
    let rest = term[3]!;
    // `risk>` may still become `risk>=`.
    const longer =
      (op === ">" || op === "<") && rest === "" && ops.includes(`${op}=` as Op)
        ? [opRow(`${op}=` as Op)]
        : [];
    if (term[2] === ":" && /^(>=|<=|>|<)/.test(rest)) {
      op = /^(>=|<=|>|<)/.exec(rest)![1] as Op;
      rest = rest.slice(op.length);
    }
    if (!ops.includes(op)) return [];
    const listedValues = rest.split(",");
    const partial = (listedValues.pop() ?? "").replace(/^"/, "").toLowerCase();
    const prefix = `${sign}${field.key}${op}${listedValues.map(quote).join(",")}${listedValues.length ? "," : ""}`;
    const values = field
      .values(entries)
      .filter(
        (one) =>
          !listedValues.some((v) => same(v, one.value)) &&
          // A value typed whole has nothing left to complete.
          !same(one.value, partial) &&
          !field.check?.(one.value, op) &&
          (one.value.toLowerCase().includes(partial) || one.label.toLowerCase().includes(partial)),
      )
      .slice(0, 50)
      .map((one) => ({
        ...take(`${prefix}${quote(one.value)}`),
        label: `${sign}${field.key}${op}${one.value}`,
        detail: one.label === one.value ? undefined : one.label,
      }));
    return [...longer, ...values];
  }
  if (term) return [];
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
  const fields = fieldsFor(inbox)
    .filter(
      (one) =>
        // The field typed whole offers its operators instead.
        (whole.length === 0 || one !== field) &&
        (one.key.startsWith(word) ||
          (!!word &&
            one.label
              .toLowerCase()
              .split(" ")
              .some((part) => part.startsWith(word)))),
    )
    // The shortest key first, so the word typed whole is the one Tab takes.
    .sort((a, b) => (word ? a.key.length - b.key.length : 0))
    .map((field) => ({
      ...take(`${sign}${field.key}`, false),
      label: `${sign}${field.key}`,
      detail: negated ? `Not ${field.label.toLowerCase()}` : field.label,
      field: true,
    }));
  return [...whole, ...fields, ...operators];
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
  const stored = readable(saved.value).pages[page];
  const inbox = page.startsWith("inbox");
  /** Keeps the page's filter, or with null none. A saved inbox's or tab's
   *  draft can be "" (everything), which is not the same as no draft. */
  const write = (text: string | null) =>
    saved.update((value) => {
      const current = readable(value);
      // Changed pages move to the end, so the oldest are dropped first.
      const { [page]: _, ...rest } = current.pages;
      const pages = text === null ? rest : { ...rest, [page]: text };
      const keys = Object.keys(pages);
      for (const key of keys.slice(0, Math.max(0, keys.length - KEEP_PAGES))) delete pages[key];
      return { ...current, pages };
    });
  return {
    query: stored ?? "",
    /** What is kept for the page, undefined when nothing is. */
    draft: stored,
    ready: saved.ready,
    error: saved.error,
    retry: saved.retry,
    /** Keeps the filter; "" keeps none. */
    set(next: string) {
      const text = tidy(next);
      if (text === (stored ?? "") || queryProblem(text, inbox)) return;
      write(text || null);
    },
    /** Keeps the filter even when "": a draft that shows everything. */
    keep(next: string) {
      const text = tidy(next);
      if (text === stored || queryProblem(text, inbox)) return;
      write(text);
    },
    /** Keeps nothing for the page. */
    reset() {
      write(null);
    },
  };
}

/** A page's filter, or over a saved inbox or tab (`saved`, its query; null
 *  for a page of neither) a draft of that query: it starts as the saved one,
 *  and a change narrows the page until saved into it. "" is then a draft that
 *  shows everything. A draft the saved query caught up with is dropped once
 *  `confirmed` (a count of saves the daemon accepted) moves, so a save shown
 *  before the daemon answers, and then refused, keeps it. */
export function useFilterDraft(page: FilterPage, saved: string | null, confirmed = 0) {
  const filter = usePageFilter(page);
  const own = saved !== null;
  const dirty = own && filter.draft !== undefined && filter.draft !== saved;
  useEffect(() => {
    if (own && filter.draft !== undefined && filter.draft === saved) filter.reset();
  }, [confirmed]);
  return {
    ...filter,
    query: own ? (filter.draft ?? saved) : filter.query,
    dirty,
    set: (next: string) =>
      !own ? filter.set(next) : next === saved ? filter.reset() : filter.keep(next),
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
    rename: (id: string, name: string) =>
      change((tabs) => tabs.map((one) => (one.id === id ? { ...one, name } : one))),
    setQuery: (id: string, query: string) => {
      const text = tidy(query);
      const tab = readable(saved.value).tabs.find((one) => one.id === id);
      if (!tab || tab.query === text || queryProblem(text, false)) return;
      change((tabs) => tabs.map((one) => (one.id === id ? { ...one, query: text } : one)));
    },
  };
}
