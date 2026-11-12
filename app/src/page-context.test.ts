import { beforeEach, expect, it } from "vitest";
import shared from "../../internal/agentcontext/testdata/page-contexts.json";
import { hrefOf, pageContextOf, pageLabel, samePage, type PageContext } from "./page-context";

// The cases the MCP server's own test checks too (TestAppPageContextsAreValid).
const clones: Record<string, string> = shared.clones;
const of = (query: string, hash = "") =>
  pageContextOf(new URLSearchParams(query), (clone) => clones[clone], hash);

beforeEach(() => {
  history.replaceState(null, "", "/s/test-ns/");
});

it("derives the page context of every address the app opens", () => {
  for (const c of shared.cases as { query: string; hash?: string; context: PageContext }[])
    expect(of(c.query, c.hash), c.query).toEqual(c.context);
});

it("names a subject only as the MCP server takes it", () => {
  const long = "x".repeat(1025);
  expect(of(`page=jobs&job=${long}`)).toEqual({ page: "jobs" });
  expect(of(`page=jobs&job=${"x".repeat(1024)}`)).toEqual({ page: "job", job: "x".repeat(1024) });
  // Bytes, not characters: 400 three-byte characters are too many.
  expect(of(`page=timeline&feature=${"€".repeat(400)}`)).toEqual({ page: "timeline" });
  expect(of(`page=pulls&long=${"x".repeat(300)}`)).toEqual({ page: "pulls" });
  expect(of("repository=acme%2Fdemo&pr=99999999999").page).toBe("inbox");
});

it("names the page as the panel's bar does, and links back to it", () => {
  const pr: PageContext = { page: "pull_request", repository: "acme/demo", pull_request: 7 };
  expect(pageLabel(pr)).toBe("acme/demo #7");
  expect(pageLabel(pr, "Fix it")).toBe("Fix it · acme/demo #7");
  expect(pageLabel({ page: "settings", section: "fixes" })).toBe("Settings · Fixes");
  expect(pageLabel({ page: "pulls" })).toBe("Pull requests");
  expect(pageLabel({ page: "session", session: "codex:s1" })).toBe("Codex session");
  for (const query of [
    "repository=acme%2Fdemo&pr=7",
    "page=pulls&group=g1",
    "page=sessions&session=claude-code%3As1",
    "page=jobs&job=j1",
    "page=timeline&feature=f1",
    "page=settings&section=fixes",
    "page=repositories&repository=acme%2Fdemo&section=prompts",
    "page=pulls&filter=all",
  ]) {
    const context = of(query);
    const href = new URL(hrefOf(context), "http://app.test");
    expect(of(href.search.slice(1))).toEqual(context);
  }
  expect(samePage(pr, { ...pr, repository: "ACME/demo", focus: { walk: "x" } })).toBe(true);
  expect(samePage(pr, { ...pr, pull_request: 8 })).toBe(false);
});
