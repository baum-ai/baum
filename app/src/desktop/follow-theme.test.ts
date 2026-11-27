import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { followTheme, resolveTheme, type SavedTheme } from "./follow-theme";

const root = document.documentElement;
let systemDark = false;
let listeners: (() => void)[] = [];
let stop: (() => void) | undefined;
const saved: SavedTheme = {
  mode: "system",
  light: null,
  dark: { base: "dark", tokens: { "--surface-page": "#112233" } },
};

function bridge(answer: () => unknown) {
  const invoke = vi.fn(async (command: string) => {
    expect(command).toBe("theme_state");
    return answer();
  });
  Object.defineProperty(window, "baumDesktop", { configurable: true, value: { invoke } });
  return invoke;
}
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

beforeEach(() => {
  systemDark = false;
  listeners = [];
  vi.stubGlobal("matchMedia", () => ({
    get matches() {
      return systemDark;
    },
    addEventListener: (_: string, fn: () => void) => listeners.push(fn),
    removeEventListener: (_: string, fn: () => void) => {
      listeners = listeners.filter((l) => l !== fn);
    },
  }));
  root.removeAttribute("data-theme");
  root.removeAttribute("style");
});
afterEach(() => {
  stop?.();
  stop = undefined;
  vi.unstubAllGlobals();
  Reflect.deleteProperty(window, "baumDesktop");
});

it("resolves the slot the mode picks, with the built-in for an empty slot or no state", () => {
  expect(resolveTheme(saved, true).tokens).toEqual({ "--surface-page": "#112233" });
  expect(resolveTheme(saved, false)).toMatchObject({ base: "light", tokens: {} });
  expect(resolveTheme({ ...saved, mode: "dark" }, false).base).toBe("dark");
  expect(resolveTheme({ ...saved, mode: "light" }, true).base).toBe("light");
  expect(resolveTheme(null, true).base).toBe("dark");
  expect(resolveTheme({ mode: "sepia" }, false).base).toBe("light");
});

it("applies the saved theme and follows the system in system mode", async () => {
  bridge(() => saved);
  stop = followTheme();
  await settle();
  expect(root.dataset.theme).toBe("light");
  expect(root.style.getPropertyValue("--surface-page")).toBe("");
  systemDark = true;
  listeners.forEach((l) => l());
  expect(root.dataset.theme).toBe("dark");
  expect(root.style.colorScheme).toBe("dark");
  expect(root.style.getPropertyValue("--surface-page")).toBe("#112233");
  systemDark = false;
  listeners.forEach((l) => l());
  expect(root.dataset.theme).toBe("light");
  expect(root.style.getPropertyValue("--surface-page")).toBe("");
});

it("reads the saved theme through the Electron bridge", async () => {
  const invoke = bridge(() => ({ ...saved, mode: "dark" }));
  stop = followTheme();
  await settle();
  expect(invoke).toHaveBeenCalledWith("theme_state", undefined);
  expect(root.dataset.theme).toBe("dark");
  expect(root.style.getPropertyValue("--surface-page")).toBe("#112233");
});

it("keeps a chosen mode whatever the system does", async () => {
  bridge(() => ({ ...saved, mode: "dark" }));
  stop = followTheme();
  await settle();
  systemDark = false;
  listeners.forEach((l) => l());
  expect(root.dataset.theme).toBe("dark");
  expect(root.style.getPropertyValue("--surface-page")).toBe("#112233");
});

it("falls back to the system's appearance when the state is unavailable or empty", async () => {
  systemDark = true;
  stop = followTheme();
  await settle();
  expect(root.dataset.theme).toBe("dark");
  stop();
  bridge(() => null);
  systemDark = false;
  stop = followTheme();
  await settle();
  expect(root.dataset.theme).toBe("light");
});

it("reads again when the window gets the focus or is shown, and ignores a hidden page", async () => {
  let answer: unknown = saved;
  const invoke = bridge(() => answer);
  stop = followTheme();
  await settle();
  expect(invoke).toHaveBeenCalledTimes(1);
  answer = {
    mode: "dark",
    light: null,
    dark: { base: "dark", tokens: { "--surface-page": "#000001" } },
  };
  window.dispatchEvent(new Event("focus"));
  await settle();
  expect(invoke).toHaveBeenCalledTimes(2);
  expect(root.style.getPropertyValue("--surface-page")).toBe("#000001");
  answer = { mode: "light", light: null, dark: null };
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
  document.dispatchEvent(new Event("visibilitychange"));
  await settle();
  expect(invoke).toHaveBeenCalledTimes(2);
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  document.dispatchEvent(new Event("visibilitychange"));
  await settle();
  expect(invoke).toHaveBeenCalledTimes(3);
  expect(root.dataset.theme).toBe("light");
  expect(root.style.getPropertyValue("--surface-page")).toBe("");
  vi.restoreAllMocks();
});

it("removes its listeners when stopped", async () => {
  bridge(() => saved);
  const win = vi.spyOn(window, "removeEventListener");
  const doc = vi.spyOn(document, "removeEventListener");
  stop = followTheme();
  await settle();
  expect(listeners).toHaveLength(1);
  stop();
  stop = undefined;
  expect(listeners).toHaveLength(0);
  expect(win).toHaveBeenCalledWith("focus", expect.any(Function));
  expect(doc).toHaveBeenCalledWith("visibilitychange", expect.any(Function));
  vi.restoreAllMocks();
});
