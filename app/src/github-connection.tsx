import { useRef, type ReactNode } from "react";
import {
  ActionLink,
  Alert,
  Badge,
  Button,
  LiveDot,
  type BadgeTone,
} from "@baum/viewer/components/ui";
import { ChevronDownIcon, ExternalLinkIcon, GitHubIcon } from "@baum/viewer/components/icons";
import type { GitHubConnection } from "./api";
import {
  ACCESS_URL,
  DEVICE_URL,
  daemonGitHubAuth,
  messages,
  terminal,
  useGitHubConnection,
} from "./github-auth";
import { PanelHeader } from "./shell";
import { useContextMenu } from "./context-menu";
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
  // Workflow access is opt-in, so a saved connection without it says what
  // that means. An explicit credential's scopes are not known here.
  const noWorkflow = Boolean(
    connection?.saved && !connection.override && !connection.error && !connection.workflow,
  );
  const notes =
    connection &&
    (connection.override ||
      connection.error ||
      terminal[connection.state] ||
      pending ||
      noWorkflow);
  const reconnect = connection?.saved || connection?.error === "reconnect_required";
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
                variant="secondary"
                size="compact"
                disabled={busy}
                onClick={() => void action("retry")}
              >
                {working === "retry" ? "Checking…" : "Check connection"}
              </Button>
              {reconnect ? (
                <ReconnectButton
                  primary={!connection.saved}
                  workflow={Boolean(connection.workflow)}
                  working={working === "start"}
                  disabled={busy || connection.state === "loading"}
                  signIn={signIn}
                />
              ) : (
                <Button
                  variant="primary"
                  size="compact"
                  className="gap-2"
                  disabled={busy || connection.state === "loading"}
                  onClick={() => signIn()}
                >
                  {working === "start" ? (
                    "Working…"
                  ) : (
                    <>
                      <GitHubIcon />
                      Sign in with GitHub
                    </>
                  )}
                </Button>
              )}
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
              {connection.error === "sso_required" ? (
                <>
                  {" "}
                  <a href={ACCESS_URL} target="_blank" rel="noreferrer" className="underline">
                    Authorize baum on GitHub ↗
                  </a>
                </>
              ) : null}
            </Alert>
          ) : null}
          {noWorkflow && !pending ? (
            <Callout tone="caution">
              baum&rsquo;s GitHub connection does not have workflow access, so it cannot merge pull
              requests that change GitHub Actions workflows. Merge those on GitHub, or choose{" "}
              <strong>Reconnect with workflow access</strong> from the Reconnect menu.
            </Callout>
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
                  variant="secondary"
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
                .{" "}
                {pending.workflow
                  ? "Grant repository, organization read and workflow access."
                  : "Grant repository and organization read access."}
              </p>
            </div>
          ) : null}
        </div>
      ) : null}
      {connection ? (
        <footer className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 border-t border-(--border-subtle) px-5 py-3 text-xs text-(--text-tertiary)">
          <div className="space-y-1">
            <p>Data is stored on this computer and never shared with baum.</p>
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
            <Button
              variant="danger-outline"
              size="compact"
              disabled={busy}
              onClick={() => void action("disconnect")}
            >
              {working === "disconnect" ? "Disconnecting…" : "Disconnect"}
            </Button>
          ) : null}
        </footer>
      ) : null}
    </section>
  );
}

function Callout({ children, tone = "info" }: { children: ReactNode; tone?: "info" | "caution" }) {
  return (
    <p
      role="status"
      className={
        tone === "caution"
          ? "rounded-(--radius-sm) bg-(--status-caution-bg) px-3 py-2 text-(--status-caution)"
          : "rounded-(--radius-sm) bg-(--status-info-bg) px-3 py-2 text-(--status-info)"
      }
    >
      {children}
    </p>
  );
}

/** Reconnect keeps the connection's current access; its menu offers the other
 *  choice, so workflow access is granted (or dropped) only on purpose. */
function ReconnectButton({
  primary,
  workflow,
  working,
  disabled,
  signIn,
}: {
  primary: boolean;
  workflow: boolean;
  working: boolean;
  disabled: boolean;
  signIn: (options?: { workflow?: boolean }) => void;
}) {
  const menu = useContextMenu();
  const ref = useRef<HTMLButtonElement>(null);
  const open = menu.anchor !== null && menu.anchor === ref.current;
  const items = [
    { label: "Reconnect", onSelect: () => signIn({ workflow }) },
    workflow
      ? { label: "Reconnect without workflow access", onSelect: () => signIn({ workflow: false }) }
      : { label: "Reconnect with workflow access", onSelect: () => signIn({ workflow: true }) },
  ];
  return (
    <span className="inline-flex">
      <Button
        variant={primary ? "primary" : "secondary"}
        size="compact"
        className="rounded-r-none!"
        disabled={disabled}
        onClick={() => signIn({ workflow })}
      >
        {working ? "Working…" : "Reconnect"}
      </Button>
      <Button
        ref={ref}
        variant={primary ? "primary" : "secondary"}
        size="compact"
        className="-ml-px rounded-l-none! px-2!"
        aria-label="More reconnect options"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menu.id : undefined}
        disabled={disabled}
        onClick={(event) => {
          // A click on the open menu's own button closes it, as the row menus do.
          const button = event.currentTarget;
          if (button.dataset.closing !== undefined) delete button.dataset.closing;
          else menu.openFrom(button, items, "Reconnect options");
        }}
        onPointerLeave={(event) => delete event.currentTarget.dataset.closing}
      >
        <ChevronDownIcon size={14} />
      </Button>
      {menu.menu}
    </span>
  );
}

function describe(connection: GitHubConnection): { label: string; tone: BadgeTone } {
  if (connection.pending) return { label: "Waiting for GitHub", tone: "caution" };
  if (connection.error) return { label: "Needs attention", tone: "critical" };
  if (connection.override) return { label: "Explicit credential", tone: "info" };
  if (connection.saved) return { label: "Connected", tone: "positive" };
  return { label: "Not connected", tone: "neutral" };
}
