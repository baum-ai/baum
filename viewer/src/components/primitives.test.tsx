import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { renderToStaticMarkup } from "react-dom/server";
import { compile } from "tailwindcss";
import { describe, expect, it } from "vitest";

import { cn } from "./classnames";
import { DialogActions, DialogDetail, DialogHeader, DialogTitle } from "./dialog-shell";
import { Field } from "./field";
import { ListRow } from "./list-row";
import { Menu, MenuItem, MenuSeparator } from "./menu";
import {
  dialogClassName,
  fieldClassName,
  LIST_ROW,
  MENU,
  menuItemClassName,
  SECTION_LABEL,
} from "./primitive-classes";
import { SectionLabel } from "./section-label";
import { Empty } from "./states";

const here = dirname(fileURLToPath(import.meta.url));
const tailwindDir = dirname(createRequire(import.meta.url).resolve("tailwindcss/package.json"));
async function loadStylesheet(id: string, base: string) {
  const path =
    id === "tailwindcss"
      ? join(tailwindDir, "index.css")
      : id.startsWith("tailwindcss/")
        ? join(tailwindDir, id.slice("tailwindcss/".length))
        : resolve(base, id);
  return { path, base: dirname(path), content: readFileSync(path, "utf8") };
}

/** The CSS Tailwind generates for `classes`, with the viewer's theme. */
async function css(classes: string) {
  const compiler = await compile(`@import "tailwindcss";\n@import "../viewer.css";`, {
    base: here,
    loadStylesheet,
  });
  return compiler.build(classes.split(/\s+/));
}

describe("Field", () => {
  it("labels its control and keeps a caller's width", () => {
    const html = renderToStaticMarkup(
      <Field label="Name">
        <input className={cn(fieldClassName(), "w-60")} />
      </Field>,
    );
    expect(html).toMatch(/^<label class="[^"]*text-xs font-medium text-secondary">Name<input/);
    expect(html).toContain("border-default");
    expect(html).toContain("w-60");
  });

  it("draws a select's chevron over the card colour", async () => {
    const select = fieldClassName({ kind: "select" });
    expect(select).toContain("bg-surface-card");
    expect(select).toContain("appearance-none");
    const generated = await css(select);
    expect(generated).toContain("background-color: var(--surface-card)");
    expect(generated).toMatch(
      /background-image: linear-gradient\(45deg,transparent 50%,var\(--text-tertiary\) 50%\)/,
    );
    expect(generated).toContain("background-position: calc(100% - 15px) 55%,calc(100% - 10px) 55%");
    expect(generated).toContain("background-size: 5px 5px");
  });
});

describe("dialog shell", () => {
  it("sizes the dialog and lets a wide one scroll", () => {
    expect(dialogClassName()).toContain("w-[36rem]");
    const wide = dialogClassName({ size: "wide" });
    expect(wide).toContain("w-[48rem]");
    expect(wide).not.toContain("w-[36rem]");
    expect(wide).toContain("overflow-y-auto");
  });

  it("sets the head's title and detail beside its aside, and rules the actions on request", () => {
    const html = renderToStaticMarkup(
      <>
        <DialogHeader aside={<button type="button">Close</button>}>
          <DialogTitle id="title">Check this computer</DialogTitle>
          <DialogDetail>What baum needs here.</DialogDetail>
        </DialogHeader>
        <DialogActions ruled>
          <button type="button">Done</button>
        </DialogActions>
      </>,
    );
    expect(html).toContain('<h2 id="title" class="text-lg leading-normal font-semibold">');
    expect(html).toContain('<p class="mt-0.5 text-xs text-tertiary">');
    expect(html).toContain("border-t border-subtle pt-4");
    expect(renderToStaticMarkup(<DialogActions>x</DialogActions>)).not.toContain("border-t");
  });
});

describe("ListRow", () => {
  it("marks itself for a row's menu button and merges the caller's padding", () => {
    const html = renderToStaticMarkup(<ListRow className="py-[0.65rem]">Repository</ListRow>);
    expect(html).toContain('data-list-row=""');
    expect(html).toContain("py-[0.65rem]");
    expect(html).not.toContain("py-[0.8rem]");
    expect(LIST_ROW).toContain("data-focused:bg-surface-sunken");
  });
});

describe("Menu", () => {
  it("is a menu of items, the stopping ones in the critical tone", () => {
    const html = renderToStaticMarkup(
      <Menu aria-label="Actions" className="fixed z-50">
        <MenuItem role="menuitem">Open</MenuItem>
        <MenuSeparator />
        <MenuItem role="menuitem" tone="danger">
          Remove
        </MenuItem>
      </Menu>,
    );
    expect(html).toMatch(/^<div role="menu" aria-label="Actions" class="[^"]*fixed z-50"/);
    expect(html).toContain("<hr ");
    expect(html).toContain('type="button"');
    expect(menuItemClassName({ tone: "danger" })).toContain("text-status-critical");
    expect(menuItemClassName({ tone: "danger" })).not.toContain("text-primary");
    expect(MENU).toContain("animate-drop-in");
  });

  it("drops into place with the theme's animation", async () => {
    const generated = await css("animate-drop-in");
    expect(generated).toContain("animation: var(--animate-drop-in)");
    expect(generated).toMatch(
      /@keyframes drop-in \{\s*from \{\s*opacity: 0;\s*transform: translateY\(-4px\);/,
    );
  });
});

describe("SectionLabel", () => {
  it("is a heading by default and text on request, without margin", () => {
    expect(renderToStaticMarkup(<SectionLabel>Checks</SectionLabel>)).toBe(
      `<h3 class="${SECTION_LABEL}">Checks</h3>`,
    );
    expect(renderToStaticMarkup(<SectionLabel as="dt">Checks</SectionLabel>)).toMatch(/^<dt /);
    expect(SECTION_LABEL).not.toMatch(/\bm[tbxy]?-/);
  });
});

describe("Empty", () => {
  it("keeps the quiet pane note by default", () => {
    const html = renderToStaticMarkup(<Empty title="No sessions">Start one.</Empty>);
    expect(html).toContain('<p class="font-medium text-secondary">No sessions</p>');
  });

  it("titles an empty page and sets its way forward apart", () => {
    const html = renderToStaticMarkup(
      <Empty variant="page" title="No repositories yet" action={<a href="/add">Add one</a>}>
        Pick GitHub repositories.
      </Empty>,
    );
    expect(html).toContain('<h2 class="mb-1 font-semibold text-primary">No repositories yet</h2>');
    expect(html).toContain("<p>Pick GitHub repositories.</p>");
    expect(html).toContain('<div class="mt-4"><a href="/add">Add one</a></div>');
  });
});
