import { cost, type Accounting } from "./api";
import { formatCount } from "@baum/viewer/components/format";
const reasons: Record<string, string> = {
  metadata_pending_redaction: "Model metadata will appear after its secret masking finishes.",
  actual_model_not_reported:
    "The provider did not report the actual model; only the configured model is known.",
  usage_not_reported: "Provider usage was not reported.",
  usage_incomplete: "Provider usage is incomplete; shown counts are reported subtotals.",
  model_not_reported: "The provider did not report a model.",
  speed_not_reported: "The provider did not report its pricing speed.",
  cache_ttl_not_reported: "The provider did not report cache creation TTL.",
  model_or_prompt_rates_unknown: "Model rates or prompt pricing tiers could not be established.",
  billed_by_cursor_subscription: "Billed by your Cursor subscription; baum can't price it.",
};
export function UsageDetails({
  accounting,
  charged,
  amount,
  reason,
}: {
  accounting?: Accounting;
  charged: number | undefined;
  amount: number | null;
  reason?: string;
}) {
  const usage = accounting?.usage;
  return (
    <div className="text-xs space-y-1">
      <p>
        Charged-token budget counter {formatCount(charged)} · Recorded cost {cost(amount)}
      </p>
      {usage ? (
        <>
          <p>
            {usage.Unreported ? "Partial provider usage" : "Provider-reported usage"}: Input{" "}
            {formatCount(usage.Input)} · Output {formatCount(usage.Output)} · Cache reads{" "}
            {formatCount(usage.CacheRead)} · Cache creation {formatCount(usage.CacheCreate)}
          </p>
          <p>
            {usage.Unreported ? "Reported processed subtotal" : "Total processed tokens"}{" "}
            {formatCount(usage.Input + usage.Output + usage.CacheRead + usage.CacheCreate)}. Cache
            reads are billed separately and excluded from the charged-token budget counter.
          </p>
          <p>
            Cache creation at 1-hour TTL:{" "}
            {usage.CacheCreate1h == null ? "Unknown" : formatCount(usage.CacheCreate1h)} · Speed:{" "}
            {usage.Speed ?? "Unknown"} · Prompt range:{" "}
            {usage.MinPrompt == null || usage.MaxPrompt == null
              ? "Unknown"
              : `${formatCount(usage.MinPrompt)}–${formatCount(usage.MaxPrompt)}`}
          </p>
          <details>
            <summary>Model attempts ({accounting.attempts.length})</summary>
            {accounting.attempts.map((a, i) => (
              <p key={i}>
                Attempt {i + 1}: {a.model || "Model not reported"}
                {a.model_source === "configured" ? " (configured; actual model not reported)" : ""}
                {a.failed ? " (failed)" : ""} · Input {formatCount(a.usage.Input)} · Output{" "}
                {formatCount(a.usage.Output)} · Cache reads {formatCount(a.usage.CacheRead)} · Cache
                creation {formatCount(a.usage.CacheCreate)}
                {a.usage.Unreported ? " · Usage incomplete" : ""}
                {" · Speed: "}
                {a.usage.Speed ?? "Unknown"}
                {" · 1-hour cache creation: "}
                {formatCount(a.usage.CacheCreate1h)}
                {" · Prompt range: "}
                {a.usage.MinPrompt == null || a.usage.MaxPrompt == null
                  ? "Unknown"
                  : `${formatCount(a.usage.MinPrompt)}–${formatCount(a.usage.MaxPrompt)}`}
              </p>
            ))}
          </details>
        </>
      ) : (
        <p>Provider token breakdown unavailable for this record.</p>
      )}
      {amount == null ? (
        <p>
          Cost unknown: {reasons[reason ?? ""] ?? "Exact pricing is unavailable for this record."}
        </p>
      ) : null}
    </div>
  );
}
