import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { STUMPY_ARTWORK } from "../../viewer/src/components/stumpy-artwork.ts";

const desktop = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const css = readFileSync(join(desktop, "../viewer/src/palette.css"), "utf8");
const light = css.split(':root[data-theme="dark"]')[0].replace(/\/\*[\s\S]*?\*\//g, "");
const tokens = Object.fromEntries(
  [...light.matchAll(/(--[\w-]+):\s*([^;]+);/g)].map((match) => [match[1], match[2]]),
);
for (const [name, value] of Object.entries(STUMPY_ARTWORK)) {
  const token = "--stumpy-" + name.replace(/[A-Z]/g, (letter) => "-" + letter.toLowerCase());
  tokens[token] = value;
}
function color(name, seen = new Set()) {
  if (!tokens[name] || seen.has(name)) throw new Error(`Invalid icon palette token: ${name}`);
  seen.add(name);
  const value = tokens[name].replace(/var\((--[\w-]+)\)/g, (_, token) =>
    color(token, new Set(seen)),
  );
  // Native icon templates use solid seeds or fixed artwork colors, so their
  // rasterizer does not need to implement the UI's CSS color-mix recipes.
  if (!/^#[\da-f]{3,8}$/i.test(value)) throw new Error(`Icon needs a solid color: ${name}`);
  return value;
}
mkdirSync(join(desktop, "target"), { recursive: true });
for (const [input, output] of [
  ["icons/source.svg", "icon-source.svg"],
  ...["idle", "running", "attention", "inbox"].map((state) => [
    `icons/tray/${state}.svg`,
    `tray-${state}.svg`,
  ]),
]) {
  const svg = readFileSync(join(desktop, input), "utf8");
  writeFileSync(
    join(desktop, "target", output),
    svg.replace(/var\((--[\w-]+)\)/g, (_, token) => color(token)),
  );
}
