// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import { reviewDocumentFor } from "../test/review-document";
import type { LoadReviewVersion, ReviewDocument } from "./review-document";
import { useSectionHistory } from "./use-section-history";

let root: Root | null = null;

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
});

function Probe(props: {
  document: ReviewDocument;
  versions: number[];
  load: LoadReviewVersion;
  onResult: (result: ReturnType<typeof useSectionHistory>) => void;
}) {
  props.onResult(useSectionHistory(props.document, props.versions, props.load));
  return null;
}

async function mount(review: ReviewDocument, versions: number[]) {
  const load = vi.fn<LoadReviewVersion>(async (version) => ({
    status: "ready",
    document: { ...structuredClone(review), review: { ...review.review, version } },
  }));
  let result: ReturnType<typeof useSectionHistory> = null;
  root = createRoot(globalThis.document.createElement("div"));
  await act(async () => {
    root!.render(
      <Probe
        document={review}
        versions={versions}
        load={load}
        onResult={(value) => {
          result = value;
        }}
      />,
    );
  });
  return { load, result: () => result };
}

function current(): ReviewDocument {
  const review = structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;
  review.review.version = 4;
  for (const section of review.sections) {
    if (section.derived.state === "stale") section.derived.state = "changed";
    for (const risk of section.risks) {
      risk.state = "active";
      risk.mitigated_version = null;
    }
  }
  return review;
}

describe("section history", () => {
  it("loads nothing while no stale section or resolved risk needs an earlier version", async () => {
    const { load, result } = await mount(current(), [3, 2, 1]);
    expect(load).not.toHaveBeenCalled();
    expect(result()).toBeNull();
  });

  it("loads only the versions before the one a resolved risk asks about", async () => {
    const review = current();
    const risk = review.sections.flatMap((section) => section.risks)[0]!;
    risk.state = "mitigated";
    risk.mitigated_version = 3;
    const { load, result } = await mount(review, [4, 3, 2, 1]);
    expect(load.mock.calls.map(([version]) => version).toSorted()).toEqual([1, 2]);
    expect(result()).not.toBeNull();
  });
});
