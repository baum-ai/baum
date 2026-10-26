import type { ComponentType } from "react";
import { formatRelative } from "@baum/viewer/components/format";
import { CircleDashedIcon } from "@baum/viewer/components/icons";
import { LiveDot } from "@baum/viewer/components/ui";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import { agentName, type LiveSession } from "./api";
import claudeLogo from "./desktop/claude-code.svg";
import codexLogo from "./desktop/codex.svg";
import cursorLogo from "./desktop/cursor.svg";
import { useNow } from "./live";

const agentLogos: Record<string, string> = {
  "claude-code": claudeLogo,
  codex: codexLogo,
  cursor: cursorLogo,
};

/** How long a session ran, in whole units a person reads at a glance:
 *  "40 s", "25 min", "1 h 5 min". */
export function lasted(seconds: number): string {
  if (seconds < 60) return `${Math.max(0, Math.round(seconds))} s`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} min`;
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return rest ? `${hours} h ${rest} min` : `${hours} h`;
}

/** The tooltip of a relative time: when it was, in the viewer's locale. */
const absolute = (iso: string) => new Date(iso).toLocaleString();

function Agent({ agent }: { agent: string }) {
  const logo = agentLogos[agent];
  return (
    <span className="flex min-w-0 items-center gap-2 font-medium text-(--text-primary)">
      {logo ? <img src={logo} alt="" width="18" height="18" className="shrink-0" /> : null}
      {agentName(agent)}
    </span>
  );
}

/** The sessions page: what agents are doing on this computer now, and what
 * ended in the last day. `prHref` is where a session's pull request opens in
 * the app, or null when it is not one the app knows. */
export function SessionsPage({
  sessions,
  prHref,
  Link,
}: {
  sessions: LiveSession[];
  prHref: (session: LiveSession) => string | null;
  Link: ComponentType<ViewerLinkProps>;
}) {
  const now = useNow();
  const active = sessions.filter((s) => s.state !== "ended");
  const endedAtOf = (s: LiveSession) => s.ended_at ?? s.last_activity_at;
  const ended = sessions
    .filter((s) => s.state === "ended")
    .sort((a, b) => endedAtOf(b).localeCompare(endedAtOf(a)));
  // Where the session works: the repository, its branch as a chip and the
  // pull request it is on, linked into the app when the inbox knows it.
  const where = (s: LiveSession) => {
    const href = s.pr ? prHref(s) : null;
    return (
      <div className="local-session-where">
        <span className="local-session-repo">{s.repository}</span>
        {s.branch ? <code className="local-session-branch">{s.branch}</code> : null}
        {s.pr ? (
          href ? (
            <Link href={href} className="local-session-pr">
              PR #{s.pr}
            </Link>
          ) : (
            <span className="local-session-pr">PR #{s.pr}</span>
          )
        ) : null}
      </div>
    );
  };
  return (
    <div className="local-stack">
      <section aria-label="Running now" className="local-panel local-sessions">
        <h2>Running now</h2>
        {active.length === 0 ? (
          <p className="local-sessions-empty">
            No agent sessions are running. Sessions of connected agents in your repositories show
            here while they work.
          </p>
        ) : (
          <ul>
            {active.map((s) => (
              <li
                key={`${s.agent}:${s.session_id}`}
                className="local-session-row"
                title={s.session_id}
              >
                <div className="local-session-agent">
                  <Agent agent={s.agent} />
                  <span className="local-session-state">
                    {s.state === "running" ? (
                      <>
                        <LiveDot className="text-(--status-positive)" /> Working
                      </>
                    ) : (
                      <>
                        <CircleDashedIcon size={12} className="flex-none text-(--status-caution)" />{" "}
                        Waiting for you
                      </>
                    )}
                  </span>
                </div>
                {where(s)}
                <div className="local-session-times">
                  <span title={absolute(s.last_activity_at)}>
                    Active {formatRelative(s.last_activity_at, now)}
                  </span>
                  <span title={absolute(s.started_at)}>
                    Started {formatRelative(s.started_at, now)}
                  </span>
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>
      <section aria-label="Ended recently" className="local-panel local-sessions">
        <h2>Ended recently</h2>
        {ended.length === 0 ? (
          <p className="local-sessions-empty">No sessions ended in the last day.</p>
        ) : (
          <ul>
            {ended.map((s) => {
              const endedAt = endedAtOf(s);
              const seconds = Math.max(
                0,
                (new Date(endedAt).getTime() - new Date(s.started_at).getTime()) / 1000,
              );
              return (
                <li
                  key={`${s.agent}:${s.session_id}`}
                  className="local-session-row"
                  title={s.session_id}
                >
                  <div className="local-session-agent">
                    <Agent agent={s.agent} />
                    <span className="local-session-state">Ended</span>
                  </div>
                  {where(s)}
                  <div className="local-session-times">
                    <span title={absolute(endedAt)}>Ended {formatRelative(endedAt, now)}</span>
                    <span>Lasted {lasted(seconds)}</span>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </section>
    </div>
  );
}
