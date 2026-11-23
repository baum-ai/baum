import { useEffect, useState } from "react";
import { CircleDashedIcon } from "@baum/viewer/components/icons";
import { LiveDot } from "@baum/viewer/components/ui";
import type { LiveSession } from "./api";
import { subscribeVisibleInterval } from "./visible-interval";

/** Re-renders now and then so relative times do not freeze, while the page is
 *  visible. */
export function useNow(interval = 30_000) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => subscribeVisibleInterval(() => setNow(Date.now()), interval), [interval]);
  return now;
}

const MAX_TIMEOUT = 2 ** 31 - 1;

/** The time, advanced only when one of `deadlines` (epoch ms) passes, so a
 *  component holding it renders when something is due rather than on a
 *  steady tick; `atMidnight` adds each local midnight. Nothing runs while no
 *  deadline is ahead or the window is hidden; it catches up once shown. */
export function useDeadlineNow(deadlines: number[], atMidnight = false) {
  const [now, setNow] = useState(() => Date.now());
  const ahead = [...deadlines, ...(atMidnight ? [nextMidnight(now)] : [])].filter(
    (time) => time > now,
  );
  const next = ahead.length ? Math.min(...ahead) : null;
  useEffect(() => {
    if (next === null) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const fire = () => setNow(Math.max(Date.now(), next));
    // A hidden window sets no timer: the change waits until it is shown again.
    const arm = () => {
      clearTimeout(timer);
      timer = undefined;
      if (document.hidden) return;
      const wait = next - Date.now();
      if (wait <= 0) fire();
      else timer = setTimeout(arm, Math.min(wait, MAX_TIMEOUT));
    };
    arm();
    document.addEventListener("visibilitychange", arm);
    return () => {
      document.removeEventListener("visibilitychange", arm);
      clearTimeout(timer);
    };
  }, [next]);
  return now;
}

/** The next local midnight after `time`. */
export function nextMidnight(time: number) {
  const date = new Date(time);
  date.setHours(24, 0, 0, 0);
  return date.getTime();
}

/** A compact inbox marker for a pull request with agent sessions in progress. */
export function LiveBadge({ sessions }: { sessions: LiveSession[] }) {
  if (sessions.length === 0) return null;
  const running = sessions.filter((s) => s.state === "running").length;
  const idle = sessions.length - running;
  const label = [running ? `${running} running` : "", idle ? `${idle} idle` : ""]
    .filter(Boolean)
    .join(", ");
  return (
    <span
      className="inline-flex flex-none items-center gap-1.5 text-xs text-secondary"
      title={`Agent sessions in progress: ${label}`}
    >
      {running ? (
        <LiveDot className="text-status-info" />
      ) : (
        <CircleDashedIcon size={12} className="flex-none text-tertiary" />
      )}
      <span className="sr-only">Agent sessions in progress: </span>
      {sessions.length === 1
        ? running
          ? "Agent running"
          : "Agent idle"
        : `${sessions.length} agents · ${label}`}
    </span>
  );
}
