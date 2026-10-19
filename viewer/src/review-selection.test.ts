import { describe, expect, it } from "vitest";

import { readSelection, sameSelection, withoutSelection, withSelection } from "./review-selection";

describe("readSelection", () => {
  it("reads summary from an empty or unrecognized pane", () => {
    expect(readSelection(new URLSearchParams())).toEqual({ kind: "summary" });
    expect(readSelection(new URLSearchParams("pane=nonsense"))).toEqual({ kind: "summary" });
  });

  it("reads changes and risks", () => {
    expect(readSelection(new URLSearchParams("pane=changes"))).toEqual({ kind: "changes" });
    expect(readSelection(new URLSearchParams("pane=risks"))).toEqual({ kind: "risks" });
  });

  it("reads a section", () => {
    expect(readSelection(new URLSearchParams("pane=section&section=sec_a"))).toEqual({
      kind: "section",
      id: "sec_a",
    });
  });

  it("reads a risk", () => {
    expect(
      readSelection(new URLSearchParams("pane=risk&risk_section=sec_a&risk_id=rsk_1")),
    ).toEqual({ kind: "risk", section: "sec_a", id: "rsk_1" });
  });

  it("reads the files list and one file", () => {
    expect(readSelection(new URLSearchParams("pane=files"))).toEqual({ kind: "files" });
    expect(readSelection(new URLSearchParams("pane=file&file=file_1"))).toEqual({
      kind: "file",
      id: "file_1",
    });
  });

  it("reads the sessions list and one session", () => {
    expect(readSelection(new URLSearchParams("pane=sessions"))).toEqual({ kind: "sessions" });
    expect(readSelection(new URLSearchParams("pane=session&session=run_1"))).toEqual({
      kind: "session",
      id: "run_1",
    });
    expect(readSelection(new URLSearchParams("pane=session"))).toEqual({ kind: "summary" });
  });

  it("reads the commits list and one commit", () => {
    expect(readSelection(new URLSearchParams("pane=commits"))).toEqual({ kind: "commits" });
    expect(readSelection(new URLSearchParams("pane=commit&commit=abc123"))).toEqual({
      kind: "commit",
      id: "abc123",
    });
    expect(readSelection(new URLSearchParams("pane=commit"))).toEqual({ kind: "summary" });
  });

  it("falls back to summary when a section or risk pane is missing its id", () => {
    expect(readSelection(new URLSearchParams("pane=section"))).toEqual({ kind: "summary" });
    expect(readSelection(new URLSearchParams("pane=risk&risk_section=sec_a"))).toEqual({
      kind: "summary",
    });
  });
});

describe("withSelection", () => {
  it("writes and round-trips every kind while keeping unrelated params", () => {
    const base = new URLSearchParams("repo=widgets&from=2026-07-01");
    for (const selection of [
      { kind: "summary" as const },
      { kind: "changes" as const },
      { kind: "risks" as const },
      { kind: "section" as const, id: "sec_a" },
      { kind: "section" as const, id: "sec_a", decision: "dec_choice" },
      { kind: "risk" as const, section: "sec_a", id: "rsk_1" },
      { kind: "files" as const },
      { kind: "file" as const, id: "file_1" },
      { kind: "sessions" as const },
      { kind: "session" as const, id: "run_1" },
      { kind: "commits" as const },
      { kind: "commit" as const, id: "abc123" },
    ]) {
      const written = withSelection(base, selection);
      expect(written.get("repo")).toBe("widgets");
      expect(readSelection(written)).toEqual(selection);
    }
  });

  it("replaces an existing selection rather than accumulating params", () => {
    const risk = withSelection(new URLSearchParams(), { kind: "risk", section: "a", id: "r1" });
    const section = withSelection(risk, { kind: "section", id: "sec_b" });
    expect([...section.keys()].toSorted()).toEqual(["pane", "section"]);
  });
  it("clears a decision target when navigating elsewhere", () => {
    const focused = withSelection(new URLSearchParams(), {
      kind: "section",
      id: "sec_a",
      decision: "dec_choice",
    });
    expect(withSelection(focused, { kind: "section", id: "sec_b" }).has("decision")).toBe(false);
    expect(withSelection(focused, { kind: "session", id: "run_1" }).has("decision")).toBe(false);
  });
});

describe("sameSelection", () => {
  it("compares by place, not by encoding", () => {
    expect(sameSelection({ kind: "summary" }, { kind: "summary" })).toBe(true);
    expect(sameSelection({ kind: "changes" }, { kind: "risks" })).toBe(false);
    expect(sameSelection({ kind: "section", id: "a" }, { kind: "section", id: "a" })).toBe(true);
    expect(sameSelection({ kind: "section", id: "a" }, { kind: "section", id: "b" })).toBe(false);
    expect(
      sameSelection(
        { kind: "risk", section: "s", id: "r" },
        { kind: "risk", section: "s", id: "r" },
      ),
    ).toBe(true);
    expect(
      sameSelection(
        { kind: "risk", section: "s", id: "r" },
        { kind: "risk", section: "t", id: "r" },
      ),
    ).toBe(false);
    // The same place written with its params in another order.
    const reordered = new URLSearchParams("section=a&pane=section&from=2026-07-01");
    expect(sameSelection(readSelection(reordered), { kind: "section", id: "a" })).toBe(true);
  });
});

describe("withoutSelection", () => {
  it("drops every selection param and keeps the rest", () => {
    const search = new URLSearchParams("pane=risk&risk_section=a&risk_id=r1&from=2026-07-01");
    expect([...withoutSelection(search).keys()]).toEqual(["from"]);
  });
});
