import { useCallback, useEffect, useState } from "react";

const STORAGE_KEY = "baum.review.conversation-collapsed";

/** Whether the reader has folded the conversation pane to a strip of tab
 *  glyphs, remembered across visits. Read after mount: the server has no
 *  storage, and a pane that differed between the two would hydrate into a
 *  mismatch. */
export function useConversationCollapsed() {
  const [collapsed, setCollapsed] = useState(false);
  useEffect(() => {
    try {
      if (localStorage.getItem(STORAGE_KEY) === "true") setCollapsed(true);
    } catch {
      // Storage is unavailable; an open pane is a fine answer.
    }
  }, []);
  const choose = useCallback((next: boolean) => {
    setCollapsed(next);
    try {
      localStorage.setItem(STORAGE_KEY, String(next));
    } catch {
      // Not remembering the choice is better than refusing to make it.
    }
  }, []);
  return [collapsed, choose] as const;
}
