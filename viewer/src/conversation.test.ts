import { describe, expect, it } from "vitest";

import { dayBreaks, promptSegments } from "./conversation";

describe("promptSegments", () => {
  it("keeps typed text as it is and lifts out only the code fences", () => {
    const prompt = [
      "Fix the import in pkg/__init__.py and lint src/**/*.ts.",
      "# not a heading",
      "",
      "```ts",
      "const a = 1;",
      "```",
      "  indented line",
    ].join("\n");
    expect(promptSegments(prompt)).toEqual([
      {
        kind: "text",
        value: "Fix the import in pkg/__init__.py and lint src/**/*.ts.\n# not a heading",
      },
      { kind: "code", value: "```ts\nconst a = 1;\n```" },
      { kind: "text", value: "  indented line" },
    ]);
  });

  it("reads backticks closed on their own line as inline code, not a fence", () => {
    expect(promptSegments("```npm i``` then fix pkg/__init__.py\n# done")).toEqual([
      { kind: "text", value: "```npm i``` then fix pkg/__init__.py\n# done" },
    ]);
  });

  it("treats Windows line endings as line breaks", () => {
    expect(promptSegments("Here:\r\n\r\n```\r\nx\r\n```\r\n")).toEqual([
      { kind: "text", value: "Here:" },
      { kind: "code", value: "```\nx\n```" },
    ]);
  });

  it("runs a fence the preview cut off to the end", () => {
    expect(promptSegments("Here:\n```py\nprint(1)")).toEqual([
      { kind: "text", value: "Here:" },
      { kind: "code", value: "```py\nprint(1)" },
    ]);
  });

  it("closes a fence only with a matching or longer run of its own marker", () => {
    expect(promptSegments("````\n```\nstill code\n````\nafter")).toEqual([
      { kind: "code", value: "````\n```\nstill code\n````" },
      { kind: "text", value: "after" },
    ]);
  });
});

describe("dayBreaks", () => {
  it("marks the first turn of each day and skips undated turns", () => {
    const breaks = dayBreaks([
      "2026-07-28T10:00:00",
      "2026-07-28T15:00:00",
      null,
      "2026-07-29T09:00:00",
    ]);
    expect(breaks.map((label) => label !== null)).toEqual([true, false, false, true]);
  });
});
