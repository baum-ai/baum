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

  it("grant notifications and clipboard writes to the app's origins only", () => {
    const app = (origin: string) => ["app://bundle", "http://127.0.0.1:4100"].includes(origin);
    expect(permissionAllowed("notifications", "app://bundle/index.html", app)).toBe(true);
    expect(permissionAllowed("clipboard-sanitized-write", "http://127.0.0.1:4100/s/x", app)).toBe(
      true,
    );
    for (const [permission, url] of [
      ["notifications", "http://127.0.0.1:4200/"],
      ["notifications", "https://example.com/"],
      ["notifications", undefined],
      ["notifications", "not a url"],
      ["media", "app://bundle/index.html"],
      ["geolocation", "http://127.0.0.1:4100/"],
      ["clipboard-read", "http://127.0.0.1:4100/"],
    ] as const) {
      expect(permissionAllowed(permission, url, app), `${permission} ${url}`).toBe(false);
    }
  });
});
