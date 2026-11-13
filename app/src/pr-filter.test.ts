import { expect, it } from "vitest";
import type { Entry } from "./api";
import type { Option } from "./pr-filter";
import {
  countValues,
  hasPart,
  hasValue,
  matchesQuery,
  togglePart,
  ownerOf,
  parseQuery,
  queryTerms,
  withoutTerm,
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

it("combines terms with AND, OR, - and parentheses", () => {
  const approvedDraft = entry({ draft: true }, { review: "approved" });
  expect(passes("review:approved is:draft", approvedDraft)).toBe(true);
  expect(passes("review:approved AND -is:draft", approvedDraft)).toBe(false);
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
  // A negated term is not the field's choice: a new one is added.
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
  expect(labels("is=draft N")).toEqual([]);
  // Operators only follow a filter, and come first while nothing is typed.
  expect(labels("is=draft ").slice(0, 2)).toEqual(["AND", "OR"]);
  expect(labels("(is=draft OR is=bot) ").slice(0, 2)).toEqual(["AND", "OR"]);
  expect(labels("N")).toEqual([]);
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

/** The query without the term written as `term`. */
const drop = (query: string, term: string) => {
  const found = queryTerms(query).find((one) => query.slice(one.start, one.end) === term);
  if (!found) throw new Error(`no term ${term} in ${query}`);
  return withoutTerm(query, found.start);
};

it("finds a query's terms in order, with where they stand and their words", () => {
  expect(queryTerms("")).toEqual([]);
  expect(queryTerms("   ")).toEqual([]);
  const query = "org=x (author=@me OR assignee=@me) -repo=y flaky";
  expect(queryTerms(query).map((term) => [query.slice(term.start, term.end), term.label])).toEqual([
    ["org=x", "Organization is x"],
    ["author=@me", "Author is Me"],
    ["assignee=@me", "Assignee is Me"],
    ["-repo=y", "Repository is not y"],
    ["flaky", "“flaky”"],
  ]);
  // One that does not read has none, and stays as it is.
  expect(queryTerms("colour:red")).toEqual([]);
  expect(queryTerms("(is:draft")).toEqual([]);
  expect(withoutTerm("(is:draft", 1)).toBe("(is:draft");
});

it("takes a term out with the AND or OR beside it, keeping the rest as written", () => {
  expect(drop("author=@me", "author=@me")).toBe("");
  expect(drop("author=@me risk>=4 flaky", "risk>=4")).toBe("author=@me flaky");
  expect(drop("author=@me AND is=draft", "author=@me")).toBe("is=draft");
  expect(drop("author=@me AND is=draft", "is=draft")).toBe("author=@me");
  const forMe = "author=@me OR assignee=@me OR review-requested=@me";
  expect(drop(forMe, "assignee=@me")).toBe("author=@me OR review-requested=@me");
  expect(drop(forMe, "author=@me")).toBe("assignee=@me OR review-requested=@me");
  expect(drop(forMe, "review-requested=@me")).toBe("author=@me OR assignee=@me");
});

it("keeps a group's parentheses while it holds several choices, and drops them at one", () => {
  const query = "org=baum-ai (author=@me OR assignee=@me OR review-requested=@me) review=none";
  expect(drop(query, "assignee=@me")).toBe(
    "org=baum-ai (author=@me OR review-requested=@me) review=none",
  );
  expect(drop(query, "org=baum-ai")).toBe(
    "(author=@me OR assignee=@me OR review-requested=@me) review=none",
  );
  expect(drop(query, "review=none")).toBe(
    "org=baum-ai (author=@me OR assignee=@me OR review-requested=@me)",
  );
  expect(drop("org=x (author=@me OR assignee=@me)", "assignee=@me")).toBe("org=x author=@me");
  expect(drop("(is=draft author=@me) OR review=none", "is=draft")).toBe(
    "author=@me OR review=none",
  );
});

it("takes a negated term out whole, and a -(…) goes once nothing is left in it", () => {
  expect(drop("-repo=x -is=draft", "-is=draft")).toBe("-repo=x");
  expect(drop("-repo=x -is=draft", "-repo=x")).toBe("-is=draft");
  expect(drop("-(is=draft OR author=@me)", "is=draft")).toBe("-author=@me");
  expect(drop("-(is=draft OR author=@me) org=x", "org=x")).toBe("-(is=draft OR author=@me)");
  expect(drop("-(is=draft OR -author=@me)", "is=draft")).toBe("-(-author=@me)");
});

it("puts a quick filter's query in, and takes it out again, as written", () => {
  const forMe = "author=@me OR assignee=@me OR review-requested=@me";
  // Added, an OR goes in parentheses; added again, it comes out.
  expect(togglePart("", forMe)).toBe(forMe);
  expect(togglePart("risk>=4", forMe)).toBe(`risk>=4 (${forMe})`);
  expect(hasPart(`risk>=4 (${forMe})`, forMe)).toBe(true);
  expect(togglePart(`risk>=4 (${forMe})`, forMe)).toBe("risk>=4");
  expect(togglePart(forMe, forMe)).toBe("");
  // Spacing and case do not hide it; a part only inside an OR is not on.
  expect(hasPart("org=x (  AUTHOR=@me OR assignee=@me OR review-requested=@me )", forMe)).toBe(
    true,
  );
  expect(hasPart("risk>=4 OR is!=draft", "risk>=4")).toBe(false);
  expect(togglePart("risk>=4 OR is!=draft", "risk>=4")).toBe("(risk>=4 OR is!=draft) risk>=4");
  // Several parts: on only when all hold; on adds the missing, off takes all.
  const unclaimed = "assignee=none review-requested=none";
  expect(hasPart("assignee=none", unclaimed)).toBe(false);
  expect(togglePart("assignee=none org=x", unclaimed)).toBe(
    "assignee=none org=x review-requested=none",
  );
  expect(togglePart("assignee=none org=x review-requested=none", unclaimed)).toBe("org=x");
  // A query that does not read is left alone.
  expect(togglePart("(org=x", "risk>=4")).toBe("(org=x");
  // A group already in parentheses is not wrapped twice.
  expect(togglePart("(org=x OR org=y)", "risk>=4")).toBe("(org=x OR org=y) risk>=4");
});

// The counts as the menu worked them out before they were found in one go:
// the query toggled and parsed again for every option and pull request.
function countedOneByOne(
  query: string,
  key: string,
  options: Option[],
  entries: Entry[],
  op: "=" | "!=" = "=",
  kindOf?: (entry: Entry) => string | undefined,
) {
  const passing = (text: string, one: Entry) => matchesQuery(text, one, kindOf?.(one));
  const live = entries.filter((one) => passing(query, one));
  return new Map(
    options.map(({ value }) => [
      value,
      hasValue(query, key, value, op)
        ? live.filter((one) => passing(toggleValue("", key, value, op), one)).length
        : entries.filter(
            (one) =>
              passing(toggleValue(query, key, value, op), one) &&
              passing(toggleValue("", key, value, op), one),
          ).length,
    ]),
  );
}

it("counts every value in one pass as it counted them one by one", () => {
  const entries = Array.from({ length: 200 }, (_, i) =>
    entry(
      {
        repository: `org${i % 7}/repo${i % 40}`,
        author: `author${i % 30}`,
        authored_by_me: i % 11 === 0,
        draft: i % 5 === 0,
        base_ref: i % 3 ? "main" : "dev",
      },
      { review: i % 4 ? "approved" : "changes_requested", checks: i % 2 ? "passing" : "failing" },
    ),
  );
  const kindOf = (one: Entry) => (one.pull_request.draft ? "review" : "merge");
  const repos = [...new Set(entries.map((one) => one.pull_request.repository as string))].map(
    (value) => ({ label: value, value }),
  );
  const authors = [
    { value: "@me", label: "Me" },
    ...[...new Set(entries.map((one) => one.pull_request.author as string))].map((value) => ({
      value,
      label: value,
    })),
  ];
  const queries = [
    "",
    "repo:org1/repo1",
    "repo:org1/repo1,org2/repo2",
    "-repo:org3/repo3",
    "repo!=org3/repo3 author:author1,author2",
    "author:@me OR author:author4",
    "(author:author1 OR repo:repo5) AND review:approved",
    "is:draft checks:failing",
    "waiting:review",
    'repo:"org1/repo1" -is:draft base:main',
    "-(author:author3 OR author:author9)",
    "author:",
    "(unclosed",
    "fix",
  ];
  for (const query of queries)
    for (const [key, options] of [
      ["repo", repos],
      ["author", authors],
    ] as const)
      for (const op of ["=", "!="] as const)
        for (const kinds of [undefined, kindOf])
          expect(countValues(query, key, options, entries, op, kinds)).toEqual(
            countedOneByOne(query, key, options, entries, op, kinds),
          );
}, 60_000);
