import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { request } from "../api";
import { useToast } from "../toast";

/** The person's own layout of a pull request's tiles, as `GET
 *  api/tiles/layout` sums it up: its revision, whether it has edits, whether
 *  an undo would remove a batch, and that batch (its host-written summary
 *  lines and the tiles it named). */
export type LayoutSummary = {
  revision: number;
  edited: boolean;
  can_undo: boolean;
  last: {
    id: string;
    by: "chat" | "page";
    reset?: boolean;
    summary: string[];
    tiles?: string[];
  } | null;
  batches: number;
};

/** What the tiles page offers about the person's layout: the strip's "Your
 *  layout", Undo and Reset, Show on a hidden tile, and the tiles the latest
 *  change named, marked briefly on their cards. */
export type LayoutControls = {
  edited: boolean;
  canUndo: boolean;
  /** The summary of the change an undo takes back. */
  lastSummary: string[];
  undo: () => void;
  reset: () => void;
  show: (tile: string) => void;
  /** The tiles the latest change named; `id` changes with each change. */
  marked: { id: number; tiles: ReadonlySet<string> } | null;
  /** The layout's revision, which a version read before it was made lacks. */
  revision: number;
};

/** A pull request's layout key, as the daemon names it in its events. */
export function layoutKey(repository: string, number: number): string {
  return `${repository.toLowerCase()}#${number}`;
}

const bus = new EventTarget();

/** Passes a `layout` event of the app's event stream to the open page:
 *  `{pr, revision}`, the pull request's key and the layout's new revision. */
export function publishLayoutEvent(data: string) {
  let event: { pr?: unknown; revision?: unknown };
  try {
    event = JSON.parse(data) as typeof event;
  } catch {
    return;
  }
  if (typeof event?.pr !== "string" || typeof event.revision !== "number") return;
  bus.dispatchEvent(new CustomEvent("layout", { detail: event }));
}

/** Tells the open page the event stream reconnected, so it may have missed a
 *  layout event: it reads its layout again. */
export function publishLayoutResync() {
  bus.dispatchEvent(new CustomEvent("layout", { detail: null }));
}

function useLayoutEvents(handler: (event: { pr: string; revision: number } | null) => void) {
  const latest = useRef(handler);
  latest.current = handler;
  useEffect(() => {
    const listener = (event: Event) => latest.current((event as CustomEvent).detail ?? null);
    bus.addEventListener("layout", listener);
    return () => bus.removeEventListener("layout", listener);
  }, []);
}

/** Whether a key press is the platform's undo, Mod+Z, outside any text
 *  field. */
export function isUndoKey(event: KeyboardEvent, mac: boolean): boolean {
  const target = event.target;
  if (
    target instanceof HTMLElement &&
    (target.isContentEditable || target.closest("input, textarea, select, [contenteditable]"))
  )
    return false;
  return (
    event.key.toLowerCase() === "z" &&
    !event.shiftKey &&
    !event.altKey &&
    (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey)
  );
}

/** What the person is told when their layout changed from `before` to
 *  `after`: the change and its Undo, the undo, or nothing (a read that found
 *  no change). The words are the host's summary lines. */
export function announcement(
  before: LayoutSummary | null,
  after: LayoutSummary,
): { title: string; detail?: string; undo: boolean; tiles: string[] } | null {
  if (!before || after.revision <= before.revision) return null;
  const lines = (summary: string[]) =>
    summary.length > 1
      ? {
          title: summary[0]!,
          detail: summary.slice(1, 4).join(" · ") + (summary.length > 4 ? " · …" : ""),
        }
      : { title: summary[0] ?? "Changed the layout" };
  const newBatch =
    after.last && after.last.id !== before.last?.id && after.batches >= before.batches;
  if (newBatch && after.last) {
    if (after.last.reset)
      return { title: "Reset the layout to the generated one", undo: true, tiles: [] };
    return { ...lines(after.last.summary), undo: true, tiles: after.last.tiles ?? [] };
  }
  if (after.batches < before.batches && before.last) {
    const undone = before.last.reset
      ? "Brought your layout back"
      : `Undid: ${before.last.summary[0] ?? "the last change"}`;
    return {
      title: undone,
      undo: false,
      tiles: before.last.reset ? [] : (before.last.tiles ?? []),
    };
  }
  return null;
}

const MAC = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);

/** The person's own layout of the pull request's tiles while the tiles page
 *  shows it: read once, then again on each `layout` event for it, when the
 *  page's tiles are read again (`reread`) and the change is announced with
 *  Undo. Its Undo, Reset and Show write through `POST api/tiles/layout`;
 *  Mod+Z undoes outside text fields. */
export function useTileLayout({
  clone,
  repository,
  number,
  enabled,
  reread,
}: {
  clone: string;
  repository: string;
  number: number;
  enabled: boolean;
  reread: () => Promise<unknown>;
}): LayoutControls | null {
  const [summary, setSummary] = useState<LayoutSummary | null>(null);
  const [marked, setMarked] = useState<LayoutControls["marked"]>(null);
  const current = useRef<LayoutSummary | null>(null);
  const rereadNow = useRef(reread);
  rereadNow.current = reread;
  const toast = useToast();
  const key = layoutKey(repository, number);
  const query = useMemo(
    () => new URLSearchParams({ clone, pr: String(number) }).toString(),
    [clone, number],
  );
  // Writes, and reads after an event, run one at a time, in order.
  const queue = useRef<Promise<unknown>>(Promise.resolve());
  const enqueue = useCallback((work: () => Promise<unknown>) => {
    queue.current = queue.current.then(work, work).catch(() => undefined);
    return queue.current;
  }, []);

  const undoRef = useRef<() => void>(() => undefined);
  /** Takes a new summary: the page's tiles are read again, then the change
   *  is announced and marked. */
  const settle = useCallback(
    async (next: LayoutSummary, announce: boolean) => {
      const before = current.current;
      if (before && next.revision <= before.revision) return;
      if (before) await rereadNow.current().catch(() => undefined);
      current.current = next;
      setSummary(next);
      const told = announce ? announcement(before, next) : null;
      if (!told) return;
      if (told.tiles.length > 0) setMarked({ id: next.revision, tiles: new Set(told.tiles) });
      toast({
        title: told.title,
        ...(told.detail ? { detail: told.detail } : {}),
        context: "Your layout",
        ...(told.undo ? { action: { label: "Undo", run: () => undoRef.current() } } : {}),
      });
    },
    [toast],
  );
  const read = useCallback(
    (announce: boolean) =>
      enqueue(async () => {
        const next = await request<LayoutSummary>(`api/tiles/layout?${query}`);
        await settle(next, announce);
      }),
    [enqueue, query, settle],
  );
  useEffect(() => {
    current.current = null;
    setSummary(null);
    setMarked(null);
    if (!enabled || !clone) return;
    void read(false);
  }, [enabled, clone, query, read]);
  useLayoutEvents((event) => {
    if (!enabled || !clone) return;
    if (event && (event.pr !== key || event.revision <= (current.current?.revision ?? -1))) return;
    void read(true);
  });
  const write = useCallback(
    (body: Record<string, unknown>) =>
      enqueue(async () => {
        try {
          const next = await request<LayoutSummary>("api/tiles/layout", {
            clone,
            pr: number,
            ...body,
          });
          await settle(next, true);
        } catch (error) {
          toast({
            title: "Your layout didn't change",
            detail: error instanceof Error ? error.message : String(error),
            error: true,
          });
          // Another change came first: read where the layout is now.
          const next = await request<LayoutSummary>(`api/tiles/layout?${query}`).catch(() => null);
          if (next) await settle(next, false);
        }
      }),
    [clone, enqueue, number, query, settle, toast],
  );
  const undo = useCallback(() => {
    const last = current.current?.last;
    if (!current.current?.can_undo || !last) return;
    void write({ action: "undo", batch: last.id });
  }, [write]);
  undoRef.current = undo;
  const reset = useCallback(() => void write({ action: "reset" }), [write]);
  const show = useCallback(
    (tile: string) => void write({ action: "edit", ops: [{ op: "show", tile }] }),
    [write],
  );
  // The mark fades on its own; a later change marks again.
  useEffect(() => {
    if (!marked) return;
    const timer = setTimeout(() => setMarked(null), 2400);
    return () => clearTimeout(timer);
  }, [marked]);
  useEffect(() => {
    if (!enabled) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.defaultPrevented || !isUndoKey(event, MAC) || !current.current?.can_undo) return;
      event.preventDefault();
      undo();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [enabled, undo]);
  return useMemo(
    () =>
      enabled && summary
        ? {
            edited: summary.edited,
            canUndo: summary.can_undo,
            lastSummary: summary.last?.reset ? ["Reset the layout"] : (summary.last?.summary ?? []),
            undo,
            reset,
            show,
            marked,
            revision: summary.revision,
          }
        : null,
    [enabled, summary, undo, reset, show, marked],
  );
}
