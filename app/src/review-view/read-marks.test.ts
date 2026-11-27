import { describe, expect, it } from "vitest";
import { review1303 } from "./fixture-1303";
import { parseReadMarks, READ_MARKS_KEPT, readMarksKey, withReadMark } from "./read-marks";

describe("read marks", () => {
  it("keys a review by its repository, pull request and version", () => {
    const document = review1303();
    const { repository, pull_request, version } = document.review;
    expect(readMarksKey(document)).toBe(`${repository}#${pull_request}@${version}`);
    expect(
      readMarksKey({ ...document, review: { ...document.review, version: version + 1 } }),
    ).not.toBe(readMarksKey(document));
  });

  it("reads nothing from a value that is not a record of lists", () => {
    expect(parseReadMarks(null)).toEqual({});
    expect(parseReadMarks("not json")).toEqual({});
    expect(parseReadMarks("[1]")).toEqual({});
    expect(parseReadMarks('{"a":["sec_a",3],"b":"sec_b"}')).toEqual({ a: ["sec_a"] });
  });

  it("adds a section once, and returns the same record when it is already read", () => {
    const one = withReadMark({}, "a", "sec_a");
    expect(one).toEqual({ a: ["sec_a"] });
    expect(withReadMark(one, "a", "sec_a")).toBe(one);
    expect(withReadMark(one, "a", "sec_b")).toEqual({ a: ["sec_a", "sec_b"] });
  });

  it("keeps the reviews touched last when there are too many", () => {
    let marks = {};
    for (let at = 0; at < READ_MARKS_KEPT; at++) marks = withReadMark(marks, `r${at}`, "sec_a");
    // Reading in the oldest review again saves it from being dropped.
    marks = withReadMark(marks, "r0", "sec_b");
    marks = withReadMark(marks, "new", "sec_a");
    const keys = Object.keys(marks);
    expect(keys).toHaveLength(READ_MARKS_KEPT);
    expect(keys).toContain("r0");
    expect(keys).not.toContain("r1");
    expect(keys.at(-1)).toBe("new");
  });
});
