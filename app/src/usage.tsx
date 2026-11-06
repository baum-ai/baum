import { cost, type Accounting, type Usage } from "./api";
import { formatCount } from "@baum/viewer/components/format";
/** Why a cost is unknown. Older records may carry reasons no longer given. */
const reasons: Record<string, string> = {
  metadata_pending_redaction: "Model metadata will appear after its secret masking finishes.",
  actual_model_not_reported:
    "The provider did not report the actual model; only the configured model is known.",
  usage_not_reported: "Provider usage was not reported.",
  usage_incomplete: "Provider usage is incomplete; shown counts are reported subtotals.",
  model_not_reported: "The provider did not report a model.",
  speed_not_reported: "The provider did not report its pricing speed.",
  cache_ttl_not_reported: "The provider did not report cache creation TTL.",
  model_or_prompt_rates_unknown: "The price list has no rates for the models used.",
  billed_by_cursor_subscription: "Billed by your Cursor subscription; baum can't price it.",
};
/** What a cost shown as a lower bound ("≥") leaves out or prices low. */
const lowerBound: Record<string, string> = {
  usage_incomplete:
    "Some usage is not reported yet or was cut off; the cost covers what was reported.",
  model_or_prompt_rates_unknown:
    "Some usage has no exact rate (a model, speed or prompt tier the price list does not cover) and is priced at the lowest rate or left out.",
  speed_not_reported: "The provider did not report its pricing speed, so standard rates are used.",
  cache_ttl_not_reported:
    "The provider did not report the cache TTL, so cache writes are priced at the 5-minute rate.",
  unpriced_calls: "Some calls could not be priced.",
};
const configured =
  "Estimated using the configured model's API rates because the provider did not report the actual model.";
function promptRange(usage: Usage) {
  return usage.MinPrompt == null || usage.MaxPrompt == null
    ? null
    : `${formatCount(usage.MinPrompt)}–${formatCount(usage.MaxPrompt)}`;
}
function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt>{label}</dt>
      <dd>{value}</dd>
    </div>
  );
}
type UsageProps = {
  accounting?: Accounting;
  charged: number | undefined;
  amount: number | null;
  reason?: string;
};
/** The figures: cost, budget and processed tokens, then the provider's
 *  buckets when it reported them. */
function Figures({ accounting, charged, amount, reason }: UsageProps) {
  const usage = accounting?.usage;
  return (
    <>
      <Stat
        label="At API prices"
        value={
          amount == null
            ? "Unknown"
            : `${lowerBound[reason ?? ""] ? "≥ " : ""}${cost(amount)}${reason === "estimated_from_configured_model" ? " (estimated)" : ""}`
        }
      />
      <Stat label="Budget tokens" value={formatCount(charged)} />
      {usage ? (
        <Stat
          label={usage.Unreported ? "Processed (partial)" : "Processed tokens"}
          value={formatCount(usage.Input + usage.Output + usage.CacheRead + usage.CacheCreate)}
        />
      ) : null}
    </>
  );
}
function Buckets({ usage }: { usage: Usage }) {
  return (
    <>
      <Stat label="Input" value={formatCount(usage.Input)} />
      <Stat label="Output" value={formatCount(usage.Output)} />
      <Stat label="Cache reads" value={formatCount(usage.CacheRead)} />
      <Stat label="Cache writes" value={formatCount(usage.CacheCreate)} />
    </>
  );
}
/** What qualifies the figures: why a cost is unknown or a lower bound, how
 *  cache reads count, what else the provider reported, and each attempt. */
function Notes({ accounting, amount, reason }: UsageProps) {
  const usage = accounting?.usage;
  const bound = lowerBound[reason ?? ""];
  const note =
    amount == null
      ? (reasons[reason ?? ""] ?? "Exact pricing is unavailable for this record.")
      : bound
        ? `${bound} The cost is at least the amount shown.`
        : reason === "estimated_from_configured_model"
          ? configured
          : null;
  // Only what the provider reported; unknown extras are left out rather than listed.
  const extras = usage
    ? [
        usage.Speed ? `Speed ${usage.Speed}` : "",
        usage.CacheCreate1h == null
          ? ""
          : `1-hour cache writes ${formatCount(usage.CacheCreate1h)}`,
        promptRange(usage) ? `Prompts ${promptRange(usage)} tokens` : "",
      ].filter(Boolean)
    : [];
  return (
    <>
      {note ? <p>{note}</p> : null}
      {usage ? (
        <>
          <p>
            Cache reads are billed separately and don't count toward budget tokens.
            {usage.Unreported ? " The provider reported only part of the usage." : ""}
          </p>
          {extras.length ? <p>{extras.join(" · ")}</p> : null}
        </>
      ) : (
        <p>Provider token breakdown unavailable for this record.</p>
      )}
      {accounting?.attempts.length ? <Attempts accounting={accounting} /> : null}
    </>
  );
}
function Attempts({ accounting }: { accounting: Accounting }) {
  return (
    <details className="local-usage-attempts">
      <summary>
        {accounting.attempts.length}{" "}
        {accounting.attempts.length === 1 ? "model attempt" : "model attempts"}
      </summary>
      <table>
        <thead>
          <tr>
            <th>Model</th>
            <th>Input</th>
            <th>Output</th>
            <th>Cache reads</th>
            <th>Cache writes</th>
          </tr>
        </thead>
        <tbody>
          {accounting.attempts.map((a, i) => (
            <tr key={i}>
              <td>
                {a.model || "Not reported"}
                {a.model_source === "configured" ? " (configured)" : ""}
                {a.failed ? " · failed" : ""}
                {a.usage.Unreported ? " · incomplete" : ""}
              </td>
              <td>{formatCount(a.usage.Input)}</td>
              <td>{formatCount(a.usage.Output)}</td>
              <td>{formatCount(a.usage.CacheRead)}</td>
              <td>{formatCount(a.usage.CacheCreate)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </details>
  );
}
export function UsageDetails(props: UsageProps) {
  const usage = props.accounting?.usage;
  return (
    <div className="local-usage">
      <dl className="local-usage-totals">
        <Figures {...props} />
      </dl>
      {usage ? (
        <dl className="local-usage-buckets">
          <Buckets usage={usage} />
        </dl>
      ) : null}
      <Notes {...props} />
    </div>
  );
}
/** A job page's usage: every figure on one line under the page's title, what
 *  qualifies them folded below. */
export function UsageBar(props: UsageProps) {
  const usage = props.accounting?.usage;
  return (
    <section aria-label="Usage" className="local-usage local-usage-bar">
      <dl>
        <Figures {...props} />
        {usage ? (
          <>
            <span aria-hidden="true" className="local-usage-divider" />
            <Buckets usage={usage} />
          </>
        ) : null}
      </dl>
      <details className="local-usage-more">
        <summary>About these figures</summary>
        <Notes {...props} />
      </details>
    </section>
  );
}
