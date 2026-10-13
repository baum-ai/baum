import { type ReactNode, useId } from "react";

import { cn } from "./classnames";

/** The editorial chrome shared by the public pages: type scale, rules, shell. */
/** `w-full` keeps the shell full-bleed when it is a flex child, not just a block child. */
export const SHELL = "mx-auto w-full max-w-[1240px] px-6 md:px-10";
export const KICKER = "text-base font-semibold tracking-[0.16em] uppercase text-(--accent-600)";
export const STEP_KICKER = "text-xs font-semibold tracking-[0.14em] uppercase text-(--accent-600)";
export const CARD_LABEL =
  "text-xs font-semibold tracking-[0.14em] uppercase text-(--text-tertiary)";
export const NAV_LINK =
  "text-base text-(--text-secondary) no-underline hover:text-(--text-primary) hover:no-underline";

/** The chrome's copy is quieter than a page's: beside the navigation it is a
 *  label for the product, not a headline. */
const wordmarkSizes = { page: "text-2xl font-medium", sidebar: "text-lg font-normal" };

/** A size variant rather than a `className` override: `cn` concatenates, so two
 *  competing font sizes or weights would both survive and stylesheet order
 *  would pick. */
export function Wordmark({
  size = "page",
  className,
}: {
  size?: keyof typeof wordmarkSizes;
  className?: string;
}) {
  return <span className={cn("tracking-[-0.02em]", wordmarkSizes[size], className)}>baum</span>;
}

/** The product's mark: a log's cut face with its branch rising on the left,
 *  which makes a lowercase b. It keeps its own bark and heartwood colours in
 *  both themes. On a dark ground the dark branch would sink into it, so a
 *  parent sets `--logo-halo` to a light colour and the mark gets a die-cut
 *  border in it, like the stickers; unset, there is none. `public/favicon.svg`
 *  is the same drawing. Decorative; the control or wordmark around it carries
 *  the name. */
export function LogoMark({ size = 20, className }: { size?: number; className?: string }) {
  // Several marks can share a page, so each needs its own gradient ids.
  const id = useId().replace(/[^a-zA-Z0-9_-]/g, "");
  return (
    <svg
      width={size}
      height={size}
      viewBox="20 23 120 120"
      aria-hidden="true"
      focusable="false"
      className={cn("flex-none", className)}
    >
      <defs>
        <radialGradient id={`${id}-bark`} cx="0.3" cy="0.25" r="0.85">
          <stop offset="0" stopColor="#a86b3c" />
          <stop offset="0.6" stopColor="#7a4a26" />
          <stop offset="1" stopColor="#4e2e16" />
        </radialGradient>
        <radialGradient id={`${id}-face`} cx="0.4" cy="0.35" r="0.75">
          <stop offset="0" stopColor="#fff3da" />
          <stop offset="0.7" stopColor="#f3d9ac" />
          <stop offset="1" stopColor="#d9b27c" />
        </radialGradient>
      </defs>
      <g fill="none" stroke="var(--logo-halo, transparent)" strokeWidth="9">
        <rect x="36.5" y="28" width="13" height="68" rx="6.5" />
        <circle cx="80" cy="94" r="44" />
      </g>
      <rect x="36.5" y="28" width="13" height="68" rx="6.5" fill={`url(#${id}-bark)`} />
      <circle cx="80" cy="94" r="44" fill={`url(#${id}-bark)`} />
      <circle cx="80" cy="94" r="35.2" fill={`url(#${id}-face)`} />
    </svg>
  );
}

/** The uppercase masthead band that runs under the header of every editorial page. */
export function Strapline({ left, right }: { left: ReactNode; right: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 border-t border-t-(--rule-masthead) border-b border-b-(--border-default) py-2.5 text-xs font-medium tracking-[0.16em] uppercase text-(--text-tertiary)">
      <span>{left}</span>
      <span>{right}</span>
    </div>
  );
}

const STATUS_RULES = {
  neutral: "border-t-(--border-default)",
  info: "border-t-(--accent-600)",
  positive: "border-t-(--status-positive)",
  caution: "border-t-(--status-caution)",
  critical: "border-t-(--status-critical)",
};

const STATUS_LABELS = {
  neutral: "text-(--text-tertiary)",
  info: "text-(--accent-600)",
  positive: "text-(--status-positive)",
  caution: "text-(--status-caution)",
  critical: "text-(--status-critical)",
};

/** The boxed form of the same tones: a drawn border and a tint at low strength,
 *  for a status that has to hold its own against the prose around it. */
const STATUS_BOXES = {
  neutral: "border-(--border-default) bg-(--surface-sunken)",
  info: "border-(--status-info)/30 bg-(--status-info-bg)/75",
  positive: "border-(--status-positive)/30 bg-(--status-positive-bg)/75",
  caution: "border-(--status-caution)/30 bg-(--status-caution-bg)/75",
  critical: "border-(--status-critical)/30 bg-(--status-critical-bg)/75",
};

export type StatusTone = keyof typeof STATUS_RULES;

/** A ruled status line: coloured top rule, uppercase label, one line of detail.
 *  How every page in the flow reports state, in place of filled banners. */
export function StatusLine({
  tone,
  label,
  children,
  className,
  boxed = false,
}: {
  tone: StatusTone;
  label: string;
  children: ReactNode;
  className?: string;
  boxed?: boolean;
}) {
  return (
    <StatusBlock tone={tone} label={label} className={className} boxed={boxed}>
      <p className="max-w-[420px] text-base leading-[1.5] text-(--text-secondary)">{children}</p>
    </StatusBlock>
  );
}

/** `StatusLine` for a body that is not one sentence — a list of failing checks,
 *  say, which cannot live inside a paragraph. Same rule, same label, so the two
 *  read as one device. */
export function StatusBlock({
  tone,
  label,
  children,
  className,
  boxed = false,
}: {
  tone: StatusTone;
  label: string;
  children: ReactNode;
  className?: string;
  /** Draws the status as a bordered, tinted box rather than a ruled line. */
  boxed?: boolean;
}) {
  return (
    <div
      className={cn(
        boxed
          ? cn("rounded-(--radius-md) border px-3 py-2.5", STATUS_BOXES[tone])
          : cn("border-t pt-3.5", STATUS_RULES[tone]),
        className,
      )}
    >
      <div className={cn("text-xs font-semibold tracking-[0.14em] uppercase", STATUS_LABELS[tone])}>
        {label}
      </div>
      <div className="mt-1.5">{children}</div>
    </div>
  );
}

export function Panel({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div
      className={cn(
        "rounded-(--radius-lg) border border-(--border-default) bg-(--surface-card) shadow-(--shadow-card)",
        className,
      )}
    >
      {children}
    </div>
  );
}
