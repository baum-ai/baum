// Writes the desktop app's own third-party notices: Electron's licence, a
// pointer to the Chromium licences Electron ships (LICENSES.chromium.html),
// and the licences of the npm packages the app bundles at run time (the
// production dependencies in package.json and theirs):
//
//   node scripts/third-party-notices.mjs OUTPUT
//
// Run it from desktop/ with the dependencies installed; scripts/
// third-party-notices.sh does, in the desktop-electron container. The
// development tooling (electron-builder, esbuild, vitest...) is not shipped
// and not listed.
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { createRequire } from "node:module";

const output = process.argv[2];
if (!output) {
  process.stderr.write("usage: node scripts/third-party-notices.mjs OUTPUT\n");
  process.exit(2);
}

const desktopDir = resolve(import.meta.dirname, "..");
const require = createRequire(join(desktopDir, "package.json"));
const electronDir = dirname(require.resolve("electron/package.json"));
const electron = JSON.parse(readFileSync(join(electronDir, "package.json"), "utf8"));
const electronLicense = readFileSync(join(electronDir, "LICENSE"), "utf8")
  .replace(/\r\n/g, "\n")
  .trim();

const packages = new Map(); // name@version -> {name, version, dir}

function walk(deps) {
  for (const [name, node] of Object.entries(deps ?? {})) {
    if (!node.path || name.startsWith("@types/")) continue;
    const key = `${name}@${node.version}`;
    if (packages.has(key)) continue;
    packages.set(key, { name, version: node.version, dir: node.path });
    walk(node.dependencies);
  }
}

const listing = JSON.parse(
  execFileSync("pnpm", ["ls", "--prod", "--depth", "Infinity", "--json"], {
    cwd: desktopDir,
    maxBuffer: 1 << 28,
  }).toString(),
);
walk(listing[0]?.dependencies);

const licenseFile = /^(licen[cs]e|copying|notice|unlicense)([-._].*)?$/i;

function describe(pkg) {
  const manifest = JSON.parse(readFileSync(join(pkg.dir, "package.json"), "utf8"));
  let license = manifest.license ?? manifest.licenses?.map((l) => l.type).join(" OR ");
  if (typeof license === "object") license = license.type;
  const texts = readdirSync(pkg.dir)
    .filter((f) => licenseFile.test(f) && !f.endsWith(".js") && !f.endsWith(".ts"))
    .toSorted()
    .map((f) => readFileSync(join(pkg.dir, f), "utf8").replace(/\r\n/g, "\n").trim())
    .filter((t) => t.length > 0);
  let repository = manifest.repository;
  if (repository && typeof repository === "object") repository = repository.url;
  const author = typeof manifest.author === "object" ? manifest.author.name : manifest.author;
  return {
    license: license ?? "UNKNOWN",
    texts,
    repository: manifest.homepage ?? repository ?? "",
    author: author ?? "",
  };
}

const entries = [...packages.values()]
  .toSorted((a, b) => (a.name + a.version).localeCompare(b.name + b.version))
  .map((pkg) => ({ ...pkg, ...describe(pkg) }));

// Packages that share one licence text are listed together under it.
const groups = new Map();
const unlicensed = [];
for (const entry of entries) {
  if (entry.texts.length === 0) {
    unlicensed.push(entry);
    continue;
  }
  const key = createHash("sha256").update(entry.texts.join("\n\n")).digest("hex");
  if (!groups.has(key)) groups.set(key, { texts: entry.texts, entries: [] });
  groups.get(key).entries.push(entry);
}

const rule = "-".repeat(78);
const out = [];
out.push(`Electron ${electron.version}  https://www.electronjs.org`);
out.push("");
out.push(electronLicense);
out.push("");
out.push("Electron includes Chromium, Node.js, V8, ffmpeg and other components, each");
out.push("under its own licence. Their licences are in LICENSES.chromium.html, which the");
out.push("app ships beside its executable (Linux, Windows) or in Contents/Resources");
out.push("(macOS), with Electron's licence as LICENSE.electron.txt.");
out.push("");
out.push(rule);
out.push(`npm packages bundled with the desktop app (${entries.length} package versions)`);
out.push("");
if (entries.length === 0) {
  out.push("None.");
  out.push("");
} else {
  out.push("Summary:");
  for (const e of entries) out.push(`  ${e.name}@${e.version}  ${e.license}`);
  out.push("");
}
for (const group of [...groups.values()].toSorted((a, b) =>
  a.entries[0].name.localeCompare(b.entries[0].name),
)) {
  out.push(rule);
  for (const e of group.entries) {
    out.push(`${e.name}@${e.version} (${e.license})${e.repository ? `  ${e.repository}` : ""}`);
  }
  out.push("");
  out.push(group.texts.join("\n\n"));
  out.push("");
}
if (unlicensed.length > 0) {
  out.push(rule);
  out.push(
    "Packages that ship no licence file; the licence is the one their package.json declares:",
  );
  out.push("");
  for (const e of unlicensed) {
    out.push(
      `${e.name}@${e.version} (${e.license})${e.author ? `  by ${e.author}` : ""}${e.repository ? `  ${e.repository}` : ""}`,
    );
  }
  out.push("");
}
writeFileSync(output, `${out.join("\n")}\n`);
process.stdout.write(
  `wrote ${output}: Electron ${electron.version}, ${entries.length} package versions, ${unlicensed.length} without a licence file\n`,
);
