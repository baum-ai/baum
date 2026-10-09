import { type ReactNode, useId, useState } from "react";

import { cn, FOCUS_RING } from "./classnames";
import { codePoints, formatCompact } from "./format";

/** A user prompt, or an agent's reply to one, collapsed by default.
 *
 *  Both sit on the conversation pane's sand, since a session is a conversation
 *  too. An agent message adds a rule of accent down its edge and an accent
 *  caption, and each says which it is in its caption. In
 *  a session card the two alternate, and a reader scanning down has to tell
 *  the question from the answer without reading either.
 *
 *  Prompts average a few kilobytes and reach sixty. Rendered at full height, a
 *  commit with three sessions becomes a page nobody scrolls; rendered as a
 *  single line, the thing the page exists to show is hidden. So: a fixed number
 *  of lines, a fade into the surface behind it, and the rest one click away.
 *
 *  `max-height` rather than `line-clamp`, which is unreliable against
 *  `pre-wrap` — and the whitespace in a prompt is meaningful, so it stays
 *  pre-wrapped. */
export function PromptBlock({
  text,
  fullLength,
  truncated = false,
  label,
  tone = "human",
  collapsedLines = 4,
  onExpand,
}: {
  text: string;
  /** Who wrote it. */
  tone?: "human" | "agent";
  /** The stored length. Larger than `text` when the server sent a preview. */
  fullLength?: number;
  truncated?: boolean;
  label?: ReactNode;
  collapsedLines?: number;
  /** Fetches the rest. Called each time the block is expanded, never when
   *  it is collapsed; a host that has the rest already ignores it. */
  onExpand?: () => void;
}) {
  const [expanded, setExpanded] = useState(false);
  // Characters as the hosts count them (code points), not UTF-16 units, so
  // the shown and full lengths compare.
  const characters = codePoints(text);
  const bodyId = useId();

  // A prompt that already fits gets no control: a "Show all" that reveals
  // nothing is noise, and there is one of these per turn.
  const collapsible = text.length > 220 || text.split("\n").length > collapsedLines;

  const toggle = () => {
    if (!expanded) onExpand?.();
    setExpanded((value) => !value);
  };

  const agent = tone === "agent";
  return (
    <figure
      className={cn(
        "m-0 min-w-0 rounded-(--radius-md) border px-3.5 py-3",
        agent
          ? "border-(--border-subtle) border-l-2 border-l-(--action-accent) bg-(--surface-aside)"
          : "border-(--border-subtle) bg-(--surface-aside)",
      )}
    >
      <figcaption className="mb-2 flex items-baseline justify-between gap-3 text-xs text-(--text-tertiary)">
        <span className="flex min-w-0 items-baseline gap-2">
          <span
            className={cn(
              "flex-none text-xs font-semibold tracking-[0.12em] uppercase",
              agent ? "text-(--action-accent)" : "text-(--text-tertiary)",
            )}
          >
            {agent ? "Agent" : "Prompt"}
          </span>
          {label ? <span className="min-w-0 truncate">{label}</span> : null}
        </span>
        <span className="flex-none tabular-nums">
          {formatCompact(fullLength ?? characters)} characters
        </span>
      </figcaption>

      <div className="relative">
        <pre
          id={bodyId}
          className={cn(
            "m-0 font-sans text-base leading-[1.55] whitespace-pre-wrap text-(--text-primary)",
            // One 58 KB prompt must not make the page two hundred screens tall
            // once it is open.
            expanded ? "max-h-[55vh] overflow-auto" : "overflow-hidden",
          )}
          style={
            expanded
              ? undefined
              : // contentVisibility keeps a large collapsed block out of layout.
                { maxHeight: `${collapsedLines * 1.55}em`, contentVisibility: "auto" }
          }
        >
          {text}
        </pre>
        {!expanded && collapsible ? (
          // The fade has to resolve to the surface behind it, or it reads as a
          // grey band rather than as text running out.
          <div
            aria-hidden
            className="pointer-events-none absolute inset-x-0 bottom-0 h-8 bg-linear-to-b from-transparent to-(--surface-aside)"
          />
        ) : null}
      </div>

      {collapsible ? (
        <button
          type="button"
          aria-expanded={expanded}
          aria-controls={bodyId}
          onClick={toggle}
          className={cn(
            "mt-2 cursor-pointer rounded-(--radius-xs) text-base text-(--text-link) hover:underline",
            FOCUS_RING,
          )}
        >
          {expanded ? "Show less" : "Show all"}
        </button>
      ) : null}

      {truncated && !expanded ? (
        <p className="mt-1.5 text-xs text-(--text-tertiary)">
          Showing the first {formatCompact(characters)} of {formatCompact(fullLength)} characters.
        </p>
      ) : null}
    </figure>
  );
}
