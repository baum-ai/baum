import { expect, it } from "vitest";
import type { Entry } from "./api";
import {
  hasValue,
  matchesQuery,
  ownerOf,
  parseQuery,
  queryParts,
  queryProblem,
  quote,
  suggest,
  toggleValue,
  withText,
} from "./pr-filter";

const entry = (pr: Partial<Entry["pull_request"]> = {}, state: Partial<Entry> = {}): Entry =>
  ({
    risk_score: null,
    review: "",
    checks: "",
    merge: "",
    ...state,
    pull_request: {
      repository: "acme/demo",
      number: 1,
      title: "Fix the flaky login test",
      author: "sam",
      draft: false,
      authored_by_me: false,
      review_requested_from_me: false,
      ...pr,
    },
  }) as Entry;
const scored = (score: number) => entry({}, { risk_score: { score, status: "assessed" } });
const passes = (query: string, one: Entry) => {
  expect(parseQuery(query).error).toBe("");
  return matchesQuery(query, one);
};

it("matches a field's value, ignoring case, and any of several", () => {
  expect(passes("repo:ACME/Demo", entry())).toBe(true);
  expect(passes("repo:demo", entry())).toBe(true);
  expect(passes("repo:other/demo", entry())).toBe(false);
  expect(passes("org:acme", entry({ repository: "github.example.com/acme/app" }))).toBe(true);
  expect(passes("review:approved,changes_requested", entry({}, { review: "approved" }))).toBe(true);
  expect(passes("review:none", entry({}, { review: "commented" }))).toBe(true);
  expect(passes("checks:none", entry())).toBe(true);
  expect(passes("is:draft", entry({ draft: true }))).toBe(true);
  expect(passes("is:mergeable", entry({}, { merge: "ready" }))).toBe(true);
  expect(passes("author:@me", entry({ authored_by_me: true }))).toBe(true);
  expect(passes("author:SAM", entry())).toBe(true);
  expect(passes("assignee:@me", entry({ assigned_to_me: true }))).toBe(true);
  expect(passes("assignee:@me", entry())).toBe(false);
  expect(passes("review-requested:@me", entry({ review_requested_from_me: true }))).toBe(true);
  expect(passes("base:main", entry({ base_ref: "main" }))).toBe(true);
});

it("compares risk, and an unscored pull request passes only risk:none", () => {
  expect(passes("risk:>=3", scored(3))).toBe(true);
  expect(passes("risk:>3", scored(3))).toBe(false);
  expect(passes("risk:<3", scored(2))).toBe(true);
  expect(passes("risk:4", scored(4))).toBe(true);
  expect(passes("risk:>=1", entry())).toBe(false);
  expect(passes("risk:<5", entry())).toBe(false);
  expect(passes("risk:none", entry())).toBe(true);
  expect(passes("risk:none", scored(1))).toBe(false);
  expect(passes("risk:None", entry())).toBe(true);
  expect(passes("author:@Me", entry({ authored_by_me: true }))).toBe(true);
  // The operator is part of the term: risk>=3 and risk:>=3 read the same.
  expect(passes("risk>=3", scored(4))).toBe(true);
  expect(passes("risk<=2", scored(3))).toBe(false);
  expect(passes("risk!=none", scored(1))).toBe(true);
});

it("reads = and : alike, and != as not any of the values", () => {
  expect(passes("review=approved", entry({}, { review: "approved" }))).toBe(true);
  expect(passes("review!=approved", entry({}, { review: "approved" }))).toBe(false);
  expect(passes("review!=approved,none", entry({}, { review: "changes_requested" }))).toBe(true);
  expect(passes("is!=draft", entry({ draft: true }))).toBe(false);
  expect(passes("-is=draft", entry({ draft: true }))).toBe(false);
});

it("combines terms with AND, OR, NOT, - and parentheses", () => {
  const approvedDraft = entry({ draft: true }, { review: "approved" });
  expect(passes("review:approved is:draft", approvedDraft)).toBe(true);
  expect(passes("review:approved AND -is:draft", approvedDraft)).toBe(false);
  expect(passes("review:approved NOT is:draft", approvedDraft)).toBe(false);
  expect(passes("checks:failing OR is:draft", approvedDraft)).toBe(true);
  // AND binds tighter than OR.
  expect(passes("checks:failing OR review:approved is:draft", approvedDraft)).toBe(true);
  expect(passes("(checks:failing OR review:approved) -is:draft", approvedDraft)).toBe(false);
  expect(passes("-(checks:failing OR is:draft)", approvedDraft)).toBe(false);
});

it("looks for other words and phrases in the title, repository and number", () => {
  expect(passes("flaky", entry())).toBe(true);
  expect(passes('"login test"', entry())).toBe(true);
  expect(passes('"test login"', entry())).toBe(false);
  expect(passes("#1", entry())).toBe(true);
  expect(passes("-flaky", entry())).toBe(false);
  expect(passes("demo", entry())).toBe(true);
});

it("says why a query does not read, and then narrows nothing", () => {
  for (const [query, problem] of [
    ["colour:red", 'Unknown filter "colour"'],
    ["review=maybe", "review takes approved, changes_requested, none."],
    ["review>=approved", "review takes = or !=."],
    ["risk=7", "risk takes 1 to 5, or none."],
    ["risk>=none", "risk compares only scores"],
    ["assignee=sam", "assignee takes @me, none."],
    ["repo=", "repo= needs a value."],
    ["(is:draft", "A parenthesis is not closed."],
    ["is:draft)", "A closing parenthesis has no opening one."],
    ["is:draft OR", "The query ends where a filter should follow."],
    ["OR is:draft", "OR needs a filter before it."],
  ] as const) {
    expect(parseQuery(query).error).toContain(problem);
    expect(matchesQuery(query, entry())).toBe(true);
  }
  expect(parseQuery("   ")).toEqual({ expr: null, error: "" });
  // waiting= is an inbox's queue alone: refused elsewhere.
  expect(queryProblem("waiting=review")).toBe("");
  expect(queryProblem("is=draft OR -waiting=merge", false)).toBe(
    "waiting works only in an inbox's queue.",
  );
  // Too long to save, though it still reads and matches.
  expect(queryProblem("a".repeat(501))).toBe("A filter is at most 500 characters.");
  expect(parseQuery("a".repeat(501)).error).toBe("");
});

it("adds a picked value to the field's term, takes it out again, and wraps an OR", () => {
  expect(toggleValue("", "review", "approved")).toBe("review=approved");
  expect(toggleValue("review:approved flaky", "review", "none")).toBe("review=approved,none flaky");
  expect(toggleValue("review=approved,none flaky", "review", "approved")).toBe("review=none flaky");
  expect(toggleValue("review=none flaky", "review", "none")).toBe("flaky");
  expect(toggleValue("is=draft OR checks=failing", "repo", "acme/demo")).toBe(
    "(is=draft OR checks=failing) repo=acme/demo",
  );
  // Each operator is a term of its own.
  expect(toggleValue("is=draft", "is", "bot", "!=")).toBe("is=draft is!=bot");
  expect(toggleValue("is!=bot,draft", "is", "bot", "!=")).toBe("is!=draft");
  expect(toggleValue("", "risk", "3", ">=")).toBe("risk>=3");
  // A term under NOT is not the field's choice: a new one is added.
  expect(toggleValue("-is=draft", "is", "conflict")).toBe("-is=draft is=conflict");
  expect(toggleValue("", "base", "release 1")).toBe('base="release 1"');
  expect(hasValue("review=approved,none", "review", "None")).toBe(true);
  expect(hasValue("review!=approved", "review", "approved")).toBe(false);
  expect(hasValue("review!=approved", "review", "approved", "!=")).toBe(true);
  expect(hasValue("-review=approved", "review", "approved")).toBe(false);
  expect(hasValue("review=approved OR is=draft", "review", "approved")).toBe(false);
  expect(withText("is=draft", "login test")).toBe('is=draft "login test"');
  expect(quote("a,b")).toBe('"a,b"');
  // The term that holds the value loses it, and parentheses left empty go.
  expect(toggleValue("is=draft is=bot", "is", "bot")).toBe("is=draft");
  expect(toggleValue("(review=approved) flaky", "review", "approved")).toBe("flaky");
  expect(toggleValue("((review=approved)) flaky", "review", "approved")).toBe("flaky");
  expect(toggleValue("review=approved AND flaky", "review", "approved")).toBe("flaky");
  expect(toggleValue("flaky AND review=approved", "review", "approved")).toBe("flaky");
  // Typed text that would read as syntax is quoted.
  expect(withText("", "OR")).toBe('"OR"');
  expect(withText("", "-wip")).toBe('"-wip"');
  expect(withText("", 'say "hi"')).toBe('"say hi"');
  expect(passes(withText("", "-wip"), entry({ title: "-wip: draft" }))).toBe(true);
});

it("suggests a field, then its operators, then its values", () => {
  const entries = [entry(), entry({ repository: "acme/web", number: 2 })];
  const labels = (query: string, caret = query.length) =>
    suggest(query, caret, entries).map((one) => one.label);
  expect(labels("re")).toEqual(["repo", "review", "review-requested"]);
  expect(labels("-re")).toEqual(["-repo", "-review", "-review-requested"]);
  // A field typed whole offers its operators, then the longer fields.
  expect(labels("review")).toEqual(["review=", "review!=", "review-requested"]);
  expect(labels("risk")).toEqual(["risk=", "risk!=", "risk>=", "risk<=", "risk>", "risk<"]);
  expect(labels("review!")).toEqual(["review!="]);
  expect(labels("repo=")).toEqual(["repo=acme/demo", "repo=acme/web"]);
  expect(labels("repo!=we")).toEqual(["repo!=acme/web"]);
  expect(labels("repo:")).toEqual(["repo=", "repo!="]);
  expect(labels("review=approved,")).toEqual(["review=changes_requested", "review=none"]);
  // Comparisons offer scores only; risk> may still become risk>=.
  expect(labels("risk>=")).toEqual(["risk>=5", "risk>=4", "risk>=3", "risk>=2", "risk>=1"]);
  expect(labels("risk>")[0]).toBe("risk>=");
  expect(labels("review>=")).toEqual([]);
  expect(labels("is=draft O")).toEqual(["OR"]);
  expect(labels("N")).toEqual(["NOT"]);
  expect(labels("is=draft ").slice(-3)).toEqual(["AND", "OR", "NOT"]);
  expect(labels("")).not.toContain("OR");
  expect(labels("colour=")).toEqual([]);
  // Inside a word, nothing: taking one would drop the rest of the word.
  expect(labels("review=approved,none", 10)).toEqual([]);
  // At a word's start, nothing either: a field would join onto it.
  expect(labels("repo=x is=draft", 7)).toEqual([]);
  expect(labels("is=draft")).toEqual([]);
  // Taking a value replaces the word at the caret and leaves a space after it.
  const [value] = suggest("is=draft repo=we checks=none", 16, entries);
  expect(value).toMatchObject({ query: "is=draft repo=acme/web checks=none", caret: 23 });
  const [field] = suggest("che", 3, entries);
  expect(field).toMatchObject({ query: "checks", caret: 6, field: true });
  const [op] = suggest("checks", 6, entries);
  expect(op).toMatchObject({ query: "checks=", caret: 7, field: true });
});

it("offers every known repository, organization, author and branch, not only the page's", () => {
  const page = [entry()];
  const known = {
    entries: [entry({ repository: "acme/web", author: "kim", base_ref: "release" })],
    repositories: ["quiet/tools", "acme/demo"],
  };
  const labels = (query: string) =>
    suggest(query, query.length, page, false, undefined, true, known).map((one) => one.label);
  expect(labels("repo=")).toEqual(["repo=acme/demo", "repo=acme/web", "repo=quiet/tools"]);
  expect(labels("org=")).toEqual(["org=acme", "org=quiet"]);
  expect(labels("author=")).toEqual(["author=@me", "author=kim", "author=sam"]);
  expect(labels("base=")).toEqual(["base=release"]);
  // Without them, only the page's.
  expect(suggest("org=", 4, page).map((one) => one.label)).toEqual(["org=acme"]);
});

it("finds the owner of an Enterprise repository", () => {
  expect(ownerOf("acme/demo")).toBe("acme");
  expect(ownerOf("github.example.com/acme/demo")).toBe("acme");
  expect(ownerOf("nothing")).toBe("");
});

it("matches assignee=none and review-requested=none when nobody holds it, and not the person", () => {
  // The daemon's fields absent read as nobody.
  expect(passes("assignee=none", entry())).toBe(true);
  expect(passes("review-requested=none", entry())).toBe(true);
  expect(passes("assignee=none", entry({ assigned: false }))).toBe(true);
  expect(passes("review-requested=none", entry({ review_requested: false }))).toBe(true);
  // Someone is assigned or asked.
  expect(passes("assignee=none", entry({ assigned: true }))).toBe(false);
  expect(passes("review-requested=none", entry({ review_requested: true }))).toBe(false);
  // The person counts as someone, whatever the daemon says of anyone.
  expect(passes("assignee=none", entry({ assigned_to_me: true }))).toBe(false);
  expect(passes("review-requested=none", entry({ review_requested_from_me: true }))).toBe(false);
  // != none is somebody.
  expect(passes("assignee!=none", entry({ assigned: true }))).toBe(true);
  expect(passes("assignee!=none", entry())).toBe(false);
  // @me still means the person alone.
  expect(passes("assignee=@me", entry({ assigned: true }))).toBe(false);
  expect(passes("review-requested=@me", entry({ review_requested: true }))).toBe(false);
});

it("matches waiting=other only for a pull request listed as nothing from the person", () => {
  expect(passes("waiting=other", entry())).toBe(false);
  expect(matchesQuery("waiting=other", entry(), "other")).toBe(true);
  for (const kind of ["review", "clone", "merge", "fix", "update"]) {
    expect(matchesQuery("waiting=other", entry(), kind)).toBe(false);
  }
  expect(matchesQuery("waiting!=other", entry(), "review")).toBe(true);
  expect(matchesQuery("waiting!=other", entry(), "other")).toBe(false);
  expect(matchesQuery("waiting=other assignee=none review-requested=none", entry(), "other")).toBe(
    true,
  );
});

it("reads a query's conditions in words, each with the query once it is taken out", () => {
  expect(queryParts("")).toEqual({ joiner: "and", parts: [] });
  expect(queryParts("   ")).toEqual({ joiner: "and", parts: [] });
  expect(queryParts("author=@me")).toEqual({
    joiner: "and",
    parts: [{ label: "Author is Me", without: "" }],
  });
  expect(queryParts("author=@me risk>=4 flaky")).toEqual({
    joiner: "and",
    parts: [
      { label: "Author is Me", without: "risk>=4 flaky" },
      { label: expect.stringContaining("Risk"), without: "author=@me flaky" },
      { label: "“flaky”", without: "author=@me risk>=4" },
    ],
  });
  // An AND written out reads the same.
  expect(queryParts("author=@me AND is=draft").parts.map((part) => part.without)).toEqual([
    "is=draft",
    "author=@me",
  ]);
});

it("reads an OR at the top as choices, and a taken-out choice leaves the others joined by OR", () => {
  const { joiner, parts } = queryParts("author=@me OR assignee=@me OR review-requested=@me");
  expect(joiner).toBe("or");
  expect(parts).toEqual([
    { label: "Author is Me", without: "assignee=@me OR review-requested=@me" },
    { label: "Assignee is Me", without: "author=@me OR review-requested=@me" },
    { label: "Review requested is Me", without: "author=@me OR assignee=@me" },
  ]);
});

it("keeps a parenthesised group's parentheses when its sibling is taken out, and words the group", () => {
  const { joiner, parts } = queryParts("org=x (author=@me OR assignee=@me)");
  expect(joiner).toBe("and");
  expect(parts).toEqual([
    { label: "Organization is x", without: "(author=@me OR assignee=@me)" },
    { label: "Author is Me or Assignee is Me", without: "org=x" },
  ]);
});

it("words a negation as its field is not the value", () => {
  const { parts } = queryParts("-repo=x NOT is=draft");
  expect(parts).toEqual([
    { label: "Repository is not x", without: "NOT is=draft" },
    { label: "State is not Draft", without: "-repo=x" },
  ]);
});

it("gives no parts for a query that does not read", () => {
  expect(queryParts("colour:red")).toEqual({ joiner: "and", parts: [] });
  expect(queryParts("(is:draft")).toEqual({ joiner: "and", parts: [] });
});
