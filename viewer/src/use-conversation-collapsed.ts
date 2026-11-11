import { useCallback } from "react";

import { useStoredSetting } from "./use-stored-setting";

const STORAGE_KEY = "baum.review.conversation-collapsed";

/** Whether the reader has folded the conversation pane to a strip of tab
 *  glyphs, remembered across visits. Open on the server and without storage. */
export function useConversationCollapsed() {
  const [stored, store] = useStoredSetting(STORAGE_KEY);
  const choose = useCallback((next: boolean) => store(String(next)), [store]);
  return [stored === "true", choose] as const;
}
