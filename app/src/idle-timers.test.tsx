import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { sameUpdate, type AppUpdate } from "./desktop/updates";
import { useDeadlineNow } from "./live";
import { sameSessions } from "./review";

const update: AppUpdate = {
  version: "0.1.0",
  enabled: true,
  installs: true,
  phase: "idle",
  available: null,
  page: null,
  checked_at: 100,
  jobs: 0,
  error: null,
  noticed: null,
};

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers();
});
afterEach(() => vi.useRealTimers());

function renderHook<T>(hook: () => T) {
  const result = { current: undefined as T };
  const Probe = () => {
    result.current = hook();
    return null;
  };
  act(() => createRoot(document.createElement("div")).render(<Probe />));
  return { result };
}

test("an update poll that finds the same state keeps the same object", () => {
  expect(sameUpdate(update, { ...update })).toBe(update);
  const changed = { ...update, checked_at: 160 };
  expect(sameUpdate(update, changed)).toBe(changed);
  expect(sameUpdate(null, update)).toBe(update);
  expect(sameUpdate(update, null)).toBeNull();
});

test("a sessions poll with identical content keeps the same state", () => {
  const load = { state: "request-failed" } as const;
  const previous = { query: "q", version: 1, load };
  expect(sameSessions(previous, { query: "q", version: 1, load: { ...load } })).toBe(previous);
  const next = { query: "q", version: 2, load };
  expect(sameSessions(previous, next)).toBe(next);
});

test("the deadline clock renders only when a deadline passes", () => {
  vi.setSystemTime(new Date(2026, 10, 10, 12, 0, 0));
  let renders = 0;
  const end = Date.now() + 10 * 60_000;
  const { result } = renderHook(() => {
    renders += 1;
    return useDeadlineNow([end]);
  });
  const first = result.current;
  act(() => {
    vi.advanceTimersByTime(9 * 60_000);
  });
  expect(renders).toBe(1);
  expect(result.current).toBe(first);
  act(() => {
    vi.advanceTimersByTime(61_000);
  });
  expect(result.current).toBeGreaterThanOrEqual(end);
  expect(renders).toBe(2);
  act(() => {
    vi.advanceTimersByTime(3_600_000);
  });
  expect(renders).toBe(2);
});

test("the deadline clock rolls over at midnight when asked", () => {
  vi.setSystemTime(new Date(2026, 10, 10, 23, 59, 0));
  const { result } = renderHook(() => useDeadlineNow([], true));
  act(() => {
    vi.advanceTimersByTime(61_000);
  });
  expect(new Date(result.current).getDate()).toBe(11);
});
