import { useCallback } from "react";

import { useStoredSetting } from "./use-stored-setting";

/** The side pane's tabs. Sessions, Commits and Decisions are tabs only where
 *  a host's places draw no rail (`ReviewPlaces.noRail`). */
export const CONVERSATION_TABS = [
  "chat",
  "details",
  "activity",
  "comments",
  "sessions",
  "commits",
  "decisions",
] as const;
export type ConversationTab = (typeof CONVERSATION_TABS)[number];

const STORAGE_KEY = "baum.review.conversation-tab";

function isTab(value: unknown): value is ConversationTab {
  return CONVERSATION_TABS.includes(value as ConversationTab);
}

/** Which of the conversation pane's tabs the reader last chose, remembered
 *  across visits. The chat is the answer on the server and without storage. */
export function useConversationTab() {
  const [stored, store] = useStoredSetting(STORAGE_KEY);
  const tab: ConversationTab = isTab(stored) ? stored : "chat";
  const choose = useCallback((next: ConversationTab) => store(next), [store]);
  return [tab, choose] as const;
}
