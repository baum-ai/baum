import { describe, expect, it } from "vitest";
import {
  decideNewWindow,
  isLoopback,
  OPEN_INTERVAL_MS,
  OpenLimit,
  schemeAndHost,
} from "./outside-url.ts";

const url = (s: string) => new URL(s);

describe("links to other sites", () => {
  it("new windows never open", () => {
    for (const u of [
      "https://github.com/",
      "https://github.com/acme/widgets/pull/7#discussion_r1",
      "http://example.com/",
      "https://docs.usebaum.com/",
    ]) {
      expect(decideNewWindow(url(u)), u).toBe("open-in-browser");
    }
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
      "tauri://localhost/setup.html",
      "app://baum/index.html",
      "http://2130706433/",
      "http://127.1/",
      "http://0x7f000001/",
      "http://LOCALHOST./",
      "http://foo.localhost./",
      "http://[::ffff:127.0.0.1]/",
      "http://0.0.0.0/",
      "http://[::]/",
    ]) {
      expect(decideNewWindow(url(u)), u).toBe("deny");
    }
  });

  it("refuses addresses with a user name or password", () => {
    for (const u of [
      "http://127.0.0.1:4100@evil.example/",
      "http://evil@127.0.0.1:4100/",
      "http://user:pass@127.0.0.1:4100/s/abc/",
      "https://user@github.com/",
      "https://user:pass@github.com/",
    ]) {
      expect(decideNewWindow(url(u)), u).toBe("deny");
    }
  });

  it("only this machine's addresses are loopback", () => {
    for (const u of [
      "http://8.8.8.8/",
      "http://128.0.0.1/",
      "http://[2001:db8::1]/",
      "http://[::2]/",
      "http://[::ffff:8.8.8.8]/",
      "http://localhost.evil/",
      "http://notlocalhost/",
    ]) {
      expect(isLoopback(url(u)), u).toBe(false);
    }
    for (const u of ["http://127.255.0.3/", "http://[::1]/", "http://[::ffff:7f00:1]/"]) {
      expect(isLoopback(url(u)), u).toBe(true);
    }
  });

  it("opens the browser at most once a second", () => {
    const limit = new OpenLimit();
    const start = 10_000;
    expect(limit.allow(start)).toBe(true);
    expect(limit.allow(start)).toBe(false);
    expect(limit.allow(start + 999)).toBe(false);
    expect(limit.allow(start + OPEN_INTERVAL_MS)).toBe(true);
    expect(limit.allow(start + OPEN_INTERVAL_MS + 500)).toBe(false);
    expect(limit.allow(start + 5000)).toBe(true);
  });

  it("a refused link is logged without path or query", () => {
    for (const [u, logged] of [
      ["https://attacker.example/s/ns/?d=secret#x", "https://attacker.example"],
      ["http://127.0.0.1:4200/s/ns/?token=x", "http://127.0.0.1"],
      ["about:blank", "about:"],
      ["data:text/html,secret", "data:"],
      ["mailto:a@example.com?body=secret", "mailto:"],
    ] as const) {
      expect(schemeAndHost(url(u)), u).toBe(logged);
    }
  });
});
