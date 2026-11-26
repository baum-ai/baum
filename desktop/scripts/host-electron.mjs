// Downloads the Electron release for the host's platform into Electron's
// download cache (/cache/electron in the desktop-electron service), checked
// against the checksums the locked electron package ships, and prints its
// version and the zip's path. `just desktop::dev` runs it in Docker
// (scripts/dev.sh), then unpacks the zip on the host and runs the app there.
// usage: node scripts/host-electron.mjs <darwin|linux> <x64|arm64>
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";

const [platform, arch] = process.argv.slice(2);
if (!["darwin", "linux"].includes(platform) || !["x64", "arm64"].includes(arch)) {
  console.error("usage: host-electron.mjs <darwin|linux> <x64|arm64>");
  process.exit(2);
}
const require = createRequire(import.meta.url);
const electron = dirname(require.resolve("electron/package.json"));
const { version } = require("electron/package.json");
const checksums = require("electron/checksums.json");
// The @electron/get the electron package installs itself with.
const get = createRequire(join(electron, "package.json")).resolve("@electron/get");
const { downloadArtifact } = await import(pathToFileURL(get).href);
const zip = await downloadArtifact({
  version,
  artifactName: "electron",
  platform,
  arch,
  cacheRoot: process.env.electron_config_cache,
  checksums,
});
console.log(`host-electron: ${version} ${zip}`);
