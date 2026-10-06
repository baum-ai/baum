import { readdirSync, readFileSync, statSync } from "node:fs";
import { builtinModules } from "node:module";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import manifest from "../package.json" with { type: "json" };

/** The package ships its source, and a host resolves the source's imports
 *  from its own dependencies. So everything the shipped source imports by
 *  name must be a peer dependency the host is told to install. */
function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return sources(path);
    return /\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name) ? [path] : [];
  });
}

function packageName(specifier: string): string {
  const parts = specifier.split("/");
  return specifier.startsWith("@") ? parts.slice(0, 2).join("/") : parts[0]!;
}

describe("dependencies", () => {
  it("imports only peer dependencies, Node built-ins and itself by name", () => {
    const allowed = new Set([...Object.keys(manifest.peerDependencies), manifest.name]);
    const offenders = sources(fileURLToPath(new URL(".", import.meta.url))).flatMap((path) => {
      const text = readFileSync(path, "utf8");
      const specifiers = [
        ...text.matchAll(/^(?:import|export)\s[^;]*?\bfrom\s+["']([^"']+)["']/gm),
        ...text.matchAll(/^import\s+["']([^"']+)["']/gm),
        ...text.matchAll(/\bimport\(\s*["']([^"']+)["']/g),
      ]
        .map((match) => match[1]!)
        .filter((specifier) => !specifier.startsWith("."));
      return specifiers
        .filter((specifier) => {
          if (specifier.startsWith("node:") || builtinModules.includes(specifier)) return false;
          return !allowed.has(packageName(specifier));
        })
        .map((specifier) => `${path}: ${specifier}`);
    });
    expect(offenders).toEqual([]);
  });
});
