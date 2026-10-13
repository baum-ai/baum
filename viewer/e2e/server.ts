import { createReadStream, existsSync, readFileSync, statSync } from "node:fs";
import { createServer } from "node:http";
import { extname, join, normalize, resolve } from "node:path";

/** A stand-in for `baum view`: it serves the built standalone app and the
 *  endpoints the app reads, from fixtures, with the same security headers.
 *  Specs replace any answer for one page with `page.route`. */

const root = resolve(process.env.E2E_DIST ?? "e2e-dist");
const port = Number.parseInt(process.env.PORT ?? "4173", 10);
const example = readFileSync(new URL("../../schema/review-example.json", import.meta.url), "utf8");

// Kept equal to `contentSecurityPolicy` in oss/internal/viewserver/server.go.
const contentSecurityPolicy =
  "default-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'";

const types: Record<string, string> = {
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".html": "text/html; charset=utf-8",
  ".woff2": "font/woff2",
  ".json": "application/json",
  ".svg": "image/svg+xml",
};

createServer((request, response) => {
  response.setHeader("Content-Security-Policy", contentSecurityPolicy);
  response.setHeader("X-Content-Type-Options", "nosniff");
  response.setHeader("Referrer-Policy", "no-referrer");
  const { pathname } = new URL(request.url ?? "/", "http://localhost");

  const json = (body: unknown, status = 200) => {
    response.writeHead(status, { "Content-Type": "application/json", "Cache-Control": "no-store" });
    response.end(typeof body === "string" ? body : JSON.stringify(body));
  };

  if (pathname === "/api/document") return json(example);
  if (pathname === "/api/health") return json({ status: "ok" });
  if (pathname === "/api/events") {
    response.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-store" });
    response.write(": ping\n\n");
    return;
  }
  // The sample belongs to no repository, so the machine has no sessions to link.
  if (pathname === "/api/sessions") {
    return json({ available: false, reason: "this is the built-in sample" });
  }
  if (pathname === "/api/patch") return json({ patch: null });
  if (pathname.startsWith("/api/")) return json({ error: "not found" }, 404);

  let file = join(root, normalize(pathname));
  if (!file.startsWith(root) || !existsSync(file) || statSync(file).isDirectory()) {
    if (pathname.startsWith("/assets/")) {
      response.writeHead(404);
      return response.end();
    }
    file = join(root, "index.html");
  }
  response.writeHead(200, {
    "Content-Type": types[extname(file)] ?? "application/octet-stream",
    ...(file.endsWith("index.html") ? { "Cache-Control": "no-cache" } : {}),
  });
  createReadStream(file).pipe(response);
}).listen(port, "127.0.0.1");
