import { describe, expect, it, vi } from "vitest";

import { createPatchSource, fetchPatch, parsePatchAnswer } from "./local-patches";

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

function fetcher(...answers: Array<() => Response | Promise<Response>>) {
  const calls: string[] = [];
  let next = 0;
  const fn = vi.fn(async (url: string | URL | Request) => {
    calls.push(String(url));
    const answer = answers[Math.min(next++, answers.length - 1)]!;
    return answer();
  });
  return { fn: fn as unknown as typeof fetch, calls };
}

describe("parsePatchAnswer", () => {
  it("reads the patch text and nothing else as a patch", () => {
    expect(parsePatchAnswer({ patch: "@@ -1 +1 @@\n-a\n+b\n" })).toBe("@@ -1 +1 @@\n-a\n+b\n");
    for (const body of [
      { patch: null },
      { patch: "" },
      { patch: 3 },
      {},
      null,
      "x",
      [],
      undefined,
    ]) {
      expect(parsePatchAnswer(body)).toBeNull();
    }
  });
});

describe("fetchPatch", () => {
  it("asks for the path, escaped, and returns the patch", async () => {
    const { fn, calls } = fetcher(() => json({ patch: "@@ -1 +1 @@\n-a\n+b\n" }));
    await expect(fetchPatch("src/a b&c.go", fn)).resolves.toBe("@@ -1 +1 @@\n-a\n+b\n");
    expect(calls).toEqual(["/api/patch?path=src%2Fa%20b%26c.go"]);
  });

  it("turns every answer that is not a patch into null", async () => {
    for (const answer of [
      () => json({ patch: null }),
      () => json({ error: "x" }),
      () => json({}, 404),
      () => new Response("not json", { status: 200 }),
    ]) {
      await expect(fetchPatch("a.go", fetcher(answer).fn)).resolves.toBeNull();
    }
  });

  it("rejects when the request fails or the server errs", async () => {
    await expect(
      fetchPatch("a.go", fetcher(() => Promise.reject(new TypeError("offline"))).fn),
    ).rejects.toThrow("offline");
    await expect(fetchPatch("a.go", fetcher(() => json({}, 500)).fn)).rejects.toThrow("500");
  });
});

describe("createPatchSource", () => {
  it("fetches each path once, however often it is asked for", async () => {
    const { fn, calls } = fetcher(() => json({ patch: "@@ -1 +1 @@\n-a\n+b\n" }));
    const source = createPatchSource(3, fn);
    const [first, again] = await Promise.all([source("a.go", 3), source("a.go", 3)]);
    expect(first).toBe(again);
    await source("a.go", 3);
    await source("b.go", 3);
    expect(calls).toEqual(["/api/patch?path=a.go", "/api/patch?path=b.go"]);
  });

  it("has no patch for a version other than the document's, without asking", async () => {
    const { fn } = fetcher(() => json({ patch: "@@ -1 +1 @@\n-a\n+b\n" }));
    const source = createPatchSource(3, fn);
    await expect(source("a.go", 2)).resolves.toBeNull();
    await expect(source("a.go", 4)).resolves.toBeNull();
    expect(fn).not.toHaveBeenCalled();
  });

  it("keeps a null answer without asking again", async () => {
    const { fn } = fetcher(() => json({ patch: null }));
    const source = createPatchSource(1, fn);
    await expect(source("a.go", 1)).resolves.toBeNull();
    await expect(source("a.go", 1)).resolves.toBeNull();
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("forgets a failed request, so the next ask tries again", async () => {
    const { fn } = fetcher(
      () => Promise.reject(new TypeError("offline")),
      () => json({ patch: "@@ -1 +1 @@\n-a\n+b\n" }),
    );
    const source = createPatchSource(1, fn);
    await expect(source("a.go", 1)).rejects.toThrow("offline");
    await expect(source("a.go", 1)).resolves.toBe("@@ -1 +1 @@\n-a\n+b\n");
    expect(fn).toHaveBeenCalledTimes(2);
  });
});
