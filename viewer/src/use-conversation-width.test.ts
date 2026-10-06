import { describe, expect, it } from "vitest";

import {
  clampPaneWidth,
  clampWidth,
  CONVERSATION_MAX,
  CONVERSATION_MIN,
  RAIL_LIMITS,
} from "./use-conversation-width";

describe("clampPaneWidth", () => {
  it("keeps a width inside the pane's own range", () => {
    expect(clampPaneWidth(500)).toBe(500);
    expect(clampPaneWidth(10)).toBe(CONVERSATION_MIN);
    expect(clampPaneWidth(5000)).toBe(CONVERSATION_MAX);
  });

  it("rounds a fractional pointer position", () => {
    expect(clampPaneWidth(420.6)).toBe(421);
  });

  it("lowers the ceiling so the review pane keeps its minimum", () => {
    // 1000 shared, 520 reserved for the review pane, so 480 is the most the
    // conversation can take however far the reader drags.
    expect(clampPaneWidth(900, 1000)).toBe(480);
  });

  it("never squeezes the conversation below its minimum, even on a narrow row", () => {
    expect(clampPaneWidth(600, 600)).toBe(CONVERSATION_MIN);
  });
});

describe("clampWidth for the section rail", () => {
  it("starts at the rail's old fixed width and keeps to its own range", () => {
    expect(RAIL_LIMITS.fallback).toBe(264);
    expect(clampWidth(10, RAIL_LIMITS)).toBe(RAIL_LIMITS.min);
    expect(clampWidth(5000, RAIL_LIMITS)).toBe(RAIL_LIMITS.max);
  });

  it("leaves the review pane its minimum", () => {
    expect(clampWidth(470, RAIL_LIMITS, 900)).toBe(380);
  });
});
