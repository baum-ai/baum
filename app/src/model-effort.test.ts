import { expect, it } from "vitest";
import { modelEfforts, supportedEffort } from "./model-effort";

it.each(["claude-haiku-5-5", "claude-sonnet-5-5", "claude-opus-5-5"])(
  "offers the five Claude Code effort levels for %s",
  (model) => {
    expect(modelEfforts("claude", model)).toEqual(["low", "medium", "high", "xhigh", "max"]);
  },
);

it("offers Ultra only on the Codex models that advertise delegation", () => {
  expect(modelEfforts("codex", "gpt-6-luna")).toEqual(["low", "medium", "high", "xhigh", "max"]);
  for (const model of ["gpt-6.1-sol", "gpt-6.1-terra", "gpt-6-astra"])
    expect(modelEfforts("codex", model)).toEqual([
      "low",
      "medium",
      "high",
      "xhigh",
      "max",
      "ultra",
    ]);
});

it("offers the standard levels for unknown and custom models", () => {
  expect(modelEfforts("claude", "custom-model")).toEqual(["low", "medium", "high", "xhigh", "max"]);
  expect(modelEfforts("codex", "gpt-6-sol")).toEqual(["low", "medium", "high", "xhigh", "max"]);
  expect(modelEfforts("claude", "gpt-6-astra")).toEqual(["low", "medium", "high", "xhigh", "max"]);
});

it("leaves an empty model and retired agents at the agent's default", () => {
  expect(modelEfforts("codex", "")).toEqual([]);
  expect(modelEfforts("cursor-agent", "custom-model")).toEqual([]);
  expect(supportedEffort("codex", "gpt-6-luna", "ultra")).toBe("");
  expect(supportedEffort("codex", "gpt-6.1-sol", "ultra")).toBe("ultra");
  expect(supportedEffort("claude", "claude-sonnet-5-5", "xhigh")).toBe("xhigh");
});
