import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { useCodexHookTrust } from "./codex-hooks";

let node: HTMLDivElement;
let root: Root;
let hook: ReturnType<typeof useCodexHookTrust>;
let renders = 0;
let answer: { state: string };
const fetched = vi.fn();

function Probe() {
  hook = useCodexHookTrust();
  renders++;
  return null;
}

async function mount() {
  await act(async () => root.render(<Probe />));
}

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] });
  vi.setSystemTime(1_000_000);
  Object.defineProperty(document, "hidden", { configurable: true, get: () => false });
  renders = 0;
  fetched.mockClear();
  answer = { state: "trusted" };
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string) => {
      fetched(String(url));
      return Promise.resolve(Response.json(answer));
    }),
  );
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

it("does not poll while the answer is healthy", async () => {
  await mount();
  expect(fetched).toHaveBeenCalledTimes(1);
  await act(async () => vi.advanceTimersByTime(5 * 60_000));
  expect(fetched).toHaveBeenCalledTimes(1);
});

it("polls every minute while the answer is a warning", async () => {
  answer = { state: "untrusted" };
  await mount();
  expect(hook.warning).toContain("approval");
  await act(async () => vi.advanceTimersByTime(60_000));
  await act(async () => vi.advanceTimersByTime(60_000));
  expect(fetched).toHaveBeenCalledTimes(3);
});

it("keeps the same status and does not render again for an unchanged answer", async () => {
  answer = { state: "untrusted" };
  await mount();
  const first = hook.status;
  const before = renders;
  await act(async () => vi.advanceTimersByTime(60_000));
  expect(fetched).toHaveBeenCalledTimes(2);
  expect(hook.status).toBe(first);
  expect(renders).toBe(before);
  answer = { state: "trusted" };
  await act(async () => vi.advanceTimersByTime(60_000));
  expect(hook.status?.state).toBe("trusted");
  expect(renders).toBeGreaterThan(before);
});

it("reads on every focus while a warning shows", async () => {
  answer = { state: "untrusted" };
  await mount();
  answer = { state: "trusted" };
  await act(async () => window.dispatchEvent(new Event("focus")));
  expect(fetched).toHaveBeenCalledTimes(2);
  expect(hook.warning).toBeNull();
});

it("throttles focus reads to once a minute while healthy", async () => {
  await mount();
  await act(async () => window.dispatchEvent(new Event("focus")));
  expect(fetched).toHaveBeenCalledTimes(1);
  vi.setSystemTime(1_000_000 + 60_000);
  await act(async () => window.dispatchEvent(new Event("focus")));
  expect(fetched).toHaveBeenCalledTimes(2);
});

it("asks for a fresh read when rechecking", async () => {
  await mount();
  await act(async () => hook.recheck());
  expect(fetched).toHaveBeenLastCalledWith(
    expect.stringContaining("api/agent-hooks/codex?refresh=1"),
  );
});
