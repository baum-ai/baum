import { useEffect, useState } from "react";
import { CircleDashedIcon } from "@baum/viewer/components/icons";
import { LiveDot } from "@baum/viewer/components/ui";
import type { LiveSession } from "./api";

/** Re-renders now and then so relative times do not freeze. */
export function useNow(interval = 30_000) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), interval);
    return () => clearInterval(timer);
  }, [interval]);
  return now;
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
      className="inline-flex flex-none items-center gap-1.5 text-xs text-(--text-secondary)"
      title={`Agent sessions in progress: ${label}`}
    >
      {running ? (
        <LiveDot className="text-(--status-info)" />
      ) : (
        <CircleDashedIcon size={12} className="flex-none text-(--text-tertiary)" />
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
