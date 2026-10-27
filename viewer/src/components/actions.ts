import { cn, FOCUS_RING } from "./classnames";

/** A button's classes, kept apart from `ui.tsx` so an element that is neither
 *  `Button` nor `ActionLink`, such as a host's own link component, can look
 *  like one. */

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

export function actionClassName({
  variant = "primary",
  size = "standard",
  grow = false,
}: ActionOptions) {
  return cn(
    "inline-flex cursor-pointer items-center justify-center font-medium disabled:cursor-not-allowed disabled:opacity-50",
    actionShape(variant),
    FOCUS_RING,
    actionSizes[size],
    actionVariants[variant],
    grow && "flex-1",
  );
}
