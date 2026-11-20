import { expect, it } from "vitest";
import { groupDotState, reviewDotState } from "./review-dot";

it("maps a pull request's review status to its dot", () => {
  expect(reviewDotState("current")).toBe("ready");
  expect(reviewDotState("outdated")).toBe("outdated");
  expect(reviewDotState("failed")).toBe("failed");
  expect(reviewDotState("generating")).toBe("generating");
  expect(reviewDotState("no_review")).toBeNull();
  expect(reviewDotState("unknown")).toBeNull();
});

it("maps a group's review to its dot, and gives a stack none", () => {
  expect(groupDotState({ version_count: 1 })).toBe("ready");
  expect(groupDotState({ version_count: 1, outdated: true })).toBe("outdated");
  expect(groupDotState({ version_count: 0 })).toBeNull();
  expect(groupDotState({ version_count: 0 }, "queued")).toBe("generating");
  expect(groupDotState({ version_count: 1 }, "running")).toBe("generating");
  expect(groupDotState({ version_count: 1 }, "completed")).toBe("ready");
  expect(groupDotState({ stack: true, version_count: 1 }, "running")).toBeNull();
});
