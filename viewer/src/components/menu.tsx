import type { ComponentProps } from "react";

import { cn } from "./classnames";
import { MENU, MENU_SEPARATOR, menuItemClassName, type MenuItemTone } from "./primitive-classes";

/** A menu and its items, drawn with the recipes in `primitive-classes.ts`
 *  (`MENU`, `menuItemClassName`). Position, width and stacking belong to the
 *  caller. */
export function Menu({ className, ...props }: ComponentProps<"div">) {
  return <div role="menu" {...props} className={cn(MENU, className)} />;
}

export function MenuItem({
  tone,
  className,
  type = "button",
  ...props
}: ComponentProps<"button"> & { tone?: MenuItemTone }) {
  return <button type={type} {...props} className={cn(menuItemClassName({ tone }), className)} />;
}

/** What stops or removes something sits apart, below a hairline. */
export function MenuSeparator({ className }: { className?: string }) {
  return <hr className={cn(MENU_SEPARATOR, className)} />;
}
