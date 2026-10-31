import { useEffect, useSyncExternalStore } from "react";
import { Button } from "@baum/viewer/components/ui";
import { appURL } from "./api";

type Change<T> = (value: T) => T;
type RecordValue<T> = { revision: number; value: T | null };
type Snapshot<T> = { value: T; ready: boolean; saving: boolean; error: string };
type Batch<T> = { id: string; changes: Change<T>[] };

/** One origin-independent record, shared by every view in this window. Mutations
 * are replayed against a newer revision; a stable batch id makes retries safe
 * when a committed write's response was lost. */
export class UIState<T> {
  private base: T;
  private client = crypto.randomUUID();
  private sequence = 0;
  private revision = 0;
  private pending: Change<T>[] = [];
  private batch: Batch<T> | null = null;
  private reading: Promise<void> | null = null;
  private writing = false;
  private listeners = new Set<() => void>();
  private snapshot: Snapshot<T>;

  readonly path: string;
  private fallback: () => T;
  constructor(path: string, fallback: () => T) {
    this.path = path;
    this.fallback = fallback;
    this.base = fallback();
    this.snapshot = { value: this.base, ready: false, saving: false, error: "" };
  }
  get = () => this.snapshot;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };
  private emit(error = this.snapshot.error) {
    this.snapshot = {
      ...this.snapshot,
      value: this.pending.reduce((value, change) => change(value), this.base),
      saving: this.pending.length > 0,
      error,
    };
    for (const listener of this.listeners) listener();
  }
  private record(raw: unknown): RecordValue<T> {
    const record = raw as RecordValue<T> | null;
    if (
      !record ||
      !Number.isSafeInteger(record.revision) ||
      record.revision < 0 ||
      !("value" in record)
    )
      throw Error("The saved state response could not be read.");
    return record;
  }
  private accept(record: RecordValue<T>) {
    this.base = record.value ?? this.fallback();
    this.revision = record.revision;
    this.snapshot = { ...this.snapshot, ready: true };
  }
  load = (): Promise<void> => {
    if (this.reading) return this.reading;
    // Refreshing must never race a write or replace an unacknowledged edit.
    if (this.writing || this.pending.length) {
      if (this.snapshot.ready) return this.flush();
    }
    this.reading = (async () => {
      try {
        const response = await fetch(appURL(this.path), { cache: "no-store" });
        if (!response.ok) throw Error("Could not read your saved state.");
        this.accept(this.record(await response.json()));
        this.emit("");
      } catch (error) {
        this.emit(error instanceof Error ? error.message : "Could not read your saved state.");
      } finally {
        this.reading = null;
      }
      if (this.snapshot.ready && !this.snapshot.error) await this.flush();
    })();
    return this.reading;
  };
  update = (change: Change<T>) => {
    const previous = this.snapshot.value;
    if (change(previous) === previous) return;
    this.pending.push(change);
    this.emit();
    if (this.snapshot.ready) {
      if (!this.snapshot.error) void this.flush();
    } else if (!this.snapshot.error) void this.load();
  };
  retry = async () => {
    this.emit("");
    if (!this.snapshot.ready) await this.load();
    else await this.flush();
  };
  private async flush(): Promise<void> {
    if (this.writing || this.reading || !this.snapshot.ready || !this.pending.length) return;
    this.writing = true;
    try {
      let conflicts = 0;
      while (this.pending.length) {
        this.batch ??= { id: `${this.client}:${++this.sequence}`, changes: [...this.pending] };
        const batch = this.batch;
        const value = batch.changes.reduce((current, change) => change(current), this.base);
        const response = await fetch(appURL(this.path), {
          method: "PUT",
          cache: "no-store",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ revision: this.revision, mutation_id: batch.id, value }),
        });
        if (response.status === 409) {
          this.accept(this.record(await response.json()));
          this.emit();
          if (++conflicts >= 8)
            throw Error(
              "Saved state keeps changing in another window. Retry to save your changes.",
            );
          continue;
        }
        if (!response.ok) throw Error("Could not save your changes.");
        this.accept(this.record(await response.json()));
        this.pending.splice(0, batch.changes.length);
        this.batch = null;
        conflicts = 0;
        this.emit("");
      }
    } catch (error) {
      this.emit(error instanceof Error ? error.message : "Could not save your changes.");
    } finally {
      this.writing = false;
    }
  }
}

/** Drop window-local snapshots when starting a new application session. */
export function forgetUIState() {
  records.clear();
}

const records = new Map<string, UIState<unknown>>();
export function uiState<T>(path: string, fallback: () => T): UIState<T> {
  let state = records.get(path);
  if (!state) {
    state = new UIState(path, fallback) as UIState<unknown>;
    records.set(path, state);
  }
  return state as UIState<T>;
}
export function useUIState<T>(state: UIState<T>) {
  const snapshot = useSyncExternalStore(state.subscribe, state.get);
  useEffect(() => {
    if (!state.get().ready) void state.load();
    const focus = () => {
      if (!state.get().saving) void state.load();
    };
    window.addEventListener("focus", focus);
    return () => window.removeEventListener("focus", focus);
  }, [state]);
  return { ...snapshot, update: state.update, retry: state.retry };
}
export function SavedStateNotice({
  state,
  label,
}: {
  state: { ready: boolean; saving: boolean; error: string; retry: () => Promise<void> };
  label: string;
}) {
  if (state.error)
    return (
      <p role="alert">
        {state.error} Your changes stay in this window until saved.{" "}
        <Button size="compact" onClick={() => void state.retry()}>
          Retry saving {label}
        </Button>
      </p>
    );
  if (!state.ready) return <p role="status">Loading saved {label}…</p>;
  if (state.saving) return <p role="status">Saving {label}…</p>;
  return null;
}
