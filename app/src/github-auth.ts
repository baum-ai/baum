import { useCallback, useEffect, useRef, useState } from "react";
import { request, type GitHubConnection } from "./api";

export const messages: Record<string, string> = {
  credential_store_unavailable: "Unlock your operating system credential store, then try again.",
  reconnect_required: "GitHub authorization expired or was revoked. Reconnect GitHub.",
  insufficient_scopes:
    "GitHub needs repository and organization read access. Reconnect and grant both permissions.",
  sso_required:
    "Authorize baum for your organization's GitHub SSO, then check the connection again.",
  permission_denied: "Check your repository access and GitHub authorization permissions.",
  auth_request_failed: "GitHub could not be reached. Check your connection and try again.",
};
export const terminal: Record<string, string> = {
  denied: "GitHub sign-in was denied. You can try again when you are ready.",
  expired: "This sign-in code expired. Start again for a new code.",
  cancelled: "GitHub sign-in was cancelled.",
};
// Fixed rather than the daemon's verification_uri: the window only ever
// sends people to GitHub's own device page.
export const DEVICE_URL = "https://github.com/login/device";

export type GitHubAuthAction = "start" | "cancel" | "retry" | "disconnect";
/** Reads the daemon's GitHub connection, or changes it with an action. */
export type GitHubAuthCall = (action?: GitHubAuthAction) => Promise<GitHubConnection>;

/** The daemon's own API, as the app pages reach it. */
export const daemonGitHubAuth: GitHubAuthCall = (action) =>
  action
    ? request<GitHubConnection>(`api/github-auth/${action}`, {})
    : request<GitHubConnection>("api/github-auth");

/** The daemon performs OAuth polling even while no page shows it. This hook
 * only observes its safe status, never GitHub's token endpoint. */
export function useGitHubConnection(call: GitHubAuthCall, onChanged?: () => void) {
  const [connection, setConnection] = useState<GitHubConnection>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [working, setWorking] = useState<GitHubAuthAction | null>(null);
  const [checking, setChecking] = useState(false);
  const [copied, setCopied] = useState("");
  const sequence = useRef(0);
  const alive = useRef(false);
  const mutating = useRef(false);
  const previous = useRef<string | undefined>(undefined);
  const transport = useRef(call);
  const changed = useRef(onChanged);
  useEffect(() => {
    transport.current = call;
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
      const body = await transport.current();
      if (alive.current && mine === sequence.current) {
        setConnection(body);
        setError("");
      }
    } catch (e) {
      if (alive.current && mine === sequence.current)
        setError(`Could not check the GitHub connection. ${failure(e)}`);
    }
  }, []);
  // The retry the person asked for shows its progress; background polls stay quiet.
  const recheck = async () => {
    setChecking(true);
    await read();
    if (alive.current) setChecking(false);
  };
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
      // Until the first answer, the daemon may still be starting.
      !connection || connection.pending || connection.state === "loading" ? 1500 : 10000,
    );
    const focus = () => void read();
    addEventListener("focus", focus);
    return () => {
      clearInterval(timer);
      removeEventListener("focus", focus);
    };
  }, [connection === undefined, connection?.pending?.id, connection?.state, read]);
  const action = async (name: GitHubAuthAction) => {
    mutating.current = true;
    const mine = ++sequence.current;
    setBusy(true);
    setWorking(name);
    setCopied("");
    try {
      const body = await transport.current(name);
      if (alive.current && mine === sequence.current) {
        setConnection(body);
        setError("");
      }
      return body;
    } catch (e) {
      if (alive.current && mine === sequence.current)
        setError(`Could not update the GitHub connection. ${failure(e)}`);
      return undefined;
    } finally {
      mutating.current = false;
      if (alive.current) {
        setBusy(false);
        setWorking(null);
      }
    }
  };
  // Opens GitHub and copies the code the daemon is about to return, both
  // inside the click: once the request returns, the click no longer counts
  // and the app window has lost focus to the browser, so a plain copy or open
  // then would be refused. The clipboard accepts a promise for the text.
  const signIn = () => {
    open(DEVICE_URL, "_blank", "noreferrer");
    const started = action("start");
    if (typeof ClipboardItem === "undefined" || !navigator.clipboard?.write) return;
    const code = started.then((body) => {
      if (!body?.pending) throw Error("no sign-in code");
      return body.pending.user_code;
    });
    code.catch(() => {});
    navigator.clipboard
      .write([new ClipboardItem({ "text/plain": code })])
      .then(() => {
        if (alive.current) setCopied("Code copied. Paste it on GitHub.");
      })
      .catch(() => {});
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
  return {
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
  };
}

/** The daemon's own reason, so a repeated failure says why it keeps failing. */
function failure(e: unknown) {
  const reason = (e instanceof Error ? e.message : String(e)).trim().replace(/\.$/, "");
  return reason ? `${reason}.` : "Try again.";
}
