import { type ReactNode, useEffect, useId, useRef, useState } from "react";

import { harnessName } from "../agent-names";
import { harnessBrand } from "../brands";
import { dayBreaks, promptSegments } from "../conversation";
import type { TranscriptImage } from "../transcript";
import { BrandMark } from "./brand-logo";
import { cn, FOCUS_RING } from "./classnames";
import { useLogLayout } from "./conversation-layout";
import { formatCompact, formatTime } from "./format";
import { Markdown } from "./markdown";

/** The pieces a session's conversation is drawn with, laid out like a
 *  messaging app: each prompt as a bubble on the right, the agent's replies as
 *  bubbles on the left, and what neither of them said as centred pills. A
 *  whole transcript and a prompt quoted on its own are built from the same
 *  pieces, so a prompt reads the same wherever it appears. */

/** The conversation's own background, which the bubbles sit on. A log has
 *  none. */
export function ChatSurface({
  children,
  className,
  plain = false,
}: {
  children: ReactNode;
  className?: string;
  plain?: boolean;
}) {
  const log = useLogLayout();
  return (
    <div
      className={cn(
        "flex flex-col",
        !log && "rounded-xl p-3 sm:p-4",
        !plain && !log && "bg-(--surface-sunken)",
        className,
      )}
    >
      {children}
    </div>
  );
}

/** A line the conversation itself did not say — a date, a status, what the
 *  agent did between messages — centred as a chat app sets them. */
export function SystemNote({
  children,
  className,
  align = "center",
}: {
  children: ReactNode;
  className?: string;
  align?: "center" | "start";
}) {
  const log = useLogLayout();
  return (
    <p
      className={cn(
        "flex max-w-full items-center gap-2 text-xs text-(--text-tertiary)",
        !log && "rounded-2xl border border-(--border-subtle) bg-(--surface-card) px-3 py-1",
        align === "start" || log ? "self-start text-left" : "self-center text-center",
        className,
      )}
    >
      {children}
    </p>
  );
}

/** A bubble's shared shape. The first of a run keeps a sharp corner on its
 *  speaker's side, where a chat app draws the tail. */
const BUBBLE = "min-w-0 max-w-[88%] rounded-2xl border px-3 pt-2 pb-1.5 sm:max-w-[80%]";

/** A prompt, as a bubble on the right: the side a chat puts its reader on.
 *  `avatar` is the person's picture beside it, when the host knows who typed
 *  it. `length` and `truncated` describe a preview, which `onExpand` asks the
 *  host to replace with the whole text. `kind` is "instruction" for what a
 *  parent agent wrote its subagent, which reads the same but is no person's. */
export function PromptBubble({
  kind = "human",
  text,
  images,
  author,
  avatar,
  length = [...text].length,
  truncated = false,
  onExpand,
  meta,
}: {
  kind?: "human" | "instruction";
  text: string;
  /** The images pasted with it, under its text. */
  images?: TranscriptImage[];
  author?: string | null;
  avatar?: ReactNode;
  length?: number;
  truncated?: boolean;
  onExpand?: () => void;
  /** The small print in the corner: the turn, and when. */
  meta?: ReactNode;
}) {
  const log = useLogLayout();
  const body = (
    <CollapsibleText
      length={length}
      // Characters as the platform counts them, not UTF-16 units.
      shown={[...text].length}
      truncated={truncated}
      lines={8}
      onExpand={onExpand}
      measure={text}
    >
      <PromptText text={text} />
    </CollapsibleText>
  );
  const pictures = images?.length ? <PromptImages images={images} /> : null;
  if (log)
    return (
      <div
        data-message={kind}
        // Set apart by a rule in the app's accent, not a fill.
        className="min-w-0 border-l-2 border-(--action-primary) pl-3.5"
      >
        <p className="mb-1 flex flex-wrap items-baseline gap-x-2 text-xs text-(--text-tertiary)">
          {author ? (
            <span className="min-w-0 truncate font-medium text-(--text-secondary)">{author}</span>
          ) : null}
          {meta}
        </p>
        {body}
        {pictures}
      </div>
    );
  return (
    <div className="flex items-start justify-end gap-2">
      <div
        data-message={kind}
        className={cn(BUBBLE, "rounded-tr-sm border-(--border-subtle) bg-(--surface-selected)")}
      >
        {author ? (
          <p className="mb-0.5 min-w-0 truncate text-right text-xs font-medium text-(--text-secondary)">
            {author}
          </p>
        ) : null}
        {body}
        {pictures}
        {meta ? <BubbleMeta>{meta}</BubbleMeta> : null}
      </div>
      {/* The person, beside their message as a group chat shows a sender. */}
      {avatar === undefined ? null : <span className="w-7 flex-none">{avatar}</span>}
    </div>
  );
}

const IMAGE_DIALOG = {
  width: "auto",
  maxWidth: "none",
  maxHeight: "none",
  padding: 0,
  border: 0,
  borderRadius: 0,
  background: "transparent",
  boxShadow: "none",
  overflow: "visible",
} as const;

/** A prompt's images as thumbnails, each opening at full size in a dialog
 *  that a click on the image or Escape closes. */
function PromptImages({ images }: { images: TranscriptImage[] }) {
  const [open, setOpen] = useState<TranscriptImage | null>(null);
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const element = dialog.current;
    if (!element) return;
    if (open && !element.open) element.showModal();
    if (!open && element.open) element.close();
  }, [open]);
  return (
    <>
      <ul className="mt-2 flex flex-wrap gap-2">
        {images.map((image) => (
          <li key={image.src}>
            <button
              type="button"
              onClick={() => setOpen(image)}
              className={cn(
                "block cursor-zoom-in overflow-hidden rounded-(--radius-md) border border-(--border-subtle) bg-(--surface-sunken) transition-colors hover:border-(--border-strong)",
                FOCUS_RING,
              )}
            >
              <img
                src={image.src}
                alt={image.alt}
                loading="lazy"
                className="block h-auto max-h-36 w-auto max-w-full object-contain sm:max-w-80"
              />
            </button>
          </li>
        ))}
      </ul>
      <dialog
        ref={dialog}
        aria-label={open?.alt}
        onClose={() => setOpen(null)}
        className="m-auto outline-none backdrop:bg-(--surface-scrim)"
        // Inline, so a host's own dialog box (its width, padding and card)
        // cannot frame the image.
        style={IMAGE_DIALOG}
      >
        {open ? (
          // The image closes itself, as Escape does.
          <button
            type="button"
            aria-label="Close image"
            onClick={() => dialog.current?.close()}
            className="block cursor-zoom-out border-0 bg-transparent p-0"
          >
            <img
              src={open.src}
              alt={open.alt}
              className="block max-h-[90vh] max-w-[90vw] rounded-(--radius-md) object-contain shadow-lg"
            />
          </button>
        ) : null}
      </dialog>
    </>
  );
}

/** Prompts quoted out of their sessions, each as the transcript's bubble with
 *  its `#N` and time in the corner and a pill for each new day. Prompts
 *  from several sessions are introduced by a pill naming their `session`, so
 *  two `#1`s do not read as one conversation, and each session counts its own
 *  days. `author` and `avatar` name who typed them, when the host knows. A
 *  prompt whose text is not carried says so in its place. */
export function QuotedPrompts({
  prompts,
  author,
  avatar,
  className,
}: {
  prompts: {
    id: string;
    index: number;
    text: string | null;
    at?: string | null;
    session?: { id: string; label: string };
  }[];
  author?: string | null;
  avatar?: ReactNode;
  className?: string;
}) {
  // Days and times in the reader's own zone, which only the browser knows.
  const mounted = useMounted();
  const starts = prompts.map(
    (prompt, index) => index === 0 || prompt.session?.id !== prompts[index - 1]?.session?.id,
  );
  const days: (string | null)[] = [];
  if (mounted) {
    for (let index = 0; index < prompts.length; ) {
      let end = index + 1;
      while (end < prompts.length && !starts[end]) end++;
      days.push(...dayBreaks(prompts.slice(index, end).map((prompt) => prompt.at ?? null)));
      index = end;
    }
  }
  const several = new Set(prompts.map((prompt) => prompt.session?.id)).size > 1;
  return (
    <ChatSurface className={className}>
      <ol className="flex flex-col gap-3">
        {prompts.map((prompt, index) => (
          <li key={prompt.id} className="flex flex-col gap-2">
            {several && prompt.session && starts[index] ? (
              <SystemNote className="mb-1 font-medium">{prompt.session.label}</SystemNote>
            ) : null}
            {days[index] ? <SystemNote className="mb-1">{days[index]}</SystemNote> : null}
            {prompt.text === null ? (
              <SystemNote>#{prompt.index}: the document does not carry its text.</SystemNote>
            ) : (
              <PromptBubble
                text={prompt.text}
                author={author}
                avatar={avatar}
                meta={
                  <>
                    <span className="tabular-nums">#{prompt.index}</span>
                    {prompt.at && mounted ? (
                      <time dateTime={prompt.at}>{formatTime(prompt.at)}</time>
                    ) : null}
                  </>
                }
              />
            )}
          </li>
        ))}
      </ol>
    </ChatSurface>
  );
}

function useMounted(): boolean {
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);
  return mounted;
}

/** The agent's picture: its harness's mark in a circle, beside the first
 *  bubble of a run; later bubbles keep the column so they line up. */
export function AgentAvatar({ vendor }: { vendor?: string }) {
  const log = useLogLayout();
  const brand = vendor === undefined ? null : harnessBrand(vendor);
  if (log) return null;
  return (
    <span className="flex w-7 flex-none justify-center">
      {vendor !== undefined ? (
        <span
          aria-hidden
          className="flex size-7 items-center justify-center rounded-full border border-(--border-subtle) bg-(--surface-card) text-xs font-medium text-(--text-secondary)"
        >
          {brand ? <BrandMark brand={brand} size={16} /> : harnessName(vendor).charAt(0)}
        </span>
      ) : null}
    </span>
  );
}

/** A prompt as its author typed it, with only pasted code fences rendered as
 *  code and quoted lines as a quote: the rest is someone's own words, where markdown would turn a
 *  `__init__` bold or a `# comment` into a heading. */
function PromptText({ text }: { text: string }) {
  return (
    <div className="space-y-2 text-base leading-[1.6] text-(--text-primary)">
      {promptSegments(text).map((segment, index) =>
        segment.kind === "code" ? (
          <Markdown key={index} html={false}>
            {segment.value}
          </Markdown>
        ) : segment.kind === "quote" ? (
          <blockquote
            key={index}
            className="m-0 border-l-2 border-(--border-default) pl-3 whitespace-pre-wrap text-(--text-secondary) [overflow-wrap:anywhere]"
          >
            {segment.value}
          </blockquote>
        ) : (
          <p key={index} className="m-0 whitespace-pre-wrap [overflow-wrap:anywhere]">
            {segment.value}
          </p>
        ),
      )}
    </div>
  );
}

/** An agent's reply, as the markdown agents write: code fences, lists and
 *  inline code render, and raw HTML tags read as written. */
function MessageText({
  text,
  muted = false,
  reading = false,
}: {
  text: string;
  muted?: boolean;
  /** Set as long text to read (Markdown's `reading`). */
  reading?: boolean;
}) {
  return (
    <Markdown
      html={false}
      reading={reading}
      className={cn(
        "text-base leading-[1.6] [overflow-wrap:anywhere]",
        muted ? "text-(--text-secondary)" : "text-(--text-primary)",
      )}
    >
      {text}
    </Markdown>
  );
}

/** The small print in a bubble's bottom corner: when, and by what. */
function BubbleMeta({ children }: { children: ReactNode }) {
  return (
    <p className="mt-0.5 flex flex-wrap items-baseline justify-end gap-x-1.5 text-xs text-(--text-tertiary)">
      {children}
    </p>
  );
}

/** One agent message, rendered as the markdown agents write. `vendor` names
 *  the agent on the first bubble of a run, as a group chat names a sender;
 *  `author` replaces that name, such as for a subagent's response. */
export function AgentText({
  vendor,
  author,
  text,
  length,
  truncated = false,
  onExpand,
  caption,
  progress = false,
}: {
  vendor?: string;
  author?: string;
  text: string;
  length?: number;
  truncated?: boolean;
  onExpand?: () => void;
  caption?: string | null;
  /** In a log, a message the agent wrote on its way to its reply: said
   *  quietly, as a point in a list, held to a few lines. */
  progress?: boolean;
}) {
  const log = useLogLayout();
  const quiet = log && progress;
  const body = (
    <CollapsibleText
      length={length ?? [...text].length}
      // Characters as the platform counts them, not UTF-16 units.
      shown={[...text].length}
      truncated={truncated}
      lines={quiet ? 4 : log ? 24 : 12}
      onExpand={onExpand}
      measure={text}
    >
      <MessageText text={text} muted={quiet} reading={log && !quiet} />
    </CollapsibleText>
  );
  if (log) {
    // A run of messages is named once; the later ones keep their model and
    // time for whoever points at them.
    return (
      // A progress note is marked as one, so keys moving a message at a time
      // can pass over it.
      <div data-agent-text={quiet ? "progress" : ""} className="flex min-w-0 flex-col gap-1">
        {vendor !== undefined ? (
          <AgentByline vendor={vendor} author={author} caption={caption} />
        ) : null}
        <div
          title={vendor === undefined ? (caption ?? undefined) : undefined}
          className={cn(
            "min-w-0",
            quiet &&
              "relative pl-4 before:absolute before:top-[0.7em] before:left-0.5 before:size-1.5 before:rounded-full before:bg-(--border-strong)",
          )}
        >
          {body}
        </div>
      </div>
    );
  }
  return (
    <div data-agent-text className="flex items-start gap-2">
      <AgentAvatar vendor={vendor} />
      <div
        className={cn(
          BUBBLE,
          "border-(--border-strong) bg-(--surface-card)",
          vendor !== undefined && "rounded-tl-sm",
        )}
      >
        {vendor !== undefined ? (
          <p className="mb-0.5 text-xs font-medium text-(--text-secondary)">
            {author ?? harnessName(vendor)}
          </p>
        ) : null}
        {body}
        {caption ? <BubbleMeta>{caption}</BubbleMeta> : null}
      </div>
    </div>
  );
}

/** Who is speaking in a log, once above a run of its messages: the agent's
 *  mark and name, or `author` in its place, then `caption`. */
export function AgentByline({
  vendor,
  author,
  caption,
}: {
  vendor: string;
  author?: string;
  caption?: string | null;
}) {
  const brand = harnessBrand(vendor);
  return (
    <p className="flex flex-wrap items-center gap-x-1.5 text-xs text-(--text-tertiary)">
      {brand && !author ? <BrandMark brand={brand} size={12} /> : null}
      <span className="font-medium text-(--text-secondary)">{author ?? harnessName(vendor)}</span>
      {caption ? <span>{caption}</span> : null}
    </p>
  );
}

/** A message held to a few lines, fading out, with the rest one click away.
 *
 *  Messages average a few kilobytes and reach sixty. The fade is a mask rather
 *  than a gradient overlay, so it resolves to whatever is behind it: whichever
 *  bubble it sits in. `max-height` rather than `line-clamp`, which
 *  cannot clamp markdown blocks or `pre-wrap` text. */
function CollapsibleText({
  children,
  measure,
  length,
  shown,
  truncated,
  lines,
  onExpand,
}: {
  children: ReactNode;
  /** The text inside, to decide whether it needs a control at all. */
  measure: string;
  /** The stored length, larger than `shown` while only a preview is here. */
  length: number;
  shown: number;
  truncated: boolean;
  lines: number;
  /** Fetches the rest. Called the first time this is expanded. */
  onExpand?: () => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const bodyId = useId();
  const body = useRef<HTMLDivElement>(null);
  // A message that already fits gets no control and no fade: a "Show all"
  // that reveals nothing is noise, and there are several per turn. Whether it
  // fits is measured once it is on screen, open or folded, as the content's
  // height against the folded one; the server render guesses.
  const [overflows, setOverflows] = useState<boolean | null>(null);
  // Whether the body itself has more than it shows, which open means it
  // scrolls.
  const [scrolls, setScrolls] = useState(false);
  useEffect(() => {
    const element = body.current;
    const content = element?.firstElementChild;
    if (!element || !(content instanceof HTMLElement) || typeof ResizeObserver === "undefined")
      return;
    const check = () => {
      const folded = parseFloat(getComputedStyle(element).fontSize) * lines * 1.6;
      setOverflows(content.offsetHeight > folded + 1);
      setScrolls(element.scrollHeight > element.clientHeight + 1);
    };
    check();
    const observer = new ResizeObserver(check);
    observer.observe(content);
    observer.observe(element);
    return () => observer.disconnect();
  }, [measure, lines, expanded]);
  const scrollable = expanded && scrolls;
  const tall = overflows ?? (measure.length > 480 || measure.split("\n").length > lines);
  const fits = !truncated && !tall;
  // Kept while expanded, so a text that turned out to fit can still be folded.
  const collapsible = expanded || !fits;

  const toggle = () => {
    if (!expanded) onExpand?.();
    // Folding a text that fits removes the control itself; focus moves to
    // the text rather than falling to the page.
    else if (fits) body.current?.focus({ preventScroll: true });
    setExpanded((value) => !value);
  };

  return (
    <>
      <div
        ref={body}
        id={bodyId}
        // When it scrolls, the keyboard must reach it to scroll; otherwise it
        // only takes focus handed to it.
        role={scrollable ? "group" : undefined}
        aria-label={scrollable ? "Message" : undefined}
        tabIndex={scrollable ? 0 : -1}
        className={cn(
          "min-w-0",
          scrollable ? FOCUS_RING : "focus:outline-none",
          // One 58 KB message must not make the page two hundred screens tall
          // once it is open.
          expanded ? "max-h-[70vh] overflow-auto" : "overflow-hidden",
          !expanded &&
            // Only text running past the fold fades; a short preview stays crisp.
            tall &&
            "[mask-image:linear-gradient(to_bottom,black_calc(100%-2.5rem),transparent)]",
        )}
        style={expanded ? undefined : { maxHeight: `${lines * 1.6}em` }}
      >
        {children}
      </div>
      {collapsible ? (
        <p className="mt-1.5 flex flex-wrap items-baseline gap-x-2 text-xs text-(--text-tertiary)">
          <button
            type="button"
            aria-expanded={expanded}
            aria-controls={bodyId}
            onClick={toggle}
            className={cn(
              "cursor-pointer rounded-(--radius-xs) text-xs text-(--text-link) hover:underline",
              FOCUS_RING,
            )}
          >
            {expanded ? "Show less" : "Show all"}
          </button>
          {truncated && expanded ? (
            <span>
              Showing the first {formatCompact(shown)} of {formatCompact(length)} characters.
            </span>
          ) : (
            <span className="tabular-nums">{formatCompact(length)} characters</span>
          )}
        </p>
      ) : null}
    </>
  );
}
