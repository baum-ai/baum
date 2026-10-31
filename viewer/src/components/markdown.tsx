import { Fragment, useEffect, useState, type ReactNode } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import rehypeRaw from "rehype-raw";
import rehypeSanitize from "rehype-sanitize";
import remarkBreaks from "remark-breaks";
import remarkGfm from "remark-gfm";
import {
  highlightCode,
  languageForFence,
  withinHighlightLimits,
  type HighlightedLine,
  type Lang,
} from "../highlight";
import { cn } from "./classnames";
import { HighlightedText } from "./highlighted-text";

const REHYPE_PLUGINS = [
  // GitHub comments routinely mix raw HTML into the markdown — bold tags,
  // `<details>` disclosures, automation markers hidden in HTML comments. Left
  // unparsed, React Markdown cannot tell an element from text and prints the
  // whole tag literally; `rehype-raw` parses it into real nodes instead
  // (which is also what makes an HTML comment disappear instead of printing).
  // `rehype-sanitize`'s default schema is itself modelled on GitHub's own
  // allow-list, so it keeps that formatting while dropping anything that
  // could execute — `<script>`, event handlers, `javascript:` links — none
  // of it authored by us. React Markdown never uses `dangerouslySetInnerHTML`,
  // so this is the only path raw HTML in a comment can reach the page.
  rehypeRaw,
  rehypeSanitize,
];

const linesText = (lines: readonly HighlightedLine[]) =>
  lines.map((line) => line.map((token) => token.content).join("")).join("\n");

/** The text of a code block's children when they are only text, else null: a
 *  block holding raw HTML elements is rendered as it is, never as a string. */
function textOf(children: ReactNode): string | null {
  if (typeof children === "string") return children;
  if (Array.isArray(children) && children.every((child) => typeof child === "string")) {
    return children.join("");
  }
  return null;
}

/** A fenced block in a known language. It renders the plain text on the server
 *  and the first client render, then colours it once the highlighter is ready;
 *  the text never changes, so nothing shifts. The tokens are only ever
 *  rendered as React text and `var(--syntax-*)` colours, never as markup. */
function HighlightedCodeBlock({
  code,
  lang,
  className,
}: {
  code: string;
  lang: Lang;
  className?: string;
}) {
  const [result, setResult] = useState<{ key: string; lines: HighlightedLine[] } | null>(null);
  const key = `${lang}\0${code}`;

  useEffect(() => {
    let current = true;
    void highlightCode(code, lang).then((lines) => {
      // Tokens that do not add up to the source would change what is shown.
      if (current && lines && linesText(lines) === code) setResult({ key, lines });
    });
    return () => {
      current = false;
    };
    // `key` stands for `lang` and `code`.
    // oxlint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  const lines = result?.key === key ? result.lines : null;
  if (!lines) return <code className={className}>{code}</code>;
  return (
    <code className={className}>
      {lines.map((tokens, index) => (
        <Fragment key={index}>
          {index > 0 ? "\n" : null}
          <HighlightedText text={tokens.map((token) => token.content).join("")} tokens={tokens} />
        </Fragment>
      ))}
    </code>
  );
}

/** A fenced block's code, coloured when its fence names a language the
 *  highlighter knows and it is within the limits. */
function codeBlock(className: string | undefined, children: ReactNode) {
  // Only a fence's own `language-*` class names a language.
  const info = /(?:^|\s)language-(\S+)/.exec(className ?? "")?.[1];
  const lang = languageForFence(info);
  const code = textOf(children);
  // Over the limits it never schedules highlighting.
  const highlight =
    lang && code !== null && withinHighlightLimits(code.split("\n").length, code.length);
  return highlight ? (
    <HighlightedCodeBlock code={code} lang={lang} className={className} />
  ) : (
    <code className={className}>{children}</code>
  );
}

const COMPONENTS: Components = {
  a: ({ href, children }) => (
    <a href={href} target="_blank" rel="noreferrer" className="text-(--text-link) underline">
      {children}
    </a>
  ),
  // No pre-wrap: `remark-breaks` turns each line break into a <br> and keeps
  // the newline after it as text, so preserving whitespace would break twice.
  p: ({ children }) => <p>{children}</p>,
  ul: ({ children }) => <ul className="list-disc space-y-1 pl-5">{children}</ul>,
  ol: ({ children }) => <ol className="list-decimal space-y-1 pl-5">{children}</ol>,
  li: ({ children }) => <li>{children}</li>,
  hr: () => <hr className="border-(--border-subtle)" />,
  blockquote: ({ children }) => (
    <blockquote className="border-l-2 border-(--border-default) pl-3 text-(--text-secondary)">
      {children}
    </blockquote>
  ),
  h1: ({ children }) => <p className="font-semibold">{children}</p>,
  h2: ({ children }) => <p className="font-semibold">{children}</p>,
  h3: ({ children }) => <p className="font-medium">{children}</p>,
  pre: ({ children }) => (
    <pre className="overflow-x-auto rounded-(--radius-sm) bg-(--code-fill) p-2 text-base">
      {children}
    </pre>
  ),
  // React Markdown stopped passing an `inline` flag, and a fenced block
  // without a language sits in the exact same `className`-less state as an
  // inline span. A backtick span cannot itself contain a literal newline, so
  // that — not the class — is what actually tells them apart: a block keeps
  // its language class (if any) and leaves the box styling to `pre` above;
  // only a genuinely inline span gets its own pill.
  code: ({ className, children }) => {
    if (String(children).includes("\n")) return codeBlock(className, children);
    return (
      <code className="rounded-(--radius-sm) bg-(--code-fill) px-1 py-0.5 text-base">
        {children}
      </code>
    );
  },
  // A comment body usually breaks anywhere so a long path cannot widen the
  // pane; a table cell inheriting that shrinks to a letter a line on a phone.
  // It keeps whole words instead and scrolls inside its own positioned box.
  table: ({ children }) => (
    <div className="relative overflow-x-auto">
      <table className="text-base [overflow-wrap:normal]">{children}</table>
    </div>
  ),
  th: ({ children }) => (
    <th className="border border-(--border-subtle) px-2 py-1 text-left font-medium">{children}</th>
  ),
  td: ({ children }) => <td className="border border-(--border-subtle) px-2 py-1">{children}</td>,
  details: ({ children }) => (
    <details className="rounded-(--radius-sm) border border-(--border-subtle) p-2">
      {children}
    </details>
  ),
  summary: ({ children }) => <summary className="cursor-pointer font-medium">{children}</summary>,
  // A comment's body is contributor-controlled, unmoderated text — an
  // `<img src>` in it would fetch that URL the moment this pane opens,
  // before anyone chose to look at anything, handing whoever controls the
  // URL the reviewer's IP and confirmation that they opened this (possibly
  // private) review. GitHub's own renderer avoids exactly this by proxying
  // every image through camo; nothing here does that, so this stays inert
  // text rather than fetching. It also deliberately isn't a link: GitHub
  // comments routinely use an image as the clickable face of a real link
  // (a bot's "Open in ..." badge), and a nested `<a>` here would split that
  // link in two instead of just adding an image-viewing shortcut. "View on
  // GitHub" on the comment above already opens the real, camo-proxied image.
  img: ({ alt }) => (
    <span className="inline-block rounded-(--radius-sm) border border-(--border-subtle) px-2 py-1 text-(--text-tertiary) italic">
      {alt || "Image"}
    </span>
  ),
};

/** Long text read on its own, such as an agent's reply in a transcript:
 *  headings set apart in the heading font with more room above them than
 *  below, code spans on a fainter fill, quieter list markers, and lists
 *  spaced by depth. */
const READING_COMPONENTS: Components = {
  ...COMPONENTS,
  // A reply's title, over the sections a level down: heavier, and ruled off.
  h1: ({ children }) => (
    <h3 className="mt-6 border-b border-(--border-subtle) pb-2 font-(family-name:--font-heading) text-lg font-extrabold text-(--text-primary) first:mt-0">
      {children}
    </h3>
  ),
  h2: ({ children }) => (
    <h3 className="mt-6 font-(family-name:--font-heading) text-lg font-bold text-(--text-primary) first:mt-0">
      {children}
    </h3>
  ),
  h3: ({ children }) => (
    <h4 className="mt-5 text-base font-semibold text-(--text-primary) first:mt-0">{children}</h4>
  ),
  h4: ({ children }) => (
    <h5 className="mt-4 text-base font-semibold text-(--text-primary) first:mt-0">{children}</h5>
  ),
  ul: ({ children }) => (
    <ul className="list-disc space-y-1.5 pl-5 marker:text-(--text-tertiary) [&_ol]:mt-1 [&_ol]:space-y-1 [&_ul]:mt-1 [&_ul]:space-y-1">
      {children}
    </ul>
  ),
  ol: ({ children }) => (
    <ol className="list-decimal space-y-1.5 pl-5 marker:text-(--text-tertiary) [&_ol]:mt-1 [&_ol]:space-y-1 [&_ul]:mt-1 [&_ul]:space-y-1">
      {children}
    </ol>
  ),
  // Cells start at the top, the header on a faint fill. A path in a cell
  // breaks after its slashes (pathBreaks), so its column stays narrow
  // without splitting a name.
  table: ({ children }) => (
    <div className="relative overflow-x-auto">
      <table className="w-full border-collapse text-base [overflow-wrap:normal]">{children}</table>
    </div>
  ),
  th: ({ children }) => (
    <th className="border border-(--border-subtle) bg-(--code-fill) px-3 py-2 text-left align-bottom font-semibold">
      {children}
    </th>
  ),
  td: ({ children }) => (
    <td className="min-w-32 border border-(--border-subtle) px-3 py-2 align-top">{children}</td>
  ),
  code: ({ className, children }) => {
    if (String(children).includes("\n")) return codeBlock(className, children);
    return (
      <code className="rounded-(--radius-xs) bg-(--code-fill) px-1 py-px font-mono text-base text-(--text-primary)">
        {pathBreaks(children)}
      </code>
    );
  },
};

/** A code span's text with a break allowed after each slash, so a long path
 *  wraps between its folders rather than overflowing or splitting a name. */
function pathBreaks(children: ReactNode): ReactNode {
  const text = textOf(children);
  if (text === null || !text.includes("/")) return children;
  return text.split(/(?<=\/)/).map((part, index) => (
    <Fragment key={index}>
      {index > 0 ? <wbr /> : null}
      {part}
    </Fragment>
  ));
}

export function Markdown({
  children,
  className,
  title,
  html = true,
  reading = false,
}: {
  children: string;
  className?: string;
  title?: string;
  /** Parses raw HTML into elements, as GitHub does in a comment. Off for
   *  text that talks about markup rather than containing it, such as an
   *  agent's reply naming `<Button>`: the tag then reads as written. */
  html?: boolean;
  /** Long text read on its own (READING_COMPONENTS). */
  reading?: boolean;
}) {
  return (
    <div className={cn(reading ? "space-y-3" : "space-y-2", className)} title={title}>
      {/* GitHub renders every line break in a comment body, not only ones
       *  followed by a blank line or two trailing spaces the way strict
       *  CommonMark does. Without `remark-breaks`, an ordinarily-typed
       *  multi-line comment collapses into a single run-on paragraph. */}
      <ReactMarkdown
        remarkPlugins={[remarkGfm, remarkBreaks]}
        rehypePlugins={html ? REHYPE_PLUGINS : undefined}
        components={reading ? READING_COMPONENTS : COMPONENTS}
      >
        {children}
      </ReactMarkdown>
    </div>
  );
}

const INLINE_ELEMENTS = ["p", "strong", "em", "del", "code", "a", "br"];

const INLINE_COMPONENTS: Components = {
  // Paragraphs run together as one line of text: the inline form sits inside
  // a heading, a button or a sentence, where a block would not belong.
  p: ({ children }) => <>{children} </>,
  // Text only: inside a button or a link a nested link is invalid, and the
  // row it sits in already leads somewhere.
  a: ({ children }) => <>{children}</>,
  br: () => " ",
  code: ({ children }) => (
    <code className="rounded-(--radius-sm) bg-(--code-fill) px-1 py-0.5">{children}</code>
  ),
};

/** Markdown for a short line inside other text, a heading or a button: the
 *  emphasis, strikethrough and code spans render, links read as their text,
 *  and anything block-level (lists, headings, quotes, fences) is unwrapped
 *  into its text. Raw HTML stays text. */
export function InlineMarkdown({ children, className }: { children: string; className?: string }) {
  return (
    <span className={className}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        allowedElements={INLINE_ELEMENTS}
        unwrapDisallowed
        components={INLINE_COMPONENTS}
      >
        {children}
      </ReactMarkdown>
    </span>
  );
}
