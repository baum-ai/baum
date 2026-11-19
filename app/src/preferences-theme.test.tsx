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

it("shows the slot's built-in when a slot names a theme that is gone", async () => {
  saved = { theme: "dark", light_theme: "c-gone", dark_theme: "c-gone", themes: [dusk] };
  await mount();
  expect(api.preferences.light_theme).toBe("default-light");
  expect(api.preferences.dark_theme).toBe("default-dark");
  saved = { theme: "dark", light_theme: "default-dark", dark_theme: "c-1", themes: [dusk] };
  await act(async () => void (await api.refresh()));
  expect(api.preferences.light_theme).toBe("default-dark");
  expect(api.preferences.dark_theme).toBe("c-1");
});

it("fetches after a save in flight instead of dropping the refresh", async () => {
  saved = { theme: "dark", light_theme: "default-light", dark_theme: "c-1", themes: [dusk] };
  await mount();
  let releasePut!: () => void;
  const gate = new Promise<void>((resolve) => (releasePut = resolve));
  let gets = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url: string, init?: RequestInit) => {
      if (init?.method === "PUT") {
        await gate;
        return Response.json({ ...saved, theme: "light" });
      }
      gets++;
      return Response.json({ ...saved, theme: "light", light_theme: "c-1" });
    }),
  );
  let refreshed = false;
  await act(async () => {
    void api.update({ theme: "light" });
  });
  await act(async () => {
    void Promise.resolve(api.refresh()).then(() => (refreshed = true));
  });
  expect(gets).toBe(0);
  expect(refreshed).toBe(false);
  await act(async () => releasePut());
  await vi.waitFor(() => expect(refreshed).toBe(true));
  expect(gets).toBe(1);
  expect(api.preferences.light_theme).toBe("c-1");
});
