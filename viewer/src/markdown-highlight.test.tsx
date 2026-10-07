// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToString } from "react-dom/server";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import { Markdown } from "./components/markdown";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

const roots: Root[] = [];

beforeAll(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  act(() => roots.splice(0).forEach((root) => root.unmount()));
  document.body.replaceChildren();
});

function mount(markdown: string): HTMLElement {
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  roots.push(root);
  act(() => root.render(<Markdown>{markdown}</Markdown>));
  return host;
}

const coloured = (el: HTMLElement) => el.querySelectorAll("pre code span[style*='--syntax-']");

describe("Markdown code block highlighting", () => {
  it("colours a block with a known language and keeps its text", async () => {
    const code = "const answer: number = 42;\n\nfunction f() {}\n";
    const el = mount("```ts\n" + code + "```");
    expect(el.querySelector("pre code")!.textContent).toBe(code);
    await vi.waitFor(() => expect(coloured(el).length).toBeGreaterThan(0));
    expect(el.querySelector("pre code")!.textContent).toBe(code);
    expect(el.querySelector("pre code")!.className).toBe("language-ts");
  });

  it("leaves unknown-language and language-less blocks plain", async () => {
    const el = mount("```nonsense\nconst a = 1;\n```\n\n```\nconst b = 2;\n```");
    // A known block alongside proves the highlighter has had time to run.
    const known = mount("```ts\nconst c = 3;\n```");
    await vi.waitFor(() => expect(coloured(known).length).toBeGreaterThan(0));
    expect(el.querySelectorAll("pre code")).toHaveLength(2);
    expect(coloured(el)).toHaveLength(0);
    expect(el.querySelectorAll("pre code span")).toHaveLength(0);
  });

  it("keeps inline code as a pill with no highlighting", () => {
    const el = mount("Use `const a = 1` here.");
    const code = el.querySelector("code")!;
    expect(code.className).toContain("px-1");
    expect(code.querySelector("span")).toBeNull();
    expect(el.querySelector("pre")).toBeNull();
  });

  it("renders no inline style on the server", () => {
    const html = renderToString(<Markdown>{"```ts\nconst a = 1;\n```"}</Markdown>);
    expect(html).toContain("const a = 1;");
    expect(html).not.toContain("style=");
  });

  it("renders HTML-like text in a block as text, never as elements", async () => {
    const code = "<img src=x onerror=alert(1)>\n<script>alert(2)</script>\n";
    const el = mount("```html\n" + code + "```");
    await vi.waitFor(() => expect(coloured(el).length).toBeGreaterThan(0));
    const block = el.querySelector("pre code")!;
    expect(block.textContent).toBe(code);
    expect(el.querySelector("img")).toBeNull();
    expect(el.querySelector("script")).toBeNull();
    expect(block.querySelectorAll("*:not(span)")).toHaveLength(0);
  });
});
