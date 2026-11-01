import { actionClassName, type ActionOptions } from "@baum/viewer/components/actions";
import { cn } from "@baum/viewer/components/classnames";

/** The shared Button's classes for an element that cannot be one, such as a
 *  router `Link` or a button another stylesheet names. Every boxed action in
 *  the app takes one of these, so the same action looks the same on every
 *  page: `PRIMARY` and `SECONDARY` in a page's or a detail's toolbar, `SMALL`
 *  beside one item in a list or a check. */
// An icon or key hint sits beside the label, so the classes carry the gap.
const boxed = (options: ActionOptions) => cn(actionClassName(options), "gap-1.5 whitespace-nowrap");

export const PRIMARY = boxed({ size: "compact" });
export const SECONDARY = boxed({ variant: "secondary", size: "compact" });
export const SMALL = boxed({ variant: "secondary", size: "tiny" });
export const SMALL_PRIMARY = boxed({ size: "tiny" });
export const DANGER = boxed({ variant: "danger-outline", size: "compact" });
