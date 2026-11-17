import { createContext } from "react";

/** The side pane as the button that opens its sheet sees it: its name
 *  ("Conversation", or "Details" without one), what its tabs flag in words,
 *  if anything, and whether it is open. */
export type ReviewAside = {
  label: string;
  alert: string | null;
  expanded: boolean;
  onOpen: () => void;
};

/** The side pane of the review page around, for `ReviewSidePaneButton`; set
 *  only where a host's places draw no rail. */
export const ReviewAsideContext = createContext<ReviewAside | null>(null);
