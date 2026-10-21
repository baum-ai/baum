import {
  contextRefUrl,
  type ReviewContext,
  type ReviewContextRef,
  type ReviewDocument,
} from "../review-document";
import { cn, FOCUS_RING } from "./classnames";
import { ChevronRightIcon } from "./icons";
import { Markdown } from "./markdown";
import { CodeChip, PROSE } from "./review-parts";

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
 *  context existed. */
export function ContextPart({ document }: { document: ReviewDocument }) {
  const context = document.summary.context;
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
            "flex cursor-pointer list-none flex-wrap items-baseline gap-x-3 gap-y-1 border-b border-(--border-subtle) pb-1.5 [&::-webkit-details-marker]:hidden",
            FOCUS_RING,
          )}
        >
          <h3 className="flex items-center gap-1.5 text-base font-semibold tracking-[0.08em] text-(--text-primary) uppercase">
            <ChevronRightIcon
              size={12}
              className="flex-none self-center text-(--text-tertiary) transition-transform group-open:rotate-90"
            />
            Context
          </h3>
          <span className="text-xs text-(--text-tertiary)">
            Where this change sits in the codebase
          </span>
        </summary>
        <div className="mt-3 space-y-4">
          {parts.map(({ key, label, part }) => (
            <div key={key} className="space-y-1.5">
              <h4 className="text-base font-medium text-(--text-secondary)">{label}</h4>
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
            </div>
          ))}
        </div>
      </details>
    </section>
  );
}

/** A file or directory the context rests on. It opens the review's file view
 *  when the pull request changes the file, and otherwise GitHub. */
function ContextRefChip({
  document,
  reference,
}: {
  document: ReviewDocument;
  reference: ReviewContextRef;
}) {
  const label = reference.symbol ? `${reference.path} · ${reference.symbol}` : reference.path;
  return (
    <CodeChip
      path={reference.path}
      href={contextRefUrl(document, reference.path)}
      title={reference.symbol ? `${reference.symbol} in ${reference.path}` : reference.path}
    >
      <span className="min-w-0 truncate">{label}</span>
    </CodeChip>
  );
}
