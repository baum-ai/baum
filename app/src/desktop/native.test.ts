import { afterEach, describe, expect, it, vi } from "vitest";
import { desktopShell, invoke } from "./native";

afterEach(() => vi.unstubAllGlobals());

describe("desktopShell", () => {
  it("is undefined in a browser", () => {
    expect(desktopShell()).toBeUndefined();
  });

  it("finds the Electron bridge", () => {
    const bridge = { invoke: vi.fn() };
    vi.stubGlobal("baumDesktop", bridge);
    expect(desktopShell()).toBe(bridge);
  });

  it("finds the Tauri bridge", () => {
    const bridge = { invoke: vi.fn() };
    vi.stubGlobal("__TAURI_INTERNALS__", bridge);
    expect(desktopShell()).toBe(bridge);
  });

  it("prefers baumDesktop when both exist", () => {
    const electron = { invoke: vi.fn() };
    vi.stubGlobal("baumDesktop", electron);
    vi.stubGlobal("__TAURI_INTERNALS__", { invoke: vi.fn() });
    expect(desktopShell()).toBe(electron);
  });

  it("reads the window it is given", () => {
    const bridge = { invoke: vi.fn() };
    expect(desktopShell({ baumDesktop: bridge } as unknown as Window)).toBe(bridge);
    expect(desktopShell({} as unknown as Window)).toBeUndefined();
  });
});

describe("invoke", () => {
  it.each(["baumDesktop", "__TAURI_INTERNALS__"])("goes through %s", async (global) => {
    const bridge = { invoke: vi.fn(async () => "ok") };
    vi.stubGlobal(global, bridge);
    await expect(invoke("save_theme", { mode: "dark" })).resolves.toBe("ok");
    expect(bridge.invoke).toHaveBeenCalledWith("save_theme", { mode: "dark" });
  });

  it("uses baumDesktop when both exist", async () => {
    const electron = { invoke: vi.fn(async () => "electron") };
    const tauri = { invoke: vi.fn(async () => "tauri") };
    vi.stubGlobal("baumDesktop", electron);
    vi.stubGlobal("__TAURI_INTERNALS__", tauri);
    await expect(invoke("x")).resolves.toBe("electron");
    expect(tauri.invoke).not.toHaveBeenCalled();
  });

  it("rejects when neither exists", async () => {
    await expect(invoke("x")).rejects.toThrow("Open this screen in the baum desktop app.");
  });
});
