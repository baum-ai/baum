import assert from "node:assert/strict";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { disallowedColorLines, isColorDefinition } from "./check-palette.mjs";
import {
  arbitraryPaletteValues,
  paletteUtilityNames,
} from "../../viewer/src/palette-utility-names.ts";

test("names the palette utility that replaces an arbitrary palette value", () => {
  const names = paletteUtilityNames(
    readFileSync(new URL("../../viewer/src/palette-utilities.css", import.meta.url), "utf8"),
  );
  const found = (source) =>
    arbitraryPaletteValues(source, names).map(({ match, replacement }) => [match, replacement]);
  assert.deepEqual(
    found('"text-(--text-secondary) hover:bg-(--surface-hover) border-b-(--border-strong)"'),
    [
      ["text-(--text-secondary)", "text-secondary"],
      ["bg-(--surface-hover)", "bg-surface-hover"],
      ["border-b-(--border-strong)", "border-b-strong"],
    ],
  );
  assert.deepEqual(found("`shadow-(--shadow-overlay) divide-(--border-subtle)`"), [
    ["shadow-(--shadow-overlay)", "shadow-overlay"],
    ["divide-(--border-subtle)", "divide-subtle"],
  ]);
  assert.deepEqual(found('"rounded-(--radius-sm) sm:rounded-tl-(--radius-lg)"'), [
    ["rounded-(--radius-sm)", "rounded-sm"],
    ["rounded-tl-(--radius-lg)", "rounded-tl-lg"],
  ]);
  // Tokens without a utility for that property, and non-palette variables.
  assert.deepEqual(
    found(
      '"bg-(--brand-forest) bg-(--border-subtle) outline-(--border-focus) shadow-(--action-shadow) rounded-(--panel-radius) rounded-(--radius-full) text-(--site-ink)"',
    ),
    [],
  );
});

test("rejects literal and utility colors across CSS, JSX and native markup", () => {
  for (const source of [
    ".row { color: #123456; }",
    ".row { background: rgb(1 2 3 / 50%); }",
    ".row { border-color: hsl(120 50% 50%); }",
    ".row { color: oklch(50% 0.1 30); }",
    ".row { color: rebeccapurple; }",
    ".row { box-shadow: 0 0 4px black; }",
    ".row { --local-color: red; }",
    ".row { color:\n  white; }",
    '<path fill="blue" />',
    '<div style={{ backgroundColor: "red" }} />',
    '<div className="bg-red-500 text-white" />',
    '<div className="border-[purple]" />',
    '<div style={{ color: "CanvasText" }} />',
  ]) {
    assert.notEqual(disallowedColorLines(source).length, 0, source);
  }
});

test("allows tokens, derived colors and non-color keywords", () => {
  for (const source of [
    ".row { color: var(--text-primary); }",
    ".row { background: color-mix(in srgb, var(--status-positive) 20%, transparent); }",
    '<path fill="currentColor" stroke="none" />',
    '<div className="bg-(--surface-card)" />',
    "/* color: #123456; */\n.row { color: inherit; }",
    '// color: red\nconst style = { color: "var(--text-primary)" };',
    '<!-- fill="red" -->',
    "type Label = { color: string };",
  ]) {
    assert.deepEqual(disallowedColorLines(source), [], source);
  }
});

test("keeps diagnostic line numbers and does not waive literal colors in inbox metadata", () => {
  assert.deepEqual(disallowedColorLines("/* example\n color: red\n */\n.x { color: #abc; }"), [
    { line: 4, text: ".x { color: #abc; }" },
  ]);
  assert.deepEqual(disallowedColorLines('color: "red",', true), []);
  assert.notEqual(disallowedColorLines('color: "#abc",', true).length, 0);
});

test("allows definitions only in palette, theme and the two fixed artwork files", () => {
  const oss = fileURLToPath(new URL("../../", import.meta.url));
  for (const file of [
    "viewer/src/palette.css",
    "viewer/src/themes/custom.css",
    "viewer/src/components/stumpy-artwork.ts",
    "viewer/src/components/brand-artwork.ts",
  ]) {
    assert.equal(isColorDefinition(resolve(oss, file)), true, file);
  }
  for (const file of [
    "app/src/style.css",
    "app/src/themes.css",
    "viewer/src/components/stumpy.tsx",
    "viewer/src/components/brand-logo.tsx",
    "desktop/icons/source.svg",
  ]) {
    assert.equal(isColorDefinition(resolve(oss, file)), false, file);
  }
});
