import ReactMarkdown, { type Components } from "react-markdown";
import rehypeRaw from "rehype-raw";
import rehypeSanitize from "rehype-sanitize";
import remarkBreaks from "remark-breaks";
import remarkGfm from "remark-gfm";
import { cn } from "./classnames";

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

const COMPONENTS: Components = {
  a: ({ href, children }) => (
    <a href={href} target="_blank" rel="noreferrer" className="text-(--text-link) underline">
      {children}
    </a>
  ),
  p: ({ children }) => <p className="whitespace-pre-wrap">{children}</p>,
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
    <pre className="overflow-x-auto rounded-(--radius-sm) bg-(--surface-sunken) p-2 text-base">
      {children}
    </pre>
  ),
  // React Markdown stopped passing an `inline` flag, and a fenced block
  // without a language sits in the exact same `className`-less state as an
  // inline span. A backtick span cannot itself contain a literal newline, so
  // that — not the class — is what actually tells them apart: a block keeps
  // its language class (if any) and leaves the box styling to `pre` above;
  // only a genuinely inline span gets its own pill.
  code: ({ className, children }) =>
    String(children).includes("\n") ? (
      <code className={className}>{children}</code>
    ) : (
      <code className="rounded-(--radius-sm) bg-(--surface-sunken) px-1 py-0.5 text-base">
        {children}
      </code>
    ),
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

export function Markdown({
  children,
  className,
  title,
}: {
  children: string;
  className?: string;
  title?: string;
}) {
  return (
    <div className={cn("space-y-2", className)} title={title}>
      {/* GitHub renders every line break in a comment body, not only ones
       *  followed by a blank line or two trailing spaces the way strict
       *  CommonMark does. Without `remark-breaks`, an ordinarily-typed
       *  multi-line comment collapses into a single run-on paragraph. */}
      <ReactMarkdown
        remarkPlugins={[remarkGfm, remarkBreaks]}
        rehypePlugins={REHYPE_PLUGINS}
        components={COMPONENTS}
      >
        {children}
      </ReactMarkdown>
    </div>
  );
}
