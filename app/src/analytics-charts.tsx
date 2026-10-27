import { useId, useState, type ReactNode } from "react";
import { seriesColor } from "@baum/viewer/series";

/** A series: its identity, the fixed colour slot it wears and its label. */
export type Series = { key: string; label: string; color: string };

/** One column of a column chart: its axis label (or "" to leave the tick
 *  out), what its tooltip is headed with, and its value per series. */
export type Column = {
  key: string;
  tick: string;
  title: string;
  values: Record<string, number>;
};

/** The top of an axis: the smallest 1, 2 or 5 times a power of ten at or above
 *  the largest value, so the ticks read as round numbers. In `unit` steps
 *  (3600 for hours) when the values are durations. */
export function niceMax(max: number, unit = 1): number {
  if (!(max > 0)) return unit;
  const scaled = max / unit;
  const power = 10 ** Math.floor(Math.log10(scaled));
  for (const step of [1, 2, 5, 10]) {
    if (step * power >= scaled) return step * power * unit;
  }
  return 10 * power * unit;
}

/** A tooltip's readout: the value leads, the series follows, keyed by a short
 *  stroke of its colour. */
function Readout({ title, rows }: { title: string; rows: { series: Series; value: string }[] }) {
  return (
    <>
      <span className="analytics-tip-title">{title}</span>
      {rows.map(({ series, value }) => (
        <span key={series.key} className="analytics-tip-row">
          <span
            aria-hidden="true"
            className="analytics-key-line"
            style={{ background: series.color }}
          />
          <strong>{value}</strong>
          <span>{series.label}</span>
        </span>
      ))}
    </>
  );
}

/** The legend under a chart of two or more series: a swatch of each mark. */
export function Legend({ series, line = false }: { series: Series[]; line?: boolean }) {
  if (series.length < 2) return null;
  return (
    <ul className="analytics-legend" aria-label="Legend">
      {series.map((s) => (
        <li key={s.key}>
          <span
            aria-hidden="true"
            className={line ? "analytics-key-line" : "analytics-key-box"}
            style={{ background: s.color }}
          />
          {s.label}
        </li>
      ))}
    </ul>
  );
}

/** Stacked columns over a shared baseline, drawn in HTML so they stay crisp at
 *  any width and take the theme's tokens. Each column is a focusable hit
 *  target wider than its bar, with a tooltip on hover and focus; the table the
 *  caller renders beside it holds every value too. */
export function ColumnChart({
  label,
  columns,
  series,
  format,
  unit = 1,
  height = 160,
}: {
  label: string;
  columns: Column[];
  series: Series[];
  format: (value: number) => string;
  unit?: number;
  height?: number;
}) {
  const [active, setActive] = useState<number | null>(null);
  const totals = columns.map((c) => series.reduce((sum, s) => sum + (c.values[s.key] ?? 0), 0));
  const top = niceMax(Math.max(0, ...totals), unit);
  // A count's half-way tick is drawn only when it is a whole number.
  const ticks = unit > 1 || Number.isInteger(top / 2) ? [top, top / 2, 0] : [top, 0];
  return (
    <figure className="analytics-chart" aria-label={label}>
      <div className="analytics-plot" style={{ height }}>
        {ticks.map((tick) => (
          <div
            key={tick}
            aria-hidden="true"
            className="analytics-grid"
            style={{ bottom: `${(tick / top) * 100}%` }}
          >
            <span>{format(tick)}</span>
          </div>
        ))}
        <div className="analytics-columns" onPointerLeave={() => setActive(null)}>
          {columns.map((column, i) => {
            const total = totals[i]!;
            const parts = series.filter((s) => (column.values[s.key] ?? 0) > 0);
            const description = `${column.title}: ${
              parts.length
                ? parts.map((s) => `${format(column.values[s.key]!)} ${s.label}`).join(", ")
                : format(0)
            }`;
            return (
              <div
                key={column.key}
                className="analytics-column"
                tabIndex={0}
                role="img"
                aria-label={description}
                data-active={active === i || undefined}
                onPointerEnter={() => setActive(i)}
                onFocus={() => setActive(i)}
                onBlur={() => setActive(null)}
              >
                {total > 0 ? (
                  <div className="analytics-stack" style={{ height: `${(total / top) * 100}%` }}>
                    {parts.map((s) => (
                      <div
                        key={s.key}
                        style={{ flexGrow: column.values[s.key], background: s.color }}
                      />
                    ))}
                  </div>
                ) : null}
                {active === i ? (
                  <div
                    role="presentation"
                    className="analytics-tip"
                    data-side={i > columns.length / 2 ? "left" : "right"}
                  >
                    <Readout
                      title={column.title}
                      rows={(parts.length ? parts : series.slice(0, 1)).map((s) => ({
                        series: s,
                        value: format(column.values[s.key] ?? 0),
                      }))}
                    />
                  </div>
                ) : null}
              </div>
            );
          })}
        </div>
      </div>
      <div aria-hidden="true" className="analytics-ticks">
        {columns.map((c) => (
          <span key={c.key}>{c.tick}</span>
        ))}
      </div>
    </figure>
  );
}

/** A point of a line: when, and its value in 0..100. */
export type Point = { at: number; value: number };

/** Step lines of percentages over a time span, one per series, with the limit
 *  at the top. The pointer's time picks every series' figure then. */
export function StepChart({
  label,
  lines,
  start: windowStart,
  end,
  height = 120,
}: {
  label: string;
  lines: { series: Series; points: Point[] }[];
  start: number;
  end: number;
  height?: number;
}) {
  // Sampling began when baum started to: the axis starts shortly before the
  // first sample rather than leaving the window's first weeks empty.
  const first = Math.min(...lines.flatMap((l) => l.points.map((p) => p.at)));
  const start = Number.isFinite(first)
    ? Math.max(windowStart, first - Math.max(3600_000, (end - first) / 20))
    : windowStart;
  const [hover, setHover] = useState<number | null>(null);
  const clip = useId();
  const span = Math.max(1, end - start);
  const x = (at: number) => ((Math.min(end, Math.max(start, at)) - start) / span) * 1000;
  const y = (value: number) => 100 - Math.min(100, Math.max(0, value));
  const path = (points: Point[]) => {
    let d = "";
    points.forEach((p, i) => {
      d += i === 0 ? `M${x(p.at)} ${y(p.value)}` : `H${x(p.at)}V${y(p.value)}`;
    });
    // A figure holds until the next sample; the last one reaches now.
    return points.length ? `${d}H${x(end)}` : "";
  };
  const at = hover === null ? null : start + (hover / 1000) * span;
  const readout =
    at === null
      ? []
      : lines.flatMap(({ series, points }) => {
          const last = points.filter((p) => p.at <= at).at(-1);
          return last ? [{ series, value: `${Math.round(last.value)}%` }] : [];
        });
  return (
    <figure className="analytics-chart" aria-label={label}>
      <div
        className="analytics-plot analytics-step"
        style={{ height }}
        onPointerMove={(event) => {
          const box = event.currentTarget.getBoundingClientRect();
          setHover(Math.min(1000, Math.max(0, ((event.clientX - box.left) / box.width) * 1000)));
        }}
        onPointerLeave={() => setHover(null)}
      >
        {[100, 50, 0].map((tick) => (
          <div
            key={tick}
            aria-hidden="true"
            className="analytics-grid"
            style={{ bottom: `${tick}%` }}
          >
            <span>{tick === 100 ? "Limit" : `${tick}%`}</span>
          </div>
        ))}
        <svg
          aria-hidden="true"
          viewBox="0 0 1000 100"
          preserveAspectRatio="none"
          className="analytics-lines"
        >
          <clipPath id={clip}>
            <rect x="0" y="-2" width="1000" height="104" />
          </clipPath>
          <g clipPath={`url(#${clip})`}>
            {lines.map(({ series, points }) => (
              <path
                key={series.key}
                d={path(points)}
                fill="none"
                stroke={series.color}
                strokeWidth={2}
                strokeLinejoin="round"
                strokeLinecap="round"
                vectorEffect="non-scaling-stroke"
              />
            ))}
          </g>
        </svg>
        {hover !== null ? (
          <>
            <div
              aria-hidden="true"
              className="analytics-crosshair"
              style={{ left: `${hover / 10}%` }}
            />
            {readout.length ? (
              <div
                role="presentation"
                className="analytics-tip"
                data-side={hover > 500 ? "left" : "right"}
                style={{ left: `${hover / 10}%` }}
              >
                <Readout title={new Date(at!).toLocaleString()} rows={readout} />
              </div>
            ) : null}
          </>
        ) : null}
      </div>
      <div aria-hidden="true" className="analytics-ticks analytics-span">
        <span>
          {new Date(start).toLocaleString(undefined, {
            month: "short",
            day: "numeric",
            hour: "numeric",
          })}
        </span>
        <span>Now</span>
      </div>
    </figure>
  );
}

/** The colour of the nth series in its fixed order. */
export const slot = seriesColor;

/** A table's figure cell: the value, or the em dash with why it is unknown. */
export function Figure({
  value,
  reason,
  children,
}: {
  value: string | null;
  reason?: string;
  children?: ReactNode;
}) {
  if (value === null) {
    return (
      <span title={reason} className="analytics-unknown">
        —{reason ? <span className="sr-only"> ({reason})</span> : null}
      </span>
    );
  }
  return (
    <span title={reason}>
      {value}
      {children}
    </span>
  );
}
