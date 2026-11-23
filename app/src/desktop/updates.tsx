import { useEffect, useRef, useState, type ReactNode } from "react";
import { ActionLink, Alert, Badge, Button } from "@baum/viewer/components/ui";
import { formatRelative } from "@baum/viewer/components/format";
import { RefreshIcon } from "@baum/viewer/components/icons";
import { LoadingStatus } from "../nine-dots";
import { PanelHeader } from "../shell";
import { useToast } from "../toast";
import { useVisibleInterval } from "../visible-interval";
import { invoke } from "./native";

/** The desktop app's updater (`update_state` in oss/desktop/src/updater.rs). */
export type AppUpdate = {
  version: string;
  enabled: boolean;
  /** Whether this copy installs `available` itself; otherwise it links to it. */
  installs: boolean;
  phase: "idle" | "checking" | "downloading" | "ready" | "waiting" | "installing";
  available: string | null;
  page: string | null;
  /** Seconds since the Unix epoch. */
  checked_at: number | null;
  jobs: number;
  error: string | null;
  /** The version whose notice the person closed since the app started. */
  noticed: string | null;
};

export type AppUpdates = {
  view: AppUpdate | null;
  error: string;
  check: () => void;
  install: () => void;
  cancel: () => void;
  /** Remembers, until the app quits, that the person saw `version`'s notice. */
  dismissNotice: (version: string) => void;
};

const BUSY = new Set<AppUpdate["phase"]>(["checking", "downloading", "waiting", "installing"]);

/** `next`, unless it says what `previous` already does: the same object keeps
 *  the app from rendering again for a poll that found nothing new. */
export function sameUpdate(previous: AppUpdate | null, next: AppUpdate | null) {
  if (previous === next) return previous;
  if (!previous || !next) return next;
  const keys = Object.keys(next) as (keyof AppUpdate)[];
  return keys.length === Object.keys(previous).length &&
    keys.every((key) => previous[key] === next[key])
    ? previous
    : next;
}

/** The newer version the sidebar counts on Settings: one the person can
 *  install now, or download when this copy cannot update itself. */
export function pendingUpdate(view: AppUpdate | null): string | null {
  if (!view?.available) return null;
  return view.phase === "ready" || !view.installs ? view.available : null;
}

/** Follows the desktop app's updater: often while it works, rarely
 *  otherwise. Outside the desktop app, or when the app offers no updater,
 *  `view` stays null. */
export function useAppUpdates(): AppUpdates {
  const [view, setView] = useState<AppUpdate | null>(null);
  const [error, setError] = useState("");
  const gone = useRef(false);
  const readUpdate = useRef<(() => void) | null>(null);
  const busy = view ? BUSY.has(view.phase) : false;
  useEffect(() => {
    if (gone.current) return;
    let stop = false;
    const missing = () => {
      // Not the desktop app, or an app without the updater.
      gone.current = true;
      if (!stop) setView((previous) => sameUpdate(previous, null));
    };
    const read = () =>
      invoke<AppUpdate>("update_state").then((next) => {
        if (typeof next?.version !== "string") missing();
        else if (!stop) setView((previous) => sameUpdate(previous, next));
      }, missing);
    void read();
    readUpdate.current = () => void read();
    return () => {
      stop = true;
      readUpdate.current = null;
    };
  }, []);
  useVisibleInterval(() => readUpdate.current?.(), busy ? 1000 : 60_000);
  const act = (command: string, args?: Record<string, unknown>) => {
    setError("");
    invoke<AppUpdate>(command, args).then(
      (next) => {
        if (typeof next?.version === "string") setView((previous) => sameUpdate(previous, next));
      },
      (cause) => setError(String(cause)),
    );
  };
  return {
    view,
    error,
    check: () => act("check_for_updates"),
    install: () => act("install_update"),
    cancel: () => act("cancel_update"),
    dismissNotice: (version) => act("dismiss_update_notice", { version }),
  };
}

/** Tells the person about an update in a toast that stays until closed:
 *  when it becomes available, and after each launch of the app while it is.
 *  Closing or following the toast, or already looking at This computer
 *  (`showing`), counts as seen until the app quits; a newer version is told
 *  again. */
export function useUpdateToast(updates: AppUpdates, open: () => void, showing: boolean) {
  const toast = useToast();
  const version = pendingUpdate(updates.view);
  const noticed = updates.view?.noticed ?? null;
  const told = useRef<string | null>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: tells once per version
  useEffect(() => {
    if (!version || version === noticed || version === told.current) return;
    told.current = version;
    if (showing) {
      updates.dismissNotice(version);
      return;
    }
    const installs = updates.view?.installs;
    toast({
      title: installs ? `baum ${version} is ready to install` : `baum ${version} is available`,
      detail: installs
        ? "Install it from This computer in Settings."
        : "Download it from This computer in Settings.",
      sticky: true,
      onClose: () => updates.dismissNotice(version),
      action: { label: "Open This computer", run: open },
    });
  }, [version, noticed, showing]);
}

function jobs(n: number) {
  return n === 1 ? "1 running job finishes" : `${n} running jobs finish`;
}

/** The app's version and updates, in Settings → This computer. */
export function UpdatesSection({ updates }: { updates: AppUpdates }) {
  const { view } = updates;
  if (!view) return null;
  const busy = BUSY.has(view.phase);
  const checked = view.checked_at
    ? `Checked ${formatRelative(new Date(view.checked_at * 1000).toISOString())}`
    : "Not checked yet";
  let status: ReactNode;
  let actions: ReactNode = null;
  const releaseNotes = view.page ? (
    <ActionLink
      variant="secondary"
      size="compact"
      href={view.page}
      target="_blank"
      rel="noreferrer"
    >
      {view.installs ? "What’s new" : `Download ${view.available}`}
    </ActionLink>
  ) : null;
  if (view.phase === "checking") {
    status = <LoadingStatus>Checking for updates…</LoadingStatus>;
  } else if (view.phase === "downloading") {
    status = <LoadingStatus>Downloading baum {view.available}…</LoadingStatus>;
  } else if (view.phase === "waiting" && view.jobs > 0) {
    status = (
      <p>
        baum {view.available} installs when {jobs(view.jobs)}, then the app restarts.
      </p>
    );
    actions = (
      <Button variant="secondary" size="compact" onClick={updates.cancel}>
        Cancel
      </Button>
    );
  } else if (view.phase === "waiting" || view.phase === "installing") {
    status = <LoadingStatus>Installing baum {view.available} and restarting…</LoadingStatus>;
  } else if (view.phase === "ready") {
    status = (
      <p>
        baum {view.available} is ready. The app restarts to finish, once no review or fix is
        running.
      </p>
    );
    actions = (
      <>
        {releaseNotes}
        <Button variant="primary" size="compact" onClick={updates.install}>
          Update and restart
        </Button>
      </>
    );
  } else if (view.available && !view.installs) {
    status = <p>baum {view.available} is available.</p>;
    actions = releaseNotes;
  } else {
    status = <p>{view.available ? `baum ${view.available} is available.` : "Up to date."}</p>;
  }
  const error = updates.error || view.error;
  return (
    <section className="local-panel p-0!" aria-labelledby="updates-heading">
      <PanelHeader
        id="updates-heading"
        icon={<RefreshIcon size={20} />}
        title="baum app"
        badge={
          pendingUpdate(view) ? (
            <Badge tone="info" size="compact">
              Update available
            </Badge>
          ) : null
        }
        detail={`Version ${view.version} · ${view.enabled ? checked : "This build does not check for updates"}`}
        actions={
          view.enabled && !actions ? (
            <Button variant="secondary" size="compact" disabled={busy} onClick={updates.check}>
              Check now
            </Button>
          ) : null
        }
      />
      {view.enabled ? (
        <div className="flex flex-wrap items-center justify-between gap-4 border-t border-subtle px-5 py-4">
          <div className="min-w-0 flex-1 text-xs text-secondary">{status}</div>
          {actions ? (
            <div className="flex flex-none flex-wrap items-center gap-2">{actions}</div>
          ) : null}
        </div>
      ) : null}
      {error ? (
        <div className="px-5 pb-4">
          <Alert>{error}</Alert>
        </div>
      ) : null}
    </section>
  );
}
