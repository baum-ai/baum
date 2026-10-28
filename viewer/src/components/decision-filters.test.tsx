// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import {
  type AuthorCount,
  type DecisionFile,
  type DecisionFilters,
  type FeatureStatus,
  NO_FILTERS,
} from "../decision-log";
import { DecisionFilterControls, type DecisionFilterControlsProps } from "./decision-filters";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

const PEOPLE: AuthorCount[] = [
  {
    email: "ada@example.com",
    name: "Ada Lovelace",
    login: "ada",
    avatar_url: null,
    decision_count: 3,
  },
];

let root: Root | null = null;
let container: HTMLElement;

function render(props: Partial<DecisionFilterControlsProps> = {}) {
  const host = {
    filters: NO_FILTERS,
    files: vi.fn(async (_prefix: string, _signal?: AbortSignal): Promise<DecisionFile[]> => []),
    people: PEOPLE,
    onChange: vi.fn<(changes: Partial<DecisionFilters>) => void>(),
    onClear: vi.fn(),
    resets: 0,
    onStatuses: vi.fn<(statuses: FeatureStatus[]) => void>(),
    ...props,
  };
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() => root!.render(<DecisionFilterControls {...host} />));
  return host;
}

function pills(): string[] {
  return [...container.querySelectorAll('[aria-label="Active filters"] button')].map(
    (each) => each.getAttribute("aria-label") ?? "",
  );
}

function button(name: string): HTMLButtonElement {
  const found = [...container.querySelectorAll("button")].find(
    (each) => each.getAttribute("aria-label") === name || each.textContent === name,
  );
  if (!found) throw new Error(`no button ${name}`);
  return found;
}

function checkbox(label: string): HTMLInputElement {
  const found = [...container.querySelectorAll("label")].find(
    (each) => each.textContent?.trim() === label,
  );
  return found!.querySelector("input")!;
}

function rowLabels(): string[] {
  return [...container.querySelectorAll("details > div > span")].map(
    (each) => each.textContent ?? "",
  );
}

/** Types into the box as a reader would, through React's own change event. */
function type(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  act(() => {
    setter.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

beforeAll(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  container.remove();
  vi.useRealTimers();
});

describe("the filter controls", () => {
  it("shows a pill per filter in force, each removing only its own", () => {
    const host = render({
      filters: {
        ...NO_FILTERS,
        q: "cursor",
        file: "platform/",
        person: "ADA@example.com",
        who: ["human", "agent"],
        categories: ["architecture"],
        from: "2026-10-01",
        superseded: true,
      },
    });
    // The keyword stays in its own box, not a pill.
    expect(pills()).toEqual([
      "Remove filter: File: platform/",
      "Remove filter: Author: Ada Lovelace",
      "Remove filter: Architecture",
      "Remove filter: Decided by human",
      "Remove filter: Decided by agent",
      "Remove filter: From 2026-10-01",
      "Remove filter: Superseded shown",
    ]);
    expect(container.querySelector("summary")?.textContent).toContain("7");

    act(() => button("Remove filter: Decided by agent").click());
    expect(host.onChange).toHaveBeenLastCalledWith({ who: ["human"] });
    // The control shows the change before the host hands it back.
    expect(pills()).not.toContain("Remove filter: Decided by agent");
    act(() => button("Remove filter: Author: Ada Lovelace").click());
    expect(host.onChange).toHaveBeenLastCalledWith({ person: "" });

    act(() => button("Clear filters").click());
    expect(host.onClear).toHaveBeenCalledTimes(1);
  });

  it("names an author it does not list by their address", () => {
    render({ filters: { ...NO_FILTERS, person: "gone@example.com" } });
    expect(pills()).toEqual(["Remove filter: Author: gone@example.com"]);
    const select = container.querySelector<HTMLSelectElement>('select[aria-label="Author"]')!;
    expect(select.value).toBe("gone@example.com");
  });

  it("never asks for agent-only decisions while hiding them", () => {
    const host = render({ filters: { ...NO_FILTERS, who: ["agent", "human"] } });
    expect(button("Agent").disabled).toBe(false);
    act(() => checkbox("Hide agent-only").click());
    expect(host.onChange).toHaveBeenLastCalledWith({ hideAgent: true, who: ["human"] });
    expect(button("Agent").disabled).toBe(true);
    expect(button("Agent").title).toBe("Agent-only decisions are hidden");
    expect(pills()).toEqual([
      "Remove filter: Decided by human",
      "Remove filter: Agent-only hidden",
    ]);
    act(() => checkbox("Hide agent-only").click());
    expect(host.onChange).toHaveBeenLastCalledWith({ hideAgent: false });
    expect(button("Agent").disabled).toBe(false);
  });

  it("on Features offers and counts only what picks features, and the status", () => {
    const host = render({
      statuses: [],
      filters: { ...NO_FILTERS, who: ["human"], categories: ["testing"], superseded: true },
    });
    expect(rowLabels()).toEqual(["File", "Author", "Status", "Dates"]);
    expect(container.querySelector('[aria-label="Decided by"]')).toBeNull();
    expect(container.querySelector<HTMLInputElement>('input[type="search"]')!.placeholder).toBe(
      "Search features",
    );
    // Decision-only filters stay in the address for All decisions, unseen here.
    expect(pills()).toEqual([]);
    expect(container.textContent).not.toContain("Clear filters");

    act(() => button("Shipped").click());
    expect(host.onStatuses).toHaveBeenLastCalledWith(["shipped"]);
    expect(pills()).toEqual(["Remove filter: Shipped"]);
    expect(container.textContent).toContain("Clear filters");
  });

  it("offers every filter on All decisions", () => {
    render();
    expect(rowLabels()).toEqual(["File", "Author", "Category", "Decided by", "Dates", "Show"]);
    expect(container.querySelector<HTMLInputElement>('input[type="search"]')!.placeholder).toBe(
      "Search decisions",
    );
  });

  it("asks for file suggestions once typing pauses, dropping the request it replaced", async () => {
    vi.useFakeTimers();
    const answers: ((files: DecisionFile[]) => void)[] = [];
    const files = vi.fn(
      (_prefix: string, _signal?: AbortSignal) =>
        new Promise<DecisionFile[]>((resolve) => {
          answers.push(resolve);
        }),
    );
    render({ files });
    const box = container.querySelector<HTMLInputElement>('input[role="combobox"]')!;

    type(box, "pl");
    act(() => vi.advanceTimersByTime(149));
    expect(files).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(1));
    expect(files).toHaveBeenCalledTimes(1);
    expect(files.mock.calls[0]![0]).toBe("pl");
    const first = files.mock.calls[0]![1]!;
    expect(first.aborted).toBe(false);

    type(box, "platform/");
    expect(first.aborted).toBe(true);
    act(() => vi.advanceTimersByTime(100));
    type(box, "platform/int");
    act(() => vi.advanceTimersByTime(149));
    expect(files).toHaveBeenCalledTimes(1);
    act(() => vi.advanceTimersByTime(1));
    expect(files).toHaveBeenCalledTimes(2);
    expect(files.mock.calls[1]![0]).toBe("platform/int");

    await act(async () => {
      answers.at(-1)!([{ path: "platform/internal/", kind: "folder", decision_count: 4 }]);
      await Promise.resolve();
    });
    const options = [...container.querySelectorAll('[role="option"]')];
    expect(options.map((each) => each.textContent)).toEqual(["platform/internal/4"]);
  });
});
