import { afterEach, describe, expect, it, vi } from "vitest";
import { log, redact } from "./log.ts";

const TOKEN = "abcdefghijklmnopqrstuvwxyz0123456789-_ABCDE";

describe("the log", () => {
  afterEach(() => vi.restoreAllMocks());

  it("never holds the launch capability", () => {
    for (const message of [
      `cannot load http://127.0.0.1:4100/#token=${TOKEN}`,
      `{"url":"http://127.0.0.1:4100/#token=${TOKEN}"}`,
      `http://127.0.0.1:1420/#token=${TOKEN}&x=1`,
      `Authorization: Bearer ${TOKEN}`,
      `?TOKEN=${TOKEN}`,
    ]) {
      const shown = redact(message);
      expect(shown).not.toContain(TOKEN);
      expect(shown).toContain("<redacted>");
    }
  });

  it("redacts what it writes", () => {
    const write = vi.spyOn(console, "error").mockImplementation(() => {});
    log(`attached to http://127.0.0.1:4100/#token=${TOKEN}`);
    expect(write).toHaveBeenCalledWith(
      "baum desktop: attached to http://127.0.0.1:4100/#token=<redacted>",
    );
  });

  it("leaves other messages alone", () => {
    expect(redact("attached to baum daemon v1 (pid 42)")).toBe(
      "attached to baum daemon v1 (pid 42)",
    );
  });
});
