import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { HighlightedText } from "./highlighted-text";

describe("HighlightedText", () => {
  it("renders the bare text without tokens", () => {
    expect(renderToStaticMarkup(<HighlightedText text="a < b" />)).toBe("a &lt; b");
    expect(renderToStaticMarkup(<HighlightedText text="a" tokens={null} />)).toBe("a");
  });

  it("renders one span per token with its colour", () => {
    const html = renderToStaticMarkup(
      <HighlightedText
        text="ab"
        tokens={[{ content: "a", color: "var(--syntax-keyword)" }, { content: "b" }]}
      />,
    );
    expect(html).toBe('<span style="color:var(--syntax-keyword)">a</span><span>b</span>');
  });

  it("maps font style flags to italic, bold, underline and strikethrough", () => {
    const html = renderToStaticMarkup(
      <HighlightedText
        text="abcd"
        tokens={[
          { content: "a", fontStyle: 1 },
          { content: "b", fontStyle: 2 },
          { content: "c", fontStyle: 4 },
          { content: "d", fontStyle: 12 },
        ]}
      />,
    );
    expect(html).toContain('<span style="font-style:italic">a</span>');
    expect(html).toContain('<span style="font-weight:700">b</span>');
    expect(html).toContain('<span style="text-decoration:underline">c</span>');
    expect(html).toContain('<span style="text-decoration:underline line-through">d</span>');
  });
});
