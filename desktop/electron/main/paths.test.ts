import { describe, expect, it } from "vitest";
import { configDir, localDataDir, sessionDataDir } from "./paths.ts";

// The tests run on Linux, so Windows paths are checked by their last parts.
const home = "/home/alice";

describe("the app's directories", () => {
  it("are Tauri's on Linux, honouring absolute XDG variables", () => {
    const linux = { platform: "linux" as const, env: {}, home };
    expect(configDir(linux)).toBe("/home/alice/.config/com.usebaum.desktop");
    expect(localDataDir(linux)).toBe("/home/alice/.local/share/com.usebaum.desktop");
    const xdg = { ...linux, env: { XDG_CONFIG_HOME: "/x/config", XDG_DATA_HOME: "/x/data" } };
    expect(configDir(xdg)).toBe("/x/config/com.usebaum.desktop");
    expect(localDataDir(xdg)).toBe("/x/data/com.usebaum.desktop");
    const relative = { ...linux, env: { XDG_CONFIG_HOME: "config", XDG_DATA_HOME: "" } };
    expect(configDir(relative)).toBe("/home/alice/.config/com.usebaum.desktop");
    expect(localDataDir(relative)).toBe("/home/alice/.local/share/com.usebaum.desktop");
  });

  it("are Tauri's on macOS", () => {
    const mac = { platform: "darwin" as const, env: { XDG_DATA_HOME: "/ignored" }, home };
    const support = "/home/alice/Library/Application Support/com.usebaum.desktop";
    expect(configDir(mac)).toBe(support);
    expect(localDataDir(mac)).toBe(support);
  });

  it("are the roaming and the local application data on Windows", () => {
    const parts = (path: string) => path.split(/[\\/]/).slice(-3);
    const windows = {
      platform: "win32" as const,
      env: {
        APPDATA: "C:\\Users\\ada\\AppData\\Roaming",
        LOCALAPPDATA: "C:\\Users\\ada\\AppData\\Local",
      },
      home: "C:\\Users\\ada",
    };
    expect(parts(configDir(windows))).toEqual(["AppData", "Roaming", "com.usebaum.desktop"]);
    expect(parts(localDataDir(windows))).toEqual(["AppData", "Local", "com.usebaum.desktop"]);
    const unset = { ...windows, env: {} };
    expect(parts(configDir(unset))).toEqual(["AppData", "Roaming", "com.usebaum.desktop"]);
    expect(parts(localDataDir(unset))).toEqual(["AppData", "Local", "com.usebaum.desktop"]);
  });

  it("keep Chromium's files apart", () => {
    expect(sessionDataDir("/c/com.usebaum.desktop")).toBe("/c/com.usebaum.desktop/chromium");
  });
});
