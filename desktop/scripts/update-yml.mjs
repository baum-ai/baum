#!/usr/bin/env node
// Writes, rewrites and checks electron-updater's update files (latest.yml,
// latest-mac.yml, latest-linux.yml, latest-linux-arm64.yml), which give the
// SHA-512 and size of every file an update downloads:
//
//   node scripts/update-yml.mjs write VERSION DIR NAME FILE...
//   node scripts/update-yml.mjs rewrite VERSION DIR [NAME...]
//   node scripts/update-yml.mjs check VERSION DIR [NAME...]
//   node scripts/update-yml.mjs publish VERSION DIR [NAME...]
//
// write creates DIR/NAME listing each FILE (a file name in DIR), the first
// as the main one. rewrite recomputes the hashes and sizes of an existing
// file (electron-builder's, after signing changed a bundle) from DIR. check
// fails unless every listed file is in DIR with that hash and size and the
// file announces VERSION; it prints each listed file name once. Without
// NAMEs, rewrite, check and publish take every latest*.yml in DIR.
//
// The files list plain names until the release publishes them: publish
// turns each name into https://downloads.usebaum.com/update/vVERSION/NAME,
// which counts the download and redirects to that file on the GitHub
// release (tools/desktop-downloads in the monorepo). The app reads the
// update files from its channel's feed (the latest release, or the
// desktop-rc-updates pre-release, which holds only the update files), and
// electron-updater resolves a plain name against that feed, so a published
// file must name the release itself. check accepts a plain name or exactly
// that URL, and nothing else.
//
// Uses only Node.js; the format is the subset of YAML electron-builder
// writes (scalars, and the list of mappings under files).
import { createHash } from "node:crypto";
import { createReadStream, readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { basename, join } from "node:path";

const NAME = /^latest(-mac|-linux|-linux-arm64)?\.yml$/;
const FILE = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;
/** Where published update files point (publish). */
const DOWNLOADS = "https://downloads.usebaum.com/update";

function published(version, name) {
  return `${DOWNLOADS}/v${version}/${name}`;
}

/** The file name a listed URL stands for: a plain name, or the published
 *  URL of a file of VERSION. */
function fileName(version, url) {
  const prefix = `${DOWNLOADS}/v${version}/`;
  const name = typeof url === "string" && url.startsWith(prefix) ? url.slice(prefix.length) : url;
  if (typeof name !== "string" || !FILE.test(name)) {
    throw new Error(`'${url}' is not a plain file name or ${prefix} and one`);
  }
  return name;
}

function fail(message) {
  console.error(`update-yml: ${message}`);
  process.exit(1);
}

function scalar(raw) {
  const value = raw.trim();
  if (value.startsWith("'") && value.endsWith("'") && value.length >= 2) {
    return value.slice(1, -1).replaceAll("''", "'");
  }
  if (value.startsWith('"') && value.endsWith('"') && value.length >= 2) {
    return JSON.parse(value);
  }
  return value;
}

/** Parses the subset of YAML electron-builder writes. */
function parse(text) {
  const doc = { files: [] };
  let inFiles = false;
  let current = null;
  for (const [index, line] of text.split("\n").entries()) {
    if (line.trim() === "") continue;
    const where = `line ${index + 1}`;
    let match = /^([A-Za-z0-9]+):(?: (.*))?$/.exec(line);
    if (match) {
      const [, key, value = ""] = match;
      inFiles = key === "files";
      current = null;
      if (inFiles) {
        if (value.trim() !== "") throw new Error(`${where}: files is not a list`);
        continue;
      }
      doc[key] = scalar(value);
      continue;
    }
    if (!inFiles) throw new Error(`${where}: unexpected '${line}'`);
    match = /^  - ([A-Za-z0-9]+): (.*)$/.exec(line);
    if (match) {
      current = { [match[1]]: scalar(match[2]) };
      doc.files.push(current);
      continue;
    }
    match = /^    ([A-Za-z0-9]+): (.*)$/.exec(line);
    if (match && current) {
      current[match[1]] = scalar(match[2]);
      continue;
    }
    throw new Error(`${where}: unexpected '${line}'`);
  }
  return doc;
}

function quote(value) {
  const text = String(value);
  // Plain unless YAML would read it as something other than a string.
  const plain =
    /^[A-Za-z0-9._/+=-]+$/.test(text) &&
    !/^[0-9]+(\.[0-9]+)?$/.test(text) &&
    text !== "true" &&
    text !== "false";
  return plain ? text : `'${text.replaceAll("'", "''")}'`;
}

/** Writes a document in the layout electron-builder uses. Numbers stay
 *  plain; the version and the date are quoted when YAML would read them
 *  otherwise. */
function format(doc) {
  const lines = [`version: ${quote(doc.version)}`, "files:"];
  for (const file of doc.files) {
    const entries = Object.entries(file);
    entries.forEach(([key, value], i) => {
      const text = key === "size" || key === "blockMapSize" ? String(value) : quote(value);
      lines.push(`${i === 0 ? "  - " : "    "}${key}: ${text}`);
    });
  }
  for (const [key, value] of Object.entries(doc)) {
    if (key === "version" || key === "files") continue;
    lines.push(`${key}: ${quote(value)}`);
  }
  return `${lines.join("\n")}\n`;
}

async function sha512(path) {
  const hash = createHash("sha512");
  for await (const chunk of createReadStream(path)) hash.update(chunk);
  return hash.digest("base64");
}

function listed(dir, name) {
  const path = join(dir, name);
  let stat;
  try {
    stat = statSync(path);
  } catch {
    throw new Error(`${name} is not in ${dir}`);
  }
  if (!stat.isFile()) throw new Error(`${name} is not a file`);
  return { path, size: stat.size };
}

/** The file a listed URL stands for in DIR, described. */
async function describe(version, dir, url) {
  const name = fileName(version, url);
  const { path, size } = listed(dir, name);
  return { url, name, sha512: await sha512(path), size };
}

function names(dir, given) {
  const found = given.length > 0 ? given : readdirSync(dir).filter((name) => NAME.test(name));
  if (found.length === 0) fail(`${dir} holds no update file`);
  for (const name of found) {
    if (!NAME.test(basename(name)) || name !== basename(name)) {
      fail(`${name} is not an update file name`);
    }
  }
  return found;
}

function read(dir, name) {
  try {
    return parse(readFileSync(join(dir, name), "utf8"));
  } catch (error) {
    fail(`${name}: ${error.message}`);
  }
}

async function write(version, dir, name, files) {
  if (files.length === 0) fail("write needs at least one file");
  names(dir, [name]);
  const described = [];
  for (const file of files) {
    const { url, sha512, size } = await describe(version, dir, file).catch((error) =>
      fail(`${name}: ${error.message}`),
    );
    described.push({ url, sha512, size });
  }
  const main = described[0];
  const doc = {
    version,
    files: described,
    path: main.url,
    sha512: main.sha512,
    releaseDate: new Date().toISOString(),
  };
  writeFileSync(join(dir, name), format(doc));
  await check(version, dir, [name]);
}

async function rewrite(version, dir, given) {
  for (const name of names(dir, given)) {
    const doc = read(dir, name);
    for (const file of doc.files) {
      const fresh = await describe(version, dir, file.url).catch((error) =>
        fail(`${name}: ${error.message}`),
      );
      file.sha512 = fresh.sha512;
      file.size = fresh.size;
    }
    const main = doc.files.find((file) => file.url === doc.path);
    if (!main) fail(`${name}: path ${doc.path} is not among its files`);
    doc.sha512 = main.sha512;
    writeFileSync(join(dir, name), format(doc));
  }
  await check(version, dir, given);
}

async function check(version, dir, given) {
  const seen = new Set();
  let failed = false;
  for (const name of names(dir, given)) {
    const doc = read(dir, name);
    const problem = (message) => {
      console.error(`FAIL ${name}: ${message}`);
      failed = true;
    };
    if (doc.version !== version) problem(`announces version '${doc.version}', not ${version}`);
    if (doc.files.length === 0) problem("lists no files");
    for (const file of doc.files) {
      let fresh;
      try {
        fresh = await describe(version, dir, file.url);
      } catch (error) {
        problem(error.message);
        continue;
      }
      if (String(file.size) !== String(fresh.size)) {
        problem(`${fresh.name} is ${fresh.size} bytes, not ${file.size}`);
      }
      if (file.sha512 !== fresh.sha512) problem(`${fresh.name} does not have the listed SHA-512`);
      seen.add(fresh.name);
    }
    const main = doc.files.find((file) => file.url === doc.path);
    if (!main) problem(`path ${doc.path} is not among its files`);
    else if (doc.sha512 !== main.sha512) problem(`its sha512 is not that of ${doc.path}`);
    if (!failed) console.error(`ok   ${name} describes ${doc.files.map((f) => f.url).join(", ")}`);
  }
  if (failed) process.exit(1);
  for (const name of seen) console.log(name);
}

async function publish(version, dir, given) {
  for (const name of names(dir, given)) {
    const doc = read(dir, name);
    let path;
    try {
      path = published(version, fileName(version, doc.path));
      for (const file of doc.files) file.url = published(version, fileName(version, file.url));
    } catch (error) {
      fail(`${name}: ${error.message}`);
    }
    doc.path = path;
    writeFileSync(join(dir, name), format(doc));
  }
  await check(version, dir, given);
}

const [command, version, dir, ...rest] = process.argv.slice(2);
const semver = /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$/;
if (!version || !dir || !semver.test(version)) {
  fail(
    "usage: update-yml.mjs write VERSION DIR NAME FILE... | rewrite VERSION DIR [NAME...] | check VERSION DIR [NAME...] | publish VERSION DIR [NAME...]",
  );
}
switch (command) {
  case "write":
    await write(version, dir, rest[0] ?? "", rest.slice(1));
    break;
  case "rewrite":
    await rewrite(version, dir, rest);
    break;
  case "check":
    await check(version, dir, rest);
    break;
  case "publish":
    await publish(version, dir, rest);
    break;
  default:
    fail(`unknown command '${command}'`);
}
