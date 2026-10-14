// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { reviewDocumentFor } from "../../test/review-document";
import type { ReviewDocument } from "../review-document";
import { ReviewFilesProvider } from "./code-file-link";
import { HunkEvidence } from "./hunk-evidence";

let root: Root | null = null;

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
});

function document(): ReviewDocument {
  const value = structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;
  const hunk = value.index.hunks.find((candidate) => candidate.id === "h_picker")!;
  hunk.diff = {
    header: "@@ -10,6 +10,6 @@",
    lines: [
      { kind: "context", old_line: 10, new_line: 10, text: "ten" },
      { kind: "context", old_line: 11, new_line: 11, text: "eleven" },
      { kind: "remove", old_line: 12, text: "before" },
      { kind: "add", new_line: 12, text: "after" },
      { kind: "context", old_line: 13, new_line: 13, text: "thirteen" },
      { kind: "context", old_line: 14, new_line: 14, text: "fourteen" },
    ],
  };
  return value;
}

describe("hunk evidence", () => {
  it("renders a selected range with two surrounding diff rows", () => {
    const html = renderToStaticMarkup(
      <HunkEvidence
        document={document()}
        anchors={[{ type: "hunk", id: "h_picker", lines: [12, 12] }]}
      />,
    );
    expect(html).toContain("Code evidence");
    expect(html).toContain("1 excerpt in 1 file · +1 −1");
    expect(html).not.toContain(">ten<");
    expect(html).toContain("thirteen");
    expect(html).toContain("fourteen");
    expect(html).toContain("@@ -11,4 +11,4 @@");
  });

  it("selects ranges from new-side coordinates only", () => {
    const doc = document();
    const hunk = doc.index.hunks.find((candidate) => candidate.id === "h_picker")!;
    hunk.diff = {
      header: "@@ -10,7 +10,4 @@",
      lines: [
        { kind: "remove", old_line: 10, text: "old ten" },
        { kind: "remove", old_line: 11, text: "old eleven" },
        { kind: "remove", old_line: 12, text: "unrelated old twelve" },
        { kind: "context", old_line: 13, new_line: 10, text: "new ten" },
        { kind: "context", old_line: 14, new_line: 11, text: "new eleven" },
        { kind: "context", old_line: 15, new_line: 12, text: "selected new twelve" },
        { kind: "context", old_line: 16, new_line: 13, text: "new thirteen" },
      ],
    };
    const html = renderToStaticMarkup(
      <HunkEvidence document={doc} anchors={[{ type: "hunk", id: "h_picker", lines: [12, 12] }]} />,
    );
    expect(html).toContain("selected new twelve");
    expect(html).not.toContain("unrelated old twelve");
  });

  it("advances the missing side of an interior add-only excerpt", () => {
    const doc = document();
    const hunk = doc.index.hunks.find((candidate) => candidate.id === "h_picker")!;
    hunk.diff = {
      header: "@@ -1,6 +1,12 @@",
      lines: [
        ...Array.from({ length: 5 }, (_, index) => ({
          kind: "context" as const,
          old_line: index + 1,
          new_line: index + 1,
          text: `context ${index + 1}`,
        })),
        ...Array.from({ length: 6 }, (_, index) => ({
          kind: "add" as const,
          new_line: index + 6,
          text: `addition ${index + 6}`,
        })),
        { kind: "context", old_line: 6, new_line: 12, text: "context 6" },
      ],
    };
    const html = renderToStaticMarkup(
      <HunkEvidence document={doc} anchors={[{ type: "hunk", id: "h_picker", lines: [8, 8] }]} />,
    );
    expect(html).toContain("@@ -5,0 +6,5 @@");
    expect(html).not.toContain("@@ -1,0");
  });

  it("deduplicates anchors and retains a source link when an excerpt is unavailable", () => {
    const doc = document();
    const hunk = doc.index.hunks.find((candidate) => candidate.id === "h_picker")!;
    delete hunk.diff;
    delete hunk.github_url;
    const anchor = { type: "hunk" as const, id: "h_picker" };
    const html = renderToStaticMarkup(<HunkEvidence document={doc} anchors={[anchor, anchor]} />);
    expect(html).toContain("1 excerpt in 1 file");
    expect(html).toContain("Excerpt unavailable");
    expect(html).toContain("View web/app/components/repository-picker.tsx");
    // With no link supplied, the source still opens on the change itself.
    expect(html).toMatch(
      /href="https:\/\/github\.com\/acme\/widgets\/pull\/42\/files#diff-[0-9a-f]{64}R\d+"/,
    );
  });

  it("opens code in the review's file view when the pull request changes the file", () => {
    const files = {
      has: (path: string) => path.endsWith("repository-picker.tsx"),
      open: () => true,
    };
    const html = renderToStaticMarkup(
      <ReviewFilesProvider files={files}>
        <HunkEvidence
          document={document()}
          anchors={[{ type: "hunk", id: "h_picker", lines: [12, 12] }]}
        />
      </ReviewFilesProvider>,
    );
    expect(html).toContain("web/app/components/repository-picker.tsx");
    expect(html).not.toContain("github.com");
  });

  it("marks a missing mitigated-risk excerpt as inline historical evidence", () => {
    const html = renderToStaticMarkup(
      <HunkEvidence
        document={document()}
        anchors={[{ type: "hunk", id: "h_historical" }]}
        historicalVersion={2}
        loadVersion={async () => ({ status: "failed" })}
      />,
    );
    expect(html).toContain("historical evidence");
    expect(html).toContain("Loading historical evidence from review v2");
    expect(html).not.toContain("View review v2");
    expect(html).not.toContain("Excerpt unavailable");
  });

  it("highlights the excerpts only once the evidence is opened", async () => {
    const container = globalThis.document.createElement("div");
    root = createRoot(container);
    await act(async () =>
      root!.render(
        <HunkEvidence
          document={document()}
          anchors={[{ type: "hunk", id: "h_picker", lines: [12, 12] }]}
        />,
      ),
    );
    // Highlighted code is coloured by `--syntax-*` variables.
    const highlighted = () => container.querySelector('[style*="--syntax-"]') !== null;
    const settle = (until: () => boolean, tries = 100) =>
      act(async () => {
        for (let wait = 0; wait < tries && !until(); wait++)
          await new Promise((resolve) => setTimeout(resolve, 20));
      });
    // Long enough for shiki to load and tokenise, were it going to.
    await settle(highlighted, 25);
    expect(highlighted()).toBe(false);
    const details = container.querySelector("details")!;
    await act(async () => {
      details.open = true;
      details.dispatchEvent(new Event("toggle"));
    });
    await settle(highlighted);
    expect(highlighted()).toBe(true);
  });
});
