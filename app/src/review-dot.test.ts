import { expect, it } from "vitest";
import { groupDotState, reviewDotState } from "./review-dot";

it("maps a pull request's review status and queued job to its dot", () => {
  expect(reviewDotState("current")).toBe("ready");
  expect(reviewDotState("outdated")).toBe("outdated");
  expect(reviewDotState("failed")).toBe("failed");
  expect(reviewDotState("generating")).toBe("generating");
  expect(reviewDotState("no_review")).toBeNull();
  expect(reviewDotState("unknown")).toBeNull();
  // A job waiting in the queue, over whatever the saved review says.
  expect(reviewDotState("no_review", true)).toBe("queued");
  expect(reviewDotState("failed", true)).toBe("queued");
  expect(reviewDotState("generating", true)).toBe("generating");
});

it("maps a group's review to its dot, and gives a stack none", () => {
  expect(groupDotState({ version_count: 1 }, false)).toBe("ready");
  expect(groupDotState({ version_count: 1, outdated: true }, false)).toBe("outdated");
  expect(groupDotState({ version_count: 0 }, false)).toBeNull();
  expect(groupDotState({ version_count: 0 }, true)).toBe("generating");
  expect(groupDotState({ stack: true, version_count: 1 }, true)).toBeNull();
});
