import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { PreferencesProvider, usePreferences } from "./preferences";
import { COMMAND_LINE_FOCUS_INTERVAL_MS, useCommandLineCheck } from "./settings";

let node: HTMLDivElement;
let root: Root;

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  vi.useFakeTimers();
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
});
afterEach(() => {
  act(() => root.unmount());
  node.remove();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  Reflect.deleteProperty(window, "__TAURI_INTERNALS__");
});

const focus = () => act(async () => void window.dispatchEvent(new Event("focus")));

it("rechecks the command line on focus at most every five minutes", async () => {
  const view = {
    command: { installed: false, target: "/app/baum", plan: null },
    hooks: { agents: [] },
  };
  const invoke = vi.fn(async () => view);
  Object.defineProperty(window, "__TAURI_INTERNALS__", { configurable: true, value: { invoke } });
  let check!: ReturnType<typeof useCommandLineCheck>;
  let renders = 0;
  function Probe() {
    check = useCommandLineCheck();
    renders++;
    return null;
  }
  await act(async () => root.render(<Probe />));
  expect(invoke).toHaveBeenCalledTimes(1);
  expect(check.missing).toBe(true);
  const rendered = renders;

  await vi.advanceTimersByTimeAsync(COMMAND_LINE_FOCUS_INTERVAL_MS - 1000);
  await focus();
  await focus();
  expect(invoke).toHaveBeenCalledTimes(1);

  // An explicit recheck is never throttled.
  await act(async () => void (await check.recheck()));
  expect(invoke).toHaveBeenCalledTimes(2);

  await vi.advanceTimersByTimeAsync(COMMAND_LINE_FOCUS_INTERVAL_MS - 1000);
  await focus();
  expect(invoke).toHaveBeenCalledTimes(2);
  await vi.advanceTimersByTimeAsync(1000);
  await focus();
  expect(invoke).toHaveBeenCalledTimes(3);
  // The answer never changed, so nothing re-rendered.
  expect(renders).toBe(rendered);
});

it("does not re-render preferences consumers when a focus reload returns the same data", async () => {
  let saved = { theme: "dark", key_hints: true, page_modifier: "" };
  const fetcher = vi.fn(async () => Response.json(saved));
  vi.stubGlobal("fetch", fetcher);
  let renders = 0;
  let seen = "";
  function Probe() {
    const { preferences } = usePreferences();
    renders++;
    seen = preferences.theme;
    return null;
  }
  await act(async () =>
    root.render(
      <PreferencesProvider>
        <Probe />
      </PreferencesProvider>,
    ),
  );
  expect(seen).toBe("dark");
  const rendered = renders;
  await focus();
  expect(fetcher).toHaveBeenCalledTimes(2);
  expect(renders).toBe(rendered);

  saved = { ...saved, theme: "light" };
  await focus();
  expect(seen).toBe("light");
  expect(renders).toBeGreaterThan(rendered);
});
