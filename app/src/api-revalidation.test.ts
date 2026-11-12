import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { KEPT_LIMIT, LocalRequestError, request, resetRevalidation } from "./api";

const path = "api/index?history=1&detail=summary";
const fetchMock = vi.fn();

beforeEach(() => {
  resetRevalidation();
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => vi.unstubAllGlobals());

const ok = (value: unknown, etag?: string) =>
  new Response(JSON.stringify(value), {
    status: 200,
    headers: etag ? { ETag: etag } : {},
  });
const init = (call: number) => fetchMock.mock.calls[call]![1] as RequestInit;
const sent = (call: number) => new Headers(init(call).headers).get("If-None-Match");

it("revalidates with the kept ETag and returns the same object on 304", async () => {
  fetchMock.mockResolvedValueOnce(ok({ n: 1 }, '"a"'));
  const first = await request<{ n: number }>(path);
  expect(sent(0)).toBeNull();
  expect(init(0).cache).toBe("no-store");

  fetchMock.mockResolvedValueOnce(new Response(null, { status: 304 }));
  const second = await request<{ n: number }>(path);
  expect(sent(1)).toBe('"a"');
  expect(init(1).cache).toBe("no-store");
  expect(second).toBe(first);

  fetchMock.mockResolvedValueOnce(ok({ n: 2 }, '"b"'));
  const third = await request<{ n: number }>(path);
  expect(sent(2)).toBe('"a"');
  expect(third).toEqual({ n: 2 });

  fetchMock.mockResolvedValueOnce(new Response(null, { status: 304 }));
  expect(await request(path)).toBe(third);
  expect(sent(3)).toBe('"b"');
});

it("retries once without the header on a 304 with nothing kept", async () => {
  fetchMock
    .mockResolvedValueOnce(new Response(null, { status: 304 }))
    .mockResolvedValueOnce(ok({ n: 1 }, '"a"'));
  expect(await request(path)).toEqual({ n: 1 });
  expect(fetchMock).toHaveBeenCalledTimes(2);
  expect(sent(0)).toBeNull();
  expect(sent(1)).toBeNull();
});

it("keeps the entry when an error answers", async () => {
  fetchMock.mockResolvedValueOnce(ok({ n: 1 }, '"a"'));
  const first = await request(path);
  fetchMock.mockResolvedValueOnce(new Response("boom", { status: 500 }));
  await expect(request(path)).rejects.toBeInstanceOf(LocalRequestError);
  fetchMock.mockResolvedValueOnce(new Response(null, { status: 304 }));
  expect(await request(path)).toBe(first);
  expect(sent(2)).toBe('"a"');
});

it("drops the entry when a 200 has no ETag", async () => {
  fetchMock.mockResolvedValueOnce(ok({ n: 1 }, '"a"'));
  await request(path);
  fetchMock.mockResolvedValueOnce(ok({ n: 2 }));
  await request(path);
  fetchMock.mockResolvedValueOnce(ok({ n: 3 }, '"c"'));
  await request(path);
  expect(sent(2)).toBeNull();
});

it("keeps one entry per path, query included", async () => {
  fetchMock.mockResolvedValueOnce(ok({ n: 1 }, '"a"'));
  await request("api/prs?view=health");
  fetchMock.mockResolvedValueOnce(ok({ n: 2 }, '"b"'));
  await request("api/prs");
  expect(sent(1)).toBeNull();
});

it("sends no condition for other paths or methods", async () => {
  fetchMock.mockImplementation(async () => ok({ n: 1 }, '"a"'));
  await request("api/repos");
  await request("api/repos");
  expect(sent(0)).toBeNull();
  expect(sent(1)).toBeNull();
  expect(init(1).cache).toBe("no-store");
  await request(path);
  await request(path, { x: 1 });
  await request(path, undefined, "POST");
  expect(sent(3)).toBeNull();
  expect(sent(4)).toBeNull();
});

it("evicts the oldest entry beyond the cap and refreshes recency on a hit", async () => {
  const doc = (i: number) => `api/document?clone=c&pr=${i}`;
  fetchMock.mockImplementation(async (_url: string, init: RequestInit) =>
    new Headers(init.headers).has("If-None-Match")
      ? new Response(null, { status: 304 })
      : ok({ n: 1 }, '"e"'),
  );
  for (let i = 0; i < KEPT_LIMIT; i++) await request(doc(i));
  // A hit on the oldest makes entry 1 the oldest.
  await request(doc(0));
  await request(doc(KEPT_LIMIT));
  fetchMock.mockClear();
  await request(doc(0));
  expect(sent(0)).toBe('"e"');
  await request(doc(1));
  expect(sent(1)).toBeNull();
});
