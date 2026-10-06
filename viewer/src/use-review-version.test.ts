import { describe, expect, it } from "vitest";

import { readHistorySelection, withHistorySelection } from "./use-review-version";

describe("review version in the URL", () => {
  it("reads a version, compared or alone, and ignores anything else", () => {
    expect(readHistorySelection(new URLSearchParams("review=3"))).toEqual({
      version: 3,
      compare: false,
    });
    expect(readHistorySelection(new URLSearchParams("review=3&compare=1"))).toEqual({
      version: 3,
      compare: true,
    });
    for (const query of ["", "review=", "review=0", "review=-1", "review=1.5", "review=v2"])
      expect(readHistorySelection(new URLSearchParams(query))).toBeNull();
  });

  it("writes the version beside the pane and clears it for the latest review", () => {
    const search = new URLSearchParams("pane=changes&review=1&compare=1");
    expect(withHistorySelection(search, { version: 2, compare: false }).toString()).toBe(
      "pane=changes&review=2",
    );
    expect(withHistorySelection(search, null).toString()).toBe("pane=changes");
  });
});
