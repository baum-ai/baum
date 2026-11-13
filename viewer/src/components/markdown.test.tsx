// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import { ReviewFilesProvider } from "./code-file-link";
import { InlineMarkdown, Markdown } from "./markdown";
import { ReviewHunksContext, type ReviewHunk } from "./review-contexts";

// `remark-breaks` is attached each time React Markdown builds its processor,
// so its call count is the number of times the pipeline was set up.
const attached = vi.hoisted(() => ({ count: 0 }));
vi.mock("remark-breaks", async (importOriginal) => {
  const actual = await importOriginal<typeof import("remark-breaks")>();
  return {
    default: (...args: Parameters<typeof actual.default>) => {
      attached.count += 1;
      return actual.default(...args);
    },
  };
});

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

const roots: Root[] = [];

beforeAll(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  act(() => roots.splice(0).forEach((root) => root.unmount()));
  document.body.replaceChildren();
});

const SAMPLES: Record<string, string> = {
  plain: "Just some text.\nSecond line.",
  table: "| a | b |\n| - | - |\n| 1 | 2 |",
  details: "<details><summary>More</summary>\n\nhidden **bold**\n\n</details>",
  img: 'before <img src="https://example.com/x.png" alt="shot"> after',
  kbd: "Press <kbd>Ctrl</kbd> + <kbd>C</kbd>",
  comment: "visible <!-- hidden marker --> text",
  blocks:
    "# Title\n\n- one\n- two\n  - nested\n\n1. a\n2. b\n\n> quote\n\n---\n\n~~gone~~ and `code` and **bold**",
  tasks: "- [x] done\n- [ ] todo",
  lessThan: "a < b and c > d",
  fence: "```ts\nconst x = 1 < 2;\n```\n\n```\nplain <b>fence</b>\n```",
  autolink: "see https://example.com/a?b=1 and <https://example.org>",
  xss: '<script>alert(1)</script><img src=x onerror="alert(1)"><a href="javascript:alert(1)">x</a>',
};

const renderAll = (props: { html?: boolean; reading?: boolean } = {}) =>
  Object.fromEntries(
    Object.entries(SAMPLES).map(([name, text]) => [
      name,
      renderToStaticMarkup(<Markdown {...props}>{text}</Markdown>),
    ]),
  );

describe("Markdown output", () => {
  it("renders representative inputs with raw HTML on", () => {
    expect(renderAll()).toMatchInlineSnapshot(`
      {
        "autolink": "<div class="space-y-2"><p>see <a href="https://example.com/a?b=1" target="_blank" rel="noreferrer" class="text-link underline">https://example.com/a?b=1</a> and <a href="https://example.org" target="_blank" rel="noreferrer" class="text-link underline">https://example.org</a></p></div>",
        "blocks": "<div class="space-y-2"><p class="font-semibold">Title</p>
      <ul class="list-disc space-y-1 pl-5">
      <li>one</li>
      <li>two
      <ul class="list-disc space-y-1 pl-5">
      <li>nested</li>
      </ul>
      </li>
      </ul>
      <ol class="list-decimal space-y-1 pl-5">
      <li>a</li>
      <li>b</li>
      </ol>
      <blockquote class="border-l-2 border-default pl-3 text-secondary">
      <p>quote</p>
      </blockquote>
      <hr class="border-subtle"/>
      <p><del>gone</del> and <code class="rounded-sm bg-code-fill px-1 py-0.5 text-base">code</code> and <strong>bold</strong></p></div>",
        "comment": "<div class="space-y-2"><p>visible  text</p></div>",
        "details": "<div class="space-y-2"><details class="rounded-sm border border-subtle p-2"><summary class="cursor-pointer font-medium">More</summary>
      <p>hidden <strong>bold</strong></p>
      </details></div>",
        "fence": "<div class="space-y-2"><pre class="overflow-x-auto rounded-sm bg-code-fill p-2 text-base"><code class="language-ts">const x = 1 &lt; 2;
      </code></pre>
      <pre class="overflow-x-auto rounded-sm bg-code-fill p-2 text-base"><code>plain &lt;b&gt;fence&lt;/b&gt;
      </code></pre></div>",
        "img": "<div class="space-y-2"><p>before <span class="inline-block rounded-sm border border-subtle px-2 py-1 text-tertiary italic">shot</span> after</p></div>",
        "kbd": "<div class="space-y-2"><p>Press <kbd>Ctrl</kbd> + <kbd>C</kbd></p></div>",
        "lessThan": "<div class="space-y-2"><p>a &lt; b and c &gt; d</p></div>",
        "plain": "<div class="space-y-2"><p>Just some text.<br/>
      Second line.</p></div>",
        "table": "<div class="space-y-2">












      <div class="relative overflow-x-auto"><table class="text-base [overflow-wrap:normal]"><thead><tr><th class="border border-subtle px-2 py-1 text-left font-medium">a</th><th class="border border-subtle px-2 py-1 text-left font-medium">b</th></tr></thead><tbody><tr><td class="border border-subtle px-2 py-1">1</td><td class="border border-subtle px-2 py-1">2</td></tr></tbody></table></div></div>",
        "tasks": "<div class="space-y-2"><ul class="list-disc space-y-1 pl-5">
      <li><input type="checkbox" disabled="" checked=""/> done</li>
      <li><input type="checkbox" disabled=""/> todo</li>
      </ul></div>",
        "xss": "<div class="space-y-2"><span class="inline-block rounded-sm border border-subtle px-2 py-1 text-tertiary italic">Image</span><a target="_blank" rel="noreferrer" class="text-link underline">x</a></div>",
      }
    `);
  });

  it("renders them as written with raw HTML off", () => {
    expect(renderAll({ html: false })).toMatchInlineSnapshot(`
      {
        "autolink": "<div class="space-y-2"><p>see <a href="https://example.com/a?b=1" target="_blank" rel="noreferrer" class="text-link underline">https://example.com/a?b=1</a> and <a href="https://example.org" target="_blank" rel="noreferrer" class="text-link underline">https://example.org</a></p></div>",
        "blocks": "<div class="space-y-2"><p class="font-semibold">Title</p>
      <ul class="list-disc space-y-1 pl-5">
      <li>one</li>
      <li>two
      <ul class="list-disc space-y-1 pl-5">
      <li>nested</li>
      </ul>
      </li>
      </ul>
      <ol class="list-decimal space-y-1 pl-5">
      <li>a</li>
      <li>b</li>
      </ol>
      <blockquote class="border-l-2 border-default pl-3 text-secondary">
      <p>quote</p>
      </blockquote>
      <hr class="border-subtle"/>
      <p><del>gone</del> and <code class="rounded-sm bg-code-fill px-1 py-0.5 text-base">code</code> and <strong>bold</strong></p></div>",
        "comment": "<div class="space-y-2"><p>visible &lt;!-- hidden marker --&gt; text</p></div>",
        "details": "<div class="space-y-2">&lt;details&gt;&lt;summary&gt;More&lt;/summary&gt;
      <p>hidden <strong>bold</strong></p>
      &lt;/details&gt;</div>",
        "fence": "<div class="space-y-2"><pre class="overflow-x-auto rounded-sm bg-code-fill p-2 text-base"><code class="language-ts">const x = 1 &lt; 2;
      </code></pre>
      <pre class="overflow-x-auto rounded-sm bg-code-fill p-2 text-base"><code>plain &lt;b&gt;fence&lt;/b&gt;
      </code></pre></div>",
        "img": "<div class="space-y-2"><p>before &lt;img src=&quot;https://example.com/x.png&quot; alt=&quot;shot&quot;&gt; after</p></div>",
        "kbd": "<div class="space-y-2"><p>Press &lt;kbd&gt;Ctrl&lt;/kbd&gt; + &lt;kbd&gt;C&lt;/kbd&gt;</p></div>",
        "lessThan": "<div class="space-y-2"><p>a &lt; b and c &gt; d</p></div>",
        "plain": "<div class="space-y-2"><p>Just some text.<br/>
      Second line.</p></div>",
        "table": "<div class="space-y-2"><div class="relative overflow-x-auto"><table class="text-base [overflow-wrap:normal]"><thead><tr><th class="border border-subtle px-2 py-1 text-left font-medium">a</th><th class="border border-subtle px-2 py-1 text-left font-medium">b</th></tr></thead><tbody><tr><td class="border border-subtle px-2 py-1">1</td><td class="border border-subtle px-2 py-1">2</td></tr></tbody></table></div></div>",
        "tasks": "<div class="space-y-2"><ul class="list-disc space-y-1 pl-5">
      <li><input type="checkbox" disabled="" checked=""/> done</li>
      <li><input type="checkbox" disabled=""/> todo</li>
      </ul></div>",
        "xss": "<div class="space-y-2">&lt;script&gt;alert(1)&lt;/script&gt;&lt;img src=x onerror=&quot;alert(1)&quot;&gt;&lt;a href=&quot;javascript:alert(1)&quot;&gt;x&lt;/a&gt;</div>",
      }
    `);
  });

  it("renders them in the reading style", () => {
    expect(renderAll({ reading: true })).toMatchInlineSnapshot(`
      {
        "autolink": "<div class="space-y-3"><p>see <a href="https://example.com/a?b=1" target="_blank" rel="noreferrer" class="text-link underline">https://example.com/a?b=1</a> and <a href="https://example.org" target="_blank" rel="noreferrer" class="text-link underline">https://example.org</a></p></div>",
        "blocks": "<div class="space-y-3"><h3 class="mt-6 border-b border-subtle pb-2 font-(family-name:--font-heading) text-lg font-extrabold text-primary first:mt-0">Title</h3>
      <ul class="list-disc space-y-1.5 pl-5 marker:text-tertiary [&amp;_ol]:mt-1 [&amp;_ol]:space-y-1 [&amp;_ul]:mt-1 [&amp;_ul]:space-y-1">
      <li>one</li>
      <li>two
      <ul class="list-disc space-y-1.5 pl-5 marker:text-tertiary [&amp;_ol]:mt-1 [&amp;_ol]:space-y-1 [&amp;_ul]:mt-1 [&amp;_ul]:space-y-1">
      <li>nested</li>
      </ul>
      </li>
      </ul>
      <ol class="list-decimal space-y-1.5 pl-5 marker:text-tertiary [&amp;_ol]:mt-1 [&amp;_ol]:space-y-1 [&amp;_ul]:mt-1 [&amp;_ul]:space-y-1">
      <li>a</li>
      <li>b</li>
      </ol>
      <blockquote class="border-l-2 border-default pl-3 text-secondary">
      <p>quote</p>
      </blockquote>
      <hr class="border-subtle"/>
      <p><del>gone</del> and <code class="rounded-xs bg-code-fill px-1 py-px font-mono text-base text-primary">code</code> and <strong>bold</strong></p></div>",
        "comment": "<div class="space-y-3"><p>visible  text</p></div>",
        "details": "<div class="space-y-3"><details class="rounded-sm border border-subtle p-2"><summary class="cursor-pointer font-medium">More</summary>
      <p>hidden <strong>bold</strong></p>
      </details></div>",
        "fence": "<div class="space-y-3"><pre class="overflow-x-auto rounded-sm bg-code-fill p-2 text-base"><code class="language-ts">const x = 1 &lt; 2;
      </code></pre>
      <pre class="overflow-x-auto rounded-sm bg-code-fill p-2 text-base"><code>plain &lt;b&gt;fence&lt;/b&gt;
      </code></pre></div>",
        "img": "<div class="space-y-3"><p>before <span class="inline-block rounded-sm border border-subtle px-2 py-1 text-tertiary italic">shot</span> after</p></div>",
        "kbd": "<div class="space-y-3"><p>Press <kbd>Ctrl</kbd> + <kbd>C</kbd></p></div>",
        "lessThan": "<div class="space-y-3"><p>a &lt; b and c &gt; d</p></div>",
        "plain": "<div class="space-y-3"><p>Just some text.<br/>
      Second line.</p></div>",
        "table": "<div class="space-y-3">












      <div class="relative overflow-x-auto"><table class="w-full border-collapse text-base [overflow-wrap:normal]"><thead><tr><th class="border border-subtle bg-code-fill px-3 py-2 text-left align-bottom font-semibold">a</th><th class="border border-subtle bg-code-fill px-3 py-2 text-left align-bottom font-semibold">b</th></tr></thead><tbody><tr><td class="min-w-32 border border-subtle px-3 py-2 align-top">1</td><td class="min-w-32 border border-subtle px-3 py-2 align-top">2</td></tr></tbody></table></div></div>",
        "tasks": "<div class="space-y-3"><ul class="list-disc space-y-1.5 pl-5 marker:text-tertiary [&amp;_ol]:mt-1 [&amp;_ol]:space-y-1 [&amp;_ul]:mt-1 [&amp;_ul]:space-y-1">
      <li><input type="checkbox" disabled="" checked=""/> done</li>
      <li><input type="checkbox" disabled=""/> todo</li>
      </ul></div>",
        "xss": "<div class="space-y-3"><span class="inline-block rounded-sm border border-subtle px-2 py-1 text-tertiary italic">Image</span><a target="_blank" rel="noreferrer" class="text-link underline">x</a></div>",
      }
    `);
  });

  it("renders inline markdown", () => {
    expect(
      renderToStaticMarkup(
        <InlineMarkdown className="x">
          {"**b** `c` [l](https://e.com) <i>t</i>\n\n# h"}
        </InlineMarkdown>,
      ),
    ).toMatchInlineSnapshot(`
      "<span class="x"><strong>b</strong> <code class="rounded-sm bg-code-fill px-1 py-0.5">c</code> l &lt;i&gt;t&lt;/i&gt; 
      h</span>"
    `);
  });
});

function Parent() {
  const [tick, setTick] = useState(0);
  return (
    <div>
      <button onClick={() => setTick(tick + 1)}>{tick}</button>
      <Markdown className="m">{"some **text** <b>x</b>"}</Markdown>
      <InlineMarkdown>{"some `code`"}</InlineMarkdown>
      <Markdown>{`changing ${tick}`}</Markdown>
    </div>
  );
}

describe("memoisation", () => {
  it("does not rebuild the pipeline for blocks whose props are unchanged", () => {
    attached.count = 0;
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    roots.push(root);
    act(() => root.render(<Parent />));
    // Two Markdown blocks; the inline form does not use `remark-breaks`.
    expect(attached.count).toBe(2);

    act(() => host.querySelector("button")!.click());
    // Only the block whose text changed ran again.
    expect(attached.count).toBe(3);
    expect(host.textContent).toContain("changing 1");
  });
});

const HUNKS = new Map<string, ReviewHunk>([
  [
    "h_c4094314a17c",
    {
      path: "oss/app/src/ui-state.tsx",
      line: 42,
      href: "https://github.com/o/r/pull/1/files#diff-abc",
      title: "Modified source hunk in oss/app/src/ui-state.tsx, +3 −1",
    },
  ],
]);

const withHunks = (text: string, html = true) =>
  renderToStaticMarkup(
    <ReviewHunksContext.Provider value={HUNKS}>
      <Markdown html={html}>{text}</Markdown>
    </ReviewHunksContext.Provider>,
  );

describe("hunk IDs in review prose", () => {
  it("shows a known hunk as its file and line, and leaves an unknown one", () => {
    const html = withHunks("Shared by every view (h_c4094314a17c); not h_000000000000.");
    expect(html).toContain(">ui-state.tsx:42<");
    expect(html).toContain('title="Modified source hunk in oss/app/src/ui-state.tsx, +3 −1"');
    expect(html).toContain('href="https://github.com/o/r/pull/1/files#diff-abc"');
    expect(html).not.toContain("h_c4094314a17c");
    expect(html).toContain("h_000000000000");
  });

  it("links a code span holding only the ID, not link text or code blocks", () => {
    const html = withHunks(
      "See `h_c4094314a17c` and [h_c4094314a17c](https://e.com).\n\n```\nh_c4094314a17c\n```",
    );
    expect(html.match(/ui-state\.tsx:42/g)).toHaveLength(1);
    expect(html.match(/h_c4094314a17c/g)).toHaveLength(2);
  });

  it("links without HTML parsing too", () => {
    expect(withHunks("cites h_c4094314a17c", false)).toContain(">ui-state.tsx:42<");
  });

  it("leaves the ID as written outside a review", () => {
    expect(renderToStaticMarkup(<Markdown>{"cites h_c4094314a17c"}</Markdown>)).toContain(
      "cites h_c4094314a17c",
    );
  });

  it("does not let a comment's markup make a hunk link", () => {
    const html = withHunks('<a data-hunk-ref="h_c4094314a17c" href="https://evil.example">x</a>');
    expect(html).not.toContain("ui-state.tsx");
    expect(html).toContain("https://evil.example");
  });

  it("opens the file view at the hunk's line", () => {
    const open = vi.fn(() => true);
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    roots.push(root);
    act(() =>
      root.render(
        <ReviewFilesProvider files={{ has: () => true, open }}>
          <ReviewHunksContext.Provider value={HUNKS}>
            <Markdown>{"cites h_c4094314a17c"}</Markdown>
          </ReviewHunksContext.Provider>
        </ReviewFilesProvider>,
      ),
    );
    const chip = host.querySelector("button")!;
    expect(chip.textContent).toBe("ui-state.tsx:42");
    // Prose laid over a row's link turns pointer events off for all but links.
    expect(chip.classList).toContain("pointer-events-auto");
    act(() => chip.click());
    expect(open).toHaveBeenCalledWith("oss/app/src/ui-state.tsx", 42);
  });
});
