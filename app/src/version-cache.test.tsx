// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, expect, it, vi } from "vitest";
import type { LoadReviewVersion, ReviewDocument } from "@baum/viewer/review-document";
import { useVersionLoader } from "./version-cache";

let root: Root | null = null;
beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});
afterEach(() => {
  act(() => root?.unmount());
  root = null;
});

const documentOf = (version: number) => ({ review: { version } }) as unknown as ReviewDocument;

function Probe(props: {
  id: string;
  current: number | undefined;
  fetchVersion: (version: number) => Promise<ReviewDocument>;
  onLoader: (loader: LoadReviewVersion) => void;
}) {
  props.onLoader(useVersionLoader(props.id, props.fetchVersion, props.current));
  return null;
}

async function mount() {
  const fetchVersion = vi.fn(async (version: number) => documentOf(version));
  let loader: LoadReviewVersion = async () => ({ status: "failed" });
  const show = (id: string, current: number | undefined) =>
    act(async () => {
      root ??= createRoot(globalThis.document.createElement("div"));
      root.render(
        <Probe
          id={id}
          current={current}
          fetchVersion={fetchVersion}
          onLoader={(value) => {
            loader = value;
          }}
        />,
      );
    });
  await show("a#1", 4);
  return { fetchVersion, show, load: (version: number) => loader(version) };
}

it("reads an earlier version once, also after a refetch of the same version", async () => {
  const { fetchVersion, show, load } = await mount();
  await load(2);
  await show("a#1", 4);
  expect(await load(2)).toEqual({ status: "ready", document: documentOf(2) });
  expect(fetchVersion).toHaveBeenCalledTimes(1);
});

it("shares one request between concurrent callers", async () => {
  const { fetchVersion, load } = await mount();
  await Promise.all([load(1), load(1), load(1)]);
  expect(fetchVersion).toHaveBeenCalledTimes(1);
});

it("retries a failed read on the next call", async () => {
  const { fetchVersion, load } = await mount();
  fetchVersion.mockRejectedValueOnce(new Error("offline"));
  expect(await load(3)).toEqual({ status: "failed" });
  expect(await load(3)).toEqual({ status: "ready", document: documentOf(3) });
  expect(fetchVersion).toHaveBeenCalledTimes(2);
});

it("does not keep the latest version, which the daemon refreshes in place", async () => {
  const { fetchVersion, load } = await mount();
  await load(4);
  await load(4);
  expect(fetchVersion).toHaveBeenCalledTimes(2);
});

it("drops the cache when the review changes", async () => {
  const { fetchVersion, show, load } = await mount();
  await load(1);
  await show("b#2", 4);
  await load(1);
  expect(fetchVersion).toHaveBeenCalledTimes(2);
});
