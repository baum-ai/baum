import { useEffect, useRef, useState } from "react";
import { Alert, Button } from "@baum/viewer/components/ui";
import { ChevronDownIcon, ExternalLinkIcon, PlusIcon } from "@baum/viewer/components/icons";
import { Part } from "@baum/viewer/components/review-parts";
import { Markdown } from "@baum/viewer/components/markdown";
import { formatRelative } from "@baum/viewer/components/format";
import { cn } from "@baum/viewer/components/classnames";
import { SECONDARY } from "./action-classes";
import { LocalRequestError, request, type Entry } from "./api";
import { MenuButton, useContextMenu, type MenuItem } from "./context-menu";
import { useModal } from "./modal";
import { NineDots } from "./nine-dots";
import { COMMAND } from "./shortcuts";
import { SavedStateNotice, useUIState } from "./ui-state";
import {
  afterSent,
  needsSummary,
  sentRecord,
  pendingReviewState,
  pendingSubmission,
  placeText,
  reviewEvents,
  type CommentTarget,
  type PendingComment,
  type PendingReview,
  type SentReview,
} from "./pending-review-model";
import type { CommentPlace, ReviewEvent } from "./walkthrough-model";

export const EVENT_LABELS: Record<ReviewEvent, string> = {
  APPROVE: "Approve",
  COMMENT: "Comment",
  REQUEST_CHANGES: "Request changes",
};

/** The reader's own review of a pull request: its comments and summary as
 *  the daemon keeps them, and the changes to them. */
export function usePendingReview(entry: Entry) {
  const state = useUIState(pendingReviewState(entry.pull_request));
  const add = (target: CommentTarget, place: CommentPlace | null, head: string, body: string) => {
    // Made once, outside the change: a change is replayed on every render,
    // save and conflict, and each replay must add the same comment.
    const comment: PendingComment = {
      id: crypto.randomUUID(),
      target,
      body,
      place,
      head,
      created: Date.now(),
    };
    state.update((current) => ({ ...current, comments: [...current.comments, comment] }));
  };
  const edit = (id: string, body: string) =>
    state.update((current) => ({
      ...current,
      comments: current.comments.map((c) => (c.id === id ? { ...c, body } : c)),
    }));
  const remove = (id: string) =>
    state.update((current) => ({
      ...current,
      comments: current.comments.filter((c) => c.id !== id),
    }));
  return { ...state, add, edit, remove };
}

/** Sends the review, as Snooze picks a time: the button opens a menu of
 *  verdicts, and each opens the dialog on it. On the reader's own pull
 *  request GitHub only takes comments, so it is a Comment button straight to
 *  the dialog. The page's v presses it. */
export function ReviewButton({
  entry,
  count,
  review,
  className,
}: {
  entry: Entry;
  /** The comments waiting to be sent. */
  count: number;
  review: (event: ReviewEvent) => void;
  className?: string;
}) {
  const menu = useContextMenu();
  const button = useRef<HTMLButtonElement>(null);
  const own = entry.pull_request.authored_by_me;
  const items: MenuItem[] = (["APPROVE", "COMMENT", "REQUEST_CHANGES"] as const).map((event) => ({
    label: EVENT_LABELS[event],
    onSelect: () => review(event),
  }));
  const open = menu.anchor !== null && menu.anchor === button.current;
  return (
    <>
      <button
        ref={button}
        type="button"
        data-review-button=""
        className={cn(SECONDARY, className)}
        aria-keyshortcuts="v"
        aria-haspopup={own ? undefined : "menu"}
        aria-expanded={own ? undefined : open}
        aria-controls={open ? menu.id : undefined}
        onClick={(event) => {
          if (own) return review("COMMENT");
          const target = event.currentTarget;
          // Opened by v rather than by pressing the button, the menu gives the
          // keys back to where they were, so Esc leaves no ring on the button.
          const opener = window.document.activeElement;
          if (target.dataset.closing !== undefined) delete target.dataset.closing;
          else
            menu.openFrom(
              target,
              items,
              "Review verdicts",
              false,
              opener === target ? undefined : opener,
            );
        }}
        onPointerLeave={(event) => delete event.currentTarget.dataset.closing}
      >
        {own ? "Comment" : "Approve"}
        {count ? <span className="review-count">{count}</span> : null}
        {own ? null : <ChevronDownIcon size={14} className="text-(--text-tertiary)" />}
        <kbd aria-hidden="true" className="key-hint">
          v
        </kbd>
      </button>
      {menu.menu}
    </>
  );
}

type Sending =
  | { state: "idle" }
  | { state: "sending" }
  | { state: "failed"; message: string; code?: string };

/** Confirms the review and sends it to GitHub with the comments and summary
 *  the reader wrote, which are then cleared from the draft. */
export function ReviewDialog({
  entry,
  event: initial,
  close,
  sent,
}: {
  entry: Entry;
  event: ReviewEvent;
  close: () => void;
  /** Called with the review's URL once GitHub has it. */
  sent: (url: string) => void;
}) {
  const pr = entry.pull_request;
  const dialog = useModal();
  const draft = usePendingReview(entry);
  const review = draft.value;
  const events = reviewEvents(pr, review);
  const [picked, setPicked] = useState(initial);
  const [sending, setSending] = useState<Sending>({ state: "idle" });
  const event = events.find((e) => e.id === picked && e.ok)?.id ?? null;
  const busy = sending.state === "sending";
  const missing = event !== null && needsSummary(event, review);
  const ready = Boolean(event) && !missing && draft.ready && !busy;
  // A comment or a request for changes opened with nothing said yet starts
  // in the summary, which it needs.
  const summary = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    if (needsSummary(initial, review)) summary.current?.focus();
  }, [draft.ready]);
  const send = async (inline: boolean) => {
    if (!event) return;
    setSending({ state: "sending" });
    const snapshot: PendingReview = review;
    try {
      const answer = await request<{ url: string }>(
        "api/prs/review",
        pendingSubmission(pr, snapshot, event, inline),
      );
      const record = sentRecord(snapshot, event, answer.url, Date.now());
      draft.update((current) => afterSent(current, snapshot, record));
      sent(answer.url);
      close();
    } catch (error) {
      setSending({
        state: "failed",
        message: error instanceof Error ? error.message : "The review could not be sent.",
        code: error instanceof LocalRequestError ? error.code : undefined,
      });
    }
  };
  const inline = review.comments.filter((c) => c.place && c.head === pr.head_sha).length;
  return (
    <dialog
      ref={dialog}
      className="review-dialog"
      onCancel={(cancel) => {
        if (busy) cancel.preventDefault();
        else close();
      }}
      onKeyDown={(key) => {
        if (key.key === "Enter" && (key.metaKey || key.ctrlKey) && ready) {
          key.preventDefault();
          void send(true);
          return;
        }
        // j and k step through the verdicts GitHub takes, as in the merge
        // dialog; the summary keeps them for typing.
        const step = key.key === "j" ? 1 : key.key === "k" ? -1 : 0;
        if (
          !step ||
          busy ||
          key.metaKey ||
          key.ctrlKey ||
          key.altKey ||
          key.nativeEvent.isComposing ||
          (key.target instanceof HTMLElement && key.target.closest("textarea"))
        )
          return;
        key.preventDefault();
        const allowed = events.filter((e) => e.ok).map((e) => e.id);
        const at = allowed.indexOf(event ?? allowed[0]!);
        const next = allowed[Math.min(allowed.length - 1, Math.max(0, at + step))];
        if (!next) return;
        setPicked(next);
        dialog.current
          ?.querySelector<HTMLInputElement>(`input[name="review-event"][value="${next}"]`)
          ?.focus();
      }}
      aria-labelledby="review-title"
    >
      <h2 id="review-title" className="text-lg font-semibold">
        Review pull request #{pr.number}
      </h2>
      <p className="my-3 text-(--text-secondary)">
        {pr.title} · {pr.repository}
      </p>
      <fieldset className="merge-methods" disabled={busy}>
        <legend className="sr-only">Verdict</legend>
        {events.map((e) => (
          <label key={e.id} aria-disabled={!e.ok} className={e.ok ? undefined : "opacity-60"}>
            <input
              type="radio"
              name="review-event"
              value={e.id}
              checked={event === e.id}
              disabled={!e.ok}
              onChange={() => setPicked(e.id)}
            />
            <span>
              {e.label}
              <small>{e.hint}</small>
            </span>
          </label>
        ))}
      </fieldset>
      <label className="review-field">
        {event !== "APPROVE" && review.comments.length === 0 ? "Summary" : "Summary (optional)"}
        <textarea
          ref={summary}
          className="walk-textarea"
          value={review.note}
          disabled={busy}
          onChange={(change) => {
            const note = change.target.value;
            draft.update((current) => ({ ...current, note }));
          }}
        />
      </label>
      {review.comments.length ? (
        <div className="review-field">
          <p>
            {review.comments.length === 1
              ? "Your comment goes with it"
              : `Your ${review.comments.length} comments go with it`}
            {inline < review.comments.length
              ? inline
                ? `, ${inline} on ${inline === 1 ? "its line" : "their lines"} and the rest in the summary:`
                : ", in the summary:"
              : ", each on its line:"}
          </p>
          <ul className="review-dialog-comments">
            {review.comments.map((comment) => (
              <li key={comment.id}>
                <span>
                  {comment.target.title}
                  {placeText(comment.place) ? ` · ${placeText(comment.place)}` : ""}
                </span>
                <p>{comment.body}</p>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {/* Saves run as the reader types; only a failure is worth saying. */}
      {draft.error ? <SavedStateNotice state={draft} label="review" /> : null}
      {sending.state === "failed" ? (
        <Alert>
          {sending.message}
          {sending.code === "invalid_line" ? (
            <>
              {" "}
              <Button size="compact" variant="secondary" onClick={() => void send(false)}>
                Put the comments in the summary instead
              </Button>
            </>
          ) : null}
        </Alert>
      ) : null}
      <div className="mt-4 flex items-center justify-end gap-2">
        {missing ? (
          <span className="mr-auto text-xs text-(--text-tertiary)">Write a summary to send it</span>
        ) : null}
        <Button variant="ghost" size="compact" disabled={busy} onClick={close}>
          Cancel
          <kbd aria-hidden="true" className="key-hint">
            Esc
          </kbd>
        </Button>
        <Button size="compact" disabled={!ready} onClick={() => void send(true)}>
          {busy ? (
            <>
              <NineDots tone="text" />
              Sending…
            </>
          ) : (
            <>
              {event ? EVENT_LABELS[event] : "Send"}
              <kbd aria-hidden="true" className="key-hint">
                {COMMAND}↵
              </kbd>
            </>
          )}
        </Button>
      </div>
    </dialog>
  );
}

/** A comment being written: a new one on `target`, or an edit of `id`. */
export type Composer =
  | { kind: "new"; target: CommentTarget; place: CommentPlace | null; head: string }
  | { kind: "edit"; id: string };

/** The side pane's Comments tab: the comments of the reader's review, in the
 *  order written, a box for the one being written, and the way to send. */
export function CommentsPane({
  entry,
  current,
  composer,
  setComposer,
  review,
}: {
  entry: Entry;
  /** What a new comment is about: the item being read. */
  current: { target: CommentTarget; place: CommentPlace | null; head: string };
  composer: Composer | null;
  setComposer: (composer: Composer | null) => void;
  review: (event: ReviewEvent) => void;
}) {
  const draft = usePendingReview(entry);
  const comments = draft.value.comments;
  const menu = useContextMenu();
  const editing = composer?.kind === "edit" ? composer.id : null;
  const items = (comment: PendingComment): MenuItem[] => [
    { label: "Edit", onSelect: () => setComposer({ kind: "edit", id: comment.id }) },
    { label: "Delete", danger: true, onSelect: () => draft.remove(comment.id) },
  ];
  const adding = composer?.kind === "new" ? composer : null;
  // The button says only Comment; what it is on shows in the box it opens.
  const on =
    current.target.kind === "general"
      ? "Comment on the pull request"
      : `Comment on ${current.target.title}`;
  return (
    <div className="space-y-7">
      <Part
        label="Your review"
        aside={<ReviewButton entry={entry} count={comments.length} review={review} />}
      >
        <div className="review-comments">
          {comments.length ? (
            <ol className="review-comments-list">
              {comments.map((comment) =>
                editing === comment.id ? (
                  <li key={comment.id}>
                    <CommentBox
                      label={`Edit the comment on ${comment.target.title}`}
                      where={placeText(comment.place)}
                      initial={comment.body}
                      save={(body) => {
                        draft.edit(comment.id, body);
                        setComposer(null);
                      }}
                      cancel={() => setComposer(null)}
                    />
                  </li>
                ) : (
                  <li
                    key={comment.id}
                    className="review-comment"
                    onContextMenu={(event) =>
                      menu.open(event, items(comment), comment.target.title)
                    }
                  >
                    <div className="review-comment-head">
                      <span className="review-comment-title">
                        {comment.target.title}
                        {placeText(comment.place) ? (
                          <code className="review-comment-where">{placeText(comment.place)}</code>
                        ) : null}
                      </span>
                      <MenuButton
                        label={`Comment on ${comment.target.title}`}
                        items={() => items(comment)}
                        menu={menu}
                      />
                    </div>
                    <Markdown html={false} className="review-comment-body">
                      {comment.body}
                    </Markdown>
                  </li>
                ),
              )}
            </ol>
          ) : null}
          {adding ? (
            <CommentBox
              label={
                adding.target.kind === "general" ? "Comment" : `Comment on ${adding.target.title}`
              }
              where={placeText(adding.place)}
              initial=""
              save={(body) => {
                draft.add(adding.target, adding.place, adding.head, body);
                setComposer(null);
              }}
              cancel={() => setComposer(null)}
            />
          ) : (
            <Button
              variant="secondary"
              size="compact"
              className="gap-1.5 self-start"
              title={on}
              aria-keyshortcuts="c"
              onClick={() => setComposer({ kind: "new", ...current })}
            >
              <PlusIcon size={14} className="flex-none text-(--text-tertiary)" />
              Comment
              <kbd aria-hidden="true" className="key-hint">
                c
              </kbd>
            </Button>
          )}
          {menu.menu}
          {draft.error ? <SavedStateNotice state={draft} label="comments" /> : null}
        </div>
      </Part>
      <SentReviews sent={draft.value.sent ?? []} />
    </div>
  );
}

const SENT_LABELS: Record<ReviewEvent, string> = {
  APPROVE: "Approved",
  COMMENT: "Commented",
  REQUEST_CHANGES: "Requested changes",
};

/** The reviews sent from here, newest first, as they were sent: the verdict,
 *  when, the summary and the comments, each a link to the review on GitHub.
 *  Other reviews of the pull request are GitHub's to show. */
function SentReviews({ sent }: { sent: SentReview[] }) {
  if (sent.length === 0) return null;
  return (
    <Part label="Sent">
      <ol className="review-comments-list">
        {[...sent].reverse().map((review) => (
          <li key={`${review.url}@${review.at}`} className="review-sent">
            <div className="review-sent-head">
              <span className={`review-sent-verdict review-sent-${review.event.toLowerCase()}`}>
                {SENT_LABELS[review.event]}
              </span>
              <time dateTime={new Date(review.at).toISOString()} className="review-sent-when">
                {formatRelative(new Date(review.at).toISOString())}
              </time>
              <a
                href={review.url}
                target="_blank"
                rel="noreferrer"
                className="review-sent-link"
                aria-label="Open the review on GitHub"
                title="Open it on GitHub"
              >
                <ExternalLinkIcon size={13} />
              </a>
            </div>
            {review.note ? (
              <Markdown html={false} className="review-comment-body">
                {review.note}
              </Markdown>
            ) : null}
            {review.comments.length ? (
              <ul className="review-sent-comments">
                {review.comments.map((comment, index) => (
                  <li key={index}>
                    <span className="review-comment-title">
                      {comment.title}
                      {comment.where ? (
                        <code className="review-comment-where">{comment.where}</code>
                      ) : null}
                    </span>
                    <Markdown html={false} className="review-comment-body">
                      {comment.body}
                    </Markdown>
                  </li>
                ))}
              </ul>
            ) : null}
          </li>
        ))}
      </ol>
    </Part>
  );
}

/** The box a comment is written in: ⌘↵ saves it, Esc puts it away. */
function CommentBox({
  label,
  where,
  initial,
  save,
  cancel,
}: {
  label: string;
  where: string;
  initial: string;
  save: (body: string) => void;
  cancel: () => void;
}) {
  const [body, setBody] = useState(initial);
  const box = useRef<HTMLTextAreaElement>(null);
  useEffect(() => box.current?.focus(), []);
  const done = () => {
    if (body.trim()) save(body.trim());
  };
  return (
    <form
      className="review-comment-box"
      onSubmit={(submit) => {
        submit.preventDefault();
        done();
      }}
    >
      <label className="review-field">
        <span>
          {label}
          {where ? <code className="review-comment-where">{where}</code> : null}
        </span>
        <textarea
          ref={box}
          data-autofocus=""
          className="walk-textarea"
          value={body}
          onChange={(change) => setBody(change.target.value)}
          onKeyDown={(key) => {
            if (key.key === "Enter" && (key.metaKey || key.ctrlKey)) {
              key.preventDefault();
              done();
            } else if (key.key === "Escape") {
              key.preventDefault();
              key.stopPropagation();
              cancel();
            }
          }}
        />
      </label>
      <div className="flex gap-2">
        <Button size="compact" type="submit" disabled={!body.trim()}>
          Save
          <kbd aria-hidden="true" className="key-hint">
            {COMMAND}↵
          </kbd>
        </Button>
        <Button variant="ghost" size="compact" type="button" onClick={cancel}>
          Cancel
          <kbd aria-hidden="true" className="key-hint">
            Esc
          </kbd>
        </Button>
      </div>
    </form>
  );
}
