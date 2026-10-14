package agent

// Usage uses disjoint buckets, as Anthropic reports them: Input excludes
// the cache reads and writes counted separately.
type Usage struct {
	Input, Output, CacheRead, CacheCreate int64
	// CacheCreate1h is the part of CacheCreate written with the 1-hour TTL and
	// Speed the speed ("standard" or "fast") every call ran at. Nil means the
	// agent did not report it, or its calls differed (a run spans many calls).
	CacheCreate1h *int64
	Speed         *string
	// MinPrompt and MaxPrompt bound the prompt (input plus cache reads and
	// writes) of the single model calls the usage covers, which decides each
	// call's long-prompt tier. They are set only when every call of the run
	// reported usage; nil means unknown, and the total is then priced with no
	// range. Codex's agent reports no per-call usage, so its range is unknown.
	MinPrompt, MaxPrompt *int64
	// Unreported means the backend could not report usage, so the zero buckets
	// are unknown rather than free.
	Unreported bool
}

// empty reports no usage at all, so the attempt or call neither decides the
// speed nor makes the 1-hour part unknown.
func (u Usage) empty() bool {
	return u.Input+u.Output+u.CacheRead+u.CacheCreate == 0
}

// IsZero reports whether nothing was reported: no tokens, no TTL split, no
// speed and no Unreported mark.
func (u Usage) IsZero() bool {
	return u.empty() && u.CacheCreate1h == nil && u.Speed == nil && !u.Unreported
}

// Add accumulates another attempt's usage: buckets sum. The 1-hour part is
// known only while every attempt that wrote to the cache reported a split;
// otherwise it is nil rather than an undercount. The speed survives only while
// every attempt that used tokens agrees on it. An attempt with no usage decides
// neither.
func (u *Usage) Add(other Usage) {
	unknown := (u.CacheCreate > 0 && u.CacheCreate1h == nil) || (other.CacheCreate > 0 && other.CacheCreate1h == nil)
	switch {
	case unknown:
		u.CacheCreate1h = nil
	case u.CacheCreate1h == nil:
		u.CacheCreate1h = other.CacheCreate1h
	case other.CacheCreate1h != nil:
		sum := *u.CacheCreate1h + *other.CacheCreate1h
		u.CacheCreate1h = &sum
	}
	switch {
	case other.empty():
	case u.empty():
		u.Speed = other.Speed
	case u.Speed == nil || other.Speed == nil || *u.Speed != *other.Speed:
		u.Speed = nil
	}
	switch {
	case other.empty():
	case u.empty():
		u.MinPrompt, u.MaxPrompt = other.MinPrompt, other.MaxPrompt
	case u.MinPrompt == nil || u.MaxPrompt == nil || other.MinPrompt == nil || other.MaxPrompt == nil:
		u.MinPrompt, u.MaxPrompt = nil, nil
	default:
		lo, hi := min(*u.MinPrompt, *other.MinPrompt), max(*u.MaxPrompt, *other.MaxPrompt)
		u.MinPrompt, u.MaxPrompt = &lo, &hi
	}
	u.Unreported = u.Unreported || other.Unreported
	if u.Unreported {
		// A call that reported nothing may have written to the cache, run at
		// another speed or sent a prompt outside the range, so none is known.
		u.CacheCreate1h, u.Speed, u.MinPrompt, u.MaxPrompt = nil, nil, nil, nil
	}
	u.Input += other.Input
	u.Output += other.Output
	u.CacheRead += other.CacheRead
	u.CacheCreate += other.CacheCreate
}

// ChargedTokens is what a run pays for in full: input, output and cache
// creation. Cache reads are not charged against a token budget.
func ChargedTokens(input, output, cacheCreation int) int { return input + output + cacheCreation }
