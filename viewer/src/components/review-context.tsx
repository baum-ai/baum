import {
  contextRefUrl,
  isContextDirectory,
  type ReviewContext,
  type ReviewContextRef,
  type ReviewDocument,
} from "../review-document";
import { cn, FOCUS_RING } from "./classnames";
import { useReviewFiles, useReviewSources } from "./review-contexts";
import { ChevronRightIcon, ExternalLinkIcon } from "./icons";
import { Markdown } from "./markdown";
import { chipLinkClass } from "./chip-classes";
import { PROSE } from "./review-parts";
import { Spinner } from "./ui";

/** The context's parts in reading order, each with its label. */
const PARTS: { key: keyof ReviewContext; label: string }[] = [
  { key: "area", label: "Area" },
  { key: "reached_by", label: "How it's reached" },
  { key: "contracts", label: "Contracts" },
  { key: "related", label: "Related code" },
];

/** Where the change sits in the codebase, for a reviewer new to that area.
 *  It starts folded under the overview, since a reviewer who knows the area
 *  does not need it. Nothing renders for a document generated before the
 *  context existed. While a host is still writing a draft's context
 *  (`pending`), the part says so in its place. */
export function ContextPart({
  document,
  pending = false,
}: {
  document: ReviewDocument;
  pending?: boolean;
}) {
  const context = document.summary.context;
  if (!context && pending) {
    return (
      <section aria-label="Context" className="min-w-0">
        <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 border-b border-subtle pb-1.5">
          <h3 className="text-base font-semibold tracking-[0.08em] text-primary uppercase">
            Context
          </h3>
          <span className="text-xs text-tertiary">Where this change sits in the codebase</span>
        </div>
        <p role="status" className="mt-3 flex items-center gap-2 text-base text-secondary">
          <Spinner size={12} className="text-tertiary" />
          Writing context…
        </p>
      </section>
    );
  }
  if (!context) return null;
  const parts = PARTS.flatMap(({ key, label }) => {
    const part = context[key];
    return part ? [{ key, label, part }] : [];
  });
  return (
    <section aria-label="Context" className="min-w-0">
      <details className="group">
        <summary
          className={cn(
            "flex cursor-pointer list-none flex-wrap items-baseline gap-x-3 gap-y-1 border-b border-subtle pb-1.5 [&::-webkit-details-marker]:hidden",
            FOCUS_RING,
          )}
        >
          <h3 className="flex items-center gap-1.5 text-base font-semibold tracking-[0.08em] text-primary uppercase">
            <ChevronRightIcon
              size={12}
              className="flex-none self-center text-tertiary transition-transform group-open:rotate-90"
            />
            Context
          </h3>
          <span className="text-xs text-tertiary">Where this change sits in the codebase</span>
        </summary>
        <dl className="mt-3 space-y-4">
          {parts.map(({ key, label, part }) => (
            <div
              key={key}
              className="grid min-w-0 grid-cols-1 gap-x-3 gap-y-0.5 @md:grid-cols-[6.5rem_minmax(0,1fr)]"
            >
              <dt className="text-xs leading-[1.7rem] font-medium tracking-[0.06em] text-tertiary uppercase">
                {label}
              </dt>
              <dd className="m-0 min-w-0 space-y-2">
                <Markdown className={PROSE}>{part.text}</Markdown>
                {part.refs.length > 0 && (
                  <ul className="flex flex-wrap gap-1.5" aria-label={`${label} files`}>
                    {part.refs.map((ref, position) => (
                      <li
                        key={`${ref.path}-${ref.symbol}-${position}`}
                        className="max-w-full min-w-0"
                      >
                        <ContextRefChip document={document} reference={ref} />
                      </li>
                    ))}
                  </ul>
                )}
              </dd>
            </div>
          ))}
        </dl>
      </details>
    </section>
  );
}

/** A file or directory the context rests on, as a compact chip: its symbol
 *  first, as code, then the file's name; the folder is in its title. A file
 *  opens in the host's dialog when it can show one (`openRepositoryFile`),
 *  changed or not; else a changed file opens the review's file view, and
 *  anything else, like a directory, GitHub. */
function ContextRefChip({
  document,
  reference,
}: {
  document: ReviewDocument;
  reference: ReviewContextRef;
}) {
  const files = useReviewFiles();
  const sources = useReviewSources();
  const directory = isContextDirectory(document, reference.path);
  const path = reference.path.replace(/\/+$/, "");
  const href = contextRefUrl(document, path);
  const title = reference.symbol ? `${reference.symbol} in ${path}` : path;
  const label = <ContextRefLabel path={path} directory={directory} symbol={reference.symbol} />;
  // A file opens as the file itself, in the host's dialog, whether or not the
  // pull request changes it: context is about the code, not the diff, and
  // the reader stays where they are. Without one, a changed file opens the
  // review's file view.
  const openRepositoryFile = sources?.openRepositoryFile;
  if (!directory && openRepositoryFile) {
    return (
      <button
        type="button"
        title={title}
        className={cn(chipLinkClass, "cursor-pointer text-left", FOCUS_RING)}
        onClick={() => {
          if (!openRepositoryFile({ path, symbol: reference.symbol })) {
            window.open(href, "_blank", "noreferrer");
          }
        }}
      >
        {label}
      </button>
    );
  }
  if (files?.has(path)) {
    return (
      <button
        type="button"
        title={title}
        className={cn(chipLinkClass, "cursor-pointer text-left", FOCUS_RING)}
        onClick={() => files.open(path)}
      >
        {label}
      </button>
    );
  }
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      title={title}
      className={cn(chipLinkClass, FOCUS_RING)}
    >
      {label}
      <ExternalLinkIcon size={10} className="flex-none text-tertiary" />
    </a>
  );
}

/** "Symbol · name.go" on one line: the symbol first, as code, since it is
 *  what the paragraph names, then the file's name in quiet text; the folder
 *  is left to the chip's title. Without a symbol, the name alone. A
 *  directory shows its last segment with a trailing "/". */
function ContextRefLabel({
  path,
  directory,
  symbol,
}: {
  path: string;
  directory: boolean;
  symbol: string | null;
}) {
  const name = `${path.slice(path.lastIndexOf("/") + 1)}${directory ? "/" : ""}`;
  return (
    <span className="flex min-w-0 items-baseline gap-1.5 whitespace-nowrap">
      {symbol ? (
        <>
          <code className="min-w-[4ch] shrink truncate font-mono text-xs text-primary">
            {symbol}
          </code>
          <span aria-hidden className="flex-none text-tertiary">
            ·
          </span>
          <span className="min-w-[4ch] shrink-[2] truncate text-secondary">{name}</span>
        </>
      ) : (
        <span className="min-w-[4ch] shrink truncate text-primary">{name}</span>
      )}
    </span>
  );
}
