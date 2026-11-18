import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { ReviewDocument } from "@baum/viewer/review-document";
import { codeLanguage, ProseText } from "./generic";

describe("a code tile's language", () => {
  const review = {
    index: { hunks: [{ id: "h_go", path: "oss/internal/jobs/local.go" }] },
  } as unknown as ReviewDocument;

  it("is the language the tile names", () => {
    expect(codeLanguage({ language: "typescript", content: "", source: [] }, review)).toBe(
      "typescript",
    );
  });

  it("is the cited hunk's file's when the tile names none it knows", () => {
    const source = [{ type: "hunk" as const, id: "h_go" }];
    expect(codeLanguage({ language: "text", content: "", source }, review)).toBe("go");
    expect(codeLanguage({ language: "text", content: "", source: [] }, review)).toBeNull();
  });
});

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
