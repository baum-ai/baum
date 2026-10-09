// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import { codePoints } from "./format";
import { PromptBlock } from "./prompt-block";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

let root: Root | null = null;
let container: HTMLElement;

function render(element: React.ReactElement) {
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() => root!.render(element));
}

beforeAll(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.replaceChildren();
});

describe("PromptBlock", () => {
  it("captions a prompt and gives a short one no control", () => {
    render(<PromptBlock text="Fix the parser" label="Jul 1" />);
    const text = container.textContent ?? "";
    expect(text).toContain("Prompt");
    expect(text).toContain("Jul 1");
    expect(text).toContain("14 characters");
    expect(container.querySelector("button")).toBeNull();
  });

  it("counts characters as code points, as the hosts do", () => {
    expect(codePoints("a😀é")).toBe(3);
    expect(codePoints("\ud83d")).toBe(1);
    render(<PromptBlock text={"😀".repeat(300)} fullLength={1000} truncated />);
    expect(container.textContent).toContain("Showing the first 300 of 1K characters.");
  });

  it("captions an agent message", () => {
    render(<PromptBlock text="Done." tone="agent" />);
    expect(container.textContent).toContain("Agent");
  });

  it("expands a long one and asks for the rest on each expand, not on collapse", () => {
    const onExpand = vi.fn();
    render(
      <PromptBlock text={"line\n".repeat(10)} fullLength={9000} truncated onExpand={onExpand} />,
    );
    expect(container.textContent).toContain("9K characters");
    expect(container.textContent).toContain("Showing the first 50 of 9K characters.");
    const toggle = container.querySelector("button")!;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    act(() => toggle.click());
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(toggle.textContent).toBe("Show less");
    act(() => toggle.click());
    act(() => toggle.click());
    expect(onExpand).toHaveBeenCalledTimes(2);
  });
});
