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
    // Days and times wait for the reader's zone, which the server lacks.
    expect(html).not.toContain("dateTime");
  });

  it("names each session when the prompts come from several", () => {
    const codex = { id: "s1", label: "Codex" };
    const claude = { id: "s2", label: "Claude Code" };
    const html = renderToStaticMarkup(
      <QuotedPrompts
        prompts={[
          { id: "a", index: 1, text: "One", session: codex },
          { id: "b", index: 2, text: "Two", session: codex },
          { id: "c", index: 1, text: "Three", session: claude },
        ]}
      />,
    );
    expect(html.match(/>Codex</g)).toHaveLength(1);
    expect(html.match(/>Claude Code</g)).toHaveLength(1);
    const single = renderToStaticMarkup(
      <QuotedPrompts prompts={[{ id: "a", index: 1, text: "One", session: codex }]} />,
    );
    expect(single).not.toContain(">Codex<");
  });

  it("names the author a host knows and numbers prompts as the transcript does", () => {
    const html = renderToStaticMarkup(
      <QuotedPrompts
        prompts={[{ id: "t3", index: 3, text: "Ship it", at: null }]}
        author="Ada Lovelace"
        avatar={<img alt="" />}
      />,
    );
    expect(html).toContain("Ada Lovelace");
    expect(html).toContain("<img");
    expect(html).toContain("#3");
  });

  it("says when the document does not carry a prompt's text", () => {
    const html = renderToStaticMarkup(
      <QuotedPrompts prompts={[{ id: "p2", index: 2, text: null }]} />,
    );
    expect(html).toContain("#2: the document does not carry its text.");
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
