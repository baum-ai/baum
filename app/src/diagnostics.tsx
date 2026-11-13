import { useCallback, useEffect, useRef, useState } from "react";
import { cn } from "@baum/viewer/components/classnames";
import {
  CircleCheckIcon,
  CircleCrossIcon,
  CircleDashedIcon,
  WarningIcon,
} from "@baum/viewer/components/icons";
import { Alert, Badge, type BadgeTone, Button } from "@baum/viewer/components/ui";
import {
  request,
  type DiagnosticCheck,
  type DiagnosticReport,
  type Diagnostics,
  type IssueParts,
} from "./api";
import { displayPath } from "./desktop/setup-model";
import { LoadingStatus } from "./nine-dots";
import { ReportProblemDialog } from "./report-problem";

const statusText: Record<DiagnosticCheck["status"], string> = {
  ok: "OK",
  warning: "Warning",
  problem: "Problem",
  info: "Info",
};
const statusTone: Record<DiagnosticCheck["status"], BadgeTone> = {
  ok: "positive",
  warning: "caution",
  problem: "critical",
  info: "neutral",
};
const statusColor: Record<DiagnosticCheck["status"], string> = {
  ok: "text-status-positive",
  warning: "text-status-caution",
  problem: "text-status-critical",
  info: "text-tertiary",
};
const order = { problem: 0, warning: 1, ok: 2, info: 3 } as const;

function summary(report: DiagnosticReport) {
  const problems = report.checks.filter((c) => c.status === "problem").length;
  const warnings = report.checks.filter((c) => c.status === "warning").length;
  if (problems > 0) return `${problems} ${problems === 1 ? "problem" : "problems"} found`;
  if (warnings > 0) return `${warnings} ${warnings === 1 ? "warning" : "warnings"}, no problems`;
  return "No problems found";
}

function StatusIcon({ status }: { status: DiagnosticCheck["status"] }) {
  const Glyph =
    status === "ok"
      ? CircleCheckIcon
      : status === "problem"
        ? CircleCrossIcon
        : status === "warning"
          ? WarningIcon
          : CircleDashedIcon;
  return <Glyph size={16} aria-hidden className={cn("flex-none", statusColor[status])} />;
}

function Detail({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex min-w-0 gap-1.5">
      <dt className="flex-none text-tertiary">{label}</dt>
      <dd
        className={cn("m-0 min-w-0 text-secondary [overflow-wrap:anywhere]", mono && "font-mono")}
      >
        {value}
      </dd>
    </div>
  );
}

function CheckRow({ check, home }: { check: DiagnosticCheck; home?: string }) {
  const display = (text: string) => displayPath(text, home);
  const flagged = check.status === "problem" || check.status === "warning";
  return (
    <li className="flex min-w-0 gap-3 px-4 py-3">
      <span className="mt-1">
        <StatusIcon status={check.status} />
      </span>
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          <h4 className="m-0 text-base font-medium">{check.label}</h4>
          {flagged ? (
            <Badge tone={statusTone[check.status]}>{statusText[check.status]}</Badge>
          ) : (
            <span className="sr-only">{statusText[check.status]}</span>
          )}
        </div>
        <p className="m-0 text-secondary break-words">{display(check.detail)}</p>
        {check.path || check.version || check.source ? (
          <dl className="m-0 flex flex-wrap gap-x-4 gap-y-0.5 text-xs">
            {check.path ? <Detail label="Path" value={display(check.path)} mono /> : null}
            {check.version ? <Detail label="Version" value={check.version} /> : null}
            {check.source ? <Detail label="Source" value={display(check.source)} /> : null}
          </dl>
        ) : null}
        {check.fix ? (
          <p className="m-0 mt-2 rounded-(--radius-md) bg-surface-wash px-3 py-2 break-words">
            <strong className="font-medium">Fix:</strong> {display(check.fix)}
          </p>
        ) : null}
      </div>
    </li>
  );
}

const groups = [
  {
    title: "Needs attention",
    has: (c: DiagnosticCheck) => c.status === "problem" || c.status === "warning",
  },
  { title: "Working", has: (c: DiagnosticCheck) => c.status === "ok" },
  { title: "Details", has: (c: DiagnosticCheck) => c.status === "info" },
];

/** How many checks of a report are problems: what stops baum from working. */
export function problemCount(report: DiagnosticReport | null | undefined) {
  return report ? report.checks.filter((c) => c.status === "problem").length : 0;
}

/** Runs the diagnostics once, when `enabled` first holds, and keeps the number
 * of problems found, so every page can point at them. A page that runs them
 * itself reports newer results through `update`. A failed run shows nothing. */
export function useDiagnosticProblems(enabled: boolean) {
  const [problems, setProblems] = useState(0);
  const started = useRef(false);
  useEffect(() => {
    if (!enabled || started.current) return;
    started.current = true;
    request<Partial<Diagnostics>>("api/diagnostics").then(
      (body) => setProblems(problemCount(body.report)),
      () => undefined,
    );
  }, [enabled]);
  const update = useCallback((report: DiagnosticReport | null) => {
    started.current = true;
    setProblems(problemCount(report));
  }, []);
  return { problems, update };
}

function sorted(report: DiagnosticReport) {
  return report.checks
    .map((check, index) => ({ check, index }))
    .sort((a, b) => order[a.check.status] - order[b.check.status] || a.index - b.index)
    .map(({ check }) => check);
}

/** The report as Markdown for a GitHub issue, in the dialog's order and with
 *  the home directory as ~, so the issue does not name the user's account. */
export function diagnosticsMarkdown(report: DiagnosticReport, home?: string) {
  const display = (text: string) => displayPath(text, home);
  const lines = [
    "### baum diagnostics",
    "",
    `**Overall:** ${statusText[report.status]}. ${summary(report)}`,
    "",
  ];
  for (const check of sorted(report)) {
    lines.push(`- **${check.label}** (${statusText[check.status]}): ${display(check.detail)}`);
    if (check.fix) lines.push(`  - Fix: ${display(check.fix)}`);
    if (check.path) lines.push(`  - Path: \`${display(check.path)}\``);
    if (check.version) lines.push(`  - Version: ${check.version}`);
    if (check.source) lines.push(`  - Source: ${display(check.source)}`);
  }
  return `${lines.join("\n")}\n`;
}

/** The report `baum internal doctor` prints, as the local server computes it. It never
 * fetches the price list; "Check again" re-inspects this computer. */
export function DiagnosticsSection({
  onReport,
}: {
  onReport?: (report: DiagnosticReport | null) => void;
} = {}) {
  const [report, setReport] = useState<DiagnosticReport | null | undefined>();
  const [home, setHome] = useState<string>();
  const [issueParts, setIssueParts] = useState<IssueParts | null>(null);
  const [reporting, setReporting] = useState(false);
  const [error, setError] = useState("");
  const [running, setRunning] = useState(true);
  const [copied, setCopied] = useState("");
  const copiedTimer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => () => clearTimeout(copiedTimer.current), []);
  const copy = async () => {
    if (!report) return;
    clearTimeout(copiedTimer.current);
    try {
      await navigator.clipboard.writeText(diagnosticsMarkdown(report, home));
      setCopied("Copied");
    } catch {
      setCopied("Could not copy");
    }
    copiedTimer.current = setTimeout(() => setCopied(""), 2000);
  };
  const sequence = useRef(0);
  // A new callback from the parent must not rerun the checks.
  const reported = useRef(onReport);
  useEffect(() => {
    reported.current = onReport;
  });
  const run = useCallback(async () => {
    const mine = ++sequence.current;
    setRunning(true);
    setError("");
    try {
      const body = await request<Diagnostics>("api/diagnostics?refresh=1");
      if (mine === sequence.current) {
        setReport(body.report);
        setHome(body.home);
        setIssueParts(body.report_issue ?? null);
        reported.current?.(body.report);
      }
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
  const checks = report ? sorted(report) : [];
  const overall = report ? report.status : null;
  return (
    <section aria-labelledby="diagnostics-heading" className="space-y-5">
      <h2 id="diagnostics-heading" className="sr-only">
        Diagnostics
      </h2>
      {reporting ? (
        <ReportProblemDialog parts={issueParts} close={() => setReporting(false)} />
      ) : null}
      {error ? (
        <Alert>
          {error}{" "}
          <Button disabled={running} onClick={() => void run()}>
            Retry
          </Button>
        </Alert>
      ) : null}
      <div
        className={cn(
          "flex flex-wrap items-center gap-3 rounded-(--radius-lg) border border-subtle px-4 py-3",
          overall === "problem"
            ? "bg-status-critical-bg"
            : overall === "warning"
              ? "bg-status-caution-bg"
              : overall
                ? "bg-status-positive-bg"
                : "bg-surface-wash",
        )}
      >
        {report ? <StatusIcon status={report.status} /> : null}
        <div className="min-w-0 flex-1 basis-56">
          {report ? (
            <p role="status" className="m-0 font-medium">
              <span className="sr-only">Overall: {statusText[report.status]}. </span>
              {summary(report)}
            </p>
          ) : running && report === undefined && !error ? (
            <LoadingStatus>Checking this computer…</LoadingStatus>
          ) : report === null ? (
            <p className="m-0">Diagnostics are not available in this build.</p>
          ) : null}
          <p className="m-0 text-xs text-tertiary">
            {report ? `${report.checks.length} checks. ` : ""}Credential values are never shown.
          </p>
          <p className="m-0 text-xs text-tertiary">
            “Report a problem” opens a new GitHub issue in your browser with as much of a redacted
            summary as you choose; nothing is sent until you submit it there.
          </p>
        </div>
        <div className="flex flex-none gap-2">
          <Button
            variant="secondary"
            size="compact"
            disabled={!report}
            title="Copy the report as Markdown, for a GitHub issue"
            onClick={() => void copy()}
          >
            <span aria-live="polite">{copied || "Copy"}</span>
          </Button>
          <Button variant="secondary" size="compact" onClick={() => setReporting(true)}>
            Report a problem
          </Button>
          <Button variant="secondary" size="compact" disabled={running} onClick={() => void run()}>
            {running ? "Checking…" : "Check again"}
          </Button>
        </div>
      </div>
      {groups.map((group) => {
        const list = checks.filter(group.has);
        if (list.length === 0) return null;
        return (
          <div key={group.title} className="space-y-2">
            <h3 className="diagnostics-group-title">
              {group.title} <span className="font-normal">· {list.length}</span>
            </h3>
            <ul className="m-0 list-none divide-y divide-subtle overflow-hidden rounded-(--radius-lg) border border-subtle bg-surface-card p-0">
              {list.map((check) => (
                <CheckRow key={check.id} check={check} home={home} />
              ))}
            </ul>
          </div>
        );
      })}
    </section>
  );
}
