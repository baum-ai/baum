import { describe, expect, it } from "vitest";

import { harnessName } from "./agent-names";

describe("harnessName", () => {
  it("names known harnesses and raises the first letter of the rest", () => {
    expect(harnessName("claude_code")).toBe("Claude Code");
    expect(harnessName("pi")).toBe("Pi");
    expect(harnessName("some_agent")).toBe("Some agent");
    expect(harnessName("")).toBe("Unknown");
    expect(harnessName("constructor")).toBe("Constructor");
    expect(harnessName("toString")).toBe("ToString");
  });
});
