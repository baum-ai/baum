import { SavedStateNotice, useUIState } from "./ui-state";
import { useState } from "react";
import type { ReviewDocument } from "@baum/viewer/review-document";
import { Button } from "@baum/viewer/components/ui";
import type { ChatAnswer, ChatConversation, ChatTurn } from "./api";
import type { FixChat } from "./fix";
import {
  answerComment,
  appendToDraft,
  draftTarget,
  progressKey,
  progressState,
  removeFromDraft,
  type DraftTarget,
  type Tour,
} from "./walkthrough-model";

/** What a person can do with an answer: make the change it describes as a fix
 *  (a confirmation first, like every fix), or add it to the review draft,
 *  which is sent only with the review. Offered under answers about the review
 *  on screen only. */
export function AnswerActions({
  conversation,
  turn,
  answer,
  document,
  tour,
  fix,
  fixRunning,
  openDraft,
}: {
  conversation: ChatConversation;
  turn: ChatTurn;
  answer: ChatAnswer;
  /** The newest review, whose draft the answer goes to. */
  document: ReviewDocument;
  tour: Tour;
  /** Opens the fix confirmation with this conversation; absent when the pull
   *  request cannot be fixed from here. */
  fix?: (chat: FixChat) => void;
  /** A fix of the pull request is already running. */
  fixRunning: boolean;
  /** Opens the walkthrough's submit panel, where the draft is. */
  openDraft: () => void;
}) {
  const [added, setAdded] = useState<{ target: DraftTarget; text: string } | null>(null);
  const [undone, setUndone] = useState<"removed" | "edited" | null>(null);
  const suggestion = answer.suggests_fix?.summary;
  const key = progressKey(document);
  const saved = useUIState(progressState(key));
  const add = () => {
    const target = draftTarget(tour, turn.focus);
    setAdded({ target, text: appendToDraft(key, target, answerComment(answer)) });
    setUndone(null);
  };
  const undo = () => {
    if (!added) return;
    const removed = removeFromDraft(key, added.target, added.text);
    setAdded(null);
    setUndone(removed ? "removed" : "edited");
  };
  return (
    <div className="ask-actions" role="group" aria-label="Use this answer">
      <SavedStateNotice state={saved} label="review draft" />
      {suggestion ? (
        <p className="ask-suggestion">
          <strong>Suggested change:</strong> {suggestion}
        </p>
      ) : null}
      <div className="ask-actions-row">
        {fix ? (
          <Button
            variant={suggestion ? "primary" : "secondary"}
            size="compact"
            disabled={fixRunning}
            title={fixRunning ? "A fix of this pull request is running" : undefined}
            onClick={() => fix({ conversation_id: conversation.id, turn: turn.seq, suggestion })}
          >
            Fix this
          </Button>
        ) : null}
        <Button variant="secondary" size="compact" disabled={!saved.ready} onClick={add}>
          Add as comment
        </Button>
      </div>
      {added ? (
        <p role="status" className="ask-added">
          Added to your review draft
          {added.target.kind === "stop" ? ` on ${added.target.title}` : " as the overall comment"}.{" "}
          <Button variant="secondary" size="compact" onClick={undo}>
            Undo
          </Button>{" "}
          <Button variant="secondary" size="compact" onClick={openDraft}>
            Open the draft
          </Button>
        </p>
      ) : undone ? (
        <p role="status" className="ask-added">
          {undone === "removed"
            ? "Removed from your review draft."
            : "The draft was edited since, so nothing was removed."}
        </p>
      ) : null}
    </div>
  );
}
