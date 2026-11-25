import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { PreferencesProvider, usePreferences } from "./preferences";
import * as native from "./desktop/native";
import type { Theme } from "./theme";

let node: HTMLDivElement;
let root: Root;
const dusk: Theme = {
  id: "c-1",
  name: "Dusk",
  base: "dark",
  tokens: { "--color-paper": "#e8eef4", "--not-allowed": "red" },
};
const initial = {
  theme: "dark",
  light_theme: "default-light",
  dark_theme: "c-1",
  themes: [dusk],
};
let saved = initial;

beforeEach(() => {
  saved = initial;
  document.documentElement.removeAttribute("data-theme");
  document.documentElement.removeAttribute("style");
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
  Reflect.deleteProperty(window, "baumDesktop");
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
  Object.defineProperty(window, "baumDesktop", { configurable: true, value: { invoke } });
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
  const invoke = vi.fn(async () => undefined);
  vi.spyOn(native, "invoke").mockImplementation(invoke);
  await mount();
  expect(api.ready).toBe(true);
  expect(invoke).not.toHaveBeenCalled();
  vi.restoreAllMocks();
});

it("leaves a pre-painted root alone until the preferences are ready", async () => {
  const html = document.documentElement;
  html.dataset.theme = "dark";
  html.style.setProperty("--color-paper", "#123456");
  let release!: () => void;
  const gate = new Promise<void>((resolve) => (release = resolve));
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => {
      await gate;
      return Response.json(saved);
    }),
  );
  await mount();
  expect(api.ready).toBe(false);
  expect(html.dataset.theme).toBe("dark");
  expect(html.style.getPropertyValue("--color-paper")).toBe("#123456");
  await act(async () => release());
  await vi.waitFor(() => expect(api.ready).toBe(true));
  expect(html.dataset.theme).toBe("dark");
  expect(html.style.getPropertyValue("--color-paper")).toBe("#e8eef4");
  expect(html.style.getPropertyValue("--not-allowed")).toBe("");
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

it("deletes a theme only after a save in flight, so its older answer cannot bring it back", async () => {
  await mount();
  let releasePut!: () => void;
  const gate = new Promise<void>((resolve) => (releasePut = resolve));
  const sent: string[] = [];
  const afterDelete = { ...saved, dark_theme: "default-dark", themes: [] };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url: string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      sent.push(method);
      if (method === "PUT") {
        await gate;
        // The PUT's answer was read before the delete: it still lists Dusk.
        return Response.json({ ...saved, theme: "light" });
      }
      if (method === "DELETE") return Response.json({ ...afterDelete, theme: "light" });
      return Response.json({ ...afterDelete, theme: "light" });
    }),
  );
  await act(async () => {
    void api.update({ theme: "light" });
  });
  let deleted = false;
  let refreshed = false;
  await act(async () => {
    void api.deleteTheme("c-1").then(() => (deleted = true));
    void Promise.resolve(api.refresh()).then(() => (refreshed = true));
  });
  expect(sent).toEqual(["PUT"]);
  expect(deleted).toBe(false);
  await act(async () => releasePut());
  await vi.waitFor(() => expect(deleted).toBe(true));
  await vi.waitFor(() => expect(refreshed).toBe(true));
  expect(sent).toEqual(["PUT", "DELETE", "GET"]);
  expect(api.preferences.themes.map((theme) => theme.id)).not.toContain("c-1");
  expect(api.preferences.dark_theme).toBe("default-dark");
  expect(api.preferences.theme).toBe("light");
});

it("rejects a failed rename without stopping the saves queued after it", async () => {
  await mount();
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url: string, init?: RequestInit) => {
      if (init?.method === "PATCH") return Response.json({ error: "name taken" }, { status: 400 });
      return Response.json({ ...saved, theme: "light" });
    }),
  );
  let failure: unknown;
  await act(async () => {
    void api.renameTheme("c-1", "Dawn").catch((cause) => (failure = cause));
    void api.update({ theme: "light" });
  });
  await vi.waitFor(() => expect(failure).toBeInstanceOf(Error));
  await vi.waitFor(() => expect(api.saving).toBe(false));
  expect(api.preferences.theme).toBe("light");
});

it("takes the built-ins and their tokens from the daemon, in its order, and applies one in a slot", async () => {
  const builtin = (
    id: string,
    name: string,
    base: "light" | "dark",
    tokens: Record<string, string>,
  ): Theme => ({
    id,
    name,
    base,
    builtin: true,
    tokens,
  });
  saved = {
    theme: "dark",
    light_theme: "default-light",
    dark_theme: "pine",
    themes: [
      builtin("default-light", "Default light", "light", {}),
      builtin("default-dark", "Default dark", "dark", {}),
      builtin("birch", "Birch", "light", { "--color-ink": "#1d2a38" }),
      builtin("pine", "Pine", "dark", { "--color-paper": "#dce9df" }),
      dusk,
    ],
  };
  const invoke = vi.fn(async () => undefined);
  Object.defineProperty(window, "baumDesktop", { configurable: true, value: { invoke } });
  await mount();
  expect(api.preferences.themes.map((theme) => theme.id)).toEqual([
    "default-light",
    "default-dark",
    "birch",
    "pine",
    "c-1",
  ]);
  expect(api.preferences.dark_theme).toBe("pine");
  expect(document.documentElement.style.getPropertyValue("--color-paper")).toBe("#dce9df");
  expect(invoke).toHaveBeenCalledWith("save_theme", {
    mode: "dark",
    light: { base: "light", tokens: {} },
    dark: { base: "dark", tokens: { "--color-paper": "#dce9df" } },
  });
});

it("keeps the two defaults first when the daemon lists no built-ins", async () => {
  saved = { theme: "dark", light_theme: "pine", dark_theme: "pine", themes: [dusk] };
  await mount();
  expect(api.preferences.themes.map((theme) => theme.id)).toEqual([
    "default-light",
    "default-dark",
    "c-1",
  ]);
  expect(api.preferences.light_theme).toBe("default-light");
});
