import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { PreferencesProvider, usePreferences } from "./preferences";
import type { Theme } from "./theme";

let node: HTMLDivElement;
let root: Root;
const dusk: Theme = {
  id: "c-1",
  name: "Dusk",
  base: "dark",
  tokens: { "--color-paper": "#e8eef4", "--not-allowed": "red" },
};
let saved = {
  theme: "dark",
  light_theme: "default-light",
  dark_theme: "c-1",
  themes: [dusk],
};

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => Response.json(saved)),
  );
});
afterEach(() => {
  act(() => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
  Reflect.deleteProperty(window, "__TAURI_INTERNALS__");
});

let api!: ReturnType<typeof usePreferences>;
function Probe() {
  api = usePreferences();
  return null;
}
const mount = () =>
  act(async () =>
    root.render(
      <PreferencesProvider>
        <Probe />
      </PreferencesProvider>,
    ),
  );

it("tells the desktop app the saved theme, but never a preview, and not twice", async () => {
  const invoke = vi.fn(async () => undefined);
  Object.defineProperty(window, "__TAURI_INTERNALS__", { configurable: true, value: { invoke } });
  await mount();
  expect(invoke).toHaveBeenCalledTimes(1);
  expect(invoke).toHaveBeenCalledWith("save_theme", {
    mode: "dark",
    light: { base: "light", tokens: {} },
    dark: { base: "dark", tokens: { "--color-paper": "#e8eef4" } },
  });
  await act(async () => api.setPreview({ ...dusk, id: "p", tokens: { "--color-paper": "#000" } }));
  await act(async () => api.setPreview(null));
  expect(invoke).toHaveBeenCalledTimes(1);
  saved = { ...saved, theme: "light" };
  await act(async () => void (await api.refresh()));
  expect(invoke).toHaveBeenCalledTimes(2);
  expect(invoke).toHaveBeenLastCalledWith("save_theme", expect.objectContaining({ mode: "light" }));
});

it("sends nothing outside the desktop app", async () => {
  await mount();
  expect(api.ready).toBe(true);
});
