import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { compile } from "tailwindcss";
import { describe, expect, it } from "vitest";

/** The palette utilities `palette-utilities.css` declares (`text-secondary`,
 *  `bg-surface-card`, `shadow-card`, ...) must each resolve to the palette
 *  variable they are named after, and must not make Tailwind redeclare that
 *  variable: `--shadow-card: var(--shadow-card)` in Tailwind's theme layer
 *  would refer to itself. */
const here = dirname(fileURLToPath(import.meta.url));
const utilitiesCss = join(here, "palette-utilities.css");
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

/** Tailwind's theme namespaces, longest first, and the utility each names. */
const namespaces: [string, string][] = [
  ["text-color-", "text-"],
  ["border-color-", "border-"],
  ["color-", "bg-"],
  ["shadow-", "shadow-"],
];

/** Each `--<namespace>-<name>: var(--<token>)` line of the reference block. */
function mappings() {
  const source = readFileSync(utilitiesCss, "utf8");
  const block = source.match(/@theme inline reference \{([\s\S]*?)\n\}/)?.[1] ?? "";
  return [...block.matchAll(/--([\w-]+):\s*var\(--([\w-]+)\);/g)].map(
    ([, key = "", token = ""]) => {
      const found = namespaces.find(([namespace]) => key.startsWith(namespace));
      const utility = found ? found[1] + key.slice(found[0].length) : "";
      return { utility, token };
    },
  );
}

describe("palette utilities", () => {
  const list = mappings();

  it("names only tokens the palette defines", () => {
    const palette = readFileSync(join(here, "palette.css"), "utf8");
    expect(list.length).toBeGreaterThan(40);
    for (const { utility, token } of list) {
      expect(utility, token).not.toBe("");
      expect(palette, token).toContain(`--${token}:`);
    }
  });

  it("resolves each utility to its palette variable without redeclaring it", async () => {
    const compiler = await compile(`@import "tailwindcss";\n@import "./viewer.css";`, {
      base: here,
      loadStylesheet,
    });
    const css = compiler.build(list.map(({ utility }) => utility));
    for (const { utility, token } of list) {
      const rule = css.match(new RegExp(`\\.${utility} \\{([^}]*)\\}`))?.[1];
      expect(rule, utility).toContain(`var(--${token})`);
      expect(css, token).not.toMatch(new RegExp(`--${token}:\\s*var\\(--${token}\\)`));
    }
  });
});
