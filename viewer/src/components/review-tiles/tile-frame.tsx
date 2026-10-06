import type { ReactNode } from "react";

import { CodeFileLink } from "../code-file-link";
import { CodeDiff, type CodeDiffFile } from "./code-diff";
import type { TileSourceFile } from "./tile-code-links";
import { type TileKeyFact, TileKeyFacts } from "./tile-key-facts";

/** Files the tile's claims were read from. Each file either shows the change
 *  to review or says why it is listed without an excerpt. */
export type TileSource = {
  code: readonly TileSourceFile[];
  codeDiff: readonly CodeDiffFile[];
};

/** The frame every tile shares: title, body, key facts and — always — the
 *  code behind it. Tiles render through this rather than assembling their
 *  own outer section, so no tile can state a conclusion without its source.
 *
 *  Every tile wears the same neutral frame as the rest of the review page.
 *  Colour inside a tile is reserved for meaning — a +/− mark, a status badge,
 *  a key-fact dot, diff lines — never for framing or emphasis, so a tile never
 *  looks louder than the page around it. */
export function TileShell({
  title,
  facts,
  source,
  children,
}: {
  title: string;
  facts?: readonly TileKeyFact[];
  source: TileSource;
  children: ReactNode;
}) {
  return (
    <section
      aria-label={title}
      className="min-w-0 rounded-(--radius-md) border border-(--border-default) bg-(--surface-sunken) p-4 [overflow-wrap:anywhere]"
    >
      <h4 className="text-base font-medium tracking-[-0.01em] text-(--text-primary)">{title}</h4>
      {children}
      {facts ? <TileKeyFacts facts={facts} /> : null}
      <TileCode source={source} />
    </section>
  );
}

/** The lead a tile opens with: what changed, as a headline and a sentence,
 *  over the counts worth knowing. A badge that would count nothing is left
 *  out by its caller, and the row goes when no badge is left. */
export function TileGlance({
  label = "At a glance",
  headline,
  description,
  badges = [],
}: {
  label?: string;
  headline: string;
  description: string;
  badges?: readonly ReactNode[];
}) {
  const shown = badges.filter(Boolean);
  return (
    <section
      aria-label={label}
      className="mt-4 overflow-hidden rounded-(--radius-md) border border-(--border-default) bg-(--surface-card)"
    >
      <div className="px-4 py-3.5">
        <p className="text-xs font-semibold tracking-[0.12em] uppercase text-(--text-tertiary)">
          At a glance
        </p>
        <h5 className="mt-1 text-lg font-semibold tracking-[-0.01em] text-(--text-primary)">
          {headline}
        </h5>
        <p className="mt-1 max-w-[62rem] text-base leading-[1.5] text-(--text-secondary)">
          {description}
        </p>
      </div>
      {shown.length > 0 && (
        <div className="flex flex-wrap gap-2 border-t border-(--border-subtle) px-4 py-2.5">
          {shown}
        </div>
      )}
    </section>
  );
}

/** Detail a reader asks for, rather than reads past: closed until wanted, with
 *  the summary saying what opening it will show. */
export function TileDisclosure({
  label,
  summary,
  children,
  onToggle,
}: {
  label: string;
  summary: string;
  children: ReactNode;
  onToggle?: (open: boolean) => void;
}) {
  return (
    <details
      className="group mt-3 rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card)"
      onToggle={(event) => onToggle?.(event.currentTarget.open)}
    >
      <summary
        aria-label={label}
        className="cursor-pointer list-none marker:hidden [&::-webkit-details-marker]:hidden"
      >
        <span className="flex items-center gap-3 px-3 py-3">
          <span
            aria-hidden
            className="text-(--text-tertiary) transition-transform group-open:rotate-90"
          >
            ›
          </span>
          <span className="min-w-0 flex-1">
            <span className="block text-base font-medium text-(--text-primary)">{label}</span>
            <span className="mt-0.5 block text-xs text-(--text-tertiary)">{summary}</span>
          </span>
          <span className="text-xs font-semibold tracking-[0.08em] uppercase text-(--text-link) group-open:hidden">
            Show
          </span>
          <span className="hidden text-xs font-semibold tracking-[0.08em] uppercase text-(--text-link) group-open:inline">
            Hide
          </span>
        </span>
      </summary>
      <div className="border-t border-(--border-subtle) px-3 pb-3">{children}</div>
    </details>
  );
}

/** A missing excerpt does not imply that a file is safe to skip. */
export function TileCode({ source }: { source: TileSource }) {
  return (
    <TileDisclosure label="Code" summary={`${source.code.length} source files`}>
      <section aria-label="Code" className="mt-3 min-w-0 space-y-3">
        {source.code.length === 0 ? (
          <p className="text-base text-(--text-tertiary)">
            No code excerpts supplied. See the source references.
          </p>
        ) : (
          source.code.map((link, index) => {
            const file = source.codeDiff.find((item) => item.sourceIndex === index);
            return (
              <div key={`${link.href}:${index}`} className="min-w-0">
                <CodeFileLink
                  path={link.label}
                  line={file?.hunks[0]?.lines.find((line) => line.newLine)?.newLine}
                  href={link.href}
                  className="inline-flex max-w-full items-center gap-1 break-all text-base text-(--text-link) hover:underline"
                >
                  {link.label}
                </CodeFileLink>
                <p className="text-base text-(--text-secondary)">{link.why}</p>
                <p className="text-xs text-(--text-tertiary)">
                  {link.older ? "Older source · " : "Source · "}review v{link.reviewVersion}
                </p>
                {file ? (
                  <CodeDiff files={[file]} />
                ) : (
                  <p className="mt-2 text-base text-(--text-tertiary)">
                    Code excerpt not supplied.
                  </p>
                )}
              </div>
            );
          })
        )}
      </section>
    </TileDisclosure>
  );
}
