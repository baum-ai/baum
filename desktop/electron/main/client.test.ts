import { createServer, type IncomingHttpHeaders } from "node:http";
import { createServer as createTcpServer, type AddressInfo } from "node:net";
import { describe, expect, it } from "vitest";
import { catchUp, get, jobSeen, type Launch, request, setAutomatic } from "./client.ts";

const TOKEN = "abcdefghijklmnopqrstuvwxyz0123456789-_ABCDE";

/** A launch on a local test server, for other modules' tests. */
function testLaunch(port: number): Launch {
  return {
    url: new URL(`http://127.0.0.1:${port}/#token=${TOKEN}`),
    bearer: () => TOKEN,
  };
}

interface Received {
  method: string;
  url: string;
  headers: IncomingHttpHeaders;
  body: string;
}

/** Answers one request and returns what it received. */
async function serveOnce(
  status: number,
  body: string,
): Promise<{ port: number; received: Promise<Received> }> {
  let resolveReceived: (r: Received) => void = () => {};
  const received = new Promise<Received>((resolve) => {
    resolveReceived = resolve;
  });
  const server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", (c: Buffer) => chunks.push(c));
    req.on("end", () => {
      resolveReceived({
        method: req.method ?? "",
        url: req.url ?? "",
        headers: req.headers,
        body: Buffer.concat(chunks).toString("utf8"),
      });
      res.writeHead(status, { "Content-Type": "application/json" });
      res.end(body);
      server.close();
    });
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  return { port: (server.address() as AddressInfo).port, received };
}

/** A raw TCP server answering one connection with exactly `response`. */
async function serveRaw(response: string): Promise<number> {
  const server = createTcpServer((socket) => {
    socket.once("data", () => socket.end(response));
    server.close();
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  return (server.address() as AddressInfo).port;
}

describe("daemon client", () => {
  it("get sends the bearer and returns the body", async () => {
    const { port, received } = await serveOnce(200, '{"jobs":[]}');
    const body = await get(testLaunch(port), "/api/jobs", 2000);
    expect(body.toString()).toBe('{"jobs":[]}');
    const r = await received;
    expect(r.method).toBe("GET");
    expect(r.url).toBe("/api/jobs");
    expect(r.headers.authorization).toBe(`Bearer ${TOKEN}`);
    expect(r.headers.origin).toBeUndefined();
  });

  it("a failing status is an error", async () => {
    const { port } = await serveOnce(401, "unauthorized");
    await expect(get(testLaunch(port), "/api/jobs", 2000)).rejects.toThrow(
      "/api/jobs answered 401",
    );
  });

  it("pause and resume put the switch with the origin the daemon requires", async () => {
    for (const enabled of [false, true]) {
      const { port, received } = await serveOnce(200, "{}");
      await setAutomatic(testLaunch(port), enabled);
      const r = await received;
      expect(r.method).toBe("PUT");
      expect(r.url).toBe("/api/automatic");
      expect(r.headers.origin).toBe(`http://127.0.0.1:${port}`);
      expect(r.headers["content-type"]).toBe("application/json");
      expect(r.body).toBe(`{"enabled":${enabled}}`);
    }
  });

  it("a refused switch change is an error", async () => {
    const { port } = await serveOnce(403, "forbidden");
    await expect(setAutomatic(testLaunch(port), false)).rejects.toThrow("403");
  });

  it("an unreachable daemon is an error", async () => {
    const server = createTcpServer();
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    const unused = (server.address() as AddressInfo).port;
    await new Promise<void>((resolve) => server.close(() => resolve()));
    await expect(get(testLaunch(unused), "/api/jobs", 200)).rejects.toThrow(
      "cannot reach the baum daemon",
    );
  });

  it("sends nothing once the daemon's process has gone", async () => {
    const { port, received } = await serveOnce(200, "{}");
    const gone = { ...testLaunch(port), running: () => false };
    await expect(get(gone, "/api/jobs", 2000)).rejects.toThrow("the baum daemon has stopped");
    const answered = await Promise.race([
      received.then(() => "received"),
      new Promise((resolve) => setTimeout(() => resolve("nothing"), 200)),
    ]);
    expect(answered).toBe("nothing");
    const running = { ...testLaunch(port), running: () => true };
    expect((await get(running, "/api/jobs", 2000)).toString()).toBe("{}");
  });

  it("rejects a response without a status", async () => {
    const garbage = await serveRaw("garbage");
    await expect(request(testLaunch(garbage), "GET", "/", null, 2000)).rejects.toThrow();
    const unreadable = await serveRaw("HTTP/1.1 abc\r\n\r\n");
    await expect(request(testLaunch(unreadable), "GET", "/", null, 2000)).rejects.toThrow();
    const noContent = await serveRaw("HTTP/1.1 204 No Content\r\n\r\n");
    const response = await request(testLaunch(noContent), "GET", "/", null, 2000);
    expect(response.status).toBe(204);
  });

  it("never puts the credential in a URL or an error", async () => {
    const { port, received } = await serveOnce(500, "");
    const error = await jobSeen(testLaunch(port), "job-1_a").catch((e: Error) => e);
    expect(String(error)).not.toContain(TOKEN);
    expect((await received).url).toBe("/api/jobs/job-1_a/seen");
  });

  it("refuses a job id that is not one", async () => {
    await expect(jobSeen(testLaunch(1), "../x")).rejects.toThrow("not a job id");
    await expect(jobSeen(testLaunch(1), "")).rejects.toThrow("not a job id");
  });

  it("catch-up expects 202", async () => {
    const { port, received } = await serveOnce(202, "{}");
    await catchUp(testLaunch(port));
    expect((await received).body).toBe("{}");
    const wrong = await serveOnce(200, "{}");
    await expect(catchUp(testLaunch(wrong.port))).rejects.toThrow("answered 200");
  });
});
