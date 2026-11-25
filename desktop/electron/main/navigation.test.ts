// The tests of navigation.ts, for the bundled origin (`app://bundle`).
import { describe, expect, it } from "vitest";
import {
  BUNDLED_SCREEN,
  bundledUrl,
  decide,
  decideNewWindow,
  isTrayPanel,
  OPEN_INTERVAL,
  OpenLimit,
  originOf,
  schemeAndHost,
  TRAY_PANEL_URL,
} from "./navigation.ts";

const daemon = "http://127.0.0.1:4100";

describe("navigation", () => {
  it("the tray panel loads only its own page", () => {
    expect(isTrayPanel(TRAY_PANEL_URL)).toBe(true);
    for (const other of [
      BUNDLED_SCREEN,
      bundledUrl("/tray.html"),
      "app://tray/index.html",
      "app://tray/tray.html/",
      "app://tray:1/tray.html",
      "app://user@tray/tray.html",
      "http://127.0.0.1:4100/tray.html",
      "https://example.com/tray.html",
      "app://elsewhere/tray.html",
      "app://bundle:1/tray.html",
      "app://user@bundle/tray.html",
    ]) {
      expect(isTrayPanel(other), other).toBe(false);
    }
  });

  it("gives the bundled pages their own origin", () => {
    expect(originOf(new URL(bundledUrl("/index.html")))).toBe("app://bundle");
    expect(originOf(new URL(TRAY_PANEL_URL))).toBe("app://tray");
    expect(originOf(new URL("http://127.0.0.1:4100/x"))).toBe(daemon);
    expect(originOf(new URL("app://bundle:9/x"))).toBe("null");
  });

  it("keeps the tray panel on an origin no bundled screen shares, so zoom is per window", () => {
    const panel = originOf(new URL(TRAY_PANEL_URL));
    for (const path of ["/", "/index.html", "/setup.html"]) {
      expect(originOf(new URL(bundledUrl(path))), path).not.toBe(panel);
    }
    expect(originOf(new URL(BUNDLED_SCREEN))).not.toBe(panel);
  });

  it("refuses the tray panel in the main window", () => {
    expect(decide(TRAY_PANEL_URL, null, true)).toBe("deny");
    expect(decide("app://tray/", null, true)).toBe("deny");
  });

  it("allows the bundled screen and the daemon", () => {
    expect(decide(BUNDLED_SCREEN, null, false)).toBe("allow");
    for (const u of ["http://127.0.0.1:4100/#token=x", "http://127.0.0.1:4100/s/abc/?page=jobs"]) {
      expect(decide(u, daemon, false), u).toBe("allow");
    }
  });

  // A navigation never opens the browser, whichever frame it comes from:
  // links to other sites are refused in place.
  it("refuses navigations to other sites", () => {
    for (const u of [
      "https://github.com/acme/widgets/pull/7",
      "http://example.com/",
      "https://attacker.example/?d=secret",
      "https://github.com/login/device",
    ]) {
      expect(decide(u, daemon, false), u).toBe("deny");
      expect(decide(u, null, true), u).toBe("deny");
    }
  });

  it("refuses other local servers and schemes", () => {
    for (const u of [
      "about:blank",
      "about:srcdoc",
      "blob:http://127.0.0.1:4100/7d3c1a52-6f0e-4d7e-9a43-1f0b4c2b1e11",
      "blob:null/7d3c1a52-6f0e-4d7e-9a43-1f0b4c2b1e11",
      "tel:+15555550100",
      "http://127.0.0.1:4200/",
      "https://127.0.0.1:4100/",
      "http://localhost:4100/",
      "http://app.localhost/",
      "http://[::1]:4100/",
      "http://[::ffff:127.0.0.1]:8080/",
      "http://[::ffff:7f00:1]:4100/",
      "http://[::ffff:0.0.0.0]:4100/",
      "http://0.0.0.0:4100/",
      "file:///etc/passwd",
      "javascript:alert(1)",
      "data:text/html,hi",
      "app://elsewhere/",
      "mailto:someone@example.com",
      "not a url",
    ]) {
      expect(decide(u, daemon, false), u).toBe("deny");
    }
  });

  it("the setup screen loads only while it shows", () => {
    expect(decide(bundledUrl("/setup.html"), null, true)).toBe("allow");
    expect(decide(bundledUrl("/setup.html"), null, false)).toBe("deny");
    expect(decide(BUNDLED_SCREEN, null, false)).toBe("allow");
    expect(decide(bundledUrl("/"), null, false)).toBe("allow");
    // Other spellings an asset server could map to a page, and other files.
    for (const path of [
      "/setup",
      "/setup.html/",
      "/setup%2Ehtml",
      "/setup%2ehtml",
      "/%73etup.html",
      "/index",
      "/index.html/",
      "/screen.js",
      "/tray.html",
    ]) {
      for (const open of [false, true]) {
        expect(decide(bundledUrl(path), null, open), path).toBe("deny");
      }
    }
  });

  it("without a daemon, loopback is refused", () => {
    expect(decide("http://127.0.0.1:4100/", null, false)).toBe("deny");
  });

  it("refuses addresses with a user name or password", () => {
    for (const u of [
      "http://127.0.0.1:4100@evil.example/",
      "http://evil@127.0.0.1:4100/",
      "http://user:pass@127.0.0.1:4100/s/abc/",
      "app://user@bundle/index.html",
    ]) {
      expect(decide(u, daemon, true), u).toBe("deny");
      expect(decideNewWindow(u), u).toBe("deny");
    }
    expect(decideNewWindow("https://user@github.com/")).toBe("deny");
  });

  // Other spellings of 127.0.0.1 parse to it, as the browser reads them, so
  // they are the daemon's own origin. An IPv4-mapped IPv6 address is another
  // host.
  it("spellings of the daemon address are its origin", () => {
    for (const u of [
      "http://2130706433:4100/",
      "http://0x7f000001:4100/",
      "http://0177.0.0.1:4100/",
      "http://127.1:4100/",
    ]) {
      expect(new URL(u).hostname, u).toBe("127.0.0.1");
      expect(decide(u, daemon, false), u).toBe("allow");
    }
    expect(decide("http://[::ffff:127.0.0.1]:4100/", daemon, false)).toBe("deny");
  });

  // The bundled origin exactly, with the parsed path: dot-segments resolve
  // before the check, and a query or fragment does not change the page.
  it("decides bundled pages by origin and resolved path", () => {
    expect(decide("app://bundle/a/../index.html", null, false)).toBe("allow");
    expect(decide("app://bundle/a/../setup.html", null, false)).toBe("deny");
    expect(decide("app://bundle/a/../setup.html", null, true)).toBe("allow");
    expect(decide("app://bundle/index.html?x=1#y", null, false)).toBe("allow");
    expect(decide("app://bundle/screen.js?x=/index.html", null, false)).toBe("deny");
    for (const u of [
      "app://bundle:1234/index.html",
      "app://user@bundle/index.html",
      "app://bundle.evil/index.html",
    ]) {
      expect(decide(u, null, true), u).toBe("deny");
    }
  });

  it("opens the browser at most once a second", () => {
    const start = 10_000;
    const limit = new OpenLimit();
    expect(limit.allow(start)).toBe(true);
    expect(limit.allow(start)).toBe(false);
    expect(limit.allow(start + 999)).toBe(false);
    expect(limit.allow(start + OPEN_INTERVAL)).toBe(true);
    expect(limit.allow(start + OPEN_INTERVAL + 500)).toBe(false);
    expect(limit.allow(start + 5000)).toBe(true);
  });

  it("logs a refused navigation without path or query", () => {
    for (const [u, logged] of [
      ["https://attacker.example/s/ns/?d=secret#x", "https://attacker.example"],
      ["http://127.0.0.1:4200/s/ns/?token=x", "http://127.0.0.1"],
      ["about:blank", "about:"],
      ["data:text/html,secret", "data:"],
      ["mailto:a@example.com?body=secret", "mailto:"],
    ] as const) {
      expect(schemeAndHost(u), u).toBe(logged);
    }
  });

  // The app's links to other sites ask for a new window, which opens the
  // browser; no window ever opens, and nothing else leaves the app.
  it("never opens a new window", () => {
    for (const u of [
      "https://github.com/",
      "https://github.com/acme/widgets/pull/7#discussion_r1",
      "http://example.com/",
      "https://docs.usebaum.com/",
    ]) {
      expect(decideNewWindow(u), u).toBe("open-in-browser");
    }
    expect(decideNewWindow(BUNDLED_SCREEN)).toBe("deny");
    for (const u of [
      "http://127.0.0.1:4100/",
      "http://localhost:4100/",
      "http://[::1]:4100/",
      "about:blank",
      "about:srcdoc",
      "data:text/html,hi",
      "blob:http://127.0.0.1:4100/7d3c1a52-6f0e-4d7e-9a43-1f0b4c2b1e11",
      "file:///etc/passwd",
      "javascript:alert(1)",
      "mailto:someone@example.com",
      "tel:+15555550100",
      "app://bundle/setup.html",
      "http://2130706433/",
      "http://127.1/",
      "http://0x7f000001/",
      "http://LOCALHOST./",
      "http://foo.localhost./",
      "http://[::ffff:127.0.0.1]/",
      "http://0.0.0.0/",
      "http://[::]/",
      "not a url",
    ]) {
      expect(decideNewWindow(u), u).toBe("deny");
    }
  });

  it("opens no link with a user name or password in the browser", () => {
    for (const u of [
      "http://127.0.0.1:4100@evil.example/",
      "http://evil@127.0.0.1:4100/",
      "http://user:pass@127.0.0.1:4100/s/abc/",
      "https://user@github.com/",
      "https://user:pass@github.com/",
    ]) {
      expect(decideNewWindow(u), u).toBe("deny");
    }
  });

  it("counts only this machine's addresses as loopback", () => {
    for (const u of [
      "http://8.8.8.8/",
      "http://128.0.0.1/",
      "http://[2001:db8::1]/",
      "http://[::2]/",
      "http://[::ffff:8.8.8.8]/",
      "http://localhost.evil/",
      "http://notlocalhost/",
    ]) {
      expect(decideNewWindow(u), u).toBe("open-in-browser");
    }
    for (const u of ["http://127.255.0.3/", "http://[::1]/", "http://[::ffff:7f00:1]/"]) {
      expect(decideNewWindow(u), u).toBe("deny");
    }
  });
});
