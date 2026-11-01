import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { PageRead, readState } from "./page-state";

it("reads as loading until a read succeeds, failed when the first one failed", () => {
  expect(readState({ loaded: false })).toBe("loading");
  expect(readState({ loaded: false, failed: true })).toBe("failed");
  expect(readState({ loaded: true, empty: true })).toBe("empty");
  expect(readState({ loaded: true })).toBe("ready");
  // A later failure keeps what was read.
  expect(readState({ loaded: true, failed: true, empty: false })).toBe("ready");
});

let root: Root;
let node: HTMLDivElement;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
});

it("draws each state with its own words and one retry", async () => {
  const retry = vi.fn();
  const draw = (state: Parameters<typeof PageRead>[0]["state"]) =>
    act(async () =>
      root.render(
        <PageRead
          state={state}
          what="jobs"
          message="The daemon is not answering."
          retry={retry}
          empty={<p>No jobs yet</p>}
        >
          <p>The jobs</p>
        </PageRead>,
      ),
    );
  await draw("loading");
  expect(node.textContent).toContain("Loading jobs…");
  await draw("failed");
  expect(node.textContent).toContain("Couldn't load jobs");
  expect(node.textContent).toContain("The daemon is not answering.");
  expect(node.textContent).not.toContain("Loading");
  expect(node.textContent).not.toContain("No jobs yet");
  expect(node.querySelectorAll("button")).toHaveLength(1);
  await act(async () => node.querySelector("button")!.click());
  expect(retry).toHaveBeenCalledOnce();
  await draw("empty");
  expect(node.textContent).toBe("No jobs yet");
  await draw("ready");
  expect(node.textContent).toBe("The jobs");
});
