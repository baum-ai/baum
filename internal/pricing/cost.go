package pricing

// Canonical vendor slugs this package reasons about. They are
// `agent_session_ingests.vendor` values, which is what analytics groups by;
// the recorder that delivered the session (upload, Entire checkpoint) never
// reaches here, because it says nothing about how the tokens were counted.
const (
	VendorClaudeCode = "claude_code"
	VendorCodex      = "codex"
)

// Speed is the service speed a request was billed at.
type Speed string

// Speeds. Unreported speed is priced as standard, which is how a caller that
// does not record speed has always been priced; fast mode has its own rates;
// ultrafast is a separate, pricier tier that the price list has no rates for.
const (
	SpeedUnknown   Speed = ""
	SpeedStandard  Speed = "standard"
	SpeedFast      Speed = "fast"
	SpeedUltrafast Speed = "ultrafast"
)

// Usage is one rollup's raw token columns, exactly as
// agent_session_model_invocations and agent_session_token_usage store them.
type Usage struct {
	Vendor           string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	// CacheWrite1hTokens is the part of CacheWriteTokens written with the
	// 1-hour TTL; 0 when unknown or none. The rest are 5-minute writes.
	CacheWrite1hTokens int64
	// Speed is the service speed the request was billed at; SpeedUnknown (the
	// zero value) when the vendor did not report it.
	Speed Speed
	// SingleRequest is true when the usage is exactly one API request, so its
	// prompt is PromptTokens() and the prompt range below is not needed.
	SingleRequest bool
	// PromptRangeKnown says MinPromptTokens and MaxPromptTokens are set: the
	// smallest and largest prompt (by the PromptTokens rule) among the single
	// requests the usage covers. A caller that sums rows can know them without
	// knowing each request, for instance from a SQL group whose rows all sit on
	// one side of every tier threshold. Without it, and without SingleRequest,
	// the range is [0, PromptTokens()]: each request's prompt is at most the
	// combined prompt, and nothing bounds it from below.
	PromptRangeKnown bool
	MinPromptTokens  int64
	MaxPromptTokens  int64
}

// promptRange is the smallest and largest prompt of the requests the usage
// covers.
func (u Usage) promptRange() (lo, hi int64) {
	switch {
	case u.SingleRequest:
		prompt := u.PromptTokens()
		return prompt, prompt
	case u.PromptRangeKnown:
		return u.MinPromptTokens, u.MaxPromptTokens
	}
	return 0, u.PromptTokens()
}

// PromptTokens is the size of the prompt a request was billed for, which
// decides whether a long-prompt tier applies. Codex's input_tokens already
// includes its cached input, so it is the prompt; for every other vendor the
// buckets are disjoint and the prompt is their sum. This is the single place
// to change if a vendor documents a different rule.
func (u Usage) PromptTokens() int64 {
	if u.Vendor == VendorCodex {
		return u.InputTokens
	}
	return u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// UncachedInput applies the vendor-dependent cache-subsumption rule.
//
// Codex's input_tokens already includes its cached input, reads and writes
// alike; Anthropic's buckets are disjoint. The token columns are stored raw, so
// any rollup that skips this branch charges a codex session for its cache
// reads (and writes) twice, inflating its cost by the cache-hit rate, which on
// a long session is 3-5x and still a believable dollar figure.
func (u Usage) UncachedInput() int64 {
	if u.Vendor == VendorCodex {
		return max(0, u.InputTokens-u.CacheReadTokens-u.CacheWriteTokens)
	}
	return u.InputTokens
}

// Cost prices one rollup. ok is false when the model has no rate or the usage
// cannot be matched to one (see List.Cost), in which case the caller must
// report the tokens without a dollar figure rather than treating the cost as
// zero.
func Cost(model Model, usage Usage) (float64, bool) { return Active().Cost(model, usage) }

// RatesFor is the rate set Cost applies to the usage: the base, fast or
// long-prompt tier rates its speed and prompt range select. ok is false where
// Cost reports not priced for the model, speed or prompt range (an unknown
// model, ultrafast, fast with no fast rates, a range straddling a threshold, an
// inverted range), so a caller estimating before a request can tell "priced at
// these rates" from "would not be priced" and never falls back to base rates.
func (l *List) RatesFor(model Model, usage Usage) (RateSet, bool) {
	if !model.Known() {
		return RateSet{}, false
	}
	entry, ok := l.entry(model.Canonical)
	if !ok {
		return RateSet{}, false
	}
	lo, hi := usage.promptRange()
	if lo < 0 || hi < lo {
		return RateSet{}, false
	}
	rates := entry.Base()
	switch {
	case usage.Speed == SpeedUltrafast:
		// The list carries no ultrafast rates (OpenAI sells that tier at a
		// much higher price than fast mode), so the request is not priced
		// rather than priced at the wrong rate.
		return RateSet{}, false
	case usage.Speed == SpeedFast:
		if entry.Fast.IsZero() {
			return RateSet{}, false
		}
		threshold := entry.Above.PromptTokens
		if !entry.FastAbove.IsZero() {
			threshold = entry.FastAbove.PromptTokens
		}
		switch {
		case entry.Above.IsZero() && entry.FastAbove.IsZero(), hi <= threshold:
			rates = entry.Fast
		case lo > threshold && !entry.FastAbove.IsZero():
			rates = entry.FastAbove.RateSet()
		default:
			// Straddling the threshold, or over it with no published fast rate.
			return RateSet{}, false
		}
	case !entry.Above.IsZero():
		switch {
		case hi <= entry.Above.PromptTokens:
		case lo > entry.Above.PromptTokens:
			rates = entry.Above.RateSet()
		default:
			return RateSet{}, false
		}
	}
	return rates, true
}

// Cost prices one rollup with the list's rates. A tier applies to a request
// whose prompt is over its threshold, so the usage's prompt range decides: at
// or under the threshold throughout is the base (or fast) rates, over it
// throughout is the tier's, and a range that straddles it is not priced,
// because the usage does not say which requests were long. It also reports not
// priced for fast mode on an entry without fast rates, for fast mode over a
// threshold the entry has no fast_above rates for, for ultrafast, for 1-hour
// writes where the chosen rates have no 1-hour price, for a 1-hour count that
// is negative or larger than the cache writes it is part of, and for an
// inverted or negative prompt range.
func (l *List) Cost(model Model, usage Usage) (float64, bool) {
	rates, ok := l.RatesFor(model, usage)
	if !ok {
		return 0, false
	}
	if usage.CacheWrite1hTokens < 0 || usage.CacheWrite1hTokens > usage.CacheWriteTokens {
		return 0, false
	}
	oneHour := usage.CacheWrite1hTokens
	fiveMinute := usage.CacheWriteTokens - oneHour
	if oneHour > 0 && rates.CacheWrite1h == 0 {
		return 0, false
	}
	const perMillion = 1_000_000.0
	cost := float64(usage.UncachedInput())*rates.Input +
		float64(usage.OutputTokens)*rates.Output +
		float64(usage.CacheReadTokens)*rates.CacheRead +
		float64(fiveMinute)*rates.CacheWrite +
		float64(oneHour)*rates.CacheWrite1h
	return cost / perMillion, true
}
