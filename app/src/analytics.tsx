import { useEffect, useRef, useState, type ComponentType, type ReactNode } from "react";
import {
  formatCompact,
  formatCount,
  formatRelative,
  formatShare,
  formatUSD,
} from "@baum/viewer/components/format";
import { Alert, Badge, Button, SegmentedControl } from "@baum/viewer/components/ui";
import { MUTED } from "@baum/viewer/series";
import { useUrlState, type ViewerLinkProps } from "@baum/viewer/navigation";
import { agentName, appURL, jobReasonText, request } from "./api";
import {
  ColumnChart,
  Figure,
  Legend,
  StepChart,
  slot,
  type Column,
  type Series,
} from "./analytics-charts";
import claudeLogo from "./desktop/claude-code.svg";
import codexLogo from "./desktop/codex.svg";
import cursorLogo from "./desktop/cursor.svg";
import { LoadingStatus } from "./nine-dots";
import { lasted } from "./sessions";
import { Notice, PageBar } from "./shell";
import "./analytics.css";

export type Cost = { usd: number | null; incomplete: boolean; reasons: string[] };
export type Row = {
  key: string;
  sessions: number;
  prompts: number;
  tool_calls: number;
  failed_tool_calls: number;
  failed_rate: number | null;
  active_seconds: number | null;
  tokens: number | null;
  tokens_incomplete: boolean;
  cost: Cost;
};
export type ModelRow = {
  model: string;
  agent: string;
  sessions: number;
  calls: number;
  tokens: number;
  cost: Cost;
};
export type SessionRow = {
  agent: string;
  repository: string;
  branch: string;
  started_at: string | null;
  ended_at: string | null;
  active_seconds: number | null;
  prompts: number;
  tool_calls: number;
  failed_tool_calls: number;
  subagents: number;
  tokens: number | null;
  cost: Cost;
};
export type Analytics = {
  window: { days: number; start: string; end: string };
  filters: { repository: string; agent: string };
  options: { repositories: string[]; agents: string[] };
  indexing: { done: number; total: number; complete: boolean };
  summary: {
    sessions: number;
    active_seconds: number | null;
    active_incomplete: boolean;
    prompts: number;
    subagents: number;
    cost: Cost;
    agent_share: number | null;
  };
  daily: { date: string; agents: Record<string, { sessions: number; active_seconds: number }> }[];
  hours: { hour: number; active_seconds: number }[];
  by_agent: Row[];
  by_model: ModelRow[];
  by_repository: Row[];
  longest: SessionRow[];
  commits: {
    available: boolean;
    reasons: string[];
    commits: number;
    agent_commits: number;
    by_session: number;
    by_checkpoint: number;
    by_trailer: number;
    share: number | null;
    by_repository: {
      repository: string;
      commits: number;
      agent_commits: number;
      share: number | null;
    }[];
  };
  baum: {
    available: boolean;
    reviews: number;
    group_reviews: number;
    fixes: number;
    automatic: number;
    completed: number;
    failed: number;
    cancelled: number;
    active: number;
    tokens: number;
    cost: Cost;
    failures: { reason: string; count: number }[];
    fixes_pushed: number;
    fixes_failed: number;
    threads_resolved: number;
    threads_replied: number;
    merges: number | null;
    branch_updates: number | null;
    check_reruns: number | null;
    /** Decision capture's model calls; null without its ledger, absent from
     *  an older daemon. */
    decisions?: {
      attempts: number;
      succeeded: number;
      failed: number;
      abandoned?: number;
      cost: Cost;
      failures: { reason: string; count: number }[];
    } | null;
  };
  delivery: {
    status: "loading" | "ready" | "unavailable";
    reasons: string[];
    fetched_at: string | null;
    repositories: number;
    opened: number;
    merged: number;
    drafts: number;
    median_merge_seconds: number | null;
    reviewed: number;
    median_first_review_seconds: number | null;
    conflict_or_failing: number;
    conflict_or_failing_share: number | null;
  };
  plan_usage: {
    available: boolean;
    series: {
      provider: string;
      window: string;
      points: { at: string; used_percent: number }[];
      peak: number;
      limit_hits: number;
    }[];
  };
};

/** The agents in their fixed colour order: a filter never repaints one. */
export const AGENTS = ["claude_code", "codex", "cursor"] as const;
const agentLogos: Record<string, string> = {
  claude_code: claudeLogo,
  codex: codexLogo,
  cursor: cursorLogo,
};
const agentColor = (agent: string) => {
  const i = AGENTS.indexOf(agent as (typeof AGENTS)[number]);
  return i < 0 ? MUTED : slot(i);
};
const agentSeries = (agents: readonly string[]): Series[] =>
  agents.map((a) => ({ key: a, label: agentName(a), color: agentColor(a) }));

export const WINDOWS = [7, 30, 90] as const;
/** The window a URL asks for, or 30 days. */
export function windowDays(raw: string | null): number {
  const days = Number(raw);
  return (WINDOWS as readonly number[]).includes(days) ? days : 30;
}

/** An older daemon, or one without the capability, answers without a report. */
export function parseAnalytics(body: unknown): Analytics | null {
  const report = body as Partial<Analytics> | null;
  return report && typeof report === "object" && report.summary && Array.isArray(report.daily)
    ? (report as Analytics)
    : null;
}

/** A cost at API prices, as text and its tooltip. Nothing priced is the em
 *  dash, never $0.00; a partly priced total is a floor. */
export function costText(cost: Cost): { text: string; title: string } {
  const why = cost.reasons.join(" ");
  if (cost.usd === null) {
    return { text: "—", title: why || "Nothing in this usage has a price." };
  }
  if (cost.incomplete) {
    return {
      text: `≥ ${formatUSD(cost.usd)}`,
      title: `At least this much; part of the usage has no price. ${why}`.trim(),
    };
  }
  return {
    text: formatUSD(cost.usd),
    title: "What this usage would cost at API prices, not what you were charged.",
  };
}

/** Active time in whole units, or null when unknown. */
export function activeText(seconds: number | null): string | null {
  return seconds === null ? null : lasted(seconds);
}

/** A median wait in a readable unit: "40 min", "5 h", "3 d". */
export function waitText(seconds: number | null): string | null {
  if (seconds === null) return null;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${Math.max(1, minutes)} min`;
  if (minutes < 48 * 60) return `${Math.round(minutes / 60)} h`;
  return `${Math.round(minutes / 1440)} d`;
}

const dayTick = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" });
const dayTitle = new Intl.DateTimeFormat(undefined, {
  weekday: "short",
  month: "short",
  day: "numeric",
});
/** A local date ("2026-10-27") at local midnight. */
const localDate = (date: string) => {
  const [y, m, d] = date.split("-").map(Number);
  return new Date(y!, m! - 1, d!);
};

function Agent({ agent }: { agent: string }) {
  const logo = agentLogos[agent];
  return (
    <span className="analytics-agent">
      {logo ? <img src={logo} alt="" width="16" height="16" /> : null}
      {agentName(agent)}
    </span>
  );
}

function Card({
  title,
  detail,
  actions,
  children,
  id,
}: {
  title: string;
  detail?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  id: string;
}) {
  return (
    <section aria-labelledby={id} className="local-panel analytics-card">
      <header className="analytics-card-head">
        <div>
          <h2 id={id}>{title}</h2>
          {detail ? <p>{detail}</p> : null}
        </div>
        {actions}
      </header>
      {children}
    </section>
  );
}

function Tile({
  label,
  value,
  reason,
  detail,
  badge,
}: {
  label: string;
  value: string | null;
  reason?: string;
  detail?: ReactNode;
  badge?: ReactNode;
}) {
  return (
    <div className="analytics-tile">
      <dt>{label}</dt>
      <dd>
        <span className="analytics-tile-value">
          <Figure value={value} reason={reason} />
        </span>
        {badge}
        {detail ? <span className="analytics-tile-detail">{detail}</span> : null}
      </dd>
    </div>
  );
}

function Summary({ data }: { data: Analytics }) {
  const s = data.summary;
  const cost = costText(s.cost);
  const share = data.commits;
  return (
    <dl className="local-panel analytics-tiles" aria-label="Summary">
      <Tile
        label="Sessions"
        value={formatCount(s.sessions)}
        detail={
          s.subagents ? `and ${formatCount(s.subagents)} subagents` : "Started in this window"
        }
      />
      <Tile
        label="Active agent time"
        value={activeText(s.active_seconds)}
        reason="No session in this window recorded its activity."
        detail={
          s.active_incomplete && s.active_seconds !== null ? (
            <span title="Sessions without a transcript have no activity to measure.">
              Some sessions unmeasured
            </span>
          ) : (
            "Agents working, not waiting"
          )
        }
      />
      <Tile
        label="Human prompts"
        value={formatCount(s.prompts)}
        detail={
          s.sessions ? `${(s.prompts / s.sessions).toFixed(1)} per session` : "What you asked"
        }
      />
      <Tile
        label="At API prices"
        value={s.cost.usd === null ? null : cost.text}
        reason={cost.title}
        badge={
          s.cost.incomplete ? (
            <Badge tone="caution" size="compact" title={cost.title}>
              Incomplete
            </Badge>
          ) : null
        }
        detail="Not what a subscription charges"
      />
      <Tile
        label="Agent-written"
        value={share.share === null ? null : formatShare(share.share)}
        reason={
          share.reasons.join(" ") || "None of your commits on your branches fall in this window."
        }
        detail={
          share.commits
            ? `${formatCount(share.agent_commits)} of ${formatCount(share.commits)} of your commits`
            : "Of your commits"
        }
      />
    </dl>
  );
}

function Activity({ data }: { data: Analytics }) {
  const [metric, setMetric] = useState<"sessions" | "active">("sessions");
  const agents = AGENTS.filter((a) =>
    data.daily.some((d) => (d.agents[a]?.sessions ?? 0) + (d.agents[a]?.active_seconds ?? 0) > 0),
  );
  const series = agentSeries(agents.length ? agents : ["claude_code"]);
  const every = data.daily.length <= 7 ? 1 : data.daily.length <= 30 ? 7 : 14;
  const last = data.daily.length - 1;
  const columns: Column[] = data.daily.map((day, i) => ({
    key: day.date,
    tick: (last - i) % every === 0 ? dayTick.format(localDate(day.date)) : "",
    title: dayTitle.format(localDate(day.date)),
    values: Object.fromEntries(
      series.map((s) => [
        s.key,
        metric === "sessions"
          ? (day.agents[s.key]?.sessions ?? 0)
          : (day.agents[s.key]?.active_seconds ?? 0),
      ]),
    ),
  }));
  const format =
    metric === "sessions" ? (v: number) => formatCount(v) : (v: number) => (v ? lasted(v) : "0");
  const empty = data.summary.sessions === 0;
  return (
    <Card
      id="analytics-activity"
      title="Agent activity"
      detail={
        metric === "sessions" ? "Sessions started each day" : "Time agents spent working each day"
      }
      actions={
        <SegmentedControl
          label="Show"
          value={metric}
          onChange={setMetric}
          items={[
            { value: "sessions", label: "Sessions" },
            { value: "active", label: "Active time" },
          ]}
        />
      }
    >
      {empty ? (
        <div className="analytics-empty">
          <h3>No agent sessions in this window</h3>
          <p>
            Claude Code, Codex and Cursor sessions whose working folder is inside one of your local
            clones count here.
          </p>
        </div>
      ) : (
        <>
          <ColumnChart
            label={`${metric === "sessions" ? "Sessions" : "Active time"} per day by agent`}
            columns={columns}
            series={series}
            format={format}
            unit={metric === "sessions" ? 1 : 3600}
          />
          <Legend series={series} />
          <details className="analytics-table-toggle">
            <summary>Show as a table</summary>
            <div className="analytics-scroll">
              <table className="analytics-table">
                <thead>
                  <tr>
                    <th scope="col">Day</th>
                    {series.map((s) => (
                      <th key={s.key} scope="col" className="num">
                        {s.label}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {columns.map((c) => (
                    <tr key={c.key}>
                      <th scope="row">{c.title}</th>
                      {series.map((s) => (
                        <td key={s.key} className="num">
                          {format(c.values[s.key] ?? 0)}
                        </td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </details>
        </>
      )}
    </Card>
  );
}

function CostCell({ cost }: { cost: Cost }) {
  const { text, title } = costText(cost);
  return <Figure value={cost.usd === null ? null : text} reason={title} />;
}

function TokensCell({ tokens, incomplete }: { tokens: number | null; incomplete: boolean }) {
  const why = "Some model calls reported no token usage.";
  if (tokens === null) return <Figure value={null} reason={why} />;
  return (
    <Figure
      value={`${incomplete ? "≥ " : ""}${formatCompact(tokens)}`}
      reason={incomplete ? `At least this many. ${why}` : `${formatCount(tokens)} tokens`}
    />
  );
}

function rowCells(row: Row) {
  return (
    <>
      <td className="num">{formatCount(row.sessions)}</td>
      <td className="num">{formatCount(row.prompts)}</td>
      <td className="num">{formatCount(row.tool_calls)}</td>
      <td className="num">
        <Figure
          value={row.failed_rate === null ? null : formatShare(row.failed_rate)}
          reason={
            row.failed_rate === null
              ? "No tool calls."
              : `${formatCount(row.failed_tool_calls)} of ${formatCount(row.tool_calls)} tool calls failed`
          }
        />
      </td>
      <td className="num">
        <Figure value={activeText(row.active_seconds)} reason="No session recorded its activity." />
      </td>
      <td className="num">
        <TokensCell tokens={row.tokens} incomplete={row.tokens_incomplete} />
      </td>
      <td className="num">
        <CostCell cost={row.cost} />
      </td>
    </>
  );
}

const rowHeads = (
  <>
    <th scope="col" className="num">
      Sessions
    </th>
    <th scope="col" className="num">
      Prompts
    </th>
    <th scope="col" className="num">
      Tool calls
    </th>
    <th scope="col" className="num" title="Share of tool calls that returned an error">
      Failed
    </th>
    <th scope="col" className="num">
      Active
    </th>
    <th scope="col" className="num">
      Tokens
    </th>
    <th scope="col" className="num">
      At API prices
    </th>
  </>
);

function Breakdown({ data }: { data: Analytics }) {
  const [by, setBy] = useState<"agent" | "model" | "repository">("agent");
  return (
    <Card
      id="analytics-breakdown"
      title="Breakdown"
      detail="Sessions in this window, by what ran them and where"
      actions={
        <SegmentedControl
          label="Break down by"
          value={by}
          onChange={setBy}
          items={[
            { value: "agent", label: "Agent" },
            { value: "model", label: "Model" },
            { value: "repository", label: "Repository" },
          ]}
        />
      }
    >
      <div className="analytics-scroll">
        {by === "model" ? (
          <table className="analytics-table">
            <thead>
              <tr>
                <th scope="col">Model</th>
                <th scope="col">Agent</th>
                <th scope="col" className="num">
                  Sessions
                </th>
                <th scope="col" className="num">
                  Model calls
                </th>
                <th scope="col" className="num">
                  Tokens
                </th>
                <th scope="col" className="num">
                  At API prices
                </th>
              </tr>
            </thead>
            <tbody>
              {data.by_model.length === 0 ? (
                <tr>
                  <td colSpan={6} className="analytics-none">
                    No model usage was recorded in this window.
                  </td>
                </tr>
              ) : (
                data.by_model.map((m) => (
                  <tr key={`${m.agent}:${m.model}`}>
                    <th scope="row">
                      <code>{m.model}</code>
                    </th>
                    <td>
                      <Agent agent={m.agent} />
                    </td>
                    <td className="num">{formatCount(m.sessions)}</td>
                    <td className="num">{formatCount(m.calls)}</td>
                    <td className="num">
                      <TokensCell tokens={m.tokens} incomplete={false} />
                    </td>
                    <td className="num">
                      <CostCell cost={m.cost} />
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        ) : (
          <table className="analytics-table">
            <thead>
              <tr>
                <th scope="col">{by === "agent" ? "Agent" : "Repository"}</th>
                {rowHeads}
              </tr>
            </thead>
            <tbody>
              {(by === "agent" ? data.by_agent : data.by_repository).length === 0 ? (
                <tr>
                  <td colSpan={8} className="analytics-none">
                    No agent sessions in this window.
                  </td>
                </tr>
              ) : (
                (by === "agent" ? data.by_agent : data.by_repository).map((row) => (
                  <tr key={row.key}>
                    <th scope="row">{by === "agent" ? <Agent agent={row.key} /> : row.key}</th>
                    {rowCells(row)}
                  </tr>
                ))
              )}
            </tbody>
          </table>
        )}
      </div>
      <p className="analytics-note">
        Costs are what the usage would cost at API prices, not what you were charged: most agents
        run on a subscription. A figure marked ≥ is a floor: part of the usage has no price or
        reported no tokens.
      </p>
    </Card>
  );
}

function Stats({ items }: { items: { label: string; value: string | null; reason?: string }[] }) {
  return (
    <dl className="analytics-stats">
      {items.map((item) => (
        <div key={item.label}>
          <dt>{item.label}</dt>
          <dd>
            <Figure value={item.value} reason={item.reason} />
          </dd>
        </div>
      ))}
    </dl>
  );
}

function Commits({ data }: { data: Analytics }) {
  const c = data.commits;
  const parts: (Series & { value: number })[] = [
    { key: "session", label: "Session recorded it", color: slot(0), value: c.by_session },
    { key: "checkpoint", label: "Entire checkpoint", color: slot(1), value: c.by_checkpoint },
    { key: "trailer", label: "Co-author trailer", color: slot(2), value: c.by_trailer },
    {
      key: "none",
      label: "No agent evidence",
      color: MUTED,
      value: c.commits - c.agent_commits,
    },
  ];
  return (
    <Card
      id="analytics-commits"
      title="Agent-written commits"
      detail="Your commits on your local branches in this window"
    >
      {!c.available || c.commits === 0 ? (
        <div className="analytics-empty">
          <h3>{c.available ? "No commits of yours in this window" : "Commits not read"}</h3>
          {(c.reasons.length
            ? c.reasons
            : ["Commit on a branch of a local clone to see it here."]
          ).map((r) => (
            <p key={r}>{r}</p>
          ))}
        </div>
      ) : (
        <>
          <p className="analytics-headline">
            <strong>{formatShare(c.share)}</strong>
            <span>
              {formatCount(c.agent_commits)} of {formatCount(c.commits)} commits had an agent
              involved
            </span>
          </p>
          <div
            className="analytics-meter"
            role="img"
            aria-label={parts.map((p) => `${p.label}: ${p.value}`).join(", ")}
          >
            {parts
              .filter((p) => p.value > 0)
              .map((p) => (
                <div
                  key={p.key}
                  title={`${p.label}: ${p.value}`}
                  style={{ flexGrow: p.value, background: p.color }}
                />
              ))}
          </div>
          <ul className="analytics-legend" aria-label="Legend">
            {parts.map((p) => (
              <li key={p.key}>
                <span
                  aria-hidden="true"
                  className="analytics-key-box"
                  style={{ background: p.color }}
                />
                {p.label} <strong>{formatCount(p.value)}</strong>
              </li>
            ))}
          </ul>
          {c.by_repository.length > 1 ? (
            <table className="analytics-table analytics-compact">
              <tbody>
                {c.by_repository.map((r) => (
                  <tr key={r.repository}>
                    <th scope="row">{r.repository}</th>
                    <td className="num">
                      {formatCount(r.agent_commits)} of {formatCount(r.commits)}
                    </td>
                    <td className="num">
                      <Figure
                        value={r.share === null ? null : formatShare(r.share)}
                        reason="No commits."
                      />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : null}
          {c.reasons.map((r) => (
            <p key={r} className="analytics-note">
              {r}
            </p>
          ))}
        </>
      )}
      <p className="analytics-note">
        Each commit counts once, under its strongest evidence. File overlap is not used, so this is
        a floor. It shows that an agent was involved, not that it caused the change.
      </p>
    </Card>
  );
}

function BaumWork({ data }: { data: Analytics }) {
  const b = data.baum;
  const runs = b.reviews + b.group_reviews + b.fixes;
  const cost = costText(b.cost);
  const noLedger = "Recorded from this version of baum on.";
  return (
    <Card
      id="analytics-baum"
      title="baum's work"
      detail="Reviews and fixes baum ran on this computer"
    >
      {!b.available ? (
        <p className="analytics-note">The jobs list is not available.</p>
      ) : runs === 0 && b.decisions?.attempts ? (
        <p className="analytics-note">No reviews or fixes in this window.</p>
      ) : runs === 0 ? (
        <div className="analytics-empty">
          <h3>baum ran nothing in this window</h3>
          <p>Reviews and fixes you start, or that start automatically, are counted here.</p>
        </div>
      ) : (
        <Stats
          items={[
            {
              label: "Reviews",
              value: `${formatCount(b.reviews)}${b.group_reviews ? ` + ${formatCount(b.group_reviews)} group` : ""}`,
            },
            { label: "Started automatically", value: formatCount(b.automatic) },
            {
              label: "Fixes pushed",
              value: `${formatCount(b.fixes_pushed)} of ${formatCount(b.fixes)}`,
            },
            { label: "Review threads resolved", value: formatCount(b.threads_resolved) },
            {
              label: "At API prices",
              value: b.cost.usd === null ? null : cost.text,
              reason: cost.title,
            },
            { label: "Failed runs", value: formatCount(b.failed) },
          ]}
        />
      )}
      {b.failures.length ? (
        <ul className="analytics-reasons" aria-label="Failures by reason">
          {b.failures.map((f) => (
            <li key={f.reason}>
              <span>{jobReasonText(f.reason)}</span>
              <strong>{formatCount(f.count)}</strong>
            </li>
          ))}
        </ul>
      ) : null}
      {b.decisions ? <DecisionCapture decisions={b.decisions} /> : null}
      <Stats
        items={[
          {
            label: "Merges from baum",
            value: b.merges === null ? null : formatCount(b.merges),
            reason: noLedger,
          },
          {
            label: "Branches updated",
            value: b.branch_updates === null ? null : formatCount(b.branch_updates),
            reason: noLedger,
          },
          {
            label: "Checks re-run",
            value: b.check_reruns === null ? null : formatCount(b.check_reruns),
            reason: noLedger,
          },
        ]}
      />
    </Card>
  );
}

const captureFailures: Record<string, string> = {
  timeout: "Timed out",
  unusable_reply: "Reply could not be used",
  call_failed: "Model call failed",
};

/** Decision capture's calls in the window, within baum's work. */
function DecisionCapture({
  decisions: d,
}: {
  decisions: NonNullable<Analytics["baum"]["decisions"]>;
}) {
  const cost = costText(d.cost);
  return (
    <div className="analytics-decisions">
      <h3>Decision capture</h3>
      <Stats
        items={[
          { label: "Capture calls", value: formatCount(d.attempts) },
          { label: "Failed calls", value: formatCount(d.failed) },
          ...(d.abandoned
            ? [{ label: "Cut short by a stop", value: formatCount(d.abandoned) }]
            : []),
          {
            label: "Capture spend",
            value: d.cost.usd === null ? null : cost.text,
            reason: d.attempts ? cost.title : "No capture calls in this window.",
          },
        ]}
      />
      {d.failures.length ? (
        <ul className="analytics-reasons" aria-label="Failed decision captures by reason">
          {d.failures.map((f) => (
            <li key={f.reason}>
              <span>{captureFailures[f.reason] ?? f.reason}</span>
              <strong>{formatCount(f.count)}</strong>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function Delivery({ data }: { data: Analytics }) {
  const d = data.delivery;
  return (
    <Card
      id="analytics-delivery"
      title="Delivery"
      detail={
        data.filters.agent
          ? "Your pull requests opened in this window, whichever agent helped"
          : "Your pull requests opened in this window, from GitHub"
      }
    >
      {d.status === "loading" ? (
        <LoadingStatus block>Loading from GitHub…</LoadingStatus>
      ) : d.status === "unavailable" ? (
        <div className="analytics-empty">
          <h3>Not available</h3>
          {d.reasons.map((r) => (
            <p key={r}>{r}</p>
          ))}
        </div>
      ) : d.repositories === 0 ? (
        <div className="analytics-empty">
          <h3>No repositories picked</h3>
          <p>Pick repositories in Repositories to see your pull requests here.</p>
        </div>
      ) : (
        <>
          <Stats
            items={[
              { label: "Opened", value: formatCount(d.opened) },
              {
                label: "Merged",
                value: `${formatCount(d.merged)}${d.opened ? ` (${formatShare(d.merged / d.opened)})` : ""}`,
              },
              {
                label: "Median time to merge",
                value: waitText(d.median_merge_seconds),
                reason: "None merged yet.",
              },
              {
                label: "Median time to first review",
                value: waitText(d.median_first_review_seconds),
                reason: "None reviewed yet.",
              },
              {
                label: "Conflict or failing checks",
                value:
                  d.conflict_or_failing_share === null
                    ? null
                    : `${formatCount(d.conflict_or_failing)} (${formatShare(d.conflict_or_failing_share)})`,
                reason: "No pull requests opened.",
              },
              { label: "Drafts, not counted above", value: formatCount(d.drafts) },
            ]}
          />
          {d.reasons.map((r) => (
            <p key={r} className="analytics-note">
              {r}
            </p>
          ))}
        </>
      )}
      <p className="analytics-note">
        Drafts are counted apart. Conflicts and checks are as GitHub reports each pull request's
        latest head now.
        {d.fetched_at ? ` Read from GitHub ${formatRelative(d.fetched_at)}.` : ""}
      </p>
    </Card>
  );
}

const providerNames: Record<string, string> = { claude: "Claude", codex: "Codex" };
const capital = (text: string) => text.charAt(0).toUpperCase() + text.slice(1);

function PlanUsage({ data }: { data: Analytics }) {
  const p = data.plan_usage;
  const start = Date.parse(data.window.start);
  const end = Date.parse(data.window.end);
  const providers = [...new Set(p.series.map((s) => s.provider))];
  return (
    <Card id="analytics-plans" title="Plan usage" detail="Share of each plan window used over time">
      {!p.available || p.series.length === 0 ? (
        <div className="analytics-empty">
          <h3>No plan usage sampled yet</h3>
          <p>
            baum reads how much of your Claude and Codex plans is used every 15 minutes while it
            runs.
          </p>
        </div>
      ) : (
        providers.map((provider) => {
          const lines = p.series
            .filter((s) => s.provider === provider)
            .map((s, i) => ({
              series: { key: s.window, label: capital(s.window), color: slot(i) },
              points: s.points.map((pt) => ({ at: Date.parse(pt.at), value: pt.used_percent })),
              raw: s,
            }));
          const name = providerNames[provider] ?? capital(provider);
          return (
            <div key={provider} className="analytics-provider">
              <h3>{name}</h3>
              <StepChart
                label={`${name} plan usage over time`}
                lines={lines}
                start={start}
                end={end}
              />
              <Legend series={lines.map((l) => l.series)} line />
              <ul className="analytics-plan-facts">
                {lines.map(({ series, raw }) => (
                  <li key={series.key}>
                    {series.label}: peak {Math.round(raw.peak)}%,{" "}
                    {raw.limit_hits === 0
                      ? "never at its limit"
                      : `at its limit ${raw.limit_hits} ${raw.limit_hits === 1 ? "time" : "times"}`}
                  </li>
                ))}
              </ul>
            </div>
          );
        })
      )}
    </Card>
  );
}

function Hours({ data }: { data: Analytics }) {
  const series = [{ key: "active", label: "Active time", color: slot(0) }];
  const total = data.hours.reduce((sum, h) => sum + h.active_seconds, 0);
  const columns: Column[] = data.hours.map((h) => ({
    key: String(h.hour),
    tick: h.hour % 6 === 0 ? `${String(h.hour).padStart(2, "0")}:00` : "",
    title: `${String(h.hour).padStart(2, "0")}:00–${String((h.hour + 1) % 24).padStart(2, "0")}:00`,
    values: { active: h.active_seconds },
  }));
  return (
    <Card
      id="analytics-hours"
      title="Busiest hours"
      detail="When in the day agents worked, local time"
    >
      {total === 0 ? (
        <p className="analytics-note">No measured agent activity in this window.</p>
      ) : (
        <ColumnChart
          label="Active agent time by hour of the day"
          columns={columns}
          series={series}
          format={(v) => (v ? lasted(v) : "0")}
          unit={3600}
          height={120}
        />
      )}
    </Card>
  );
}

function Longest({ data }: { data: Analytics }) {
  return (
    <Card id="analytics-longest" title="Longest sessions" detail="By time spent working">
      {data.longest.length === 0 ? (
        <p className="analytics-note">No measured sessions in this window.</p>
      ) : (
        <ol className="analytics-sessions">
          {data.longest.map((s, i) => (
            <li key={`${s.agent}:${s.started_at}:${i}`}>
              <div className="analytics-session-main">
                <Agent agent={s.agent} />
                <span className="analytics-session-where">
                  {s.repository}
                  {s.branch ? <code>{s.branch}</code> : null}
                </span>
              </div>
              <div className="analytics-session-facts">
                <strong>{activeText(s.active_seconds)}</strong>
                <span>
                  {formatCount(s.prompts)} {s.prompts === 1 ? "prompt" : "prompts"} ·{" "}
                  {formatCount(s.tool_calls)} tool calls
                  {s.failed_tool_calls ? ` (${formatCount(s.failed_tool_calls)} failed)` : ""}
                </span>
                <span title={s.started_at ? new Date(s.started_at).toLocaleString() : undefined}>
                  {s.started_at ? `Started ${formatRelative(s.started_at)}` : null}
                  {s.cost.usd !== null ? ` · ${costText(s.cost).text}` : ""}
                </span>
              </div>
            </li>
          ))}
        </ol>
      )}
    </Card>
  );
}

/** The report, drawn. Pure so it renders without the daemon. */
export function AnalyticsReport({
  data,
  Link,
}: {
  data: Analytics;
  Link: ComponentType<ViewerLinkProps>;
}) {
  return (
    <>
      {!data.indexing.complete ? (
        <Notice>
          {data.indexing.total
            ? `Reading agent sessions on this computer: ${formatCount(data.indexing.done)} of ${formatCount(data.indexing.total)}. Figures fill in as it goes.`
            : "Looking for agent sessions on this computer…"}
        </Notice>
      ) : null}
      {data.options.repositories.length === 0 ? (
        <Notice action={<Link href={appURL("?page=repositories")}>Open repositories</Link>}>
          Pick repositories and give them a local clone to see what your agents did in them.
        </Notice>
      ) : null}
      <Summary data={data} />
      <Activity data={data} />
      <Breakdown data={data} />
      <div className="analytics-pair">
        <Commits data={data} />
        <BaumWork data={data} />
      </div>
      <div className="analytics-pair">
        <Delivery data={data} />
        <PlanUsage data={data} />
      </div>
      <div className="analytics-pair">
        <Hours data={data} />
        <Longest data={data} />
      </div>
    </>
  );
}

/** How soon to read again: quickly while the index or GitHub is still
 *  answering, else every five minutes. */
export function nextRead(data: Analytics | null): number {
  if (data && (!data.indexing.complete || data.delivery.status === "loading")) return 2000;
  return 5 * 60 * 1000;
}

/** The Analytics page (`?page=analytics&days=30&repo=&agent=`): what the
 *  agents did in the person's repositories, cost and delivered. */
export function AnalyticsPage({ Link }: { Link: ComponentType<ViewerLinkProps> }) {
  const url = useUrlState();
  const days = windowDays(url.search.get("days"));
  const repo = url.search.get("repo") ?? "";
  const agent = url.search.get("agent") ?? "";
  const [data, setData] = useState<Analytics | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [unavailable, setUnavailable] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const query = `days=${days}${repo ? `&repo=${encodeURIComponent(repo)}` : ""}${agent ? `&agent=${agent}` : ""}`;
  const shown = useRef("");
  useEffect(() => {
    const abort = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const read = async () => {
      setLoading(true);
      try {
        const body = await request<unknown>(
          `api/analytics?${query}`,
          undefined,
          "GET",
          abort.signal,
        );
        const report = parseAnalytics(body);
        setUnavailable(report === null);
        setData(report);
        setError("");
        shown.current = query;
        timer = setTimeout(() => void read(), nextRead(report));
      } catch (e) {
        if (abort.signal.aborted) return;
        setError(e instanceof Error ? e.message : "Local request failed.");
      } finally {
        if (!abort.signal.aborted) setLoading(false);
      }
    };
    void read();
    return () => {
      abort.abort();
      clearTimeout(timer);
    };
  }, [query, attempt]);
  const set = (key: string, value: string) =>
    url.update((current) => {
      if (value) current.set(key, value);
      else current.delete(key);
      return current;
    });
  const repositories = data?.options.repositories ?? (repo ? [repo] : []);
  const agents = data?.options.agents ?? [];
  return (
    <>
      <PageBar
        title="Analytics"
        detail="What your agents did, its cost at API prices and what you delivered"
      />
      <div className="local-filter-bar analytics-filters">
        <SegmentedControl
          label="Window"
          value={String(days)}
          onChange={(value) => set("days", value === "30" ? "" : value)}
          items={WINDOWS.map((d) => ({ value: String(d), label: `${d} days` }))}
        />
        <div className="analytics-selects">
          <label className="local-repo-filter">
            <span className="sr-only">Repository</span>
            <select value={repo} onChange={(e) => set("repo", e.target.value)}>
              <option value="">All repositories</option>
              {repositories.map((name) => (
                <option key={name}>{name}</option>
              ))}
            </select>
          </label>
          <label className="local-repo-filter">
            <span className="sr-only">Agent</span>
            <select value={agent} onChange={(e) => set("agent", e.target.value)}>
              <option value="">All agents</option>
              {[...new Set([...agents, ...(agent ? [agent] : [])])].map((a) => (
                <option key={a} value={a}>
                  {agentName(a)}
                </option>
              ))}
            </select>
          </label>
        </div>
      </div>
      <div
        className="local-page-body analytics"
        aria-busy={loading}
        data-stale={(loading && data && shown.current !== query) || undefined}
      >
        {error ? (
          <Alert>
            {error}{" "}
            <Button
              onClick={() => {
                setError("");
                setAttempt((n) => n + 1);
              }}
            >
              Retry
            </Button>
          </Alert>
        ) : null}
        {unavailable ? (
          <div className="local-empty">
            <h2>Analytics are not available</h2>
            <p>This baum daemon is older than the app. Update baum to see analytics.</p>
          </div>
        ) : data ? (
          <AnalyticsReport data={data} Link={Link} />
        ) : loading ? (
          <LoadingStatus block>Loading analytics…</LoadingStatus>
        ) : null}
      </div>
    </>
  );
}
