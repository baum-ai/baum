import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { trackHidden, useVisibleInterval } from "./visible-interval";

let node: HTMLDivElement;
let root: Root;
let hidden = false;
const calls = vi.fn();

function Probe({ ms }: { ms: number | null }) {
  useVisibleInterval(calls, ms);
  return null;
}

function setHidden(next: boolean) {
  hidden = next;
  document.dispatchEvent(new Event("visibilitychange"));
}

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  vi.useFakeTimers();
  hidden = false;
  calls.mockClear();
  Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.useRealTimers();
  delete document.documentElement.dataset.hidden;
});

it("ticks while visible", async () => {
  await act(async () => root.render(<Probe ms={1000} />));
  vi.advanceTimersByTime(3500);
  expect(calls).toHaveBeenCalledTimes(3);
});

it("is paused while hidden and catches up once on becoming visible", async () => {
  await act(async () => root.render(<Probe ms={1000} />));
  act(() => setHidden(true));
  vi.advanceTimersByTime(60_000);
  expect(calls).not.toHaveBeenCalled();
  act(() => setHidden(false));
  expect(calls).toHaveBeenCalledTimes(1);
  vi.advanceTimersByTime(2000);
  expect(calls).toHaveBeenCalledTimes(3);
});

it("does not start while hidden at mount, and stops with null", async () => {
  hidden = true;
  await act(async () => root.render(<Probe ms={1000} />));
  vi.advanceTimersByTime(5000);
  expect(calls).not.toHaveBeenCalled();
  act(() => setHidden(false));
  expect(calls).toHaveBeenCalledTimes(1);
  await act(async () => root.render(<Probe ms={null} />));
  vi.advanceTimersByTime(5000);
  expect(calls).toHaveBeenCalledTimes(1);
});

it("marks the document while hidden", () => {
  trackHidden();
  expect(document.documentElement.dataset.hidden).toBeUndefined();
  setHidden(true);
  expect(document.documentElement.dataset.hidden).toBe("");
  setHidden(false);
  expect(document.documentElement.dataset.hidden).toBeUndefined();
});
