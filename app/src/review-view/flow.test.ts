import { describe, expect, it } from "vitest";
import { flowFit } from "./native-tiles";

// The rem each column count first lays out across and with its labels. Before
// FlowDiagram measured its width, flow.css had a rule for each of 28, 32, 36,
// 40, 44, 48, 52, 56, 64 and 72rem and a flow asked for the first one that was
// at least 8.5 a column (across) or 9 a column and 6 for the labels. The rule
// sizes are not kept: a flow now switches at exactly what it asks for.
const ASKS: [columns: number, across: number, labels: number | null][] = [
  [1, 28, 28],
  [2, 28, 28],
  [3, 28, 33],
  [4, 34, 42],
  [5, 42.5, 51],
  [6, 51, 60],
  [7, 59.5, 69],
  [8, 68, null],
  [9, 72, null],
  [14, 72, null],
];

describe("flowFit", () => {
  it.each(ASKS)("%i columns switch at the width they ask for", (columns, across, labels) => {
    const at = (rem: number) => flowFit(columns, true, rem);
    expect(at(across - 0.01).across).toBe(false);
    expect(at(across).across).toBe(true);
    if (labels === null) {
      expect(at(200).labels).toBe(false);
      return;
    }
    expect(at(labels - 0.01).labels).toBe(false);
    expect(at(labels).labels).toBe(true);
    expect(at(labels).across).toBe(true);
  });

  it("stays a list under 28rem and without lane labels has no label column", () => {
    expect(flowFit(1, true, 27.9)).toEqual({ across: false, labels: false });
    expect(flowFit(2, false, 100)).toEqual({ across: true, labels: false });
  });
});
