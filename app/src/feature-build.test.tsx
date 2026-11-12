import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { useBuildState } from "./feature-build";

const read = vi.hoisted(() => vi.fn());
vi.mock("./api", async () => ({
  ...(await vi.importActual<typeof import("./api")>("./api")),
  request: read,
}));
let node: HTMLDivElement;
let root: Root;
beforeEach(() => {
  read.mockReset();
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
});
function Harness({ repository, revision = "1" }: { repository: string; revision?: string }) {
  const state = useBuildState(repository, revision);
  return <output>{JSON.stringify(state)}</output>;
}
const saved = (repository: string) => ({
  building: {
    placed: 1,
    total: 2,
    stopped: "interrupted",
    spent_usd: 1,
    coverage: { source: { revision: repository } },
  },
  current_coverage: { source: { revision: repository } },
});
it("retains a same-repository build after a transient failure", async () => {
  read
    .mockResolvedValueOnce(saved("source-A"))
    .mockRejectedValueOnce(new Error("temporarily down"));
  await act(async () => root.render(<Harness repository="acme/A" />));
  expect(node.textContent).toContain("source-A");
  await act(async () => root.render(<Harness repository="acme/A" revision="2" />));
  expect(node.textContent).toContain("source-A");
});
it("does not show another repository's saved build when the new read fails", async () => {
  read.mockResolvedValueOnce(saved("source-A")).mockRejectedValueOnce(new Error("B unavailable"));
  await act(async () => root.render(<Harness repository="acme/A" />));
  await act(async () => root.render(<Harness repository="acme/B" />));
  expect(node.textContent).not.toContain("source-A");
  expect(node.textContent).toContain('"building":null');
});
it("ignores an aborted older repository response that arrives last", async () => {
  let send: (value: unknown) => void = () => {};
  read
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          send = resolve;
        }),
    )
    .mockResolvedValueOnce(saved("source-B"));
  await act(async () => root.render(<Harness repository="acme/A" />));
  await act(async () => root.render(<Harness repository="acme/B" />));
  expect(node.textContent).toContain("source-B");
  await act(async () => send(saved("source-A")));
  expect(node.textContent).toContain("source-B");
  expect(node.textContent).not.toContain("source-A");
});
