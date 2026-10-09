import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { PromptBubble, QuotedPrompts } from "./chat";

describe("quoted prompts", () => {
  it("shows a prompt as typed, rendering only its pasted fences as code", () => {
    const html = renderToStaticMarkup(
      <QuotedPrompts
        prompts={[
          {
            id: "p1",
            index: 1,
            at: "2026-07-29T10:00:00Z",
            text: "Fix pkg/__init__.py\n# not a heading\n\n```py\nprint(1)\n```",
          },
        ]}
      />,
    );
    expect(html).toContain('data-message="human"');
    expect(html).toContain("Fix pkg/__init__.py\n# not a heading");
    expect(html).not.toContain("<strong>");
    expect(html).not.toContain("<h1");
    expect(html).toContain("<pre");
    expect(html).toContain("#1");
    expect(html).toContain('dateTime="2026-07-29T10:00:00Z"');
  });

  it("says when the document does not carry a prompt's text", () => {
    const html = renderToStaticMarkup(
      <QuotedPrompts prompts={[{ id: "p2", index: 2, text: null }]} />,
    );
    expect(html).toContain("Prompt 2 is not carried in the document.");
    expect(html).not.toContain('data-message="human"');
  });
});

describe("prompt bubble", () => {
  it("names its author and keeps the avatar column only when given one", () => {
    const named = renderToStaticMarkup(
      <PromptBubble text="Ship it" author="Ada Lovelace" avatar={<img alt="" />} />,
    );
    expect(named).toContain("Ada Lovelace");
    expect(named).toContain("<img");
    const anonymous = renderToStaticMarkup(<PromptBubble text="Ship it" />);
    expect(anonymous).not.toContain("w-7");
  });
});
