import { useCallback, useEffect, useRef, useState } from "react";
import { Alert, Button } from "@baum/viewer/components/ui";
import { request, type GitHubConnection } from "./api";

const messages: Record<string, string> = {
  credential_store_unavailable: "Unlock your operating system credential store, then try again.",
  reconnect_required: "GitHub authorization expired or was revoked. Reconnect GitHub.",
  insufficient_scopes:
    "GitHub needs repository and organization read access. Reconnect and grant both permissions.",
  sso_required:
    "Authorize baum for your organization's GitHub SSO, then check the connection again.",
  permission_denied: "Check your repository access and GitHub authorization permissions.",
  auth_request_failed: "GitHub could not be reached. Check your connection and try again.",
};
const terminal: Record<string, string> = {
  denied: "GitHub sign-in was denied. You can try again when you are ready.",
  expired: "This sign-in code expired. Start again for a new code.",
  cancelled: "GitHub sign-in was cancelled.",
};

/** The daemon performs OAuth polling even while this panel is closed. This
 * panel only observes its safe status, never GitHub's token endpoint. */
export function GitHubConnectionSection({ onChanged }: { onChanged?: () => void } = {}) {
  const [connection, setConnection] = useState<GitHubConnection>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState("");
  const sequence = useRef(0);
  const alive = useRef(false);
  const mutating = useRef(false);
  const previous = useRef<string | undefined>(undefined);
  const changed = useRef(onChanged);
  useEffect(() => {
    changed.current = onChanged;
  });
  useEffect(() => {
    if (!connection) return;
    const identity = JSON.stringify([
      connection.saved,
      connection.login,
      connection.source,
      connection.error,
    ]);
    if (previous.current !== undefined && previous.current !== identity) changed.current?.();
    previous.current = identity;
  }, [connection]);
  const read = useCallback(async () => {
    if (mutating.current) return;
    const mine = ++sequence.current;
    try {
      const body = await request<GitHubConnection>("api/github-auth");
      if (alive.current && mine === sequence.current) {
        setConnection(body);
        setError("");
      }
    } catch {
      if (alive.current && mine === sequence.current)
        setError("Could not check the GitHub connection. Try again.");
    }
  }, []);
  useEffect(() => {
    alive.current = true;
    void read();
    return () => {
      alive.current = false;
      sequence.current++;
    };
  }, [read]);
  useEffect(() => {
    const timer = setInterval(
      () => void read(),
      connection?.pending || connection?.state === "loading" ? 1500 : 10000,
    );
    const focus = () => void read();
    addEventListener("focus", focus);
    return () => {
      clearInterval(timer);
      removeEventListener("focus", focus);
    };
  }, [connection?.pending?.id, connection?.state, read]);
  const action = async (name: string) => {
    mutating.current = true;
    const mine = ++sequence.current;
    setBusy(true);
    setCopied("");
    try {
      const body = await request<GitHubConnection>(`api/github-auth/${name}`, {});
      if (alive.current && mine === sequence.current) {
        setConnection(body);
        setError("");
      }
    } catch {
      if (alive.current && mine === sequence.current)
        setError("Could not update the GitHub connection. Check the connection and try again.");
    } finally {
      mutating.current = false;
      if (alive.current) setBusy(false);
    }
  };
  const pending = connection?.pending;
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(pending?.user_code ?? "");
      if (alive.current) setCopied("Code copied.");
    } catch {
      if (alive.current) setCopied("Could not copy. Select and copy the code above.");
    }
  };
  return (
    <section aria-labelledby="github-connection-heading" className="local-panel space-y-3">
      <h2 id="github-connection-heading" className="text-lg">
        GitHub connection
      </h2>
      <p>
        Connect GitHub.com to discover pull requests and refresh reviews. Saved reviews remain
        available when disconnected.
      </p>
      {!connection && !error ? <p role="status">Checking GitHub connection…</p> : null}
      {error ? (
        <Alert>
          {error}{" "}
          <Button disabled={busy} onClick={() => void read()}>
            Check again
          </Button>
        </Alert>
      ) : null}
      {connection ? (
        <>
          <p className="text-xs">
            Host: {connection.host}. Credential source: {connection.source || "none"}.
          </p>
          {connection.override ? (
            <p role="status">
              Using an explicit credential from {connection.source}. It takes precedence over
              desktop sign-in. Remove that override from your environment or configuration to use
              the saved account.
            </p>
          ) : null}
          {connection.saved ? (
            <p role="status">
              Saved GitHub account: <strong>{connection.login}</strong>
              {connection.override ? " (not currently used)." : "."}
            </p>
          ) : !pending ? (
            <p>No GitHub account saved in baum.</p>
          ) : null}
          {connection.error ? (
            <Alert>
              {messages[connection.error] ?? "GitHub connection needs attention. Try again."}
            </Alert>
          ) : null}
          {terminal[connection.state] ? <p role="status">{terminal[connection.state]}</p> : null}
          {pending ? (
            <>
              <p>Enter this code on GitHub to authorize baum:</p>
              <p
                className="text-2xl font-mono select-all break-all"
                aria-label="GitHub sign-in code"
              >
                {pending.user_code}
              </p>
              <div className="flex flex-wrap items-center gap-3">
                <Button variant="secondary" onClick={() => void copy()}>
                  Copy code
                </Button>
                <a href="https://github.com/login/device" target="_blank" rel="noreferrer">
                  Open GitHub device sign-in
                </a>
                <Button variant="secondary" disabled={busy} onClick={() => void action("cancel")}>
                  Cancel sign-in
                </Button>
              </div>
              {copied ? <p role="status">{copied}</p> : null}
              <p role="status">
                Waiting for GitHub authorization… You can close this window and return later.
              </p>
              <p className="text-xs">
                Code expires {new Date(pending.expires_at).toLocaleTimeString()}. Grant repository
                and organization read access. Credentials are saved in your operating system
                credential store.
              </p>
            </>
          ) : (
            <div className="flex flex-wrap gap-3">
              <Button
                disabled={busy || connection.state === "loading"}
                onClick={() => void action("start")}
              >
                {busy
                  ? "Working…"
                  : connection.saved || connection.error === "reconnect_required"
                    ? "Reconnect GitHub"
                    : "Sign in with GitHub"}
              </Button>
              <Button variant="secondary" disabled={busy} onClick={() => void action("retry")}>
                Check connection again
              </Button>
              {connection.saved ? (
                <Button
                  variant="secondary"
                  disabled={busy}
                  onClick={() => void action("disconnect")}
                >
                  Disconnect saved account
                </Button>
              ) : null}
            </div>
          )}
          {connection.saved ? (
            <p className="text-xs">
              Disconnect removes baum's saved credential from this computer.{" "}
              {connection.override ? "The explicit credential remains active. " : ""}It does not
              revoke access on GitHub.
            </p>
          ) : null}
        </>
      ) : null}
    </section>
  );
}
