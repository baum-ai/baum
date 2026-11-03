import { describe, expect, it } from "vitest";
import {
  timelineFields,
  timelineFilter,
  timelineQuery,
  withTimelineQuery,
} from "./timeline-filter";

const fields = (features: boolean) =>
  timelineFields({ features, repositories: ["acme/app", "acme/web"], people: [], files: [] });

describe("the Timeline's filter", () => {
  it("writes a query into the address and reads it back", () => {
    const query =
      "repo=acme/app who=human,agent category=testing date>=2026-10-01 is=superseded cache";
    const next = withTimelineQuery(new URLSearchParams("tab=all"), query, fields(false), false);
    expect(next.getAll("repo")).toEqual(["acme/app"]);
    expect(next.getAll("who")).toEqual(["human", "agent"]);
    expect(next.get("category")).toBe("testing");
    expect(next.get("from")).toBe("2026-10-01");
    expect(next.get("superseded")).toBe("1");
    expect(next.get("q")).toBe("cache");
    expect(timelineQuery(next, false)).toBe(
      "repo=acme/app who=human,agent category=testing date>=2026-10-01 is=superseded cache",
    );
  });

  it("hides the agents' decisions with who!=agent", () => {
    const next = withTimelineQuery(new URLSearchParams(), "who!=agent", fields(false), false);
    expect(next.get("agent")).toBe("hide");
    expect(timelineFilter(fields(false)).problem("who!=human")).toBe("who!= takes agent alone.");
  });

  it("keeps the decision filters on Features and offers only its own", () => {
    const start = new URLSearchParams("who=human");
    const next = withTimelineQuery(start, "status=shipped", fields(true), true);
    expect(next.get("who")).toBe("human");
    expect(next.get("status")).toBe("shipped");
    expect(timelineQuery(next, true)).toBe("status=shipped");
    expect(timelineFilter(fields(true)).problem("who=human")).toContain('Unknown filter "who"');
  });

  it("refuses what the server cannot apply, and changes nothing for it", () => {
    const { problem } = timelineFilter(fields(true));
    expect(problem("status=shipped OR status=abandoned")).toContain("must all hold");
    expect(problem("-status=shipped")).toContain("must all hold");
    expect(problem("status=shipped status=abandoned")).toContain("status=a,b");
    expect(problem("repo!=acme/app")).toContain("repo takes =");
    expect(problem("repo=acme/gone")).toContain("repo takes acme/app, acme/web.");
    expect(problem("date>=yesterday")).toContain("date takes a day");
    const start = new URLSearchParams("status=shipped");
    expect(withTimelineQuery(start, "status=a OR b", fields(true), true)).toBe(start);
  });

  it("leaves out a repository it does not serve, and takes one file and author", () => {
    const search = new URLSearchParams("repo=acme/gone&repo=acme/app");
    expect(timelineQuery(search, true, ["acme/app", "acme/web"])).toBe("repo=acme/app");
    const { problem } = timelineFilter(fields(true));
    expect(problem("file=a,b")).toBe("file takes one path.");
    expect(problem("person=a,b")).toBe("person takes one author.");
  });

  it("keeps the search as written when another filter changes", () => {
    const search = new URLSearchParams('q=say "hi"');
    const query = `status=shipped ${timelineQuery(search, true)}`;
    expect(withTimelineQuery(search, query, fields(true), true).get("q")).toBe('say "hi"');
  });

  it("quotes text that would read as a filter", () => {
    expect(timelineQuery(new URLSearchParams("q=cache OR store"), true)).toBe('"cache OR store"');
  });
});
