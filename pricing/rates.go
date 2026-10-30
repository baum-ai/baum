package pricing

// The rates themselves live in prices.json; review that file as data. What
// stays here is what the numbers cannot say.
//
// Every rate is listed explicitly rather than derived from the input rate.
// Most vendors read cache at 0.1× input and write it at 1.25×, but enough do
// not (Opus 5.5 reads at 0.05×, older OpenAI models at 0.25× or 0.5× and write
// for free) that a derivation would be a rule with a growing exception list.
//
// The 1-hour cache TTL costs 2× input rather than 1.25×. An entry lists it as
// cache_write_1h, and it is priced when the usage reports the 1-hour part of
// its cache writes (Usage.CacheWrite1hTokens). Usage that does not report the
// split is priced at the 5-minute rate, so a workload using 1-hour caching is
// under-reported on that one component until its source records the TTL.
//
// Anthropic figures are first-party API rates; partner-operated platforms
// (Bedrock, Vertex) price separately and are not modelled — a workload running
// there is reported at first-party rates and the difference is a known
// limitation, not a bug to hunt.

// knownUnpriced are models we recognise but deliberately do not price, because
// no rate is available to cite. They are distinct from unknown models: a model
// here has been looked at and left out on purpose, so it does not need to
// resurface every time somebody audits the coverage list. Its tokens are still
// reported; only its cost is absent.
var knownUnpriced = map[string]string{
	"codex-auto-review": "Codex's review model has no separately published rate",
	"synthetic":         "not a model — a synthetic invocation carrying a session-level aggregate",
	"claude-2":          "retired before this table existed",
	"claude-2-1":        "retired before this table existed",
	"claude-instant-1":  "retired before this table existed",
	"gemini-2-5-pro":    "no adapter reaches Postgres yet",
	"gemini-2-5-flash":  "no adapter reaches Postgres yet",
	"gemini-3-pro":      "no adapter reaches Postgres yet",
}

// UnpricedReason explains why a recognised model has no rate, or "" if the
// model is priced or unknown.
func UnpricedReason(model Model) string {
	if !model.Known() {
		return ""
	}
	return knownUnpriced[model.Canonical]
}
