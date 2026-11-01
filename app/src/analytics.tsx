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
import { agentName, appURL, request } from "./api";
import {
  ColumnChart,
  Figure,
  Legend,
  StepChart,
  TwoAxisChart,
  niceMax,
  slot,
  type Column,
  type Series,
} from "./analytics-charts";
import claudeLogo from "./desktop/claude-code.svg";
import codexLogo from "./desktop/codex.svg";
import cursorLogo from "./desktop/cursor.svg";
import { LoadingStatus } from "./nine-dots";
import { lasted } from "./sessions";
import { Notice, PageBar, RefreshButton, useReread } from "./shell";
import "./analytics.css";

export type BaumWeek = {
  reviews: number;
  fixes_pushed: number;
  threads_resolved: number;
  merges: number | null;
  branch_updates: number | null;
  check_reruns: number | null;
  decisions: number | null;
};
export type FlowCounts = {
  opened: number;
  opened_merged: number;
  merged: number;
  merge_seconds: number[];
};
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
  daily: {
    date: string;
    agents: Record<string, { sessions: number; active_seconds: number }>;
    /** Each session under its main model; absent from an older daemon. */
    models?: Record<string, { sessions: number; active_seconds: number }>;
  }[];
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
    /** Absent from an older daemon. */
    reviews_completed?: number;
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
    /** The last seven days against the seven before; absent from an older
     *  daemon. */
    weeks?: { this: BaumWeek; previous: BaumWeek };
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
  /** The pull request flow, from the feature log; absent from an older
   *  daemon. */
  flow?: {
    available: boolean;
    yours: boolean;
    unlisted: string[];
    /** Repositories whose pull requests could not be read. */
    unread?: string[];
    days: { date: string; mine: FlowCounts; team: FlowCounts }[];
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
  agents.map((a) => ({ key: a, label: agentName(a), color: agentColor(a), icon: agentLogos[a] }));
/** A model's maker's logo, read from its name: Claude's, OpenAI's under
 *  Codex, or Cursor's own. */
export function modelLogo(model: string): string | undefined {
  const name = model.toLowerCase();
  if (name.startsWith("claude")) return claudeLogo;
  if (/^(gpt|o\d|codex)/.test(name)) return codexLogo;
  if (/^(cursor|composer)/.test(name)) return cursorLogo;
  return undefined;
}

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

/** The most models the activity chart names; the rest stack as Other. */
const MODEL_SLOTS = 4;
const OTHER = "\u0000other";

function Activity({ data }: { data: Analytics }) {
  const [metric, setMetric] = useState<"sessions" | "active">("sessions");
  const [by, setBy] = useState<"agent" | "model">("agent");
  const value = (part?: { sessions: number; active_seconds: number }) =>
    metric === "sessions" ? (part?.sessions ?? 0) : (part?.active_seconds ?? 0);
  const withModels = data.daily.some((d) => d.models);
  const byModel = by === "model" && withModels;
  // The models with the most of the metric in the window, in that order.
  const totals = new Map<string, number>();
  for (const day of data.daily)
    for (const [model, part] of Object.entries(day.models ?? {}))
      totals.set(model, (totals.get(model) ?? 0) + value(part));
  const ranked = [...totals].filter(([, n]) => n > 0).sort((a, b) => b[1] - a[1]);
  const named = ranked.slice(0, ranked.length > MODEL_SLOTS ? MODEL_SLOTS - 1 : MODEL_SLOTS);
  const agents = AGENTS.filter((a) =>
    data.daily.some((d) => (d.agents[a]?.sessions ?? 0) + (d.agents[a]?.active_seconds ?? 0) > 0),
  );
  const series: Series[] = byModel
    ? [
        ...named.map(([model], i) => ({
          key: model,
          label: model,
          color: slot(i),
          icon: modelLogo(model),
        })),
        ...(ranked.length > named.length ? [{ key: OTHER, label: "Other", color: MUTED }] : []),
      ]
    : agentSeries(agents.length ? agents : ["claude_code"]);
  const modelValue = (day: Analytics["daily"][number], key: string) =>
    key === OTHER
      ? Object.entries(day.models ?? {})
          .filter(([model]) => !named.some(([name]) => name === model))
          .reduce((sum, [, part]) => sum + value(part), 0)
      : value(day.models?.[key]);
  const every = data.daily.length <= 7 ? 1 : data.daily.length <= 30 ? 7 : 14;
  const last = data.daily.length - 1;
  const columns: Column[] = data.daily.map((day, i) => ({
    key: day.date,
    tick: (last - i) % every === 0 ? dayTick.format(localDate(day.date)) : "",
    title: dayTitle.format(localDate(day.date)),
    values: Object.fromEntries(
      series.map((s) => [s.key, byModel ? modelValue(day, s.key) : value(day.agents[s.key])]),
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
        <div className="analytics-switches">
          {withModels ? (
            <SegmentedControl
              label="Split by"
              value={by}
              onChange={setBy}
              items={[
                { value: "agent", label: "Harness" },
                { value: "model", label: "Model" },
              ]}
            />
          ) : null}
          <SegmentedControl
            label="Show"
            value={metric}
            onChange={setMetric}
            items={[
              { value: "sessions", label: "Sessions" },
              { value: "active", label: "Active time" },
            ]}
          />
        </div>
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
            label={`${metric === "sessions" ? "Sessions" : "Active time"} per day by ${byModel ? "model" : "agent"}`}
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
                          {c.values[s.key] === null ? "—" : format(c.values[s.key] ?? 0)}
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

type Stat = {
  label: string;
  value: string | null;
  reason?: string;
  /** This week's count and the week before's, shown beside the figure. */
  trend?: { week: number; previous: number };
};

function Stats({ items }: { items: Stat[] }) {
  return (
    <dl className="analytics-stats">
      {items.map((item) => (
        <div key={item.label}>
          <dt>{item.label}</dt>
          <dd>
            <Figure value={item.value} reason={item.reason} />
            {item.trend ? <Trend {...item.trend} /> : null}
          </dd>
        </div>
      ))}
    </dl>
  );
}

/** This week's count beside a figure, green when it beat the week before. */
function Trend({ week, previous }: { week: number; previous: number }) {
  const up = week > previous;
  return (
    <span
      className="analytics-trend"
      data-up={up || undefined}
      title={`${formatCount(week)} in the last 7 days, ${formatCount(previous)} in the 7 before`}
    >
      {up ? "↑ " : ""}
      {formatCount(week)} this week
      <span className="sr-only">, {formatCount(previous)} the 7 days before</span>
    </span>
  );
}

/** What baum did for the person: what it generated, pushed, merged and
 *  set off, never what failed (Jobs has that). */
function BaumWork({ data }: { data: Analytics }) {
  const b = data.baum;
  const noLedger = "Recorded from this version of baum on.";
  const trend = (key: keyof BaumWeek) => {
    const week = b.weeks?.this[key];
    const previous = b.weeks?.previous[key];
    return week == null || previous == null ? undefined : { week, previous };
  };
  const items: Stat[] = [
    {
      label: "Reviews generated",
      value: formatCount(b.reviews_completed ?? b.reviews + b.group_reviews),
      trend: trend("reviews"),
    },
    { label: "Fixes pushed", value: formatCount(b.fixes_pushed), trend: trend("fixes_pushed") },
    {
      label: "Threads resolved",
      value: formatCount(b.threads_resolved),
      trend: trend("threads_resolved"),
    },
    {
      label: "Merged through baum",
      value: b.merges === null ? null : formatCount(b.merges),
      reason: noLedger,
      trend: trend("merges"),
    },
    {
      label: "Branches updated",
      value: b.branch_updates === null ? null : formatCount(b.branch_updates),
      reason: noLedger,
      trend: trend("branch_updates"),
    },
    {
      label: "Checks re-run",
      value: b.check_reruns === null ? null : formatCount(b.check_reruns),
      reason: noLedger,
      trend: trend("check_reruns"),
    },
    ...(b.decisions
      ? [
          {
            label: "Decision capture runs",
            value: formatCount(b.decisions.succeeded),
            trend: trend("decisions"),
          },
        ]
      : []),
  ];
  return (
    <Card id="analytics-baum" title="baum's work" detail="What baum did for you">
      {b.available ? (
        <Stats items={items} />
      ) : (
        <p className="analytics-note">The jobs list is not available.</p>
      )}
    </Card>
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

/** The window's days in the spans a chart of it draws: each day of a week,
 *  else each week from the window's first day, the current one partial. */
function spansOf(dates: string[]): string[][] {
  const size = dates.length <= 7 ? 1 : 7;
  const spans: string[][] = [];
  for (let start = 0; start < dates.length; start += size)
    spans.push(dates.slice(start, start + size));
  return spans;
}

/** The middle value, or the mean of the two middle ones, as the daemon's
 *  medians; null when there are none. */
const median = (values: number[]) => {
  const sorted = [...values].sort((a, b) => a - b);
  const half = Math.floor(sorted.length / 2);
  if (!sorted.length) return null;
  return sorted.length % 2 ? sorted[half]! : (sorted[half - 1]! + sorted[half]!) / 2;
};

/** Pull requests created and shipped, the merge rate and the time to merge,
 *  the person's own or the team's, as the hosted velocity charts draw them,
 *  from the pull requests the Timeline's feature log keeps. */
function Flow({ data }: { data: Analytics }) {
  const flow = data.flow;
  const [whose, setWhose] = useState<"mine" | "team">("mine");
  if (!flow) return null;
  const own = flow.yours ? whose : "team";
  const byDay = new Map(flow.days.map((day) => [day.date, day[own]]));
  const spans = spansOf(flow.days.map((day) => day.date));
  const weekly = spans.some((span) => span.length > 1);
  // A quarter's thirteen weeks name every other one, from the first.
  const every = spans.length > 7 ? 2 : 1;
  const sums = spans.map((span) => {
    const counts = span.map((date) => byDay.get(date)!);
    return {
      opened: counts.reduce((n, c) => n + c.opened, 0),
      openedMerged: counts.reduce((n, c) => n + c.opened_merged, 0),
      merged: counts.reduce((n, c) => n + c.merged, 0),
      seconds: counts.flatMap((c) => c.merge_seconds),
    };
  });
  const columns = (values: (i: number) => Column["values"]): Column[] =>
    spans.map((span, i) => {
      const first = localDate(span[0]!);
      return {
        key: span[0]!,
        tick: i % every === 0 ? dayTick.format(first) : "",
        title: weekly
          ? `${dayTick.format(first)} – ${dayTick.format(localDate(span[span.length - 1]!))}`
          : dayTitle.format(first),
        values: values(i),
      };
    });
  const created = { key: "created", label: "Created", color: slot(0) };
  const shipped = { key: "shipped", label: "Shipped", color: slot(1) };
  const rate = { key: "rate", label: "Merge rate", color: slot(0) };
  const wait = { key: "wait", label: "Median time to merge", color: slot(1) };
  const waitTop = niceMax(Math.max(0, ...sums.map((sum) => median(sum.seconds) ?? 0)), 3600);
  const per = weekly ? "week" : "day";
  const empty = sums.every((sum) => sum.opened + sum.merged === 0);
  return (
    <Card
      id="analytics-flow"
      title="Pull requests"
      detail={`Created and shipped each ${per}${own === "team" ? ", by everyone" : ", yours"}`}
      actions={
        flow.yours ? (
          <SegmentedControl
            label="Whose"
            value={whose}
            onChange={setWhose}
            items={[
              { value: "mine", label: "Yours" },
              { value: "team", label: "Team" },
            ]}
          />
        ) : null
      }
    >
      {!flow.available ? (
        <p className="analytics-note">
          The Timeline keeps the pull requests these charts read; it is not available on this
          computer.
        </p>
      ) : empty ? (
        <div className="analytics-empty">
          <h3>No pull requests in this window</h3>
          <p>Pull requests created or merged in your picked repositories count here.</p>
        </div>
      ) : (
        <>
          <ColumnChart
            label={`Pull requests created and shipped by ${per}`}
            columns={columns((i) => ({ created: sums[i]!.opened, shipped: sums[i]!.merged }))}
            series={[created, shipped]}
            format={(v) => formatCount(v)}
            grouped
          />
          <Legend series={[created, shipped]} />
          <TwoAxisChart
            label={`Merge rate and median time to merge by ${per}`}
            axes={[
              { series: rate, format: (v) => formatShare(v), top: 1 },
              { series: wait, format: (v) => (v ? (waitText(v) ?? "—") : "0"), top: waitTop },
            ]}
            points={columns(() => ({})).map((column, i) => ({
              key: column.key,
              tick: column.tick,
              title: column.title,
              values: [
                sums[i]!.opened ? sums[i]!.openedMerged / sums[i]!.opened : null,
                median(sums[i]!.seconds),
              ],
            }))}
          />
          <Legend series={[rate, wait]} line />
        </>
      )}
      {flow.unlisted.length ? (
        <p className="analytics-note">
          Not counted: {flow.unlisted.join(", ")}. The Timeline reads a repository's pull requests
          once its decisions are on and it has a clone.
        </p>
      ) : null}
      {flow.unread?.length ? (
        <p className="analytics-note">
          Could not read the pull requests of {flow.unread.join(", ")}. They are tried again on the
          next refresh.
        </p>
      ) : null}
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
      <Flow data={data} />
      <div className="analytics-pair">
        <Delivery data={data} />
        <Hours data={data} />
      </div>
      <div className="analytics-pair">
        <PlanUsage data={data} />
        <Longest data={data} />
      </div>
      <BaumWork data={data} />
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
  const reread = useReread();
  const query = `days=${days}${repo ? `&repo=${encodeURIComponent(repo)}` : ""}${agent ? `&agent=${agent}` : ""}`;
  const shown = useRef("");
  useEffect(() => {
    const abort = new AbortController();
    const count = reread.count;
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
        if (!abort.signal.aborted) {
          setLoading(false);
          reread.settled(count);
        }
      }
    };
    void read();
    return () => {
      abort.abort();
      clearTimeout(timer);
    };
  }, [query, attempt, reread.count]);
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
        actions={<RefreshButton refresh={reread.again} />}
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
