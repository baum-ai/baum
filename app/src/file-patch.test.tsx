import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";

const parse = vi.hoisted(() => vi.fn());
vi.mock("@baum/viewer/patch", async (original) => {
  const real = await original<typeof import("@baum/viewer/patch")>();
  return {
    ...real,
    parseFilePatch: (patch: string) => {
      parse(patch);
      return real.parseFilePatch(patch);
    },
  };
});

import { FilePatch } from "./facts";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const PATCH = "@@ -1,1 +1,1 @@\n-old\n+new\n";

it("parses an opened file's patch once across re-renders with the same patch", async () => {
  const patches = vi.fn(async () => PATCH);
  const host = document.createElement("div");
  const root = createRoot(host);
  const render = () =>
    root.render(
      <FilePatch
        path="a.ts"
        additions={1}
        deletions={1}
        patches={patches}
        href="https://x/files"
      />,
    );
  await act(async () => render());
  await act(async () => render());
  await act(async () => render());
  expect(parse).toHaveBeenCalledTimes(1);
  expect(patches).toHaveBeenCalledTimes(1);
  await act(async () => root.unmount());
});
