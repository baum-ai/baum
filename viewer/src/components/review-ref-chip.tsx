import type { ReactNode } from "react";

import { CHECK_CONCLUSION_TONES, humanize, type ReviewRef, type Tone } from "../review-document";
import { harnessName } from "../agent-names";
import { chipClass, chipLinkClass } from "./chip-classes";
import { cn } from "./classnames";
import { ExternalLinkIcon } from "./icons";
import { useReviewJump, useReviewRefs } from "./review-contexts";

const DOT: Record<Tone, string> = {
  neutral: "bg-(--text-tertiary)",
  info: "bg-(--status-info)",
  positive: "bg-(--status-positive)",
  caution: "bg-(--status-caution)",
  critical: "bg-(--status-critical)",
};

// Clickable even in prose laid over a row's own link with pointer events off
// (the risks overview), where only `a` and `button` get them back.
const CHIP = "pointer-events-auto mx-0.5 align-baseline";

function Dot({ tone }: { tone: Tone }) {
  return <span aria-hidden className={cn("h-1.5 w-1.5 flex-none rounded-full", DOT[tone])} />;
}

function basename(path: string) {
  return path.slice(path.lastIndexOf("/") + 1);
}

/** What a chip shows and where it leads, for one reference. */
const NO_LINK = "no link available";

function describe(ref: ReviewRef): {
  content: ReactNode;
  title: string;
  href?: string;
} {
  switch (ref.kind) {
    case "item":
      return {
        content: <span className="truncate">{ref.label}</span>,
        title: `${humanize(ref.item)} ${ref.id}`,
      };
    case "check": {
      // A check from an earlier head says nothing of the current state.
      const result = ref.stale ? "earlier head" : humanize(ref.conclusion);
      return {
        content: (
          <>
            <Dot
              tone={ref.stale ? "neutral" : (CHECK_CONCLUSION_TONES[ref.conclusion] ?? "neutral")}
            />
            <span className="truncate">{ref.name}</span>
            <span className="flex-none text-(--text-tertiary)">{result}</span>
          </>
        ),
        title: `Check ${ref.name}: ${result}${ref.linkDropped ? `, ${NO_LINK}` : ""}`,
        href: ref.url,
      };
    }
    case "thread": {
      const place = ref.path ? `${basename(ref.path)}${ref.line ? `:${ref.line}` : ""}` : "Thread";
      return {
        content: (
          <>
            <Dot tone={ref.resolved ? "positive" : "caution"} />
            <span className="truncate font-mono">{place}</span>
            <span className="flex-none text-(--text-tertiary)">
              {ref.resolved ? "resolved" : "open"}
            </span>
          </>
        ),
        title: `Review thread${ref.path ? ` on ${ref.path}` : ""}, ${ref.resolved ? "resolved" : "open"}${ref.linkDropped ? `, ${NO_LINK}` : ""}`,
        href: ref.url,
      };
    }
    case "comment":
      return {
        content: <span className="truncate">Comment by {ref.author}</span>,
        title: `Comment ${ref.id} by ${ref.author}${ref.linkDropped ? `, ${NO_LINK}` : ""}`,
        href: ref.url,
      };
    case "session":
      return {
        content: (
          <span className="truncate">{ref.title || `${harnessName(ref.vendor)} session`}</span>
        ),
        title: `Session ${ref.id}`,
      };
  }
}

/** A review's own item, check, thread, comment or session that the prose
 *  names, as a chip saying what it is. An item or session opens in the review
 *  workspace and a check, thread or comment on GitHub; where there is nowhere
 *  to open, the chip is plain. An ID the review does not have never gets
 *  here: the markdown pass leaves it as written. */
export function ReviewRefChip({ refKey }: { refKey: string }) {
  const ref = useReviewRefs()?.get(refKey);
  const jump = useReviewJump();
  if (!ref) return <code>{refKey}</code>;
  const { content, title, href } = describe(ref);
  const open =
    ref.kind === "item" && jump
      ? () => jump.item(ref.section, ref.item === "section" ? undefined : ref.id)
      : ref.kind === "session" && jump?.session
        ? () => jump.session?.(ref.id)
        : null;
  const className = cn(chipLinkClass, CHIP, "max-w-[28ch]");
  if (open) {
    return (
      <button
        type="button"
        title={title}
        data-review-ref={ref.kind}
        onClick={open}
        className={cn(className, "cursor-pointer")}
      >
        {content}
      </button>
    );
  }
  if (href) {
    return (
      <a
        href={href}
        target="_blank"
        rel="noreferrer"
        title={title}
        data-review-ref={ref.kind}
        className={className}
      >
        {content}
        <ExternalLinkIcon size={10} className="flex-none text-(--text-tertiary)" />
      </a>
    );
  }
  return (
    <span title={title} data-review-ref={ref.kind} className={cn(chipClass, CHIP, "max-w-[28ch]")}>
      {content}
    </span>
  );
}
