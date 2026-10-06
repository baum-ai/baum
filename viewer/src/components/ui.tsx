import type {
  AnchorHTMLAttributes,
  ButtonHTMLAttributes,
  ComponentProps,
  KeyboardEvent,
  ReactNode,
} from "react";

import { cn, FOCUS_RING } from "./classnames";
import { tabIds } from "./tabs";

type CardSize = "standard" | "compact" | "panel";

type CardProps = ComponentProps<"section"> & {
  children: ReactNode;
  className?: string;
  size?: CardSize;
};

const cardSizes: Record<CardSize, string> = {
  // A centred form card: the fixed width is the point, but the padding is not —
  // held at 40px it eats a quarter of a phone before the card has said anything.
  standard: "w-130 max-w-full px-6 py-7 sm:px-10 sm:py-9",
  compact: "w-full max-w-md p-6",
  // A dashboard panel, sized by its grid cell rather than by itself. `min-w-0`
  // is what lets a chart inside it shrink as well as grow.
  panel: "flex min-w-0 flex-col p-5",
};

export function Card({ children, className, size = "standard", ...props }: CardProps) {
  return (
    <section
      {...props}
      className={cn(
        "rounded-(--radius-lg) border border-(--border-default) bg-(--surface-card) shadow-(--shadow-card)",
        cardSizes[size],
        className,
      )}
    >
      {children}
    </section>
  );
}

/** `pane` is `panel` with the title of a reading pane: the subject of its own
 *  column, rather than a label on one widget among several in a grid. */
type CardHeaderSize = CardSize | "pane";

const headerSizes = {
  standard: {
    title: "text-2xl font-medium tracking-[-0.015em]",
    detail: "mt-2.5 leading-[1.55] text-(--text-secondary)",
    row: undefined,
    heading: "h1",
  },
  compact: {
    title: "text-2xl font-semibold",
    detail: "mt-3 leading-6 text-(--text-secondary)",
    row: undefined,
    heading: "h1",
  },
  panel: {
    title: "text-base font-medium",
    detail: "mt-1 text-base leading-[1.45] text-(--text-tertiary)",
    row: "mb-4 flex items-start justify-between gap-3",
    heading: "h2",
  },
  pane: {
    title: "text-lg font-medium tracking-[-0.015em]",
    detail: "mt-1.5 text-base leading-[1.45] text-(--text-tertiary)",
    // On a phone the badges lead on a line of their own, so a long title and
    // its statistics keep the pane's width rather than wrapping beside them.
    row: "mb-4 flex flex-col-reverse items-start gap-2 sm:flex-row sm:justify-between sm:gap-3",
    heading: "h2",
  },
} as const;

type CardHeaderProps = {
  title: ReactNode;
  detail?: ReactNode;
  size?: CardHeaderSize;
  tone?: "neutral" | "positive" | "critical";
  /** Overrides the heading level. A card inside a page that already has an `h1`
   *  must not emit a second one, and a panel in a grid is a section heading. */
  as?: "h1" | "h2" | "h3";
  aside?: ReactNode;
};

export function CardHeader({
  title,
  detail,
  size = "standard",
  tone = "neutral",
  as,
  aside,
}: CardHeaderProps) {
  const toneClass =
    tone === "positive"
      ? "text-(--status-positive)"
      : tone === "critical"
        ? "text-(--status-critical)"
        : "text-(--text-primary)";

  const scale = headerSizes[size];
  const Heading = as ?? scale.heading;

  return (
    <div className={scale.row}>
      <div className="min-w-0">
        <Heading className={cn(scale.title, toneClass)}>{title}</Heading>
        {detail ? <p className={scale.detail}>{detail}</p> : null}
      </div>
      {aside ? <div className="flex flex-none items-center gap-2">{aside}</div> : null}
    </div>
  );
}

const badgeTones = {
  neutral: "bg-(--status-neutral-bg) text-(--status-neutral)",
  info: "bg-(--status-info-bg) text-(--status-info)",
  positive: "bg-(--status-positive-bg) text-(--status-positive)",
  caution: "bg-(--status-caution-bg) text-(--status-caution)",
  critical: "bg-(--status-critical-bg) text-(--status-critical)",
};

export type BadgeTone = keyof typeof badgeTones;

const badgeSizes = {
  large: "px-2.5 py-1 text-base",
  standard: "px-2 py-0.5 text-xs",
  // Dense table rows, where a standard pill sets the row height.
  compact: "px-1.5 py-0 text-xs",
};

export type BadgeProps = {
  children: ReactNode;
  tone?: BadgeTone;
  size?: keyof typeof badgeSizes;
  className?: string;
  title?: string;
  role?: "status" | "alert";
};

/** A pill-shaped state label. Tones are the reserved status scale, never chart
 *  slots — a status colour must not be able to impersonate a series. An origin
 *  or any other categorical value belongs in `OriginTag`, not here. */
export function Badge({
  children,
  tone = "neutral",
  size = "standard",
  className,
  title,
  role,
}: BadgeProps) {
  return (
    <span
      role={role}
      title={title}
      className={cn(
        "inline-flex items-center rounded-full whitespace-nowrap",
        badgeSizes[size],
        badgeTones[tone],
        className,
      )}
    >
      {children}
    </span>
  );
}

export type ActionOptions = {
  variant?: "primary" | "secondary" | "ghost" | "danger-outline" | "tag";
  size?: "tiny" | "compact" | "standard" | "large";
  grow?: boolean;
};

const actionSizes = {
  // Table-row scale. Exists so a caller never reaches for
  // `className="px-3 py-1.5"`, which cannot reliably beat the size it is
  // fighting — `cn` concatenates, so stylesheet order decides.
  tiny: "h-7 px-2.5 text-base",
  compact: "h-8 px-3.5",
  standard: "px-4 py-[11px]",
  large: "px-4 py-3 text-base",
};

const actionVariants = {
  primary:
    "bg-(--action-primary) text-(--text-inverse) hover:bg-(--action-primary-hover) hover:text-(--text-inverse) hover:no-underline",
  secondary:
    "border border-(--border-default) bg-(--surface-card) text-(--text-primary) hover:bg-(--action-secondary-hover) hover:text-(--text-primary) hover:no-underline",
  // A row action that should not compete with the message it sits under: no
  // border, no fill, and it only takes on weight when the pointer is over it.
  ghost:
    "font-normal text-(--text-tertiary) hover:bg-(--action-secondary-hover) hover:text-(--text-primary) hover:no-underline",
  "danger-outline":
    "border border-(--border-default) bg-transparent px-[18px] whitespace-nowrap text-(--status-critical) hover:bg-(--surface-hover)",
  // An action that reads as a tag beside the thing it acts on: a quiet pill
  // rather than a box. Its shape comes from `actionShape`.
  tag: "border border-(--border-default) bg-(--surface-card) whitespace-nowrap text-(--text-secondary) hover:bg-(--surface-hover) hover:text-(--text-primary) hover:no-underline",
};

/** Kept apart from the variants because two `rounded-*` utilities would both
 *  survive `cn` and the stylesheet, not the variant, would pick the winner. */
function actionShape(variant: NonNullable<ActionOptions["variant"]>) {
  return variant === "tag" ? "rounded-full" : "rounded-(--radius-md)";
}

function actionClassName({ variant = "primary", size = "standard", grow = false }: ActionOptions) {
  return cn(
    "inline-flex cursor-pointer items-center justify-center font-medium disabled:cursor-not-allowed disabled:opacity-50",
    actionShape(variant),
    FOCUS_RING,
    actionSizes[size],
    actionVariants[variant],
    grow && "flex-1",
  );
}

type ButtonProps = ComponentProps<"button"> & ActionOptions;

export function Button({ className, variant, size, grow, type = "button", ...props }: ButtonProps) {
  return (
    <button
      type={type}
      className={cn(actionClassName({ variant, size, grow }), className)}
      {...props}
    />
  );
}

type ActionLinkProps = AnchorHTMLAttributes<HTMLAnchorElement> & ActionOptions;

export function ActionLink({ className, variant, size, grow, ...props }: ActionLinkProps) {
  return <a className={cn(actionClassName({ variant, size, grow }), className)} {...props} />;
}

type AlertProps = {
  children: ReactNode;
  className?: string;
};

export function Alert({ children, className }: AlertProps) {
  return (
    <p
      role="alert"
      className={cn(
        "rounded-(--radius-sm) bg-(--status-critical-bg) px-3 py-2 text-base text-(--status-critical)",
        className,
      )}
    >
      {children}
    </p>
  );
}

type SegmentedItem<T extends string> = {
  value: T;
  label: string;
  icon?: ReactNode;
  /** A state the tab reports whether or not it is selected: its underline
   *  takes the colour when selected, and a dot beside its label carries it
   *  when not. Only the `tabs` variant draws it. */
  tone?: "positive" | "critical" | "caution";
};

const TAB_TONES = {
  positive: { rule: "border-b-(--status-positive)", dot: "bg-(--status-positive)" },
  critical: { rule: "border-b-(--status-critical)", dot: "bg-(--status-critical)" },
  caution: { rule: "border-b-(--status-caution)", dot: "bg-(--status-caution)" },
} as const;

/** A choice between a few views of the same pane.
 *
 *  `segmented` is a raised tile inside a sunken track rather than a filled
 *  button beside an outlined one: two buttons of different weight read as "do
 *  this, or maybe that", where a track reads as one control with a position.
 *  `tabs` is the same control drawn as a ruled strip, for a pane whose header
 *  is a hairline and whose tabs should sit on it. Both say the choice the
 *  same way, as `aria-pressed` on a button in a named group — unless the strip
 *  is given a `tablist` id, when it switches panels (see `tabPanelProps`) and
 *  says so as a tab list: one tab in the tab order, the arrows between them. */
export function SegmentedControl<T extends string>({
  label,
  value,
  items,
  onChange,
  variant = "segmented",
  tablist,
  className,
}: {
  label: string;
  value: T;
  items: SegmentedItem<T>[];
  onChange: (value: T) => void;
  variant?: "segmented" | "tabs";
  tablist?: string;
  className?: string;
}) {
  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    if (!tablist) return;
    const last = items.length - 1;
    const next =
      event.key === "ArrowRight"
        ? index === last
          ? 0
          : index + 1
        : event.key === "ArrowLeft"
          ? index === 0
            ? last
            : index - 1
          : event.key === "Home"
            ? 0
            : event.key === "End"
              ? last
              : null;
    if (next === null) return;
    event.preventDefault();
    const target = items[next]!;
    onChange(target.value);
    document.getElementById(tabIds(tablist, target.value).tab)?.focus();
  };
  return (
    <div
      role={tablist ? "tablist" : "group"}
      aria-label={label}
      className={cn(
        variant === "segmented"
          ? "inline-flex items-center gap-0.5 rounded-(--radius-md) bg-(--surface-sunken) p-[3px]"
          : // Scrolls sideways when the pane is at its narrowest, without a
            // scrollbar of its own: a tab cut off at the edge is the cue.
            "flex items-center gap-0.5 overflow-x-auto overflow-y-hidden border-b border-(--border-subtle) [scrollbar-width:none] [&::-webkit-scrollbar]:hidden",
        className,
      )}
    >
      {items.map((item, index) => {
        const selected = item.value === value;
        const ids = tablist ? tabIds(tablist, item.value) : null;
        return (
          <button
            key={item.value}
            type="button"
            {...(ids
              ? {
                  role: "tab",
                  id: ids.tab,
                  "aria-selected": selected,
                  "aria-controls": ids.panel,
                  tabIndex: selected ? 0 : -1,
                }
              : { "aria-pressed": selected })}
            onClick={() => onChange(item.value)}
            onKeyDown={(event) => onKeyDown(event, index)}
            className={cn(
              "inline-flex cursor-pointer items-center gap-1.5 text-base whitespace-nowrap transition-colors duration-150",
              FOCUS_RING,
              variant === "segmented"
                ? cn(
                    "h-7 rounded-(--radius-sm) px-2.5",
                    selected
                      ? "bg-(--surface-card) font-medium text-(--text-primary) shadow-(--shadow-card)"
                      : "text-(--text-secondary) hover:text-(--text-primary)",
                  )
                : cn(
                    "-mb-px h-9 border-b-2 px-2 transition-[border-color,color] duration-150",
                    selected
                      ? cn(
                          "font-medium text-(--text-primary)",
                          item.tone ? TAB_TONES[item.tone].rule : "border-b-(--action-accent)",
                        )
                      : "border-b-transparent text-(--text-secondary) hover:border-b-(--border-strong) hover:text-(--text-primary)",
                  ),
            )}
          >
            {/* A ruled strip has no room for glyphs: five labelled tabs already
                fill a 400px pane. */}
            {variant === "segmented" ? item.icon : null}
            {item.label}
            {variant === "tabs" && item.tone ? (
              <span
                aria-hidden
                className={cn("h-1.5 w-1.5 flex-none rounded-full", TAB_TONES[item.tone].dot)}
              />
            ) : null}
          </button>
        );
      })}
    </div>
  );
}

/** A ring that turns, for something the page is fetching or refreshing right
 *  now. With a `label` it is the status itself, announced once; without one it
 *  decorates text that already says what is loading. Still for a reader who
 *  asked motion to be. */
export function Spinner({
  size = 14,
  className,
  label,
}: {
  size?: number;
  className?: string;
  label?: string;
}) {
  return (
    <span
      role={label ? "status" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      className={cn(
        "inline-block flex-none animate-spin rounded-full border-[1.5px] border-current border-t-transparent motion-reduce:animate-none",
        className,
      )}
      style={{ width: size, height: size }}
    />
  );
}

/** A dot that breathes, for a state that is changing while the reader looks
 *  at it — a review being generated, a check still running. Decorative: the
 *  text beside it says what is live, so it is hidden from assistive
 *  technology, and it holds still for a reader who asked motion to. */
export function LiveDot({ className }: { className?: string }) {
  return (
    <span aria-hidden className={cn("relative inline-flex h-2 w-2 flex-none", className)}>
      <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-current opacity-60 motion-reduce:animate-none" />
      <span className="relative inline-flex h-2 w-2 rounded-full bg-current" />
    </span>
  );
}

/** A control whose glyph is its whole label. The name is required, because an
 *  icon button with no accessible name is a button nobody but a sighted mouse
 *  user can press. */
export function IconButton({
  label,
  children,
  className,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { label: string }) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      className={cn(
        "inline-flex h-7 w-7 flex-none cursor-pointer items-center justify-center rounded-(--radius-sm) text-(--text-tertiary) hover:bg-(--surface-hover) hover:text-(--text-primary) disabled:cursor-not-allowed disabled:opacity-50",
        FOCUS_RING,
        className,
      )}
      {...props}
    >
      {children}
    </button>
  );
}

/** An on/off setting drawn as a sliding switch. The input underneath is a
 *  real checkbox with the switch role, stretched invisibly over the track, so
 *  the label, keyboard and assistive technology treat it as the control it is.
 *  The knob stays white in both themes: it reads as a physical slider on the
 *  grey track and on the accent. */
export function Toggle({
  checked,
  onChange,
  disabled = false,
  title,
  children,
  className,
}: {
  checked: boolean;
  onChange: (checked: boolean) => void;
  disabled?: boolean;
  title?: string;
  /** The setting's name, which is also the control's accessible name. */
  children: ReactNode;
  className?: string;
}) {
  return (
    <label
      title={title}
      className={cn(
        "inline-flex items-center gap-2 text-base text-(--text-secondary)",
        disabled ? "cursor-not-allowed opacity-60" : "cursor-pointer",
        className,
      )}
    >
      <span className="relative inline-flex h-4 w-7 flex-none">
        <input
          type="checkbox"
          role="switch"
          aria-checked={checked}
          checked={checked}
          disabled={disabled}
          onChange={(event) => onChange(event.target.checked)}
          className="peer absolute inset-0 z-10 m-0 h-full w-full cursor-[inherit] appearance-none opacity-0"
        />
        <span
          aria-hidden
          className={cn(
            "h-full w-full rounded-full bg-(--border-strong) transition-colors duration-150 peer-checked:bg-(--action-accent)",
            "peer-focus-visible:shadow-(--focus-ring)",
            "after:absolute after:top-0.5 after:left-0.5 after:h-3 after:w-3 after:rounded-full after:bg-(--control-thumb) after:shadow-sm after:transition-transform after:duration-150 peer-checked:after:translate-x-3 motion-reduce:after:transition-none",
          )}
        />
      </span>
      {children}
    </label>
  );
}
