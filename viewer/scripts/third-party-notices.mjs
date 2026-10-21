// Writes the notices for the npm packages compiled into the embedded viewer
// and local-app bundles (internal/viewer/dist, internal/localapp/dist):
//
//   node scripts/third-party-notices.mjs OUTPUT
//
// Run it from the viewer package with its and the local app's dependencies
// installed (../app); `just third-party-notices` does that in Docker. The
// bundles contain the runtime packages the sources import and everything those
// packages depend on, not the test, lint and build tooling. The roots are the
// viewer's peer dependencies; vite and tailwindcss are added alone because
// small parts of them (the preload helper, the CSS reset) end up in the output.
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";

const output = process.argv[2];
if (!output) {
  console.error("usage: node scripts/third-party-notices.mjs OUTPUT");
  process.exit(2);
}

const viewerDir = resolve(import.meta.dirname, "..");
const projects = [viewerDir, resolve(viewerDir, "../app")];
const roots = Object.keys(JSON.parse(readFileSync(join(viewerDir, "package.json"), "utf8")).peerDependencies);
const standalone = ["vite", "tailwindcss"];

const packages = new Map(); // name@version -> {name, version, dir}

function add(name, node) {
  if (name.startsWith("@types/")) return; // type declarations are not in the bundles
  packages.set(`${name}@${node.version}`, { name, version: node.version, dir: node.path });
}

function walk(deps) {
  for (const [name, node] of Object.entries(deps ?? {})) {
    if (!node.path) continue; // link: or unresolved
    add(name, node);
    walk(node.dependencies);
  }
}

for (const dir of projects) {
  const listing = JSON.parse(
    execFileSync("pnpm", ["ls", "--depth", "Infinity", "--json"], { cwd: dir, maxBuffer: 1 << 28 }).toString(),
  );
  const top = { ...listing[0].dependencies, ...listing[0].devDependencies };
  for (const name of roots) {
    if (!top[name]) throw new Error(`${dir}: ${name} is not installed`);
    add(name, top[name]);
    walk(top[name].dependencies);
  }
  for (const name of standalone) {
    if (!top[name]) throw new Error(`${dir}: ${name} is not installed`);
    add(name, top[name]);
  }
}

const licenseFile = /^(licen[cs]e|copying|notice|unlicense)([-._].*)?$/i;

function describe(pkg) {
  const manifest = JSON.parse(readFileSync(join(pkg.dir, "package.json"), "utf8"));
  let license = manifest.license ?? manifest.licenses?.map((l) => l.type).join(" OR ");
  if (typeof license === "object") license = license.type;
  const texts = readdirSync(pkg.dir)
    .filter((f) => licenseFile.test(f) && !f.endsWith(".js") && !f.endsWith(".ts"))
    .sort()
    .map((f) => readFileSync(join(pkg.dir, f), "utf8").replace(/\r\n/g, "\n").trim())
    .filter((t) => t.length > 0);
  let repository = manifest.repository;
  if (repository && typeof repository === "object") repository = repository.url;
  const author = typeof manifest.author === "object" ? manifest.author.name : manifest.author;
  return { license: license ?? "UNKNOWN", texts, repository: manifest.homepage ?? repository ?? "", author: author ?? "" };
}

const entries = [...packages.values()]
  .sort((a, b) => (a.name + a.version).localeCompare(b.name + b.version))
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
out.push(`npm packages compiled into the embedded viewer and local app (${entries.length} package versions)`);
out.push("");
out.push("Summary:");
for (const e of entries) out.push(`  ${e.name}@${e.version}  ${e.license}`);
out.push("");
for (const group of [...groups.values()].sort((a, b) => a.entries[0].name.localeCompare(b.entries[0].name))) {
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
  out.push("Packages that ship no licence file; the licence is the one their package.json declares:");
  out.push("");
  for (const e of unlicensed) {
    out.push(`${e.name}@${e.version} (${e.license})${e.author ? `  by ${e.author}` : ""}${e.repository ? `  ${e.repository}` : ""}`);
  }
  out.push("");
}
writeFileSync(output, `${out.join("\n")}\n`);
console.log(`wrote ${output}: ${entries.length} package versions, ${unlicensed.length} without a licence file`);
