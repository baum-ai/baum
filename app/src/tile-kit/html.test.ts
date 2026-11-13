import { afterEach, describe, expect, it } from "vitest";
import { frame, type Frame } from "./harness";

// Payloads that turn markup into script when a renderer parses data as HTML.
const XSS = [
  "<script>alert(1)</script>",
  "<img src=x onerror=alert(1)>",
  '"><img src=x onerror=alert(1)>',
  "'><svg onload=alert(1)>",
  "<svg><script>alert(1)</script></svg>",
  "<svg><a xlink:href='javascript:alert(1)'><text>x</text></a></svg>",
  "<math><mtext><table><mglyph><style><img src=x onerror=alert(1)>",
  "<math href='javascript:alert(1)'>x</math>",
  "<iframe srcdoc='<script>alert(1)</script>'></iframe>",
  '<a href="javascript:alert(1)">x</a>',
  "javascript:alert(1)",
  "<!--<img src=x onerror=alert(1)>-->",
  "</p><p onclick=alert(1)>",
  "<template><img src=x onerror=alert(1)></template>",
  "<noscript><p title='</noscript><img src=x onerror=alert(1)>'>",
  "${alert(1)}",
  "{{constructor.constructor('alert(1)')()}}",
  "<style>body{background:url(https://example.invalid/x)}</style>",
  "<base href='https://example.invalid/'>",
  "<meta http-equiv='refresh' content='0;url=https://example.invalid/'>",
];

let current: Frame | null = null;
function kit() {
  current = frame();
  return current;
}
afterEach(() => {
  current?.remove();
  current = null;
});

/** Runs baum.html in the frame (so the tag's strings are a real template
 *  object) and returns a container holding the result. */
function render(f: Frame, expression: string, values: Record<string, unknown> = {}) {
  const win = f.window as unknown as Record<string, unknown>;
  // Values arrive in the frame's own realm, as data from the page does.
  win.__values = (f.window.JSON.parse(JSON.stringify(values)) as unknown) ?? {};
  f.run(
    `window.__out = document.createElement("div"); try { window.__out.append((() => { const v = window.__values; return ${expression}; })()); } catch (error) { window.__error = error; }`,
  );
  const error = win.__error as Error | undefined;
  win.__error = undefined;
  if (error) throw error;
  return win.__out as HTMLElement;
}

describe("baum.html", () => {
  it("keeps every payload as text in a text position", () => {
    const f = kit();
    for (const payload of XSS) {
      const out = render(f, "baum.html`<p>${v.x}</p>`", { x: payload });
      expect(out.querySelectorAll("*")).toHaveLength(1);
      expect(out.firstElementChild!.textContent).toBe(payload);
    }
  });

  it("keeps every payload as an attribute's value", () => {
    const f = kit();
    for (const payload of XSS) {
      const out = render(f, "baum.html`<p title=\"${v.x}\" class=${v.x} data-x='${v.x}'>x</p>`", {
        x: payload,
      });
      const p = out.firstElementChild!;
      expect(out.querySelectorAll("*")).toHaveLength(1);
      expect(p.getAttribute("title")).toBe(payload);
      expect(p.getAttribute("class")).toBe(payload);
      expect(p.getAttribute("data-x")).toBe(payload);
      expect(p.getAttributeNames().sort()).toEqual(["class", "data-x", "title"]);
    }
  });

  it("composes nested templates and arrays", () => {
    const f = kit();
    const out = render(
      f,
      "baum.html`<ul>${v.items.map((item) => baum.html`<li><b>${item}</b></li>`)}${null}${false}${undefined}</ul><p>${3} ${0}</p>`",
      { items: XSS.slice(0, 3) },
    );
    const items = out.querySelectorAll("li > b");
    expect([...items].map((item) => item.textContent)).toEqual(XSS.slice(0, 3));
    expect(out.querySelectorAll("script, img, svg")).toHaveLength(0);
    expect(out.querySelector("p")!.textContent).toBe("3 0");
  });

  it("keeps rows in their table instead of moving them out", () => {
    const f = kit();
    const out = render(
      f,
      "baum.html`<table><tbody>${v.rows.map((row) => baum.html`<tr><td>${row}</td></tr>`)}</tbody></table>`",
      { rows: ["a", "b"] },
    );
    expect(out.querySelectorAll("table > tbody > tr > td")).toHaveLength(2);
    expect(out.firstElementChild!.nodeName).toBe("TABLE");
  });

  it("joins static and interpolated parts of an attribute", () => {
    const f = kit();
    const out = render(f, 'baum.html`<p class="row ${v.tone} ${v.list}">x</p>`', {
      tone: "is-<b>",
      list: ["a", "b"],
    });
    expect(out.firstElementChild!.getAttribute("class")).toBe("row is-<b> a b");
  });

  it("drops an attribute given null or false and sets one given true", () => {
    const f = kit();
    const out = render(
      f,
      "baum.html`<input disabled=${v.no} readonly=${v.yes} title=${v.nothing}><details open=${v.no}></details>`",
      { no: false, yes: true, nothing: null },
    );
    const input = out.querySelector("input")!;
    expect(input.hasAttribute("disabled")).toBe(false);
    expect(input.getAttribute("readonly")).toBe("");
    expect(input.hasAttribute("title")).toBe(false);
    expect(out.querySelector("details")!.hasAttribute("open")).toBe(false);
  });

  it("allows data: and blob: URLs, fragments and relative paths", () => {
    const f = kit();
    for (const url of ["data:image/png;base64,AAAA", "blob:null/1", "#part", "a/b", ""]) {
      const out = render(f, "baum.html`<img src=${v.url}><a href=${v.url}>x</a>`", { url });
      expect(out.querySelector("img")!.getAttribute("src")).toBe(url);
    }
  });

  it.each([
    ["javascript:alert(1)"],
    [" JaVaScRiPt:alert(1)"],
    ["java\tscript:alert(1)"],
    ["java\nscript:alert(1)"],
    ["\u0001javascript:alert(1)"],
    ["vbscript:x"],
    ["https://example.invalid/x"],
    ["HTTP://example.invalid/x"],
  ])("refuses the URL %j", (url) => {
    const f = kit();
    expect(() => render(f, "baum.html`<a href=${v.url}>x</a>`", { url })).toThrow(/href/);
    expect(() => render(f, "baum.html`<img src=${v.url}>`", { url })).toThrow(/src/);
    expect(() =>
      render(f, "baum.html`<svg><a href=${v.url}><text>x</text></a></svg>`", { url }),
    ).toThrow();
  });

  it.each([
    ["an interpolated handler", "baum.html`<img onerror=${v.x}>`"],
    ["an interpolated handler in upper case", "baum.html`<img ONERROR=${v.x}>`"],
    ["a static handler", 'baum.html`<button onclick="go()">x</button>`'],
    ["a static javascript: URL", 'baum.html`<a href="javascript:void 0">x</a>`'],
    ["a static external image", 'baum.html`<img src="https://example.invalid/x.png">`'],
    [
      "an SVG animation of href",
      'baum.html`<svg><a><animate attributeName="href" to="x"></animate></a></svg>`',
    ],
    ["an SVG xlink:href", "baum.html`<svg><a xlink:href=${v.js}><text>x</text></a></svg>`"],
    ["a script element", "baum.html`<script>alert(1)</script>`"],
    ["an SVG script element", "baum.html`<svg><script>alert(1)</script></svg>`"],
    ["an iframe", "baum.html`<iframe></iframe>`"],
    ["an object", "baum.html`<object></object>`"],
    ["an embed", "baum.html`<embed>`"],
    ["a form", "baum.html`<form></form>`"],
    ["a base", "baum.html`<base>`"],
    ["a meta", "baum.html`<meta>`"],
    ["a link", "baum.html`<link>`"],
    ["a value as an attribute name", "baum.html`<p ${v.x}=1>x</p>`"],
    ["a value as a tag name", "baum.html`<${v.x}>x</p>`"],
    ["a value in a comment", "baum.html`<p><!-- ${v.x} --></p>`"],
    ["a value in a style element", "baum.html`<style>p { color: ${v.x} }</style>`"],
    ["a value in a textarea", "baum.html`<textarea>${v.x}</textarea>`"],
    ["an object", "baum.html`<p>${{ a: 1 }}</p>`"],
    ["an object in an attribute", "baum.html`<p title=${{ a: 1 }}>x</p>`"],
    ["a node in an attribute", "baum.html`<p title=${document.body}>x</p>`"],
    ["a call that is not a tagged template", 'baum.html("<p>x</p>")'],
  ])("refuses %s", (_, expression) => {
    const f = kit();
    let thrown: unknown;
    try {
      render(f, expression, { x: "onclick", js: "javascript:alert(1)" });
    } catch (error) {
      thrown = error;
    }
    // The frame's own TypeError, from another realm than this test's.
    expect((thrown as Error | undefined)?.name).toBe("TypeError");
    expect((thrown as Error).message).toMatch(/^baum\.html/);
  });

  it("returns a new fragment every call from one cached template", () => {
    const f = kit();
    f.run(
      "window.__make = (x) => baum.html`<p class=${x}>${x}</p>`; window.__a = __make('a'); window.__b = __make('b');",
    );
    const win = f.window as unknown as Record<string, DocumentFragment>;
    expect(win.__a!.firstElementChild!.className).toBe("a");
    expect(win.__b!.firstElementChild!.textContent).toBe("b");
    expect(win.__a!.firstElementChild).not.toBe(win.__b!.firstElementChild);
  });

  it("never runs a payload", async () => {
    const f = kit();
    const win = f.window as unknown as Record<string, unknown>;
    win.alert = () => {
      win.__ran = true;
    };
    for (const payload of XSS) {
      const out = render(f, "baum.html`<p title=${v.x}>${v.x}</p>`", { x: payload });
      f.document.body.append(out);
    }
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(win.__ran).toBeUndefined();
  });
});
