import { useId, type ReactNode, type Ref } from "react";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import { formatCount } from "@baum/viewer/components/format";
import { ChevronRightIcon } from "@baum/viewer/components/icons";

/** The tones of a section's dot. */
export const LIST_TONES = {
  caution: "text-(--status-caution)",
  critical: "text-(--status-critical)",
  positive: "text-(--status-positive)",
  info: "text-(--status-info)",
  quiet: "text-(--text-tertiary)",
} as const;
export type ListTone = keyof typeof LIST_TONES;

type HeadingProps = {
  title: string;
  count?: number;
  tone?: ListTone;
  hint?: string;
  collapsed?: boolean;
  /** Folds and unfolds the section; without it the heading does not fold. */
  toggle?: () => void;
  /** A control at the heading's end, such as a batch action. */
  action?: ReactNode;
};

/** A list section's heading, as every list in the app draws one: a chevron
 *  when it folds, a dot in the section's tone, its name, how many it holds
 *  and a hint. `controls` names the rows it folds. */
export function ListHeading({
  title,
  count,
  tone = "quiet",
  hint,
  collapsed = false,
  toggle,
  action,
  controls,
}: HeadingProps & { controls?: string }) {
  const label = (
    <>
      {toggle ? (
        <ChevronRightIcon size={12} className={cn("list-heading-chevron", !collapsed && "open")} />
      ) : null}
      <span aria-hidden="true" className={cn("list-heading-dot", LIST_TONES[tone])} />
      <span>{title}</span>
      {count ? (
        <>
          {" "}
          <span className="list-heading-count">{formatCount(count)}</span>
        </>
      ) : null}
      {hint ? (
        <>
          {" "}
          <span className="list-heading-hint">· {hint}</span>
        </>
      ) : null}
    </>
  );
  return (
    <h2 className="list-heading">
      {toggle ? (
        <button
          type="button"
          aria-expanded={!collapsed}
          aria-controls={controls}
          onClick={toggle}
          className={cn("list-heading-label", FOCUS_RING)}
        >
          {label}
        </button>
      ) : (
        <span className="list-heading-label">{label}</span>
      )}
      {action}
    </h2>
  );
}

/** A list's section: its heading, then its rows (`<li className="list-row">`)
 *  between hairlines, hidden while the section is folded. */
export function ListSection({
  className,
  listClassName,
  listRef,
  children,
  ...heading
}: HeadingProps & {
  className?: string;
  listClassName?: string;
  listRef?: Ref<HTMLUListElement>;
  children: ReactNode;
}) {
  const id = useId();
  return (
    <section aria-label={heading.title} className={className}>
      <ListHeading {...heading} controls={id} />
      <ul ref={listRef} id={id} hidden={heading.collapsed} className={cn("list", listClassName)}>
        {children}
      </ul>
    </section>
  );
}
