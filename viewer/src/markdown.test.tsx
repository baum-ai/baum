import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { Markdown } from "./components/markdown";

it("renders basic formatting and treats a single newline as a line break", () => {
  const html = renderToStaticMarkup(
    <Markdown>{"**bold** and a line\nbreak, plus a list:\n\n- one\n- two"}</Markdown>,
  );
  expect(html).toContain("<strong>bold</strong>");
  expect(html).toContain("<br");
  expect(html).toContain("<li>one</li>");
  expect(html).toContain("<li>two</li>");
});

it("breaks a line once, not once for the <br> and again for its newline", () => {
  const html = renderToStaticMarkup(<Markdown>{"one\ntwo"}</Markdown>);
  // The newline after the <br> stays in the text, so a paragraph that kept
  // its whitespace would show a blank line between the two.
  expect(html).toContain("<br/>\ntwo");
  expect(html).toMatch(/<p>one<br\/>/);
  expect(html).not.toContain("pre-wrap");
});

it("hides an HTML comment instead of printing it as text", () => {
  const html = renderToStaticMarkup(
    <Markdown>{"Before.\n<!-- CURSOR_AUTOMATION_ID: abc -->\nAfter."}</Markdown>,
  );
  expect(html).toContain("Before.");
  expect(html).toContain("After.");
  expect(html).not.toContain("CURSOR_AUTOMATION_ID");
  expect(html).not.toContain("<!--");
});

it("renders safe raw HTML but strips anything that could execute", () => {
  const html = renderToStaticMarkup(
    <Markdown>
      {'<b>bold html</b> and <script>alert(1)</script> and <img src="x" onerror="alert(1)">'}
    </Markdown>,
  );
  expect(html).toContain("<b>bold html</b>");
  expect(html).not.toContain("<script");
  expect(html).not.toContain("onerror");
});

it("never renders an <img>, even for a plain markdown image", () => {
  const html = renderToStaticMarkup(
    <Markdown>{"![a screenshot](https://example.com/x.png)"}</Markdown>,
  );
  expect(html).not.toContain("<img");
  expect(html).toContain("a screenshot");
});

it("styles a language-less fenced code block as a block, not an inline span", () => {
  const html = renderToStaticMarkup(<Markdown>{"```\nplain block\n```"}</Markdown>);
  expect(html).toContain("plain block");
  expect(html).not.toContain("px-1 py-0.5");
});
