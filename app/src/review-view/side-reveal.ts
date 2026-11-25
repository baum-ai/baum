import { createContext, useContext } from "react";

/** Shows a file of the section in the side column beside a wide section's
 *  Overview, at a line when given, without leaving the page: false when there
 *  is no column (a narrow section, or one the reader collapsed) or the
 *  section doesn't draw the file. Kept apart from the component that
 *  provides it, so that file exports components only, as fast refresh needs. */
export type SideReveal = (path: string, line?: number) => boolean;

export const SideRevealContext = createContext<SideReveal | null>(null);

/** The side column's reveal, or null where the section has no side column. */
export function useSideReveal(): SideReveal | null {
  return useContext(SideRevealContext);
}
