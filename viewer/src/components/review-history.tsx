import { type ReactNode, useId, useRef, useState } from "react";

import { type ReviewDocument, shortSha } from "../review-document";
import { cn, FOCUS_RING } from "./classnames";
import { formatDateTime } from "./format";
import { CheckIcon, ChevronDownIcon, CloseIcon } from "./icons";
import { IconButton } from "./ui";

import type { HistorySelection, useHistoryView } from "../use-review-version";

/** A native popover keeps the version rows out of the pane's scroll clipping.
 * Each row has one version action and a separate comparison checkbox. */
export function HistoryMenu({
  label,
  currentVersion,
  currentLabel,
  versions,
  value,
  onChange,
}: {
  label: string;
  currentVersion: number;
  currentLabel?: string;
  versions: number[];
  value: HistorySelection | null;
  onChange: (value: HistorySelection | null) => void;
}) {
  const id = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const popup = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const choose = (next: HistorySelection | null) => {
    onChange(next);
    popup.current?.hidePopover();
    trigger.current?.focus();
  };

  return (
    <>
      <button
        ref={trigger}
        type="button"
        aria-label={label}
        aria-expanded={open}
        aria-controls={id}
        popoverTarget={id}
        className={cn(
          "inline-flex h-8 max-w-full items-center gap-2 rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card) px-2 text-base text-(--text-primary)",
          FOCUS_RING,
        )}
        onClick={() => {
          const button = trigger.current;
          const menu = popup.current;
          if (!button || !menu) return;
          const rect = button.getBoundingClientRect();
          const below = window.innerHeight - rect.bottom - 8;
          const above = rect.top - 8;
          menu.style.left = `${Math.max(8, Math.min(rect.left, window.innerWidth - 248))}px`;
          menu.style.top = below >= Math.min(240, above) ? `${rect.bottom + 4}px` : "auto";
          menu.style.bottom =
            below >= Math.min(240, above) ? "auto" : `${window.innerHeight - rect.top + 4}px`;
          menu.style.maxHeight = `${Math.max(80, Math.min(320, below >= Math.min(240, above) ? below : above))}px`;
        }}
      >
        <span>
          {value
            ? `${value.compare ? "Compare" : "Viewing"} · v${value.version}`
            : (currentLabel ?? `Review v${currentVersion} · Current`)}
        </span>
        <ChevronDownIcon size={12} />
      </button>
      <div
        ref={popup}
        id={id}
        popover="auto"
        role="group"
        aria-label={`${label} options`}
        onToggle={(event) => setOpen(event.newState === "open")}
        className="fixed m-0 w-60 max-w-[calc(100vw-1rem)] overflow-y-auto rounded-(--radius-md) border border-(--border-default) bg-(--surface-card) p-1 text-base text-(--text-primary) shadow-(--shadow-overlay)"
      >
        <button
          type="button"
          aria-pressed={value === null}
          className={cn(
            "flex w-full items-center gap-2 rounded-(--radius-sm) px-2 py-2 text-left hover:bg-(--surface-hover)",
            FOCUS_RING,
          )}
          onClick={() => choose(null)}
        >
          <span className="w-3.5">{!value && <CheckIcon size={13} />}</span>
          Current · v{currentVersion}
        </button>
        {versions.map((version) => (
          <div
            key={version}
            className="flex items-center rounded-(--radius-sm) hover:bg-(--surface-hover)"
          >
            <button
              type="button"
              aria-label={`View v${version}`}
              aria-pressed={value?.version === version && !value.compare}
              className={cn(
                "flex min-w-0 flex-1 items-center gap-2 rounded-(--radius-sm) px-2 py-2 text-left",
                FOCUS_RING,
              )}
              onClick={() => choose({ version, compare: false })}
            >
              <span className="w-3.5">{value?.version === version && <CheckIcon size={13} />}</span>
              v{version}
            </button>
            <label className="flex cursor-pointer items-center gap-1.5 px-2 py-2 text-(--text-secondary)">
              <input
                type="checkbox"
                aria-label={`Compare v${version}`}
                checked={value?.version === version && value.compare}
                onChange={(event) => choose({ version, compare: event.target.checked })}
              />
              Compare
            </label>
          </div>
        ))}
      </div>
    </>
  );
}

export function HistoryToolbar({
  currentVersion,
  currentLabel,
  versions,
  view,
  status,
  children,
}: {
  currentVersion: number;
  currentLabel?: string;
  versions: number[];
  view: ReturnType<typeof useHistoryView>;
  /** Drawn right after the menu, such as how old the review is. */
  status?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-center gap-3">
      <HistoryMenu
        label="Review version"
        currentVersion={currentVersion}
        currentLabel={currentLabel}
        versions={versions}
        value={view.selection}
        onChange={view.setSelection}
      />
      {view.selection && (
        <IconButton
          label={view.selection.compare ? "Close history comparison" : "Return to current version"}
          onClick={() => view.setSelection(null)}
        >
          <CloseIcon />
        </IconButton>
      )}
      {status}
      {view.selection ? (
        <span className="text-base text-(--text-tertiary)">
          {view.selection.compare
            ? `Comparing current v${currentVersion} with v${view.selection.version}`
            : `Viewing historical review v${view.selection.version} · Read only`}
        </span>
      ) : null}
      {children}
    </div>
  );
}

export function ReviewHistory({
  document,
  view,
  children,
}: {
  document: ReviewDocument;
  view: ReturnType<typeof useHistoryView>;
  children: (document: ReviewDocument, historical: boolean) => ReactNode;
}) {
  const { selection, state } = view;
  const compare = selection?.compare === true;
  return (
    <>
      <div
        className={
          compare
            ? "grid min-h-0 flex-1 gap-5 xl:overflow-hidden 2xl:grid-cols-2 2xl:divide-x 2xl:divide-(--border-subtle)"
            : "contents"
        }
      >
        {(!selection || compare) && (
          <div
            className={
              compare ? "flex min-h-0 min-w-0 flex-col xl:overflow-hidden 2xl:pr-5" : "contents"
            }
          >
            {compare && (
              <p className="mb-3 text-base font-medium text-(--text-tertiary)">
                Current · Review v{document.review.version}
              </p>
            )}
            {children(document, false)}
          </div>
        )}
        {selection && (
          <section
            aria-label={`Historical ${compare ? "comparison" : "view"} v${selection.version}`}
            className={cn(
              "flex min-h-0 min-w-0 flex-1 flex-col xl:overflow-hidden",
              compare && "2xl:pl-5",
            )}
          >
            <p className="mb-3 text-base font-medium text-(--text-tertiary)">
              Historical · Review v{selection.version}
              {state?.status === "ready" &&
                ` · Generated against ${shortSha(state.document.review.head)} · ${formatDateTime(state.document.review.generated_at)}`}
            </p>
            {!state || state.status === "loading" ? (
              <p role="status" className="text-base text-(--text-tertiary)">
                Loading review v{selection.version}…
              </p>
            ) : state.status !== "ready" ? (
              <div role="alert" className="space-y-2 text-base text-(--status-critical)">
                <p>
                  Review v{selection.version} could not be loaded. No other version was substituted.
                </p>
                <button type="button" className="underline" onClick={view.retry}>
                  Retry
                </button>
              </div>
            ) : (
              children(state.document, true)
            )}
          </section>
        )}
      </div>
    </>
  );
}
