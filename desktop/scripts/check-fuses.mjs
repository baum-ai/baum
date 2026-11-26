// Checks that a packaged app's Electron fuses are the ones
// electron-builder.mjs's `electronFuses` asks for:
//
//   node scripts/check-fuses.mjs <the app's executable, or baum.app>
//
// It reads the fuse wire with @electron/fuses, which electron-builder flips
// the fuses with (it comes with electron-builder, not as a dependency of its
// own), and exits non-zero naming every fuse that differs.
import { createRequire } from "node:module";
import config from "../electron-builder.mjs";

const require = createRequire(import.meta.url);
const builder = createRequire(require.resolve("electron-builder"));
const lib = createRequire(builder.resolve("app-builder-lib"));
const { FuseV1Options, getCurrentFuseWire } = lib("@electron/fuses");

const app = process.argv[2];
if (!app) {
  console.error("usage: node scripts/check-fuses.mjs <app executable or .app>");
  process.exit(2);
}

const ENABLE = "1".charCodeAt(0);
const DISABLE = "0".charCodeAt(0);
const wire = await getCurrentFuseWire(app);
const wrong = [];
for (const [key, wanted] of Object.entries(config.electronFuses)) {
  // electron-builder's runAsNode is @electron/fuses's RunAsNode.
  const fuse = FuseV1Options[key[0].toUpperCase() + key.slice(1)];
  if (fuse === undefined) {
    wrong.push(`${key}: not a fuse @electron/fuses knows`);
    continue;
  }
  const state = wire[fuse];
  if (state !== (wanted ? ENABLE : DISABLE)) {
    const found = state === undefined ? "missing" : String.fromCharCode(state);
    wrong.push(`${key}: wanted ${wanted ? "on" : "off"}, found ${found}`);
  }
}
if (wrong.length > 0) {
  console.error(`check-fuses: ${app} has the wrong fuses:\n  ${wrong.join("\n  ")}`);
  process.exit(1);
}
console.log(`check-fuses: ${app}: ${Object.keys(config.electronFuses).join(", ")} as configured`);
