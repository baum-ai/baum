import { useEffect, useState } from "react";
import { LoadingStatus } from "../nine-dots";
import { invoke } from "./native";

type Connection = { phase: string; message?: string; details?: string[] };

/** The window's first screen. The app reports the connection state through
 *  its `connection` command; this screen only renders it and asks to
 *  reconnect. withGlobalTauri is off so the daemon's pages get no Tauri
 *  globals, which leaves the core IPC bridge Tauri injects into its own
 *  pages. */
export function Connecting() {
  const [connection, setConnection] = useState<Connection>({ phase: "connecting" });
  useEffect(() => {
    let active = true;
    let timer: number | undefined;
    const poll = async () => {
      try {
        const next = await invoke<Connection>("connection");
        if (active) setConnection(next);
      } catch (error) {
        if (active) setConnection({ phase: "failed", message: String(error), details: [] });
      }
      if (active) timer = window.setTimeout(() => void poll(), 500);
    };
    void poll();
    return () => {
      active = false;
      window.clearTimeout(timer);
    };
  }, []);
  const details = connection.details ?? [];
  return (
    <main>
      {connection.phase === "failed" ? (
        <section id="failed">
          <h1>baum is not running</h1>
          <p id="message">{connection.message}</p>
          <button
            id="reconnect"
            type="button"
            onClick={() => {
              setConnection({ phase: "connecting" });
              invoke("reconnect").catch((error) =>
                setConnection({ phase: "failed", message: String(error), details: [] }),
              );
            }}
          >
            Start baum again
          </button>
          {details.length > 0 && (
            <details id="details">
              <summary>Recent daemon log</summary>
              <pre id="log">{details.join("\n")}</pre>
            </details>
          )}
        </section>
      ) : (
        <section id="connecting">
          <LoadingStatus block detail="Connecting to the baum daemon on this computer.">
            Starting baum…
          </LoadingStatus>
        </section>
      )}
    </main>
  );
}
