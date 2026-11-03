import { act, useState } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { FilterBar, FilterButton } from "./filter-menu";
import type { Entry } from "./api";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

it("opens with slash and focuses Add filter after positioning", async () => {
  const node = document.createElement("div");
  document.body.append(node);
  const root = createRoot(node);
  await act(async () => root.render(<FilterButton query="" onChange={vi.fn()} entries={[]} />));
  await act(async () => {
    document.body.dispatchEvent(
      new KeyboardEvent("keydown", { key: "/", bubbles: true, cancelable: true }),
    );
  });
  expect(document.activeElement?.getAttribute("aria-label")).toBe("Add filter");
  await act(async () => {
    document.activeElement?.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    );
  });
  expect(node.querySelector(".filter-menu-panel")).toBeNull();
  expect(document.activeElement).not.toBe(node.querySelector("summary"));
  await act(async () => root.unmount());
  node.remove();
});

it("counts each organisation's matches rather than the combined selection", async () => {
  const node = document.createElement("div");
  document.body.append(node);
  const root = createRoot(node);
  const entries = ["acme/a", "acme/b", "other/c"].map(
    (repository) => ({ pull_request: { repository } }) as Entry,
  );
  await act(async () =>
    root.render(<FilterButton query="org=acme" onChange={vi.fn()} entries={entries} />),
  );
  await act(async () => node.querySelector<HTMLElement>("summary")!.click());
  const org = Array.from(node.querySelectorAll<HTMLElement>("[data-filter-row]")).find((row) =>
    row.textContent?.includes("org"),
  )!;
  expect(document.activeElement?.getAttribute("aria-label")).toBe("Add filter");
  await act(async () => org.click());
  const options = Array.from(
    node.querySelectorAll(
      '[aria-label="Organisation"] [role="option"], [aria-label="Organization"] [role="option"]',
    ),
  );
  expect(options.map((row) => row.querySelector(".filter-menu-count")?.textContent)).toEqual([
    "2",
    "1",
  ]);
  await act(async () => root.unmount());
  node.remove();
});

it("hides the empty query box", async () => {
  const node = document.createElement("div");
  const root = createRoot(node);
  await act(async () => root.render(<FilterBar query="" onChange={vi.fn()} entries={[]} />));
  expect(node.querySelector("textarea")).toBeNull();
  await act(async () => root.unmount());
});

for (const modifier of ["metaKey", "ctrlKey"]) {
  it(`clears an open filter with ${modifier}+C and keeps Add filter focused`, async () => {
    const node = document.createElement("div");
    document.body.append(node);
    const root = createRoot(node);
    function Filters() {
      const [query, setQuery] = useState("org=acme");
      return (
        <FilterButton
          query={query}
          onChange={setQuery}
          entries={[]}
          footer={<FilterBar query={query} onChange={setQuery} entries={[]} />}
        />
      );
    }
    await act(async () => root.render(<Filters />));
    await act(async () => node.querySelector<HTMLElement>("summary")!.click());
    await act(async () => {
      const box = node.querySelector("textarea")!;
      box.focus();
      box.dispatchEvent(
        new KeyboardEvent("keydown", {
          key: "c",
          [modifier]: true,
          bubbles: true,
          cancelable: true,
        }),
      );
    });
    expect(node.querySelector("textarea")).toBeNull();
    expect(node.querySelector("details")?.open).toBe(true);
    expect(document.activeElement?.getAttribute("aria-label")).toBe("Add filter");
    await act(async () => root.unmount());
    node.remove();
  });
}
