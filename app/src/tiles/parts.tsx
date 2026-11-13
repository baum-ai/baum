import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
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

/** A run of counts separated by a middle dot, wrapping as a whole. */
export function Counts({ children, className }: { children: ReactNode[]; className?: string }) {
  const shown = children.filter(Boolean);
  return (
    <p className={cn("flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-base", className)}>
      {shown.map((child, index) => (
        <span key={index} className="inline-flex items-baseline gap-2">
          {index > 0 ? (
            <span aria-hidden className="text-tertiary">
              ·
            </span>
          ) : null}
          {child}
        </span>
      ))}
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

/** The label over a part of a glance. */
export function GlanceLabel({ children }: { children: ReactNode }) {
  return (
    <p className="text-xs font-medium tracking-[0.06em] text-tertiary uppercase">{children}</p>
  );
}

/** Text as a glance shows it: one paragraph of plain words, its markdown
 *  marks dropped and the IDs it names read as what they stand for. */
export function plainText(
  text: string | null | undefined,
  labels?: ReadonlyMap<string, string>,
): string {
  if (!text) return "";
  return text
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/\b[a-z]{1,4}_[A-Za-z0-9_]+\b/g, (id) => labels?.get(id) ?? id)
    .replace(/[`*~#>]/g, "")
    .replace(/\s+/g, " ")
    .trim();
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
 *  grows with the row its card stands in, showing more of what it holds, and
 *  fades at its foot when that is not all. Its content does not make the card
 *  taller: a row is as tall as its cards' other content asks. */
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
  const [over, setOver] = useState(false);
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    const measure = () => setOver(element.scrollHeight > element.clientHeight + 1);
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    for (const child of element.children) observer.observe(child);
    return () => observer.disconnect();
  }, []);
  return (
    <div
      ref={ref}
      style={{ minHeight: min }}
      className={cn(
        "relative shrink-0 grow basis-0 overflow-hidden",
        over && "[mask-image:linear-gradient(to_bottom,black_calc(100%-1.5rem),transparent)]",
        className,
      )}
    >
      {children}
    </div>
  );
}
