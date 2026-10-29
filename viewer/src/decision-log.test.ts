import { describe, expect, it } from "vitest";

import {
  activitySpan,
  clearDecisionFilters,
  type DecisionSummary,
  decisionQuery,
  featureCounts,
  featureHeadings,
  featureQuery,
  type FeatureSummary,
  hasDecisionFilters,
  hasFeatureFilters,
  locateDecision,
  localDayStart,
  NO_FILTERS,
  parseDecisionFilters,
  parseFeatureStatuses,
  personLabel,
  whoPresentation,
  withDecisionFilters,
  withFeatureStatuses,
} from "./decision-log";

function decision(overrides: Partial<DecisionSummary> = {}): DecisionSummary {
  return {
    id: "d0",
    session_id: "s1",
    turn: 3,
    decided_at: "2026-10-06T14:03:11Z",
    who: "human",
    person: null,
    vendor: "claude_code",
    category: "data_model",
    summary: "Store decisions per session",
    superseded: false,
    file_count: 1,
    ...overrides,
  };
}

/** A feature last active at `last`, or never. */
function at(id: string, last: string | null): FeatureSummary {
  return { id, first_activity_at: null, last_activity_at: last } as unknown as FeatureSummary;
}

/** The start of a local day, as the headings carry it. */
function day(date: string): string {
  return new Date(`${date}T00:00:00`).toISOString();
}

const ada = { email: "ada@example.com", name: "Ada Lovelace", login: "ada", avatar_url: null };

describe("who decided", () => {
  it("makes a human decision stand out and an agent's recede", () => {
    expect(whoPresentation("human", ada)).toEqual({
      marker: { by: "human" },
      weight: "strong",
      approvedBy: null,
    });
    expect(whoPresentation("agent", ada)).toEqual({
      marker: { by: "agent" },
      weight: "quiet",
      approvedBy: null,
    });
  });

  it("names who approved an agent's proposal in words", () => {
    expect(whoPresentation("agent_proposed_human_approved", ada)).toEqual({
      marker: { by: "agent", approved_by_human: true },
      weight: "normal",
      approvedBy: "Ada Lovelace",
    });
    expect(
      whoPresentation("agent_proposed_human_approved", { ...ada, name: null }).approvedBy,
    ).toBe("ada");
    expect(
      whoPresentation("agent_proposed_human_approved", { ...ada, name: null, login: null })
        .approvedBy,
    ).toBe("ada@example.com");
    expect(whoPresentation("agent_proposed_human_approved", null).approvedBy).toBe("a person");
  });

  it("names an unknown person by their address", () => {
    expect(personLabel({ email: "x@y.z", name: null, login: null, avatar_url: null })).toBe(
      "x@y.z",
    );
  });
});

describe("a feature row", () => {
  it("spans its activity, one day when it began and ended on it", () => {
    expect(activitySpan(null, null)).toBeNull();
    expect(activitySpan("2026-10-06T09:00:00", "2026-10-06T17:00:00")).toBe("Oct 6, 2026");
    expect(activitySpan("2026-10-01T09:00:00", "2026-10-06T17:00:00")).toBe("Oct 1 – Oct 6, 2026");
    expect(activitySpan("2025-12-30T09:00:00", "2026-01-02T17:00:00")).toBe(
      "Dec 30, 2025 – Jan 2, 2026",
    );
  });

  it("counts pull requests, sessions and decisions, zero included", () => {
    expect(featureCounts({ pull_request_count: 1, session_count: 0, decision_count: 12 })).toBe(
      "1 PR · 0 sessions · 12 decisions",
    );
  });

  it("finds an opened decision under its pull request or session", () => {
    const detail = {
      pull_requests: [
        {
          number: 7,
          decisions: [decision({ id: "d0" }), decision({ id: "d1" })],
        },
      ],
      sessions: [{ id: "s2", decisions: [decision({ id: "s2-d0", session_id: "s2", turn: 1 })] }],
    } as unknown as Parameters<typeof locateDecision>[0];
    expect(locateDecision(detail, "d1")).toEqual({
      group: "pull:7",
      decision: detail.pull_requests[0]!.decisions[1],
    });
    expect(locateDecision(detail, "s2-d0")?.group).toBe("session:s2");
    expect(locateDecision(detail, "d9")).toBeNull();
  });

  it("starts a month or day heading only where the last feature with activity differs", () => {
    const headings = featureHeadings([
      at("a", "2026-10-28T10:00:00"),
      at("b", "2026-10-28T09:00:00"),
      at("idle", null),
      at("c", "2026-10-21T10:00:00"),
      at("d", "2026-09-30T10:00:00"),
    ]);
    expect(headings).toEqual([
      { month: day("2026-10-01"), day: day("2026-10-28") },
      { month: null, day: null },
      { month: null, day: null },
      { month: null, day: day("2026-10-21") },
      { month: day("2026-09-01"), day: day("2026-09-30") },
    ]);
  });
});

describe("decision filters in the address", () => {
  it("reads every filter", () => {
    const filters = parseDecisionFilters(
      new URLSearchParams(
        "q=+json+&file=platform/&person=ada@example.com&who=agent&who=human&category=testing&category=architecture&from=2026-10-01&to=2026-10-06&superseded=1&agent=hide&open=6f1c0d2e-5a4b-4c3d-8e2f-000000000003",
      ),
    );
    expect(filters).toEqual({
      q: "json",
      file: "platform/",
      person: "ada@example.com",
      // Canonical order, whatever order the address had, and without the
      // agent-only decisions the address also hides.
      who: ["human"],
      categories: ["architecture", "testing"],
      from: "2026-10-01",
      to: "2026-10-06",
      superseded: true,
      hideAgent: true,
    });
  });

  it("defaults with nothing set", () => {
    expect(parseDecisionFilters(new URLSearchParams())).toEqual(NO_FILTERS);
  });

  it("drops stale and invalid values rather than sending them on", () => {
    const filters = parseDecisionFilters(
      new URLSearchParams(
        "who=robot&who=human&who=human&category=vibes&from=2026-02-30&to=yesterday&superseded=yes&agent=show",
      ),
    );
    expect(filters.who).toEqual(["human"]);
    expect(filters.categories).toEqual([]);
    expect(filters.from).toBeNull();
    expect(filters.to).toBeNull();
    expect(filters.superseded).toBe(false);
    expect(filters.hideAgent).toBe(false);
  });

  it("writes filters, keeping the open decision and leaving defaults off", () => {
    const next = withDecisionFilters(
      new URLSearchParams("open=6f1c0d2e-5a4b-4c3d-8e2f-000000000003&q=old"),
      {
        q: "",
        who: ["agent", "human"],
        categories: ["process"],
        from: "2026-10-01",
        superseded: true,
      },
    );
    expect(next.toString()).toBe(
      "open=6f1c0d2e-5a4b-4c3d-8e2f-000000000003&who=human&who=agent&category=process&from=2026-10-01&superseded=1",
    );
    expect(withDecisionFilters(new URLSearchParams(), { hideAgent: true }).toString()).toBe(
      "agent=hide",
    );
  });

  it("round-trips through the address", () => {
    const filters = {
      ...NO_FILTERS,
      q: "cursor",
      file: "web/app/",
      person: "ada@example.com",
      who: ["agent_proposed_human_approved" as const],
      categories: ["interface" as const],
      from: "2026-09-01",
      to: "2026-09-30",
      superseded: true,
      hideAgent: true,
    };
    expect(parseDecisionFilters(withDecisionFilters(new URLSearchParams(), filters))).toEqual(
      filters,
    );
  });

  it("clears every filter but keeps the open decision", () => {
    const cleared = clearDecisionFilters(
      new URLSearchParams(
        "q=x&file=a&who=human&agent=hide&superseded=1&open=6f1c0d2e-5a4b-4c3d-8e2f-000000000003",
      ),
    );
    expect(cleared.toString()).toBe("open=6f1c0d2e-5a4b-4c3d-8e2f-000000000003");
    expect(hasDecisionFilters(parseDecisionFilters(cleared))).toBe(false);
    expect(hasDecisionFilters({ ...NO_FILTERS, superseded: true })).toBe(true);
    expect(hasDecisionFilters({ ...NO_FILTERS, file: "app/" })).toBe(true);
  });
});

describe("the API query", () => {
  it("sends local day bounds, the end exclusive at the next day's start", () => {
    const query = decisionQuery({ ...NO_FILTERS, from: "2026-10-01", to: "2026-10-06" });
    const from = query.get("from")!;
    const to = query.get("to")!;
    expect(from).toMatch(/^2026-10-01T00:00:00[+-]\d{2}:\d{2}$/);
    expect(to).toMatch(/^2026-10-07T00:00:00[+-]\d{2}:\d{2}$/);
    expect(Date.parse(from)).toBe(new Date(2026, 9, 1).getTime());
    expect(Date.parse(to)).toBe(new Date(2026, 9, 7).getTime());
  });

  it("crosses month and year ends", () => {
    expect(localDayStart("2026-12-31", 1)).toMatch(/^2027-01-01T00:00:00/);
    expect(Date.parse(localDayStart("2026-02-28", 1))).toBe(new Date(2026, 2, 1).getTime());
  });

  it("maps every filter onto the API's parameters", () => {
    const query = decisionQuery(
      {
        ...NO_FILTERS,
        q: "json column",
        file: "platform/",
        person: "ada@example.com",
        who: ["human"],
        categories: ["data_model", "interface"],
        superseded: true,
      },
      "abc",
    );
    expect(query.toString()).toBe(
      "q=json+column&file=platform%2F&person=ada%40example.com&who=human&category=data_model&category=interface&superseded=true&cursor=abc",
    );
    expect(decisionQuery(NO_FILTERS).toString()).toBe("");
  });

  it("hides agent-only decisions on the server", () => {
    expect(decisionQuery({ ...NO_FILTERS, hideAgent: true }).getAll("who")).toEqual([
      "human",
      "agent_proposed_human_approved",
    ]);
    expect(
      decisionQuery({ ...NO_FILTERS, hideAgent: true, who: ["agent", "human"] }).getAll("who"),
    ).toEqual(["human"]);
    // Hiding wins over asking for agent-only decisions.
    expect(decisionQuery({ ...NO_FILTERS, hideAgent: true, who: ["agent"] }).getAll("who")).toEqual(
      ["human", "agent_proposed_human_approved"],
    );
  });

  it("never holds both the Agent filter and hiding agent-only decisions", () => {
    const both = parseDecisionFilters(new URLSearchParams("who=agent&who=human&agent=hide"));
    expect(both.who).toEqual(["human"]);
    expect(both.hideAgent).toBe(true);
    expect(parseDecisionFilters(new URLSearchParams("agent=hide&who=agent")).who).toEqual([]);
    // Turning hiding on drops the Agent filter from the address.
    expect(
      withDecisionFilters(new URLSearchParams("who=agent&who=human"), {
        hideAgent: true,
      }).toString(),
    ).toBe("who=human&agent=hide");
  });
});

describe("the feature filters", () => {
  it("reads known statuses once, in order, and writes them back", () => {
    const search = new URLSearchParams(
      "status=shipped&status=bogus&status=in_progress&status=shipped",
    );
    expect(parseFeatureStatuses(search)).toEqual(["in_progress", "shipped"]);
    expect(withFeatureStatuses(new URLSearchParams("q=x"), ["abandoned"]).toString()).toBe(
      "q=x&status=abandoned",
    );
    expect(withFeatureStatuses(search, []).toString()).toBe("");
  });

  it("clears on All decisions every decision filter but keeps the status", () => {
    expect(
      clearDecisionFilters(
        new URLSearchParams("tab=all&q=json&who=human&status=shipped&feature=f1"),
      ).toString(),
    ).toBe("tab=all&status=shipped&feature=f1");
  });

  it("clears on Features only the filters shown there", () => {
    expect(
      clearDecisionFilters(
        new URLSearchParams(
          "q=json&file=a&person=p&from=2026-10-01&who=human&category=interface&agent=hide&superseded=1&status=shipped&feature=f1",
        ),
        true,
      ).toString(),
    ).toBe("feature=f1&who=human&category=interface&superseded=1&agent=hide");
  });

  it("sends the feature list the filters that pick features and the statuses", () => {
    const query = featureQuery(
      {
        ...NO_FILTERS,
        q: "json",
        file: "web/app",
        person: "ada@example.com",
        from: "2026-10-01",
        to: "2026-10-02",
        who: ["human"],
        categories: ["testing"],
        superseded: true,
        hideAgent: true,
      },
      ["shipped", "abandoned"],
      "c1",
    );
    expect([...query.keys()]).toEqual([
      "q",
      "file",
      "person",
      "from",
      "to",
      "status",
      "status",
      "cursor",
    ]);
    expect(query.get("to")).toBe(localDayStart("2026-10-02", 1));
    expect(featureQuery({ ...NO_FILTERS, who: ["agent"], hideAgent: true }, []).toString()).toBe(
      "",
    );
  });

  it("tells a filtered feature list by a search, a file, an author or dates", () => {
    expect(hasFeatureFilters(NO_FILTERS)).toBe(false);
    expect(hasFeatureFilters({ ...NO_FILTERS, who: ["human"], superseded: true })).toBe(false);
    expect(hasFeatureFilters({ ...NO_FILTERS, q: "json" })).toBe(true);
    expect(hasFeatureFilters({ ...NO_FILTERS, file: "ui" })).toBe(true);
    expect(hasFeatureFilters({ ...NO_FILTERS, person: "ada@example.com" })).toBe(true);
    expect(hasFeatureFilters({ ...NO_FILTERS, to: "2026-10-02" })).toBe(true);
  });
});
