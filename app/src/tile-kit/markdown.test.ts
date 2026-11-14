import { afterAll, describe, expect, it } from "vitest";
import { frame } from "./harness";

const f = frame();
afterAll(() => f.remove());

function md(text: string, options?: { inline?: boolean }): HTMLElement {
  return f.window.baum.md(text, options) as HTMLElement;
}

describe("baum.md", () => {
  it("shows raw HTML as text", () => {
    const text =
      "<script>alert(1)</script> and <b>bold</b>\n\n<img src=x onerror=alert(1)>\n<details><summary>x</summary></details>";
    const node = md(text);
    expect(node.querySelectorAll("script, b, img, details")).toHaveLength(0);
    expect(node.textContent).toContain("<script>alert(1)</script> and <b>bold</b>");
    expect(node.textContent).toContain("<img src=x onerror=alert(1)>");
  });

  it("draws strong, emphasis, strikethrough and code spans", () => {
    const node = md(
      "a **strong** and *em* and _em_ and ~~gone~~ and `x <b> y` and snake_case_name",
    );
    const p = node.querySelector("p")!;
    expect(p.querySelector("strong")!.textContent).toBe("strong");
    expect([...p.querySelectorAll("em")].map((em) => em.textContent)).toEqual(["em", "em"]);
    expect(p.querySelector("del")!.textContent).toBe("gone");
    expect(p.querySelector("code")!.textContent).toBe("x <b> y");
    expect(p.textContent).toContain("snake_case_name");
  });

  it("draws links as text with the URL in the title, and never navigates", () => {
    const node = md("see [the **docs**](https://example.invalid/x) or [run](javascript:alert(1))");
    expect(node.querySelectorAll("a")).toHaveLength(0);
    const [docs, run] = [...node.querySelectorAll(".baum-link")];
    expect(docs!.textContent).toBe("the docs");
    expect(docs!.getAttribute("title")).toBe("https://example.invalid/x");
    expect(docs!.querySelector("strong")).not.toBeNull();
    expect(run!.getAttribute("title")).toBe("javascript:alert(1)");
    expect(run!.getAttributeNames()).toEqual(["class", "title"]);
  });

  it("draws headings, lists, quotes, rules and fenced code", () => {
    const node = md(
      [
        "# Title",
        "### Section",
        "#### Part",
        "",
        "- one",
        "- two",
        "  - nested",
        "",
        "3. three",
        "4. four",
        "",
        "> quoted *text*",
        "",
        "---",
        "",
        "```sql",
        "<b>SELECT</b> 1;",
        "  indented",
        "```",
      ].join("\n"),
    );
    expect(node.querySelector("p.kit-strong")!.textContent).toBe("Title");
    expect(node.querySelector("h3")!.textContent).toBe("Section");
    expect(node.querySelector("h4")!.textContent).toBe("Part");
    const lists = node.querySelectorAll(":scope > ul");
    expect(lists).toHaveLength(1);
    expect(lists[0]!.querySelectorAll(":scope > li")).toHaveLength(2);
    expect(lists[0]!.querySelector("li ul li")!.textContent).toBe("nested");
    const ordered = node.querySelector("ol")!;
    expect(ordered.getAttribute("start")).toBe("3");
    expect(ordered.querySelectorAll("li")).toHaveLength(2);
    expect(node.querySelector("blockquote em")!.textContent).toBe("text");
    expect(node.querySelector("hr")).not.toBeNull();
    expect(node.querySelector("pre code")!.textContent).toBe("<b>SELECT</b> 1;\n  indented");
    expect(node.querySelector("pre b")).toBeNull();
  });

  it("keeps a paragraph's line breaks", () => {
    const node = md("first line\nsecond line\n\nnext paragraph");
    const paragraphs = node.querySelectorAll("p");
    expect(paragraphs).toHaveLength(2);
    expect(paragraphs[0]!.querySelectorAll("br")).toHaveLength(1);
  });

  it("draws one line of words inline", () => {
    const node = md("## Heading\n\n- **one**\n- two\n\n```\ncode\n```", { inline: true });
    expect(node.hasAttribute("data-inline")).toBe(true);
    expect(node.querySelectorAll("p, ul, li, pre, h3")).toHaveLength(0);
    // A code block's words stay, as the review's inline Markdown keeps them.
    expect(node.textContent).toBe("Heading one two code");
    expect(node.querySelector("strong")!.textContent).toBe("one");
  });

  it("treats unclosed marks as text", () => {
    const node = md("a * b ** c ` d [e](f g) ~~ h _i");
    expect(node.textContent).toBe("a * b ** c ` d [e](f g) ~~ h _i");
    expect(node.querySelectorAll("em, strong, code, del, .baum-link")).toHaveLength(0);
  });

  // Payloads that were quadratic: each must parse in well under a frame's
  // budget even in jsdom, which builds DOM far slower than a browser.
  const HOSTILE = [
    "[".repeat(9995) + "](" + "(".repeat(9992),
    "[[x](".repeat(3998),
    "[x](".repeat(4997),
    "[x](".repeat(4997) + ")",
    "[x](a".repeat(3998) + ")",
    "# a" + " ".repeat(19990) + "x",
    "# a" + " \t".repeat(9995) + "x",
    "*".repeat(20000),
    "*a ".repeat(6666),
    "`".repeat(20000),
    "``x`".repeat(5000),
    "> ".repeat(10000),
    "- ".repeat(10000),
    `- a${"\n".repeat(19990)}- b`,
    "**a ".repeat(5000),
    "~~".repeat(10000),
    "- - ".repeat(5000) + "x",
  ];

  it.each(HOSTILE.map((input, index) => [index, input]))(
    "parses hostile input %i quickly",
    (_, input) => {
      md(input as string);
      const start = performance.now();
      md(input as string);
      expect(performance.now() - start).toBeLessThan(150);
    },
  );

  it("parses random punctuation quickly", () => {
    let seed = 7;
    const random = () => {
      seed = (seed * 1103515245 + 12345) % 2147483648;
      return seed / 2147483648;
    };
    const alphabet = "[]()*_`~#>-+1. \t\n\\!x";
    let worst = 0;
    for (let round = 0; round < 40; round++) {
      let input = "";
      while (input.length < 20000) input += alphabet[Math.floor(random() * alphabet.length)];
      const start = performance.now();
      md(input);
      worst = Math.max(worst, performance.now() - start);
    }
    expect(worst).toBeLessThan(250);
  });

  it("accepts anything, drawing it as text", () => {
    expect(md(undefined as unknown as string).textContent).toBe("");
    expect(md(42 as unknown as string).textContent).toBe("42");
  });
});
