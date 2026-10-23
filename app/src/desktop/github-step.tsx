import { useEffect } from "react";
import type { GitHubConnection } from "../api";
import {
  DEVICE_URL,
  messages,
  terminal,
  useGitHubConnection,
  type GitHubAuthCall,
} from "../github-auth";
import { invoke } from "./native";

/** The setup screen reaches the daemon's GitHub sign-in through the app. */
const viaApp: GitHubAuthCall = (action) =>
  invoke<GitHubConnection>("github_connection", action ? { action } : {});

function GitHubLogo() {
  return (
    <svg className="agent-logo" viewBox="0 0 16 16" aria-hidden="true">
      <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27s1.36.09 2 .27c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8Z" />
    </svg>
  );
}

/** First setup step: sign in to GitHub, so baum can find pull requests. */
export function GitHubStep({ onConnected }: { onConnected: (connected: boolean) => void }) {
  const { connection, pending, error, busy, checking, copied, recheck, action, signIn, copy } =
    useGitHubConnection(viaApp);
  // An explicit credential (environment or config) is in use and wins over a
  // saved account, so it counts as connected.
  const connected = Boolean((connection?.saved || connection?.override) && !connection.error);
  const known = connection !== undefined;
  useEffect(() => {
    if (known) onConnected(connected);
  }, [known, connected, onConnected]);
  const state = connection?.saved
    ? `Signed in as ${connection.login}.`
    : connection?.override
      ? `Using the GitHub credential from ${connection.source}.`
      : pending
        ? "Waiting for you to enter the code on GitHub."
        : connection
          ? "Not connected."
          : "Checking the GitHub connection…";
  return (
    <div id="github-panel">
      <div className="setup-card agent-list">
        <div className="agent-row">
          <GitHubLogo />
          <div className="agent-info">
            <h2>GitHub</h2>
            <p id="github-state" className={connected ? "ok" : undefined} role="status">
              {state}
            </p>
          </div>
          {connection && !pending && (
            <div className="actions">
              <button
                id="github-sign-in"
                type="button"
                disabled={busy || connection.state === "loading"}
                className={`${connected ? "secondary" : ""}${busy ? " loading" : ""}`}
                onClick={signIn}
              >
                {connection.saved ? "Reconnect" : "Sign in with GitHub"}
              </button>
            </div>
          )}
        </div>
        {pending && (
          <div className="github-code-step">
            <div className="github-code">
              <span aria-label="GitHub sign-in code">{pending.user_code}</span>
              <button type="button" className="secondary" onClick={() => void copy()}>
                Copy code
              </button>
            </div>
            {copied && <p className="github-note">{copied}</p>}
            <div className="actions">
              <a
                className="button"
                href={DEVICE_URL}
                target="_blank"
                rel="noreferrer"
                onClick={() => void copy()}
              >
                Open GitHub ↗
              </a>
              <button
                type="button"
                className="quiet"
                disabled={busy}
                onClick={() => void action("cancel")}
              >
                Cancel
              </button>
            </div>
            <p className="github-note">
              The code expires at{" "}
              {new Date(pending.expires_at).toLocaleTimeString([], {
                hour: "2-digit",
                minute: "2-digit",
              })}
              . Grant repository and organization read access.
            </p>
          </div>
        )}
        {(connection?.error || (connection && terminal[connection.state]) || error) && (
          <div className="github-problem" role="alert">
            {connection?.error
              ? (messages[connection.error] ?? "GitHub connection needs attention. Try again.")
              : error || terminal[connection!.state]}
            {(error || connection?.error) && (
              <button
                type="button"
                className="quiet"
                disabled={checking || busy}
                onClick={() => void (error ? recheck() : action("retry"))}
              >
                {checking ? "Checking…" : "Check again"}
              </button>
            )}
          </div>
        )}
      </div>
      <p className="repo-note">
        Pull requests are downloaded to this computer and shared only with your model provider,
        never with baum.
      </p>
    </div>
  );
}
