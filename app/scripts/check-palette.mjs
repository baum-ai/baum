import { readdirSync, readFileSync } from "node:fs";
import { dirname, extname, join, relative, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import {
  arbitraryPaletteValues,
  paletteUtilityNames,
} from "../../viewer/src/palette-utility-names.ts";

const app = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const oss = resolve(app, "..");
const palette = join(oss, "viewer/src/palette.css");
const paletteUtilities = join(oss, "viewer/src/palette-utilities.css");
const markup = new Set([".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".html"]);
const extensions = new Set([
  ".css",
  ".ts",
  ".tsx",
  ".js",
  ".jsx",
  ".mjs",
  ".cjs",
  ".html",
  ".svg",
  ".rs",
]);
const literal = /#[\da-f]{3,8}\b|\b(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch|color)\(/i;
const property =
  /(?:--[\w-]+|\b(?:color|background(?:-color)?|backgroundColor|border(?:-[\w-]+)?|borderColor|outline(?:-color)?|outlineColor|fill|fillStyle|stroke|strokeStyle|stop-color|stopColor|box-shadow|boxShadow|shadowColor|text-shadow|textShadow))\s*["']?\s*[:=]\s*([^;{}\n]+)/gi;
const names =
  "aliceblue antiquewhite aqua aquamarine azure beige bisque black blanchedalmond blue blueviolet brown burlywood cadetblue chartreuse chocolate coral cornflowerblue cornsilk crimson cyan darkblue darkcyan darkgoldenrod darkgray darkgreen darkgrey darkkhaki darkmagenta darkolivegreen darkorange darkorchid darkred darksalmon darkseagreen darkslateblue darkslategray darkslategrey darkturquoise darkviolet deeppink deepskyblue dimgray dimgrey dodgerblue firebrick floralwhite forestgreen fuchsia gainsboro ghostwhite gold goldenrod gray green greenyellow grey honeydew hotpink indianred indigo ivory khaki lavender lavenderblush lawngreen lemonchiffon lightblue lightcoral lightcyan lightgoldenrodyellow lightgray lightgreen lightgrey lightpink lightsalmon lightseagreen lightskyblue lightslategray lightslategrey lightsteelblue lightyellow lime limegreen linen magenta maroon mediumaquamarine mediumblue mediumorchid mediumpurple mediumseagreen mediumslateblue mediumspringgreen mediumturquoise mediumvioletred midnightblue mintcream mistyrose moccasin navajowhite navy oldlace olive olivedrab orange orangered orchid palegoldenrod palegreen paleturquoise palevioletred papayawhip peachpuff peru pink plum powderblue purple rebeccapurple red rosybrown royalblue saddlebrown salmon sandybrown seagreen seashell sienna silver skyblue slateblue slategray slategrey snow springgreen steelblue tan teal thistle tomato turquoise violet wheat white whitesmoke yellow yellowgreen";
const systemNames =
  "accentcolor accentcolortext activetext buttonborder buttonface buttontext canvas canvastext field fieldtext graytext highlight highlighttext linktext mark marktext selecteditem selecteditemtext visitedtext";
const namedColor = new RegExp(`\\b(?:${`${names} ${systemNames}`.split(" ").join("|")})\\b`, "i");
const arbitraryUtility = new RegExp(
  `\\b(?:bg|text|border|fill|stroke|ring|outline|shadow|from|via|to|decoration|accent|caret)-\\[(?:${names.split(" ").join("|")})\\]`,
  "i",
);
const utility =
  /\b(?:bg|text|border|fill|stroke|ring|outline|shadow|from|via|to|decoration|accent|caret)-(?:black|white|(?:red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose|slate|gray|zinc|neutral|stone)-\d{2,3})\b/;

function files(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory())
      return ["precompiled", "generated"].includes(entry.name) ? [] : files(path);
    return extensions.has(extname(path)) && !/\.(test|spec)\./.test(path) ? [path] : [];
  });
}

function sourceFiles() {
  return [
    ...files(join(app, "src")),
    ...files(join(oss, "viewer/src")),
    ...files(join(oss, "desktop/icons")),
    ...files(join(oss, "desktop/src")),
    join(oss, "desktop/tauri.conf.json"),
    ...readdirSync(app)
      .filter((name) => name.endsWith(".html"))
      .map((name) => join(app, name)),
  ];
}
const mask = (text) => text.replace(/[^\n]/g, " ");
/** Blanks comments, keeping line numbers, so documentation may quote what
 *  the rules reject. */
function withoutComments(original) {
  return original
    .replace(/\/\*[\s\S]*?\*\//g, mask)
    .replace(/<!--[\s\S]*?-->/g, mask)
    .replace(/^[ \t]*\/\/[^\n]*/gm, mask);
}
function matches(source, expression) {
  const flags = expression.flags.includes("g") ? expression.flags : expression.flags + "g";
  return [...source.matchAll(new RegExp(expression.source, flags))];
}
export function isColorDefinition(path) {
  const themeDefinition =
    dirname(path) === join(oss, "viewer/src/themes") && extname(path) === ".css";
  // Fixed illustration and vendor artwork are independent of the UI theme.
  return (
    path === palette ||
    themeDefinition ||
    path === join(oss, "viewer/src/components/stumpy-artwork.ts") ||
    path === join(oss, "viewer/src/components/brand-artwork.ts")
  );
}

export function disallowedColorLines(original, inboxKeys = false) {
  const source = withoutComments(original);
  // These are stored filter keys, resolved through --inbox-*, not CSS values.
  const styling = inboxKeys
    ? source.replace(/(["'])(?:blue|green|red|amber|purple|grey)\1/g, mask)
    : source;
  const offending = [
    ...matches(source, literal),
    ...matches(source, utility),
    ...matches(source, arbitraryUtility),
    ...matches(styling, property).filter((match) =>
      namedColor.test(match[1].replace(/\b(?:var|url)\([^)]*\)/g, "")),
    ),
  ];
  const lines = original.split("\n");
  const lineNumbers = new Set(
    offending.map((match) => original.slice(0, match.index).split("\n").length),
  );
  return [...lineNumbers]
    .sort((a, b) => a - b)
    .map((line) => ({ line, text: lines[line - 1].trim() }));
}

function main() {
  const failures = [];
  const arbitrary = [];
  const names = paletteUtilityNames(readFileSync(paletteUtilities, "utf8"));
  let checked = 0;
  for (const path of sourceFiles()) {
    if (isColorDefinition(path)) continue;
    checked++;
    const source = readFileSync(path, "utf8");
    const lines = disallowedColorLines(source, path === join(app, "src/inboxes.ts"));
    for (const { line, text } of lines) failures.push(`${relative(oss, path)}:${line}: ${text}`);
    if (!markup.has(extname(path))) continue;
    for (const { line, match, replacement } of arbitraryPaletteValues(
      withoutComments(source),
      names,
    ))
      arbitrary.push(`${relative(oss, path)}:${line}: ${match} -> ${replacement}`);
  }
  if (failures.length) {
    console.error(
      "Define colors in viewer/src/palette.css or viewer/src/themes/*.css and use their tokens:\n" +
        failures.join("\n"),
    );
    process.exitCode = 1;
  }
  if (arbitrary.length) {
    console.error(
      "Use the palette utility (viewer/src/palette-utilities.css) rather than the arbitrary value:\n" +
        arbitrary.join("\n"),
    );
    process.exitCode = 1;
  }
  if (!failures.length && !arbitrary.length) {
    console.log(`Palette lint passed (${checked} source files).`);
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) main();
