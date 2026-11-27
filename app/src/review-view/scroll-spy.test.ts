import { describe, expect, it } from "vitest";
import { landedWrite, partScrollTop, readingPart } from "./scroll-spy";

/** Parts stacked from `start`, each as tall as its height. */
const stack = (start: number, ...heights: number[]) => {
  let top = start;
  return heights.map((height, at) => {
    const part = { id: `p${at}`, top, bottom: top + height };
    top += height;
    return part;
  });
};

const view = { height: 900, atEnd: false };

describe("the part being read", () => {
  it("is the one at the top, or the long one still filling the page", () => {
    // p1 has just reached the top.
    expect(readingPart(stack(-500, 500, 800, 800), view)).toBe("p1");
    // Far into a long part, the next one's top still below the upper third.
    expect(readingPart(stack(-3000, 4000, 800), view)).toBe("p0");
    // Once the next one's top is in the upper third it is the one read.
    expect(readingPart(stack(-3000, 3250, 800), view)).toBe("p1");
  });

  it("goes to the nearer top between two short parts", () => {
    // A short part taken to the top stays the one read, though the next
    // starts a little below it.
    expect(readingPart(stack(0, 150, 800), view)).toBe("p0");
    expect(readingPart(stack(-60, 150, 800), view)).toBe("p0");
    expect(readingPart(stack(-90, 150, 800), view)).toBe("p1");
  });

  it("is the first before any part reaches the top, and the last at the end of the page", () => {
    expect(readingPart(stack(600, 500, 500), view)).toBe("p0");
    expect(readingPart(stack(-400, 500, 200, 100), { height: 900, atEnd: true })).toBe("p2");
    expect(readingPart([], view)).toBeNull();
  });

  it("holds a part the reader was taken to until they scroll themselves", () => {
    // The second to last part can never reach the top of a page at its end.
    const parts = stack(-400, 500, 200, 100);
    expect(readingPart(parts, { height: 900, atEnd: true }, "p1")).toBe("p1");
    // A part no longer on the page holds nothing.
    expect(readingPart(parts, view, "gone")).toBe("p1");
  });
});

describe("the address the spy wrote", () => {
  it("is its own while it is pending, spending every write up to it", () => {
    expect(landedWrite(["a", "b", "c"], "b")).toEqual({ own: true, pending: ["c"] });
    // Anything else is the reader's: a place to scroll to.
    expect(landedWrite(["a"], "z")).toEqual({ own: false, pending: [] });
    expect(landedWrite([], "a")).toEqual({ own: false, pending: [] });
  });
});

describe("where a part lands", () => {
  it("puts its top on the line under the bars, within the page", () => {
    expect(partScrollTop(1000, 400, 56, 5000)).toBe(1344);
    expect(partScrollTop(1000, -2000, 56, 5000)).toBe(0);
    // A part near the end goes as far as the page scrolls.
    expect(partScrollTop(1000, 4800, 56, 5000)).toBe(5000);
  });
});
