import { expect, it } from "vitest";
import type { Entry } from "./api";
import {
  hasValue,
  matchesQuery,
  ownerOf,
  parseQuery,
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
    ["colour:red", 'Unknown filter "colour:"'],
    ["review:maybe", "review: takes approved, changes_requested, none."],
    ["risk:7", "risk: takes"],
    ["assignee:sam", "assignee: takes @me."],
    ["repo:", "repo: needs a value."],
    ["(is:draft", "A parenthesis is not closed."],
    ["is:draft)", "A closing parenthesis has no opening one."],
    ["is:draft OR", "The query ends where a filter should follow."],
    ["OR is:draft", "OR needs a filter before it."],
    ["a".repeat(501), "A filter is at most 500 characters."],
  ] as const) {
    expect(parseQuery(query).error).toContain(problem);
    expect(matchesQuery(query, entry())).toBe(true);
  }
  expect(parseQuery("   ")).toEqual({ expr: null, error: "" });
});

it("adds a picked value to the field's term, takes it out again, and wraps an OR", () => {
  expect(toggleValue("", "review", "approved")).toBe("review:approved");
  expect(toggleValue("review:approved flaky", "review", "none")).toBe("review:approved,none flaky");
  expect(toggleValue("review:approved,none flaky", "review", "approved")).toBe("review:none flaky");
  expect(toggleValue("review:none flaky", "review", "none")).toBe("flaky");
  expect(toggleValue("is:draft OR checks:failing", "repo", "acme/demo")).toBe(
    "(is:draft OR checks:failing) repo:acme/demo",
  );
  // A term under NOT is not the field's choice: a new one is added.
  expect(toggleValue("-is:draft", "is", "conflict")).toBe("-is:draft is:conflict");
  expect(toggleValue("", "base", "release 1")).toBe('base:"release 1"');
  expect(hasValue("review:approved,none", "review", "None")).toBe(true);
  expect(hasValue("-review:approved", "review", "approved")).toBe(false);
  expect(hasValue("review:approved OR is:draft", "review", "approved")).toBe(false);
  expect(withText("is:draft", "login test")).toBe('is:draft "login test"');
  expect(quote("a,b")).toBe('"a,b"');
  // The term that holds the value loses it, and parentheses left empty go.
  expect(toggleValue("is:draft is:bot", "is", "bot")).toBe("is:draft");
  expect(toggleValue("(review:approved) flaky", "review", "approved")).toBe("flaky");
  // Typed text that would read as syntax is quoted.
  expect(withText("", "OR")).toBe('"OR"');
  expect(withText("", "-wip")).toBe('"-wip"');
  expect(withText("", 'say "hi"')).toBe('"say hi"');
  expect(passes(withText("", "-wip"), entry({ title: "-wip: draft" }))).toBe(true);
});

it("suggests fields, then their values, then the operators", () => {
  const entries = [entry(), entry({ repository: "acme/web", number: 2 })];
  const labels = (query: string, caret = query.length) =>
    suggest(query, caret, entries).map((one) => one.label);
  expect(labels("re")).toEqual(["repo:", "review:", "review-requested:"]);
  expect(labels("-re")).toEqual(["-repo:", "-review:", "-review-requested:"]);
  expect(labels("repo:")).toEqual(["repo:acme/demo", "repo:acme/web"]);
  expect(labels("repo:we")).toEqual(["repo:acme/web"]);
  expect(labels("review:approved,")).toEqual(["review:changes_requested", "review:none"]);
  expect(labels("is:draft O")).toEqual(["OR"]);
  expect(labels("N")).toEqual(["NOT"]);
  expect(labels("is:draft ").slice(-3)).toEqual(["AND", "OR", "NOT"]);
  expect(labels("")).not.toContain("OR");
  expect(labels("colour:")).toEqual([]);
  // Inside a word, nothing: taking one would drop the rest of the word.
  expect(labels("review:approved,none", 10)).toEqual([]);
  expect(labels("is:draft")).toEqual([]);
  // Taking a value replaces the word at the caret and leaves a space after it.
  const [value] = suggest("is:draft repo:we checks:none", 16, entries);
  expect(value).toMatchObject({ query: "is:draft repo:acme/web checks:none", caret: 23 });
  const [field] = suggest("che", 3, entries);
  expect(field).toMatchObject({ query: "checks:", caret: 7, field: true });
});

it("finds the owner of an Enterprise repository", () => {
  expect(ownerOf("acme/demo")).toBe("acme");
  expect(ownerOf("github.example.com/acme/demo")).toBe("acme");
  expect(ownerOf("nothing")).toBe("");
});
