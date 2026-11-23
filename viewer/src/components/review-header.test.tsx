// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, expect, it } from "vitest";

import example from "../../../schema/review-example.json" with { type: "json" };
import type { ReviewDocument } from "../review-document";
import { ReviewHeader } from "./review-header";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

let root: Root | null = null;

beforeAll(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.IntersectionObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
    takeRecords() {
      return [];
    }
  } as unknown as typeof IntersectionObserver;
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.replaceChildren();
});

it("draws the way back before the title, and keeps the hidden compact bar out of reach", async () => {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () =>
    root!.render(
      <ReviewHeader
        document={example as unknown as ReviewDocument}
        slots={{ back: { href: "/inbox", label: "Inbox" } }}
      />,
    ),
  );
  const header = container.querySelector("header")!;
  const back = header.querySelector<HTMLAnchorElement>('a[aria-label="Inbox"]');
  expect(back).not.toBeNull();
  expect(back!.compareDocumentPosition(header.querySelector("h1")!)).toBe(
    Node.DOCUMENT_POSITION_FOLLOWING,
  );
  // The compact bar is not shown yet: its controls must not take focus.
  const bar = container.querySelector<HTMLElement>("[aria-hidden]")!;
  expect(bar.hasAttribute("inert")).toBe(true);
});
