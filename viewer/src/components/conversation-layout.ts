import { createContext, useContext } from "react";

/** How a conversation is laid out: as a messaging app (`chat`), or as a
 *  compact log (`log`), every line from the left: prompts on a rule, the
 *  agent's replies as plain text under its name, and notes as small print. */
export type ConversationLayout = "chat" | "log";

export const ConversationLayoutContext = createContext<ConversationLayout>("chat");

export function useLogLayout(): boolean {
  return useContext(ConversationLayoutContext) === "log";
}
