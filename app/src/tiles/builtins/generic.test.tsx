import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ProseText } from "./generic";

describe("a prose tile's text", () => {
  it("draws emphasis, code and lists, links as text and HTML as text", () => {
    const html = renderToStaticMarkup(
      <ProseText
        text={
          "Deploy **the reader** before `writer`.\n\n- one\n- two\n\n[here](https://example.com) <img src=x onerror=alert(1)> <b>bold</b>"
        }
      />,
    );
    expect(html).toContain("<strong>the reader</strong>");
    expect(html).toContain("<code");
    expect(html).toContain("<li>one</li>");
    expect(html).not.toContain("<a");
    expect(html).not.toContain("href");
    expect(html).not.toContain("<img");
    expect(html).not.toContain("<b>");
    expect(html).toContain("here");
  });
});
