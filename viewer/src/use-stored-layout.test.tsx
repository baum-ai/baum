// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToString } from "react-dom/server";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import { useConversationCollapsed } from "./use-conversation-collapsed";
import { useConversationTab } from "./use-conversation-tab";
import {
  CONVERSATION_LIMITS,
  RAIL_LIMITS,
  useConversationWidth,
  useRailWidth,
} from "./use-conversation-width";

let root: Root | null = null;

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

beforeEach(() => localStorage.clear());

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  vi.restoreAllMocks();
});

type Layout = {
  tab: ReturnType<typeof useConversationTab>;
  collapsed: ReturnType<typeof useConversationCollapsed>;
  split: ReturnType<typeof useConversationWidth>;
  rail: ReturnType<typeof useRailWidth>;
};

function setup() {
  const renders: Layout[] = [];
  function Probe() {
    renders.push({
      tab: useConversationTab(),
      collapsed: useConversationCollapsed(),
      split: useConversationWidth(),
      rail: useRailWidth(),
    });
    return null;
  }
  const mount = () => {
    const container = document.createElement("div");
    root = createRoot(container);
    act(() => root!.render(<Probe />));
  };
  return { renders, mount, Probe };
}

function remember() {
  localStorage.setItem("baum.review.conversation-tab", "activity");
  localStorage.setItem("baum.review.conversation-collapsed", "true");
  localStorage.setItem(CONVERSATION_LIMITS.storageKey, "600");
  localStorage.setItem(RAIL_LIMITS.storageKey, "300");
}

describe("stored layout state", () => {
  it("renders once, with the remembered values, when only the client renders", () => {
    remember();
    const { renders, mount } = setup();
    mount();
    expect(renders).toHaveLength(1);
    const [first] = renders;
    expect(first?.tab[0]).toBe("activity");
    expect(first?.collapsed[0]).toBe(true);
    expect(first?.split.width).toBe(600);
    expect(first?.rail.width).toBe(300);
  });

  it("starts from the defaults when nothing is remembered", () => {
    const { renders, mount } = setup();
    mount();
    const [first] = renders;
    expect(first?.tab[0]).toBe("chat");
    expect(first?.collapsed[0]).toBe(false);
    expect(first?.split.width).toBe(CONVERSATION_LIMITS.fallback);
    expect(first?.rail.width).toBe(RAIL_LIMITS.fallback);
  });

  it("renders the defaults on the server whatever is remembered", () => {
    remember();
    const { renders, Probe } = setup();
    renderToString(<Probe />);
    expect(renders[0]?.tab[0]).toBe("chat");
    expect(renders[0]?.collapsed[0]).toBe(false);
    expect(renders[0]?.split.width).toBe(CONVERSATION_LIMITS.fallback);
  });

  it("ignores values that are not a tab or a width", () => {
    localStorage.setItem("baum.review.conversation-tab", "nonsense");
    localStorage.setItem(CONVERSATION_LIMITS.storageKey, "wide");
    const { renders, mount } = setup();
    mount();
    expect(renders[0]?.tab[0]).toBe("chat");
    expect(renders[0]?.split.width).toBe(CONVERSATION_LIMITS.fallback);
  });

  it("remembers a choice and shows it", () => {
    const { renders, mount } = setup();
    mount();
    act(() => renders.at(-1)?.tab[1]("comments"));
    act(() => renders.at(-1)?.split.choose(500));
    expect(renders.at(-1)?.tab[0]).toBe("comments");
    expect(renders.at(-1)?.split.width).toBe(500);
    expect(localStorage.getItem("baum.review.conversation-tab")).toBe("comments");
    expect(localStorage.getItem(CONVERSATION_LIMITS.storageKey)).toBe("500");
  });

  it("fits a remembered width to the row without forgetting it", () => {
    localStorage.setItem(CONVERSATION_LIMITS.storageKey, "700");
    const { renders, mount } = setup();
    mount();
    act(() => renders.at(-1)?.split.fit(1000));
    expect(renders.at(-1)?.split.width).toBe(480);
    act(() => renders.at(-1)?.split.fit(2000));
    expect(renders.at(-1)?.split.width).toBe(700);
    expect(localStorage.getItem(CONVERSATION_LIMITS.storageKey)).toBe("700");
  });

  it("keeps working for the session when storage throws", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    const { renders, mount } = setup();
    mount();
    expect(renders[0]?.tab[0]).toBe("chat");
    act(() => renders.at(-1)?.collapsed[1](true));
    expect(renders.at(-1)?.collapsed[0]).toBe(true);
  });
});
