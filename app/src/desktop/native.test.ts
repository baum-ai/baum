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

  it("reads the window it is given", () => {
    const bridge = { invoke: vi.fn() };
    expect(desktopShell({ baumDesktop: bridge } as unknown as Window)).toBe(bridge);
    expect(desktopShell({} as unknown as Window)).toBeUndefined();
  });
});

describe("invoke", () => {
  it("goes through baumDesktop", async () => {
    const bridge = { invoke: vi.fn(async () => "ok") };
    vi.stubGlobal("baumDesktop", bridge);
    await expect(invoke("save_theme", { mode: "dark" })).resolves.toBe("ok");
    expect(bridge.invoke).toHaveBeenCalledWith("save_theme", { mode: "dark" });
  });

  it("rejects in a browser", async () => {
    await expect(invoke("x")).rejects.toThrow("Open this screen in the baum desktop app.");
  });
});
