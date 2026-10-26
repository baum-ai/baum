import type { ReactNode } from "react";
import {
  ActionLink,
  Alert,
  Badge,
  Button,
  LiveDot,
  type BadgeTone,
} from "@baum/viewer/components/ui";
import { ExternalLinkIcon, GitHubIcon } from "@baum/viewer/components/icons";
import type { GitHubConnection } from "./api";
import {
  DEVICE_URL,
  daemonGitHubAuth,
  messages,
  terminal,
  useGitHubConnection,
} from "./github-auth";
import { PanelHeader } from "./shell";
import { NineDots } from "./nine-dots";

export function GitHubConnectionSection({ onChanged }: { onChanged?: () => void } = {}) {
  const {
    connection,
    pending,
    error,
    busy,
    working,
    checking,
    copied,
    recheck,
    action,
    signIn,
    copy,
  } = useGitHubConnection(daemonGitHubAuth, onChanged);
  const status = connection ? describe(connection) : null;
  const idle = connection && !pending;
  const notes =
    connection &&
    (connection.override || connection.error || terminal[connection.state] || pending);
  return (
    <section aria-labelledby="github-connection-heading" className="local-panel p-0!">
      <PanelHeader
        id="github-connection-heading"
        icon={<GitHubIcon size={20} />}
        title="GitHub connection"
        badge={status ? <Badge tone={status.tone}>{status.label}</Badge> : null}
        detail={
          connection?.saved ? (
            <>
              Signed in as <strong className="text-(--text-primary)">{connection.login}</strong> on{" "}
              {connection.host}
              {connection.override ? " (not currently used)" : ""}.
            </>
          ) : pending ? (
            "Enter the code below on GitHub to finish signing in."
          ) : (
            "Connect GitHub.com to discover pull requests and refresh reviews."
          )
        }
        actions={
          idle ? (
            <>
              <Button
                variant="ghost"
                size="compact"
                disabled={busy}
                onClick={() => void action("retry")}
              >
                {working === "retry" ? "Checking…" : "Check connection"}
              </Button>
              <Button
                variant={connection.saved ? "secondary" : "primary"}
                size="compact"
                className="gap-2"
                disabled={busy || connection.state === "loading"}
                onClick={signIn}
              >
                {working === "start" ? (
                  "Working…"
                ) : connection.saved || connection.error === "reconnect_required" ? (
                  "Reconnect"
                ) : (
                  <>
                    <GitHubIcon />
                    Sign in with GitHub
                  </>
                )}
              </Button>
            </>
          ) : null
        }
      />
      {!connection || error || notes ? (
        <div className="space-y-4 border-t border-(--border-subtle) p-5">
          {!connection && !error ? (
            <p role="status" className="flex items-center gap-2 text-(--text-secondary)">
              <NineDots /> Checking GitHub connection…
            </p>
          ) : null}
          {error ? (
            <Alert className="flex flex-wrap items-center justify-between gap-3">
              <span>{error}</span>
              <Button
                variant="secondary"
                size="compact"
                disabled={busy || checking}
                onClick={() => void recheck()}
              >
                {checking ? (
                  <span className="inline-flex items-center gap-2">
                    <NineDots /> Checking…
                  </span>
                ) : (
                  "Check again"
                )}
              </Button>
            </Alert>
          ) : null}
          {connection?.override ? (
            <Callout>
              Using an explicit credential from {connection.source}. It takes precedence over
              desktop sign-in. Remove that override from your environment or configuration to use
              the saved account.
            </Callout>
          ) : null}
          {connection?.error ? (
            <Alert>
              {messages[connection.error] ?? "GitHub connection needs attention. Try again."}
            </Alert>
          ) : null}
          {connection && terminal[connection.state] ? (
            <p role="status" className="text-(--text-secondary)">
              {terminal[connection.state]}
            </p>
          ) : null}
          {pending ? (
            <div className="space-y-4">
              <div className="rounded-(--radius-md) border border-(--border-default) bg-(--surface-wash) px-4 py-4">
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <p
                    className="font-mono text-2xl font-semibold tracking-[0.2em] select-all break-all"
                    aria-label="GitHub sign-in code"
                  >
                    {pending.user_code}
                  </p>
                  <Button variant="secondary" size="compact" onClick={() => void copy()}>
                    Copy code
                  </Button>
                </div>
                <p
                  role="status"
                  className="mt-3 flex items-center gap-2 text-xs text-(--text-secondary)"
                >
                  <LiveDot className="text-(--status-positive)" />
                  {copied || "Waiting for you to enter this code on GitHub."}
                </p>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <ActionLink
                  href={DEVICE_URL}
                  target="_blank"
                  rel="noreferrer"
                  size="compact"
                  className="gap-2"
                  onClick={() => void copy()}
                >
                  Open GitHub <ExternalLinkIcon size={14} />
                </ActionLink>
                <Button
                  variant="ghost"
                  size="compact"
                  disabled={busy}
                  onClick={() => void action("cancel")}
                >
                  {working === "cancel" ? "Cancelling…" : "Cancel sign-in"}
                </Button>
              </div>
              <p className="text-xs text-(--text-tertiary)">
                The code expires at{" "}
                {new Date(pending.expires_at).toLocaleTimeString([], {
                  hour: "2-digit",
                  minute: "2-digit",
                })}
                . Grant repository and organization read access.
              </p>
            </div>
          ) : null}
        </div>
      ) : null}
      {connection ? (
        <footer className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 border-t border-(--border-subtle) px-5 py-3 text-xs text-(--text-tertiary)">
          <div className="space-y-1">
            <p>Data is downloaded to this computer and never shared with baum.</p>
            {connection.saved ? (
              <p>
                Credential: {connection.source || "none"}.{" "}
                {connection.override
                  ? "The explicit credential stays active if you disconnect. "
                  : ""}
                Disconnecting does not revoke access on GitHub.
              </p>
            ) : null}
          </div>
          {connection.saved && !pending ? (
            <button
              type="button"
              disabled={busy}
              onClick={() => void action("disconnect")}
              className="cursor-pointer text-(--status-critical) hover:underline disabled:cursor-not-allowed disabled:opacity-50"
            >
              {working === "disconnect" ? "Disconnecting…" : "Disconnect"}
            </button>
          ) : null}
        </footer>
      ) : null}
    </section>
  );
}

function Callout({ children }: { children: ReactNode }) {
  return (
    <p
      role="status"
      className="rounded-(--radius-sm) bg-(--status-info-bg) px-3 py-2 text-(--status-info)"
    >
      {children}
    </p>
  );
}

function describe(connection: GitHubConnection): { label: string; tone: BadgeTone } {
  if (connection.pending) return { label: "Waiting for GitHub", tone: "caution" };
  if (connection.error) return { label: "Needs attention", tone: "critical" };
  if (connection.override) return { label: "Explicit credential", tone: "info" };
  if (connection.saved) return { label: "Connected", tone: "positive" };
  return { label: "Not connected", tone: "neutral" };
}
