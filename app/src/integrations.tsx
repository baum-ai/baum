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
  resolves_issues?: boolean;
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

// Linear's monochrome logomark from https://linear.app/brand.
function LinearIcon({ size }: { size: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 100 100"
      fill="currentColor"
      aria-hidden="true"
      className="shrink-0"
    >
      <path d="M1.22541 61.5228c-.2225-.9485.90748-1.5459 1.59638-.857L39.3342 97.1782c.6889.6889.0915 1.8189-.857 1.5964C20.0515 94.4522 5.54779 79.9485 1.22541 61.5228ZM.00189135 46.8891c-.01764375.2833.08887215.5599.28957165.7606L52.3503 99.7085c.2007.2007.4773.3075.7606.2896 2.3692-.1476 4.6938-.46 6.9624-.9259.7645-.157 1.0301-1.0963.4782-1.6481L2.57595 39.4485c-.55186-.5519-1.49117-.2863-1.648174.4782-.465915 2.2686-.77832 4.5932-.92588465 6.9624ZM4.21093 29.7054c-.16649.3738-.08169.8106.20765 1.1l64.77602 64.776c.2894.2894.7262.3742 1.1.2077 1.7861-.7956 3.5171-1.6927 5.1855-2.684.5521-.328.6373-1.0867.1832-1.5407L8.43566 24.3367c-.45409-.4541-1.21271-.3689-1.54074.1832-.99132 1.6684-1.88843 3.3994-2.68399 5.1855ZM12.6587 18.074c-.3701-.3701-.393-.9637-.0443-1.3541C21.7795 6.45931 35.1114 0 49.9519 0 77.5927 0 100 22.4073 100 50.0481c0 14.8405-6.4593 28.1724-16.7199 37.3375-.3903.3487-.984.3258-1.3542-.0443L12.6587 18.074Z" />
    </svg>
  );
}

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
            icon={c.id === "linear" ? <LinearIcon size={20} /> : <ExternalLinkIcon />}
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
      if (
        !Array.isArray(rows) ||
        !rows.some((row) => row.state === "connected" && row.resolves_issues)
      )
        return;
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
          {issue.url.startsWith("https://linear.app/") && <LinearIcon size={14} />}
          <strong>{issue.key}</strong> {issue.title} · {issue.state}
        </a>
      ))
    : null;
}
export function PullRequestIssueLinks({ pr }: { pr: PR }) {
  return useIssueLinks(pr);
}
