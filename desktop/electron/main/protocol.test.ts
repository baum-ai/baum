import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import {
  type AssetSets,
  assetSets,
  CONTENT_SECURITY_POLICY,
  contentType,
  resolveRequest,
} from "./protocol.ts";

const dirs: string[] = [];
afterEach(() => {
  for (const dir of dirs.splice(0)) rmSync(dir, { recursive: true, force: true });
});

function dist(): string {
  const dir = mkdtempSync(join(tmpdir(), "baum-dist-"));
  dirs.push(dir);
  mkdirSync(join(dir, "assets"));
  const files: Record<string, string> = {
    "index.html": '<script src="./assets/connecting-A.js"></script>',
    "setup.html": '<link href="./assets/setup-B.css">',
    "tray.html": '<script src="./assets/tray-C.js"></script>',
    "assets/connecting-A.js": 'import("./shared-D.js")',
    "assets/shared-D.js": "export const x = 1;",
    "assets/setup-B.css": "@font-face{src:url(./font-E.woff2)}",
    "assets/font-E.woff2": "",
    "assets/tray-C.js": "",
    "assets/app-F.js": "the daemon app's chunk",
  };
  for (const [name, text] of Object.entries(files)) writeFileSync(join(dir, name), text);
  return dir;
}

function sets(bundle: string[], tray: string[] = []): AssetSets {
  return { bundle: new Set(bundle), tray: new Set(tray) };
}

describe("the app scheme", () => {
  it("serves the tray panel and its assets only from its own origin", () => {
    const assets = sets(["connecting-A.js"], ["tray-C.js"]);
    expect(resolveRequest("app://tray/tray.html", false, assets)).toBe("tray.html");
    expect(resolveRequest("app://tray/tray.html?x#y", true, assets)).toBe("tray.html");
    expect(resolveRequest("app://tray/assets/tray-C.js", false, assets)).toBe("assets/tray-C.js");
  });

  it("serves the assets the bundled pages reference, through each other", () => {
    const found = assetSets(dist());
    expect([...found.bundle].sort()).toEqual([
      "connecting-A.js",
      "font-E.woff2",
      "setup-B.css",
      "shared-D.js",
    ]);
    expect([...found.tray]).toEqual(["tray-C.js"]);
  });

  it("serves exactly the pages, and the setup screen only while it shows", () => {
    const assets = sets(["connecting-A.js"]);
    expect(resolveRequest("app://bundle/", false, assets)).toBe("index.html");
    expect(resolveRequest("app://bundle/index.html?x=1#y", false, assets)).toBe("index.html");
    expect(resolveRequest("app://bundle/tray.html", false, assets)).toBeNull();
    expect(resolveRequest("app://bundle/setup.html", true, assets)).toBe("setup.html");
    expect(resolveRequest("app://bundle/setup.html", false, assets)).toBeNull();
    expect(resolveRequest("app://bundle/assets/connecting-A.js", false, assets)).toBe(
      "assets/connecting-A.js",
    );
  });

  it("refuses other paths, spellings and hosts", () => {
    const assets = sets(["connecting-A.js", "app-F.js"], ["tray-C.js"]);
    for (const url of [
      "app://bundle/setup",
      "app://bundle/setup.html/",
      "app://bundle/setup%2Ehtml",
      "app://bundle/%73etup.html",
      "app://bundle/index",
      "app://bundle/index.html/",
      "app://bundle/assets/",
      "app://bundle/assets/missing.js",
      "app://bundle/assets/connecting-A%2Ejs",
      "app://bundle/assets/../index.html/x",
      "app://bundle/assets/sub/connecting-A.js",
      "app://bundle/dist/index.html",
      "app://bundle:1/index.html",
      "app://user@bundle/index.html",
      "app://other/index.html",
      "app://tray/index.html",
      "app://tray/setup.html",
      "app://tray/",
      "app://tray/tray.html/",
      "app://tray/assets/connecting-A.js",
      "app://tray:1/tray.html",
      "app://user@tray/tray.html",
      "app://bundle/assets/tray-C.js",
      "http://bundle/index.html",
      "not a url",
    ]) {
      expect(resolveRequest(url, true, assets), url).toBeNull();
    }
  });

  it("names its content types and keeps its policy without connect-src", () => {
    expect(contentType("index.html")).toBe("text/html; charset=utf-8");
    expect(contentType("assets/a.js")).toBe("text/javascript; charset=utf-8");
    expect(contentType("assets/a.woff2")).toBe("font/woff2");
    expect(contentType("assets/a.bin")).toBe("application/octet-stream");
    expect(CONTENT_SECURITY_POLICY).toBe(
      "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'none'; frame-ancestors 'none'",
    );
  });
});
