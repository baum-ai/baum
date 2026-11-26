// pnpm may skip electron's own install script, which downloads the Electron
// binary; run it when the binary is missing.
import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";

const require = createRequire(import.meta.url);
const dir = dirname(require.resolve("electron/package.json"));
if (!existsSync(join(dir, "path.txt"))) {
  execFileSync(process.execPath, [join(dir, "install.js")], { stdio: "inherit" });
}
