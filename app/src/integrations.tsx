import { useEffect, useState } from "react";
import { ExternalLinkIcon } from "@baum/viewer/components/icons";
import { Alert, Badge, Button } from "@baum/viewer/components/ui";
import { request, type PR } from "./api";
import { openExternal } from "./outside-links";
import { PanelHeader } from "./shell";

type Connection = {
  id: string;
  name: string;
  state: "disconnected" | "connected" | "pending" | "reconnect" | "unconfigured" | "unavailable";
  account?: string;
  message?: string;
};
type Issue = { key: string; title: string; state: string; url: string };
const labels = {
  connected: "Connected",
  disconnected: "Disconnected",
  pending: "Awaiting sign-in",
  reconnect: "Reconnect required",
  unconfigured: "Setup required",
  unavailable: "Unavailable",
};

export function IntegrationConnections() {
  const [connections, setConnections] = useState<Connection[]>([]);
  const [error, setError] = useState("");
  const [readFailed, setReadFailed] = useState(false);
  const [browserLink, setBrowserLink] = useState<{ id: string; url: string } | null>(null);
  const [busy, setBusy] = useState("");
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    const abort = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    async function read() {
      try {
        const rows = await request<Connection[]>(
          "api/integrations",
          undefined,
          "GET",
          abort.signal,
        );
        if (abort.signal.aborted || !Array.isArray(rows)) return;
        setConnections(rows);
        if (rows.some((row) => row.state === "pending")) timer = setTimeout(read, 1500);
      } catch {
        if (!abort.signal.aborted) {
          setReadFailed(true);
          setError("Could not read integration connections.");
        }
      }
    }
    void read();
    return () => {
      abort.abort();
      clearTimeout(timer);
    };
  }, [revision]);
  async function change(id: string, action: "connect" | "disconnect") {
    setBusy(id);
    setReadFailed(false);
    setBrowserLink(null);
    setError("");
    try {
      const result = await request<{ url: string }>(`api/integrations/${id}/${action}`, {});
      if (action === "connect") {
        setBrowserLink({ id, url: result.url });
        openExternal(result.url);
      }
    } catch {
      setError("Could not change the connection. Try again.");
    } finally {
      setBusy("");
      setRevision((n) => n + 1);
    }
  }
  return (
    <>
      {error && (
        <Alert>
          {error}{" "}
          {readFailed && (
            <Button
              size="compact"
              onClick={() => {
                setError("");
                setRevision((n) => n + 1);
              }}
            >
              Retry
            </Button>
          )}
        </Alert>
      )}
      {connections.map((c) => (
        <section key={c.id} aria-labelledby={`connection-${c.id}`} className="local-panel p-0!">
          <PanelHeader
            id={`connection-${c.id}`}
            icon={<ExternalLinkIcon />}
            title={c.name}
            badge={
              <Badge tone={c.state === "connected" ? "positive" : "neutral"}>
                {labels[c.state]}
              </Badge>
            }
            detail={
              c.message ||
              c.account ||
              (c.state === "pending" ? "Finish signing in in your browser." : undefined)
            }
            actions={
              <>
                {c.state === "pending" && browserLink?.id === c.id && (
                  <a href={browserLink.url} target="_blank" rel="noreferrer">
                    Open sign-in page
                  </a>
                )}
                {c.state !== "unconfigured" && c.state !== "pending" && (
                  <Button
                    disabled={Boolean(busy)}
                    size="compact"
                    onClick={() => void change(c.id, "connect")}
                  >
                    {c.account ? "Reconnect" : "Connect"}
                  </Button>
                )}
                {(c.account || c.state === "pending") && (
                  <Button
                    disabled={Boolean(busy)}
                    variant="secondary"
                    size="compact"
                    onClick={() => void change(c.id, "disconnect")}
                  >
                    Disconnect
                  </Button>
                )}
              </>
            }
          />
        </section>
      ))}
    </>
  );
}

function useLinkedIssues({ repository, number, updated_at }: PR) {
  const [issues, setIssues] = useState<Issue[]>([]);
  useEffect(() => {
    const abort = new AbortController();
    setIssues([]);
    async function read() {
      const rows = await request<Connection[]>("api/integrations", undefined, "GET", abort.signal);
      if (!Array.isArray(rows) || !rows.some((row) => row.state === "connected")) return;
      const pr = await request<{ body: string }>(
        `api/prs/description?repository=${encodeURIComponent(repository)}&pr=${number}`,
        undefined,
        "GET",
        abort.signal,
      );
      if (!pr.body) return;
      const result = await request<Issue[]>(
        "api/integrations/issues",
        { description: pr.body },
        "POST",
        abort.signal,
      );
      if (!abort.signal.aborted && Array.isArray(result)) setIssues(result);
    }
    void read().catch(() => {
      /* Linked issues are optional when a provider is offline. */
    });
    return () => abort.abort();
  }, [repository, number, updated_at]);
  return issues;
}

export function useIssueLinks(pr: PR) {
  const issues = useLinkedIssues(pr);
  return issues.length
    ? issues.map((issue) => (
        <a
          key={issue.url}
          href={issue.url}
          target="_blank"
          rel="noreferrer"
          className="local-group-link"
        >
          <strong>{issue.key}</strong> {issue.title} · {issue.state}
        </a>
      ))
    : null;
}
export function PullRequestIssueLinks({ pr }: { pr: PR }) {
  return useIssueLinks(pr);
}
