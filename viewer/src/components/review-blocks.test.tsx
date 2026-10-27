import Ajv2020 from "ajv/dist/2020.js";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import reviewSchema from "../../../schema/review-1.json" with { type: "json" };
import { reviewDocumentFor } from "../../test/review-document";
import { AttachmentUrlProvider } from "../host";
import type { Block, IndexedAttachment, ReviewDocument } from "../review-document";
import { ReviewSection } from "./review-section";

/** One schema-valid block of every kind the review schema defines, with the
 *  text each must show. Validating the fixtures against the shared schema is
 *  what catches the UI reading a field the schema names differently. */
const source = [{ type: "file" as const, path: "src/picker-source.ts" }];
const hunk = "h_0123456789ab";
const fixtures: Record<string, { block: unknown; shows: string[] }> = {
  intent: {
    block: { kind: "intent", text: "Intent text", anchors: source },
    shows: ["Intent text"],
  },
  behavior: {
    block: {
      kind: "behavior",
      steps: [{ id: "beh_open", text: "Open the picker step", anchors: source, evidence: [] }],
    },
    shows: ["Open the picker step"],
  },
  entities: {
    block: {
      kind: "entities",
      items: [{ kind: "function", name: "quuxfn", change: "added", hunks: [hunk] }],
    },
    // Left out of the page for now; see the test below.
    shows: [],
  },
  groups: {
    block: {
      kind: "groups",
      items: [{ id: "grp_renames", rule: "Renamed imports rule", hunks: [hunk], count: 1 }],
    },
    shows: ["Renamed imports rule"],
  },
  decisions: {
    block: {
      kind: "decisions",
      items: [
        {
          id: "dec_store",
          by: "human",
          text: "Keep one store decision",
          rejected: "Two stores",
          anchors: source,
        },
      ],
    },
    shows: ["Keep one store decision"],
  },
  dependency: {
    block: {
      kind: "dependency",
      items: [
        {
          name: "left-pad-dependency",
          ecosystem: "npm",
          from: "1.0.0",
          to: "1.1.0",
          hunks: [hunk],
          changelog: { type: "external_url", url: "https://example.com/changelog" },
        },
      ],
    },
    shows: ["left-pad-dependency"],
  },
  included_prs: {
    block: { kind: "included_prs", items: [{ pr: 17, note: "Split out the store note" }] },
    shows: ["#17", "Split out the store note"],
  },
  diagram: {
    block: { kind: "diagram", format: "mermaid", content: "graph TD; picker-->store", source },
    shows: ["graph TD; picker--&gt;store", "src/picker-source.ts"],
  },
  code: {
    block: {
      kind: "code",
      language: "ts",
      content: "const picked = pick();",
      caption: "Code caption",
      source,
    },
    shows: ["const picked = pick();", "Code caption", "src/picker-source.ts"],
  },
  media: {
    block: {
      kind: "media",
      url: "https://example.com/shot.png",
      caption: "Media caption",
      source,
    },
    shows: ["Media caption", 'href="https://example.com/shot.png"', "src/picker-source.ts"],
  },
  images: {
    block: {
      kind: "images",
      items: [{ attachment: "att_0123456789abcdef", caption: "Images caption" }],
      source: [{ type: "pr_body", section: null }],
    },
    shows: ["Images caption", 'src="/attachments/att_0123456789abcdef"', "Description"],
  },
  repro: {
    block: { kind: "repro", steps: ["Reproduce step one"], source },
    shows: ["Reproduce step one", "src/picker-source.ts"],
  },
  table: {
    block: {
      kind: "table",
      columns: ["Before col", "After col"],
      rows: [["one", "two cell"]],
      source,
    },
    shows: ["Before col", "two cell", "src/picker-source.ts"],
  },
};

const ajv = new Ajv2020({ strict: false, validateFormats: false });
const validBlock = ajv.compile({ $ref: "#/$defs/block", $defs: reviewSchema.$defs });
const schemaKinds = (
  reviewSchema.$defs.block.oneOf as { properties: { kind: { const: string } } }[]
).map((variant) => variant.properties.kind.const);

/** Stored description images as a document indexes them. */
const attachments: IndexedAttachment[] = [
  {
    id: "att_0123456789abcdef",
    position: 1,
    source_url: "https://github.com/user-attachments/assets/first",
    content_type: "image/png",
    width: 1200,
    height: 800,
    alt: "First alt",
  },
  {
    id: "att_fedcba9876543210",
    position: 2,
    source_url: "https://github.com/user-attachments/assets/second",
    content_type: "image/webp",
    width: 640,
    height: 480,
    alt: "Second alt",
  },
];

function render(blocks: unknown[], repository = "acme/widgets") {
  const document = structuredClone(reviewDocumentFor(repository, 42)) as ReviewDocument;
  document.index.attachments = attachments;
  const section = { ...document.sections[0]!, blocks: blocks as Block[] };
  return renderToStaticMarkup(
    <AttachmentUrlProvider resolve={(id) => `/attachments/${id}`}>
      <ReviewSection document={document} section={section} onSelectSection={() => {}} />
    </AttachmentUrlProvider>,
  );
}

describe("review block fixtures", () => {
  it("cover every block kind the schema defines", () => {
    expect(Object.keys(fixtures).toSorted()).toEqual(schemaKinds.toSorted());
  });

  it("leave a section's entities out", () => {
    const html = render([fixtures.entities!.block]);
    expect(html).not.toContain("Changed entities");
    expect(html).not.toContain("quuxfn");
  });

  for (const [kind, { block, shows }] of Object.entries(fixtures)) {
    it(`render a schema-valid ${kind} block with its content`, () => {
      expect(validBlock(block), JSON.stringify(validBlock.errors)).toBe(true);
      const html = render([block]);
      for (const text of shows) expect(html).toContain(text);
      expect(html).not.toContain("[object Object]");
      expect(html).not.toContain("undefined");
    });
  }

  it("render every kind together in one section", () => {
    const html = render(Object.values(fixtures).map(({ block }) => block));
    for (const { shows } of Object.values(fixtures)) {
      for (const text of shows) expect(html).toContain(text);
    }
  });
});

describe("media blocks", () => {
  const media = (url: string) => ({ kind: "media", url, caption: "Attachment caption", source });

  it("link non-GitHub http(s) URLs, GitHub uploads and this repository's GitHub pages", () => {
    for (const url of [
      "https://github.com/acme/widgets/pull/42/files",
      "https://github.com/user-attachments/assets/0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0",
      "https://ci.example.com/run/1",
      "http://example.com/shot.png",
    ]) {
      const html = render([media(url)]);
      expect(html, url).toContain(`href="${url}"`);
    }
  });

  it("show the caption without a link for other schemes and other repositories", () => {
    for (const url of [
      // Built so no literal script URL sits in the source.
      ["java", "script:alert(1)"].join(""),
      "data:text/html,<script>alert(1)</script>",
      "file:///etc/passwd",
      "https://github.com/someone-else/widgets/blob/main/secret.png",
      "not a url",
    ]) {
      expect(validBlock(media(url)), url).toBe(true);
      const html = render([media(url)]);
      expect(html, url).toContain("Attachment caption");
      expect(html, url).not.toContain('href="' + url);
      expect(html, url).not.toMatch(/href="(?:java|data|file)/);
    }
  });
});

const images = (items: { attachment: string; caption?: string }[]) => ({
  kind: "images",
  items,
  source: [{ type: "pr_body", section: "Screenshots" }],
});

describe("images blocks", () => {
  it("draw each image in reading order at its stored size, captioned or else with its alt text", () => {
    const block = images([
      { attachment: "att_fedcba9876543210" },
      { attachment: "att_0123456789abcdef", caption: "The picker, open" },
    ]);
    expect(validBlock(block)).toBe(true);
    const html = render([block]);
    const second = html.indexOf("attachments/att_fedcba9876543210");
    const first = html.indexOf("attachments/att_0123456789abcdef");
    expect(second).toBeGreaterThan(-1);
    expect(first).toBeGreaterThan(second);
    expect(html).toContain(
      '<figcaption class="text-base text-(--text-tertiary)">Second alt</figcaption>',
    );
    expect(html).toContain(
      '<figcaption class="text-base text-(--text-tertiary)">The picker, open</figcaption>',
    );
    expect(html).toContain('width="640" height="480"');
    expect(html).toContain("Description § Screenshots");
    // Only the stored copy is loaded, never the description's own address.
    expect(html).not.toContain("user-attachments");
  });

  it("leave out images the document does not list, and hide a block left empty", () => {
    const html = render([images([{ attachment: "att_00000000" }])]);
    expect(html).not.toContain('aria-label="Images"');
    expect(html).not.toContain("attachments/att_00000000");
  });

  it("draw from the version's own index, not the current description", () => {
    // A stored version lists an image the current description has dropped: the
    // page draws it from that version's index alone.
    const document = structuredClone(reviewDocumentFor("acme/widgets", 42, 1)) as ReviewDocument;
    document.index.attachments = [attachments[1]!];
    const section = {
      ...document.sections[0]!,
      blocks: [images([{ attachment: "att_fedcba9876543210" }])] as Block[],
    };
    const html = renderToStaticMarkup(
      <AttachmentUrlProvider resolve={(id) => `/attachments/${id}`}>
        <ReviewSection document={document} section={section} onSelectSection={() => {}} />
      </AttachmentUrlProvider>,
    );
    expect(html).toContain("attachments/att_fedcba9876543210");
    expect(html).toContain("Second alt");
  });
});
