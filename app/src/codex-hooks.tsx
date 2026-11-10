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

const POLL_MS = 60_000;

/** Reads on load, on window focus (at most once a minute) and, only while the
 * last answer is a warning, once a minute while visible. `recheck` asks the
 * daemon for a fresh read: it caches answers, and callers use it after acting
 * on the warning. Failed or older endpoints never turn a known warning into a
 * ready state. */
export function useCodexHookTrust() {
  const [status, setStatus] = useState<CodexHookTrust>();
  const [checking, setChecking] = useState(false);
  const alive = useRef(false);
  const pending = useRef(false);
  const lastRead = useRef(0);
  const last = useRef("");
  const read = useCallback(async (fresh = false) => {
    if (pending.current) return;
    pending.current = true;
    lastRead.current = Date.now();
    try {
      const body = await request<Partial<CodexHookTrust>>(
        fresh ? "api/agent-hooks/codex?refresh=1" : "api/agent-hooks/codex",
      );
      const key = JSON.stringify(body);
      if (alive.current && body.state && key !== last.current) {
        last.current = key;
        setStatus(body as CodexHookTrust);
      }
    } catch {
      // Preserve any warning until a successful check replaces it.
    } finally {
      pending.current = false;
    }
  }, []);
  const recheck = useCallback(async () => {
    setChecking(true);
    await read(true);
    if (alive.current) setChecking(false);
  }, [read]);
  useEffect(() => {
    alive.current = true;
    void read();
    const focus = () => {
      if (Date.now() - lastRead.current >= POLL_MS) void read();
    };
    window.addEventListener("focus", focus);
    return () => {
      alive.current = false;
      window.removeEventListener("focus", focus);
    };
  }, [read]);
  const warning = codexHookWarning(status);
  useVisibleInterval(() => void read(), warning ? POLL_MS : null);
  return { status, warning, checking, recheck };
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
