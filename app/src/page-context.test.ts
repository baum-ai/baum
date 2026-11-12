import { beforeEach, expect, it } from "vitest";
import { hrefOf, pageContextOf, pageLabel, samePage, type PageContext } from "./page-context";

const of = (query: string, hash = "") =>
  pageContextOf(
    new URLSearchParams(query),
    (clone) => (clone === "c1" ? "acme/demo" : undefined),
    hash,
  );

beforeEach(() => {
  history.replaceState(null, "", "/s/test-ns/");
});

it("derives the page context of every address the app opens", () => {
  expect(of("")).toEqual({ page: "inbox" });
  expect(of("inbox=abc")).toEqual({ page: "inbox" });
  expect(of("page=inbox-new")).toEqual({ page: "inbox" });
  expect(of("page=pulls&filter=all&q=author%3A%40me")).toEqual({
    page: "pulls",
    filters: { filter: "all", q: "author:@me" },
  });
  expect(of("page=timeline&who=human")).toEqual({ page: "timeline", filters: { who: "human" } });
  expect(of("page=sessions")).toEqual({ page: "sessions" });
  expect(of("page=sessions&peek=claude-code%3As1")).toEqual({ page: "sessions" });
  expect(of("page=analytics")).toEqual({ page: "analytics" });
  expect(of("page=jobs")).toEqual({ page: "jobs" });
  // Pages of one thing.
  expect(of("repository=acme%2Fdemo&pr=7")).toEqual({
    page: "pull_request",
    repository: "acme/demo",
    pull_request: 7,
  });
  expect(of("clone=c1&pr=7&walk=b2", "#sec_rest")).toEqual({
    page: "pull_request",
    repository: "acme/demo",
    pull_request: 7,
    focus: { walk: "b2", anchor: "sec_rest" },
  });
  expect(of("page=pulls&group=g1")).toEqual({ page: "group", group: "g1" });
  expect(of("page=sessions&session=claude-code%3As1")).toEqual({
    page: "session",
    session: "claude-code:s1",
  });
  expect(of("page=jobs&job=j1")).toEqual({ page: "job", job: "j1" });
  expect(of("page=timeline&feature=f1")).toEqual({ page: "feature", feature: "f1" });
  // Sections, as the MCP server names them.
  expect(of("page=settings")).toEqual({ page: "settings", section: "general" });
  expect(of("page=settings&section=fixes")).toEqual({ page: "settings", section: "fixes" });
  expect(of("page=settings&section=unknown")).toEqual({ page: "settings" });
  expect(of("page=diagnostics")).toEqual({ page: "settings", section: "computer" });
  expect(of("page=repositories")).toEqual({ page: "repositories" });
  expect(of("page=repositories&repository=acme%2Fdemo&section=prompts")).toEqual({
    page: "repositories",
    repository: "acme/demo",
    section: "prompts",
  });
  expect(of("page=repositories&repository=acme%2Fdemo")).toEqual({
    page: "repositories",
    repository: "acme/demo",
    section: "reviews",
  });
  // A pull request whose clone is unknown has no repository to name: the
  // list it is on stands in.
  expect(of("page=pulls&clone=gone&pr=7")).toEqual({ page: "pulls" });
});

it("keeps only filters the MCP server's links take", () => {
  const long = "x".repeat(300);
  const context = of(`page=pulls&Bad=1&ok_key=1&long=${long}&inbox=a`);
  expect(context.filters).toEqual({ ok_key: "1" });
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
