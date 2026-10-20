import { useEffect, useState } from "react";
import { formatRelative } from "@baum/viewer/components/format";
import { CircleDashedIcon } from "@baum/viewer/components/icons";
import { LiveDot } from "@baum/viewer/components/ui";
import { agentName, type LiveSession } from "./api";

/** Re-renders now and then so relative times do not freeze. */
function useNow(interval = 30_000) {
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

/** The agent sessions in progress on a pull request's branch. Once one ends the
 * page reads its full session from disk, as for any finished session. */
export function LiveSessions({ sessions }: { sessions: LiveSession[] }) {
  const now = useNow();
  if (sessions.length === 0) return null;
  return (
    <section aria-label="Sessions in progress" className="mb-3 local-panel space-y-2">
      <p>
        <strong>Sessions in progress</strong>
      </p>
      <ul className="space-y-1">
        {sessions.map((s) => (
          <li
            key={`${s.agent}:${s.session_id}`}
            className="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-(--text-tertiary)"
            title={s.session_id}
          >
            {s.state === "running" ? (
              <LiveDot className="text-(--status-info)" />
            ) : (
              <CircleDashedIcon size={12} className="flex-none" />
            )}
            <span className="font-semibold text-(--text-primary)">{agentName(s.agent)}</span>
            <span>{s.state === "running" ? "Working" : "Waiting for you"}</span>
            {s.branch ? <span className="font-mono">{s.branch}</span> : null}
            <span title={s.started_at}>Started {formatRelative(s.started_at, now)}</span>
            <span title={s.last_activity_at}>
              Last activity {formatRelative(s.last_activity_at, now)}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}
