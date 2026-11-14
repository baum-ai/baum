import { useLayoutEffect, useRef, type ReactNode } from "react";
import { cn } from "@baum/viewer/components/classnames";
import { LiveDot } from "@baum/viewer/components/ui";
import { CheckIcon } from "@baum/viewer/components/icons";
import { TIER_COLOR, type RiskTier } from "@baum/viewer/review-document";

/** Small pieces the tile faces share. */

/** One line of plain text, cut with an ellipsis. */
export function Line({ children, className }: { children: ReactNode; className?: string }) {
  return <p className={cn("min-w-0 truncate", className)}>{children}</p>;
}

/** Text held to `lines` lines. */
export function Clamp({
  lines,
  children,
  className,
}: {
  lines: 1 | 2 | 3 | 4 | 5 | 6;
  children: ReactNode;
  className?: string;
}) {
  const clamp = {
    1: "line-clamp-1",
    2: "line-clamp-2",
    3: "line-clamp-3",
    4: "line-clamp-4",
    5: "line-clamp-5",
    6: "line-clamp-6",
  }[lines];
  return <div className={cn("min-w-0 [overflow-wrap:anywhere]", clamp, className)}>{children}</div>;
}

/** A count beside its label, as a glance's facts are written: "3 missing". */
export function Count({
  value,
  label,
  tone = "neutral",
}: {
  value: number;
  label: string;
  tone?: "neutral" | "positive" | "caution" | "critical";
}) {
  return (
    <span className="inline-flex items-baseline gap-1 whitespace-nowrap">
      <span
        className={cn(
          "font-semibold tabular-nums",
          tone === "positive"
            ? "text-status-positive"
            : tone === "caution"
              ? "text-status-caution"
              : tone === "critical"
                ? "text-status-critical"
                : "text-primary",
        )}
      >
        {value.toLocaleString("en-US")}
      </span>{" "}
      <span className="text-secondary">{label}</span>
    </span>
  );
}

/** A run of counts, each one unit that wraps whole. */
export function Counts({ children, className }: { children: ReactNode[]; className?: string }) {
  return (
    <p className={cn("flex flex-wrap items-baseline gap-x-3 gap-y-0.5 text-base", className)}>
      {children.filter(Boolean)}
    </p>
  );
}

/** A risk tier's swatch and count, as the Risks glance tallies them. */
export function TierCount({ tier, count }: { tier: RiskTier; count: number }) {
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap text-base">
      <span
        aria-hidden
        className="h-2 w-2 flex-none rounded-full"
        style={{ background: TIER_COLOR[tier] }}
      />
      <span className="font-semibold tabular-nums text-primary">{count}</span>{" "}
      <span className="text-secondary">{tier}</span>
    </span>
  );
}

/** A face's word that its subject is being written. */
export function Writing({ children = "Writing details…" }: { children?: ReactNode }) {
  return (
    <p role="status" className="flex items-center gap-2 text-base text-secondary">
      <LiveDot />
      {children}
    </p>
  );
}

/** A face's word that all is well, such as no open risks. */
export function AllClear({ children }: { children: ReactNode }) {
  return (
    <p className="flex items-center gap-2 text-base text-secondary">
      <CheckIcon size={14} className="flex-none text-status-positive" />
      {children}
    </p>
  );
}

/** A face with nothing to say, which still says so plainly. */
export function Quiet({ children }: { children: ReactNode }) {
  return <p className="text-base text-tertiary">{children}</p>;
}

/** Text as a glance shows it: one paragraph of plain words, its markdown
 *  marks dropped and the IDs it names read as what they stand for. */
export function plainText(
  text: string | null | undefined,
  labels?: ReadonlyMap<string, string>,
): string {
  if (!text) return "";
  return (
    text
      .replace(/```[\s\S]*?```/g, " ")
      .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
      // Markdown's marks only where they are markup: a heading, quote or list
      // marker at a line's start, and emphasis or code around words. "PR #123",
      // "C#" and "a > b" keep their characters.
      .replace(/^[ \t]{0,3}#{1,6}[ \t]+/gm, "")
      .replace(/^[ \t]{0,3}>[ \t]?/gm, "")
      .replace(/^[ \t]*[-*+][ \t]+/gm, "")
      .replace(/`([^`\n]+)`/g, "$1")
      .replace(/(\*\*|__|~~)(?=\S)([^\n]*?\S)\1/g, "$2")
      .replace(/(^|[\s(])\*(?=\S)([^*\n]*?\S)\*(?=[\s).,;:!?]|$)/gm, "$1$2")
      .replace(/\b[a-z]{1,4}_[A-Za-z0-9_]+\b/g, (id) => labels?.get(id) ?? id)
      .replace(/\s+/g, " ")
      .trim()
  );
}

/** The first sentence of `plainText`. */
export function plainSentence(
  text: string | null | undefined,
  labels?: ReadonlyMap<string, string>,
): string {
  const plain = plainText(text, labels);
  const match = /^(.+?[.!?])(\s|$)/.exec(plain);
  return match ? match[1]! : plain;
}

/** The part of a glance that takes the room its row leaves: it asks for
 *  `min` (what the glance must show, so its card is that tall at least), then
 *  grows with the row its card stands in, showing more of what it holds. Its
 *  content does not make the card taller: a row is as tall as its cards'
 *  other content asks. What does not fit is cut on whole lines and rows only
 *  (`fit`): prose ends on its last whole line with an ellipsis, a list on its
 *  last whole row, followed by "+N more". */
export function Fill({
  children,
  min = "0",
  className,
}: {
  children: ReactNode;
  min?: string;
  className?: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const more = useRef<HTMLParagraphElement>(null);
  useLayoutEffect(() => {
    const element = ref.current;
    const inner = content.current;
    const count = more.current;
    if (!element || !inner || !count) return;
    let frame = 0;
    const measure = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => fit(element, inner, count));
    };
    fit(element, inner, count);
    // The box as its row resizes it, and the content as it changes after
    // mount (text arriving, an image): not its styles, which `fit` sets.
    const resized = new ResizeObserver(measure);
    resized.observe(element);
    const changed = new MutationObserver(measure);
    changed.observe(inner, { childList: true, subtree: true, characterData: true });
    return () => {
      cancelAnimationFrame(frame);
      resized.disconnect();
      changed.disconnect();
    };
  }, []);
  return (
    <div
      ref={ref}
      style={{ minHeight: min }}
      className="relative shrink-0 grow basis-0 overflow-hidden"
    >
      <div ref={content} className={className}>
        {children}
      </div>
      <p ref={more} hidden className="text-xs text-tertiary" />
    </div>
  );
}

/** The blocks `fit` cuts between: each item of a list, each other child. */
function rowsOf(content: HTMLElement): HTMLElement[] {
  return [...content.children].flatMap((child) =>
    child instanceof HTMLUListElement || child instanceof HTMLOListElement
      ? ([...child.children] as HTMLElement[])
      : [child as HTMLElement],
  );
}

const CLAMPED = "data-fill-clamped";

/** Shows what fits `box` of `content` on whole lines and rows. */
export function fit(box: HTMLElement, content: HTMLElement, count: HTMLElement) {
  const rows = rowsOf(content);
  for (const row of rows) {
    row.hidden = false;
    if (row.hasAttribute(CLAMPED)) {
      row.removeAttribute(CLAMPED);
      row.style.removeProperty("display");
      row.style.removeProperty("-webkit-box-orient");
      row.style.removeProperty("-webkit-line-clamp");
      row.style.removeProperty("overflow");
    }
  }
  count.hidden = true;
  const limit = box.clientHeight;
  const top = box.getBoundingClientRect().top;
  const bottom = (element: Element) => element.getBoundingClientRect().bottom - top;
  const first = rows.findIndex((row) => bottom(row) > limit + 0.5);
  if (first < 0) return;
  const row = rows[first]!;
  const listed = row instanceof HTMLLIElement;
  if (!listed) {
    // Prose: the paragraph that does not fit ends on its last whole line.
    const height = parseFloat(getComputedStyle(row).lineHeight) || 24;
    const lines = Math.floor((limit - (row.getBoundingClientRect().top - top)) / height);
    for (const later of rows.slice(first + 1)) later.hidden = true;
    if (lines < 1) {
      row.hidden = true;
      return;
    }
    row.setAttribute(CLAMPED, "");
    row.style.setProperty("display", "-webkit-box");
    row.style.setProperty("-webkit-box-orient", "vertical");
    row.style.setProperty("-webkit-line-clamp", String(lines));
    row.style.setProperty("overflow", "hidden");
    return;
  }
  // A list: whole rows, then how many were left out, on a line of its own.
  let shown = first;
  const hide = () => {
    for (const [index, each] of rows.entries()) each.hidden = index >= shown;
    count.textContent = `+${rows.length - shown} more`;
    count.hidden = false;
  };
  hide();
  while (shown > 0 && bottom(count) > limit + 0.5) {
    shown--;
    hide();
  }
  // No room even for the count: the card's own figures say how many.
  if (bottom(count) > limit + 0.5) count.hidden = true;
}
