import { beforeEach, describe, expect, it } from "vitest";
import { clearCommands, dispatch } from "./commands.ts";
import { LinkOpener, registerLinkCommands } from "./links.ts";
import { OPEN_INTERVAL_MS } from "./outside-url.ts";

const url = (s: string) => new URL(s);

function opener() {
  const state = { now: 0, opened: [] as string[] };
  const links = new LinkOpener({
    openExternal: (u) => {
      state.opened.push(u);
    },
    now: () => state.now,
  });
  return { state, links };
}

describe("links", () => {
  it("opens only links to other sites", () => {
    for (const refused of [
      "http://127.0.0.1:4100/",
      "http://localhost/",
      "https://user:pass@github.com/",
      "file:///etc/passwd",
      "javascript:alert(1)",
      "mailto:someone@example.com",
    ]) {
      const { state, links } = opener();
      expect(links.open(url(refused)), refused).toBe("refused");
      expect(state.opened).toEqual([]);
    }
    const { state, links } = opener();
    expect(links.open(url("https://github.com/acme/widgets/pull/7"))).toBe("browser");
    expect(state.opened).toEqual(["https://github.com/acme/widgets/pull/7"]);
  });

  it("a second link within the interval is dropped", () => {
    const { state, links } = opener();
    const link = url("https://github.com/");
    expect(links.open(link)).toBe("browser");
    expect(links.open(link)).toBe("dropped");
    // A refused URL neither opens nor counts.
    state.now = OPEN_INTERVAL_MS;
    expect(links.open(url("http://localhost/"))).toBe("refused");
    expect(links.open(link)).toBe("browser");
    expect(state.opened).toHaveLength(2);
  });

  describe("open_link", () => {
    beforeEach(clearCommands);
    const context = {
      window: null,
      role: "daemon-pages",
      origin: "http://127.0.0.1:4100",
    } as const;

    it("is an error for something that is not a link or not another site", async () => {
      const { links } = opener();
      registerLinkCommands(links);
      await expect(dispatch("open_link", { url: "nope" }, context)).rejects.toThrow(
        "That is not a link.",
      );
      await expect(dispatch("open_link", { url: 7 }, context)).rejects.toThrow(
        "That is not a link.",
      );
      await expect(dispatch("open_link", { url: "http://localhost/" }, context)).rejects.toThrow(
        "Only links to other web sites open in the browser.",
      );
    });

    it("opens a link and drops a second without an error", async () => {
      const { state, links } = opener();
      registerLinkCommands(links);
      await dispatch("open_link", { url: "https://github.com/" }, context);
      await dispatch("open_link", { url: "https://github.com/x" }, context);
      expect(state.opened).toEqual(["https://github.com/"]);
    });
  });
});
