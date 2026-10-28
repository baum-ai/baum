import { describe, expect, it } from "vitest";

import {
  activitySpan,
  type DecisionSummary,
  featureCounts,
  locateDecision,
  personLabel,
  whoPresentation,
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
});
