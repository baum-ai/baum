import { useCallback, useEffect, useRef, useState } from "react";
import { Button, Alert } from "@baum/viewer/components/ui";
import { request, type DiagnosticCheck, type DiagnosticReport, type Diagnostics } from "./api";

const statusText: Record<DiagnosticCheck["status"], string> = {
  ok: "OK",
  warning: "Warning",
  problem: "Problem",
  info: "Info",
};
const statusTone: Record<DiagnosticCheck["status"], string> = {
  ok: "bg-(--status-positive-bg) text-(--status-positive)",
  warning: "bg-(--status-caution-bg) text-(--status-caution)",
  problem: "bg-(--status-critical-bg) text-(--status-critical)",
  info: "bg-(--status-neutral-bg) text-(--status-neutral)",
};
const order = { problem: 0, warning: 1, ok: 2, info: 3 } as const;

function summary(report: DiagnosticReport) {
  const problems = report.checks.filter((c) => c.status === "problem").length;
  const warnings = report.checks.filter((c) => c.status === "warning").length;
  if (problems > 0) return `${problems} ${problems === 1 ? "problem" : "problems"} found.`;
  if (warnings > 0) return `${warnings} ${warnings === 1 ? "warning" : "warnings"}, no problems.`;
  return "No problems found.";
}

function Detail({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-0.5 sm:flex-row sm:gap-2">
      <dt className="shrink-0 text-(--text-tertiary)">{label}</dt>
      <dd className="m-0 min-w-0 [overflow-wrap:anywhere]">{value}</dd>
    </div>
  );
}

/** The report `baum doctor` prints, as the local server computes it. It never
 * fetches the price list; "Check again" re-inspects this machine. */
export function DiagnosticsSection() {
  const [report, setReport] = useState<DiagnosticReport | null | undefined>();
  const [error, setError] = useState("");
  const [running, setRunning] = useState(true);
  const sequence = useRef(0);
  const run = useCallback(async () => {
    const mine = ++sequence.current;
    setRunning(true);
    setError("");
    try {
      const body = await request<Diagnostics>("/api/diagnostics");
      if (mine === sequence.current) setReport(body.report);
    } catch (e) {
      if (mine === sequence.current)
        setError(e instanceof Error ? e.message : "Local request failed.");
    } finally {
      if (mine === sequence.current) setRunning(false);
    }
  }, []);
  useEffect(() => {
    void run();
    return () => {
      sequence.current++;
    };
  }, [run]);
  const checks = report
    ? report.checks
        .map((check, index) => ({ check, index }))
        .sort((a, b) => order[a.check.status] - order[b.check.status] || a.index - b.index)
    : [];
  return (
    <section aria-labelledby="diagnostics-heading" className="local-panel space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 id="diagnostics-heading" className="text-lg">
          Diagnostics
        </h2>
        <Button variant="secondary" disabled={running} onClick={() => void run()}>
          {running ? "Checking…" : "Check again"}
        </Button>
      </div>
      <p className="text-xs">
        The same report as <code>baum doctor</code>: which tools and credentials baum found on this
        machine, never their values.
      </p>
      {error ? (
        <Alert>
          {error}{" "}
          <Button disabled={running} onClick={() => void run()}>
            Retry
          </Button>
        </Alert>
      ) : null}
      {running && report === undefined && !error ? (
        <p role="status">Checking this machine…</p>
      ) : null}
      {report === null ? <p>Diagnostics are not available in this build.</p> : null}
      {report ? (
        <>
          <p role="status" className="font-medium">
            Overall: {statusText[report.status]}. {summary(report)}
          </p>
          <ul className="m-0 list-none space-y-3 p-0">
            {checks.map(({ check }) => (
              <li
                key={check.id}
                className="min-w-0 space-y-1 border-t border-(--border-default) pt-3"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <h3 className="text-base font-medium">{check.label}</h3>
                  <span
                    className={`rounded-(--radius-sm) px-2 py-0.5 text-xs font-medium ${statusTone[check.status]}`}
                  >
                    {statusText[check.status]}
                  </span>
                </div>
                <p className="m-0 break-words">{check.detail}</p>
                {check.path || check.version || check.source ? (
                  <dl className="m-0 space-y-0.5 text-xs">
                    {check.path ? <Detail label="Path" value={check.path} /> : null}
                    {check.version ? <Detail label="Version" value={check.version} /> : null}
                    {check.source ? <Detail label="Source" value={check.source} /> : null}
                  </dl>
                ) : null}
                {check.fix ? (
                  <p className="m-0 break-words">
                    <strong>Fix:</strong> {check.fix}
                  </p>
                ) : null}
              </li>
            ))}
          </ul>
        </>
      ) : null}
    </section>
  );
}
