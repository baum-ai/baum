import { describe, expect, it } from "vitest";
import { parseDeclaration } from "../shared/contract.ts";
import { CHECK_WINDOW_PAGE, SHELL } from "./declaration.ts";
import { permissionAllowed, securePreferences } from "./windows.ts";

describe("the app's windows", () => {
  it("are isolated and sandboxed, with the preload in the main frame only", () => {
    const preferences = securePreferences("/build/preload.cjs", SHELL);
    expect(preferences).toMatchObject({
      preload: "/build/preload.cjs",
      contextIsolation: true,
      sandbox: true,
      nodeIntegration: false,
      nodeIntegrationInSubFrames: false,
      webviewTag: false,
    });
    expect(parseDeclaration(preferences.additionalArguments ?? [])).toEqual(SHELL);
    expect(securePreferences("/p", null).additionalArguments).toEqual([]);
  });

  it("declare tile frames, links and the check window", () => {
    expect(SHELL).toEqual({ tileFrames: true, checkWindow: true, openLinks: true });
    expect(CHECK_WINDOW_PAGE).toEqual({ tileFrames: true, checkWindow: true, openLinks: false });
  });

  it("grant notifications, clipboard writes and full screen to the app's origins only", () => {
    const app = (origin: string) => ["app://bundle", "http://127.0.0.1:4100"].includes(origin);
    const main = { isMainFrame: true };
    expect(permissionAllowed("notifications", "app://bundle/index.html", app, main)).toBe(true);
    expect(
      permissionAllowed("clipboard-sanitized-write", "http://127.0.0.1:4100/s/x", app, main),
    ).toBe(true);
    expect(permissionAllowed("fullscreen", "app://bundle/index.html", app, main)).toBe(true);
    for (const [permission, url] of [
      ["notifications", "http://127.0.0.1:4200/"],
      ["notifications", "https://example.com/"],
      ["notifications", undefined],
      ["notifications", "not a url"],
      ["fullscreen", "https://example.com/"],
      ["media", "app://bundle/index.html"],
      ["geolocation", "http://127.0.0.1:4100/"],
      ["clipboard-read", "http://127.0.0.1:4100/"],
    ] as const) {
      expect(permissionAllowed(permission, url, app, main), `${permission} ${url}`).toBe(false);
    }
  });

  it("grant permissions to a main frame only", () => {
    const app = (origin: string) => origin === "http://127.0.0.1:4100";
    const url = "http://127.0.0.1:4100/s/x";
    expect(permissionAllowed("notifications", url, app, { isMainFrame: false })).toBe(false);
    expect(
      permissionAllowed("notifications", url, app, {
        isMainFrame: true,
        embeddingOrigin: "http://127.0.0.1:4100",
      }),
    ).toBe(false);
  });

  it("check every origin Electron reports for the frame", () => {
    const app = (origin: string) => origin === "http://127.0.0.1:4100";
    const url = "http://127.0.0.1:4100/s/x";
    const check = (...origins: (string | undefined)[]) =>
      permissionAllowed("notifications", url, app, { isMainFrame: true, origins });
    expect(check("http://127.0.0.1:4100", undefined)).toBe(true);
    // Electron may report an origin as a URL with a trailing slash.
    expect(check("http://127.0.0.1:4100/", "http://127.0.0.1:4100")).toBe(true);
    expect(check("http://127.0.0.1:4100", "null")).toBe(false);
    expect(check("null")).toBe(false);
    expect(check("http://127.0.0.1:4100", "https://example.com")).toBe(false);
    expect(check("")).toBe(false);
    // A main frame whose own URL has an opaque origin.
    expect(
      permissionAllowed("notifications", "data:text/html,x", () => true, { isMainFrame: true }),
    ).toBe(false);
  });
});
