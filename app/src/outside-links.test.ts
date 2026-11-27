import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { openExternal, openOutsideLinksInBrowser } from "./outside-links";

describe.each(["__TAURI_INTERNALS__", "baumDesktop"])("under %s", (bridge) => {
  const people = new WeakSet<Event>();
  let opened: { href: string; target: string; rel: string }[];
  let remove: () => void;
  const click = HTMLAnchorElement.prototype.click;
  beforeEach(() => {
    opened = [];
    // openExternal clicks a detached `target="_blank"` link; record it instead.
    HTMLAnchorElement.prototype.click = function (this: HTMLAnchorElement) {
      opened.push({ href: this.href, target: this.target, rel: this.rel });
    };
    vi.stubGlobal(bridge, {});
    remove = openOutsideLinksInBrowser(window, (event) => people.has(event));
  });
  afterEach(() => {
    remove();
    HTMLAnchorElement.prototype.click = click;
    vi.unstubAllGlobals();
    document.body.replaceChildren();
  });

  /** A person's click, or a script's (`trusted` false). */
  function personClicks(element: Element, init: MouseEventInit = {}, trusted = true) {
    const event = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0, ...init });
    if (trusted) people.add(event);
    element.dispatchEvent(event);
    return event;
  }
  function link(html: string) {
    document.body.innerHTML = html;
    return document.body.querySelector("a")!;
  }

  it("opens a same-window link to another site in the browser", () => {
    for (const [html, href] of [
      [
        '<a href="https://github.com/acme/widgets/pull/7">pr</a>',
        "https://github.com/acme/widgets/pull/7",
      ],
      [
        '<a href="http://example.com/docs" target="_self"><span>docs</span></a>',
        "http://example.com/docs",
      ],
    ]) {
      opened = [];
      const a = link(html!);
      const event = personClicks(a.querySelector("span") ?? a);
      expect(event.defaultPrevented, html).toBe(true);
      expect(opened, html).toEqual([{ href, target: "_blank", rel: "noreferrer" }]);
    }
  });

  it("leaves every other click alone", () => {
    const cases: [string, MouseEventInit?, boolean?][] = [
      [`<a href="${location.origin}/s/x/?page=jobs">app</a>`],
      ['<a href="?page=jobs">relative</a>'],
      ['<a href="https://github.com/" target="_blank" rel="noreferrer">new window</a>'],
      ['<a href="mailto:someone@example.com">mail</a>'],
      ['<a href="https://example.com/file" download>download</a>'],
      ['<a href="https://github.com/">modified</a>', { ctrlKey: true }],
      ['<a href="https://github.com/">middle</a>', { button: 1 }],
      ['<a href="https://github.com/">script</a>', {}, false],
    ];
    for (const [html, init, trusted] of cases) {
      const event = personClicks(link(html), init, trusted);
      expect(event.defaultPrevented, html).toBe(false);
    }
    expect(opened).toEqual([]);
  });

  it("leaves a link the app handles itself", () => {
    const a = link('<a href="https://github.com/">handled</a>');
    a.addEventListener("click", (event) => event.preventDefault());
    personClicks(a);
    expect(opened).toEqual([]);
  });

  it("does nothing in a browser", () => {
    remove();
    vi.unstubAllGlobals();
    remove = openOutsideLinksInBrowser(window, () => true);
    const event = personClicks(link('<a href="https://github.com/">pr</a>'));
    expect(event.defaultPrevented).toBe(false);
    expect(opened).toEqual([]);
  });

  describe("in a shell that opens links itself", () => {
    let invoked: [string, Record<string, unknown> | undefined][];
    beforeEach(() => {
      invoked = [];
      vi.stubGlobal(bridge, {
        invoke: (command: string, args?: Record<string, unknown>) => {
          invoked.push([command, args]);
          return Promise.resolve();
        },
      });
      vi.stubGlobal("__BAUM_DESKTOP__", Object.freeze({ tileFrames: true, openLinks: true }));
    });

    it("opens new-window and same-window links to other sites through the shell", () => {
      for (const html of [
        '<a href="https://github.com/acme/widgets/pull/7" target="_blank" rel="noreferrer">pr</a>',
        '<a href="https://github.com/acme/widgets/pull/7">pr</a>',
      ]) {
        invoked = [];
        const event = personClicks(link(html));
        expect(event.defaultPrevented, html).toBe(true);
        expect(invoked, html).toEqual([
          ["open_link", { url: "https://github.com/acme/widgets/pull/7" }],
        ]);
      }
      expect(opened).toEqual([]);
    });

    it("leaves the app's own links and scripted clicks alone", () => {
      for (const [html, trusted] of [
        [`<a href="${location.origin}/s/x/?page=jobs" target="_blank">app</a>`, true],
        ['<a href="mailto:someone@example.com" target="_blank">mail</a>', true],
        ['<a href="https://github.com/" target="_blank">script</a>', false],
      ] as const) {
        const event = personClicks(link(html), {}, trusted);
        expect(event.defaultPrevented, html).toBe(false);
      }
      expect(invoked).toEqual([]);
    });

    it("opens a page from the app through the shell", () => {
      openExternal("https://github.com/acme/widgets");
      expect(invoked).toEqual([["open_link", { url: "https://github.com/acme/widgets" }]]);
      expect(opened).toEqual([]);
    });
  });

  it("opens a page from the app as a new window in a shell without the command", () => {
    openExternal("https://github.com/acme/widgets");
    expect(opened).toEqual([
      { href: "https://github.com/acme/widgets", target: "_blank", rel: "noreferrer" },
    ]);
  });
});
