// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToString } from "react-dom/server";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import { Markdown } from "./components/markdown";
import { highlightCode } from "./highlight";

vi.mock("./highlight", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./highlight")>();
  return { ...actual, highlightCode: vi.fn(actual.highlightCode) };
});

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

const roots: Root[] = [];

beforeAll(() => {
  vi.mocked(highlightCode).mockClear();
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  vi.mocked(highlightCode).mockClear();
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
    const el = mount(
      "```ts\nconst c = 3;\n```\n\n```nonsense\nconst a = 1;\n```\n\n```\nconst b = 2;\n```",
    );
    const [known, unknown, bare] = [...el.querySelectorAll("pre")];
    await vi.waitFor(() =>
      expect(known!.querySelectorAll("span[style]").length).toBeGreaterThan(0),
    );
    expect(unknown!.querySelectorAll("span")).toHaveLength(0);
    expect(bare!.querySelectorAll("span")).toHaveLength(0);
    expect(vi.mocked(highlightCode)).toHaveBeenCalledTimes(1);
  });

  it("never highlights an over-limit block", () => {
    vi.mocked(highlightCode).mockClear();
    const code = "a\n".repeat(3001);
    const el = mount("```ts\n" + code + "```");
    expect(vi.mocked(highlightCode)).not.toHaveBeenCalled();
    expect(el.querySelector("pre code")!.textContent).toBe(code);
  });

  it("keeps raw HTML elements inside a block, not as a string", async () => {
    const el = mount('<pre><code class="language-ts">line1\n<b>x</b>\nline3</code></pre>');
    await new Promise((resolve) => setTimeout(resolve, 50));
    const block = el.querySelector("pre code")!;
    expect(block.querySelector("b")!.textContent).toBe("x");
    expect(block.textContent).toBe("line1\nx\nline3");
    expect(el.textContent).not.toContain("[object Object]");
  });

  it("stays plain when the tokens do not add up to the source", async () => {
    const code = "const a = 1;\r\nconst b = 2;\r\n";
    const el = mount("```ts\n" + code + "```");
    const before = el.querySelector("pre code")!.textContent;
    expect(before).toContain("\r\n");
    await vi.waitFor(() => expect(vi.mocked(highlightCode)).toHaveBeenCalled());
    await vi.mocked(highlightCode).mock.results.at(-1)!.value;
    const block = el.querySelector("pre code")!;
    expect(block.querySelectorAll("span")).toHaveLength(0);
    expect(block.textContent).toBe(before);
  });

  it("cannot inject attributes through the info string", async () => {
    const el = mount('```ts" style="color:red" onclick="alert(1)\nconst a = 1;\n```');
    await new Promise((resolve) => setTimeout(resolve, 50));
    const block = el.querySelector("pre code")!;
    expect(block.hasAttribute("style")).toBe(false);
    expect(block.hasAttribute("onclick")).toBe(false);
    expect(block.querySelectorAll("[onclick]")).toHaveLength(0);
    expect(block.textContent).toBe("const a = 1;\n");
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
