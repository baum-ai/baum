import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { openOutsideLinksInBrowser } from "./outside-links";

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
  vi.stubGlobal("__TAURI_INTERNALS__", {});
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
