import { describe, expect, it } from "vitest";
import cases from "../../internal/prfilter/testdata/cases.json";
import type { Entry } from "./api";
import { foldedQuery, type Inbox } from "./inboxes";
import { matchesQuery, parseQuery, queryProblem } from "./pr-filter";

// The Go twin of the filter language (oss/internal/prfilter) runs the same
// cases, so the two cannot drift. Change both and the fixtures together.
type Case = { query: string; kind?: string; error?: string; matches: string[] };
const entries = cases.entries as unknown as Record<string, Entry>;
const names = Object.keys(entries).sort();

describe("shared filter fixtures", () => {
  it("lists the entries in name order", () => {
    expect(Object.keys(entries)).toEqual(names);
  });

  it.each(cases.queries as Case[])("query $query ($kind)", (c) => {
    expect(parseQuery(c.query).error).toBe(c.error ?? "");
    const got = names.filter((name) => matchesQuery(c.query, entries[name]!, c.kind));
    expect(got).toEqual(c.matches);
  });

  it.each(cases.problems)("problem for $query", (c) => {
    expect(queryProblem(c.query, true)).toBe(c.problem);
  });

  it.each(cases.folded)("folded $query", (c) => {
    const inbox = {
      id: "x",
      name: "x",
      color: "blue",
      badge: false,
      ...c.inbox,
    } as unknown as Inbox;
    expect(foldedQuery(inbox)).toBe(c.query);
  });
});
