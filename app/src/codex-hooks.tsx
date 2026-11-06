import { useCallback, useEffect, useRef, useState } from "react";
import { request } from "./api";
import { Notice } from "./shell";
import { useVisibleInterval } from "./visible-interval";

export type CodexHookTrust = {
  state:
    | "trusted"
    | "untrusted"
    | "hooks_disabled"
    | "disabled"
    | "missing"
    | "unknown"
    | "not_installed";
};

export function codexHookWarning(status?: CodexHookTrust | null): string | null {
  switch (status?.state) {
    case "untrusted":
      return "Codex hooks need approval. Open /hooks in Codex and trust baum's hooks so new sessions appear.";
    case "hooks_disabled":
      return "Codex lifecycle hooks are turned off. Run codex features enable hooks, then start a new conversation.";
    case "disabled":
      return "Codex hooks are disabled. Enable baum and its hooks in Codex so new sessions appear.";
    case "missing":
      return "Some baum hooks are missing in Codex. Update the baum plugin, then review /hooks in Codex.";
    case "unknown":
      return "Could not verify Codex hook approval. Open /hooks in Codex to check baum's hooks, then check again.";
    case "trusted":
    case "not_installed":
      return null;
    default:
      return status
        ? "Could not verify Codex hook approval. Open /hooks in Codex to check baum's hooks, then check again."
        : null;
  }
}

/** Rechecks on returning from Codex, after Settings actions, and once a minute.
 * Failed or older endpoints never turn a known warning into a ready state. */
export function useCodexHookTrust() {
  const [status, setStatus] = useState<CodexHookTrust>();
  const [checking, setChecking] = useState(false);
  const alive = useRef(false);
  const pending = useRef(false);
  const read = useCallback(async () => {
    if (pending.current) return;
    pending.current = true;
    try {
      const body = await request<Partial<CodexHookTrust>>("api/agent-hooks/codex");
      if (alive.current && body.state) setStatus(body as CodexHookTrust);
    } catch {
      // Preserve any warning until a successful check replaces it.
    } finally {
      pending.current = false;
    }
  }, []);
  const recheck = useCallback(async () => {
    setChecking(true);
    await read();
    if (alive.current) setChecking(false);
  }, [read]);
  useEffect(() => {
    alive.current = true;
    void read();
    const focus = () => void read();
    window.addEventListener("focus", focus);
    return () => {
      alive.current = false;
      window.removeEventListener("focus", focus);
    };
  }, [read]);
  useVisibleInterval(() => void read(), 60_000);
  return { status, warning: codexHookWarning(status), checking, recheck };
}

/** `open` shows the setting that fixes the warning, when the page has it. */
export function CodexHookWarning({
  check,
  open,
}: {
  check?: ReturnType<typeof useCodexHookTrust>;
  open?: () => void;
}) {
  if (!check?.warning) return null;
  return (
    <Notice
      action={
        <>
          {open ? (
            <button type="button" onClick={open}>
              Open agent hooks
            </button>
          ) : null}
          <button type="button" disabled={check.checking} onClick={() => void check.recheck()}>
            {check.checking ? "Checking…" : "Check again"}
          </button>
        </>
      }
    >
      {check.warning}
    </Notice>
  );
}
