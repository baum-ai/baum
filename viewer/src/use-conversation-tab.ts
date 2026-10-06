import { useCallback, useEffect, useState } from "react";

export const CONVERSATION_TABS = ["chat", "details", "activity"] as const;
export type ConversationTab = (typeof CONVERSATION_TABS)[number];

const STORAGE_KEY = "baum.review.conversation-tab";

function isTab(value: unknown): value is ConversationTab {
  return CONVERSATION_TABS.includes(value as ConversationTab);
}

/** Which of the conversation pane's tabs the reader last chose, remembered
 *  across visits. Read after mount for the same reason as the fold: the
 *  server has no storage, and a pane that differed between the two would
 *  hydrate into a mismatch. */
export function useConversationTab() {
  const [tab, setTab] = useState<ConversationTab>("chat");
  useEffect(() => {
    try {
      const stored = localStorage.getItem(STORAGE_KEY);
      if (isTab(stored)) setTab(stored);
    } catch {
      // Storage is unavailable; the chat is a fine answer.
    }
  }, []);
  const choose = useCallback((next: ConversationTab) => {
    setTab(next);
    try {
      localStorage.setItem(STORAGE_KEY, next);
    } catch {
      // Not remembering the choice is better than refusing to make it.
    }
  }, []);
  return [tab, choose] as const;
}
