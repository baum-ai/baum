import { readFileSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { createProxyServer } from "http-proxy-3";
import type { Plugin } from "vite";

/** Keep login, cookies, CSRF and event streams on the frontend's origin.
 * The desktop writes only the daemon origin, never its launch capability. */
export function desktopDevelopment(directory: string): Plugin {
  function target(): string {
    const origin = readFileSync(join(directory, "daemon-origin"), "utf8");
    const url = new URL(origin);
    if (url.protocol !== "http:" || url.hostname !== "127.0.0.1" || !url.port)
      throw Error("Expected a loopback daemon origin");
    return url.origin;
  }
  return {
    name: "desktop-development",
    apply: "serve",
    config() {
      return {
        base: "/",
        server: {
          host: "127.0.0.1",
          port: 1420,
          strictPort: true,
          fs: {
            allow: [
              resolve(import.meta.dirname),
              resolve(import.meta.dirname, "../viewer"),
              resolve(import.meta.dirname, "../desktop/ui"),
            ],
          },
        },
      };
    },
    configureServer(server) {
      const proxy = createProxyServer({ changeOrigin: true });
      proxy.on("error", (_error, _request, response) => {
        // Never log the URL: API paths contain the private session namespace.
        if ("writeHead" in response && !response.headersSent) {
          response.writeHead(502);
          response.end("The development daemon is unavailable.");
        }
      });
      server.httpServer?.once("close", () => proxy.close());
      server.middlewares.use((request, response, next) => {
        const pathname = new URL(request.url ?? "/", "http://127.0.0.1").pathname;
        // App pages and modules use Vite; only bootstrap and API use Go.
        if (!/^\/(?:$|launch\.js$|api\/|s\/[^/]+\/api\/)/.test(pathname)) {
          next();
          return;
        }
        try {
          const origin = target();
          if (request.headers.origin === "http://127.0.0.1:1420") request.headers.origin = origin;
          proxy.web(request, response, { target: origin });
        } catch {
          response.writeHead(503);
          response.end("Waiting for the development daemon.");
        }
      });
      server.httpServer?.once("listening", () => {
        writeFileSync(join(directory, "vite-ready"), "ready");
      });
    },
  };
}
