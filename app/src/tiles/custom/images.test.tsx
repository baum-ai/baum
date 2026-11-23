import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ReviewFilesContext } from "@baum/viewer/components/review-contexts";
import { AttachmentUrlProvider } from "@baum/viewer/host";
import type { ReviewDocument } from "@baum/viewer/review-document";
import { fixtureSource, fixtureTile } from "../fixture-test-helper";
import { TileHostContext } from "../host";
import type { TileContext } from "../types";
import { forgetImages, imageDataURL, imageRefs, withImages } from "./images";
import { PROTOCOL } from "./protocol";
import { TileFrame } from "./tile-frame";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

const ID = "att_0123456789abcdef0123456789abcdef";
const OTHER = "att_ffffffffffffffffffffffffffffffff";
const REF = `baum-image:${ID}`;
const PNG = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
const PNG_URL = "data:image/png;base64,iVBORw0KGgo=";

const answer = (body: BodyInit, type: string, status = 200) =>
  new Response(body, { status, headers: { "Content-Type": type } });

afterEach(() => {
  forgetImages();
  vi.unstubAllGlobals();
});

describe("a custom tile's stored images", () => {
  it("are found wherever the data holds a reference, each once", () => {
    const data = {
      shot: REF,
      items: [{ img: REF }, { img: `baum-image:${OTHER}` }],
      words: `see ${REF}`,
      other: "baum-image:att_short",
    };
    expect(imageRefs(data)).toEqual([ID, OTHER]);
    expect(imageRefs({ a: "b", n: 1, none: null })).toEqual([]);
    expect(withImages(data, new Map([[ID, PNG_URL]]))).toEqual({
      shot: PNG_URL,
      items: [{ img: PNG_URL }, { img: "" }],
      words: `see ${REF}`,
      other: "baum-image:att_short",
    });
  });

  it("are inlined only up to a total, however often the data repeats one", () => {
    const urls = new Map([[ID, PNG_URL]]);
    const limit = PNG_URL.length * 2;
    expect(withImages([REF, REF, REF, REF], urls, limit)).toEqual([PNG_URL, PNG_URL, "", ""]);
  });

  it("are read once as data: URLs, and only images", async () => {
    const load = vi.fn(async (url: string | URL | Request) =>
      String(url).endsWith("png") ? answer(PNG, "image/png") : answer("<p>hi</p>", "text/html"),
    );
    expect(await imageDataURL("/a.png", load)).toBe(PNG_URL);
    expect(await imageDataURL("/a.png", load)).toBe(PNG_URL);
    expect(load).toHaveBeenCalledTimes(1);
    expect(await imageDataURL("/page", load)).toBe("");
    const missing = vi.fn(async () => answer("not found", "image/png", 404));
    expect(await imageDataURL("/gone.png", missing)).toBe("");
    // A failed read is tried again.
    expect(await imageDataURL("/gone.png", missing)).toBe("");
    expect(missing).toHaveBeenCalledTimes(2);
  });
});

describe("a custom tile with a stored image", () => {
  let root: Root | null = null;
  const sent: unknown[] = [];
  const target = { postMessage: vi.fn((message: unknown) => sent.push(message)) };
  beforeAll(() => {
    globalThis.IS_REACT_ACT_ENVIRONMENT = true;
    Object.defineProperty(HTMLIFrameElement.prototype, "contentWindow", {
      configurable: true,
      get: () => target,
    });
  });
  afterEach(() => {
    act(() => root?.unmount());
    root = null;
    document.body.replaceChildren();
  });

  it("gives its frame the image as a data: URL, read by the page", async () => {
    const fetched: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) => {
        fetched.push(url);
        return answer(PNG, "image/png");
      }),
    );
    const tile = fixtureTile("route_changes");
    tile.id = "route_changes_image";
    tile.data = { ...(tile.data as Record<string, unknown>), shot: REF };
    const ctx: TileContext = {
      source: fixtureSource(),
      review: {} as ReviewDocument,
      open: vi.fn(),
      openChange: vi.fn(),
      openPane: vi.fn(),
      patches: null,
      idPrefix: "",
      labels: new Map(),
    };
    const container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    await act(async () =>
      root!.render(
        <AttachmentUrlProvider resolve={(id) => `/api/attachments/${id}?clone=c&pr=42`}>
          <TileHostContext.Provider value={{ draft: false, ask: vi.fn(), reread: vi.fn() }}>
            <ReviewFilesContext.Provider value={{ has: () => true, open: vi.fn(() => true) }}>
              <TileFrame tile={tile} ctx={ctx} face="glance" />
            </ReviewFilesContext.Provider>
          </TileHostContext.Provider>
        </AttachmentUrlProvider>,
      ),
    );
    await act(async () => {});
    expect(fetched).toEqual([`/api/attachments/${ID}?clone=c&pr=42`]);
    expect(container.querySelector("iframe")).not.toBeNull();
    act(() => {
      const event = new MessageEvent("message", {
        data: { protocol: PROTOCOL, kind: "hello", kit: 1, registered: true },
        origin: "null",
      });
      Object.defineProperty(event, "source", { value: target });
      window.dispatchEvent(event);
    });
    expect(sent[0]).toMatchObject({ kind: "init", data: { shot: PNG_URL } });
    // The tile itself keeps the reference: nothing else sees the bytes.
    expect((tile.data as { shot: string }).shot).toBe(REF);
  });
});
