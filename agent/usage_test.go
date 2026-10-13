package agent

import "testing"

func TestUsageAddKeepsOnlyWhatEveryAttemptAgreesOn(t *testing.T) {
	fastUsage := Usage{Input: 1, CacheCreate: 5, CacheCreate1h: new(int64(2)), Speed: new("fast")}
	var total Usage
	total.Add(fastUsage)
	total.Add(fastUsage)
	if total.Input != 2 || total.CacheCreate != 10 || total.CacheCreate1h == nil || *total.CacheCreate1h != 4 || total.Speed == nil || *total.Speed != "fast" {
		t.Fatalf("two fast attempts = %+v", total)
	}
	// An attempt with no usage decides nothing.
	total.Add(Usage{})
	if total.CacheCreate1h == nil || *total.CacheCreate1h != 4 || total.Speed == nil || *total.Speed != "fast" {
		t.Fatalf("after an empty attempt = %+v", total)
	}
	total.Add(Usage{Input: 1, Speed: new("standard")})
	if total.CacheCreate1h == nil || *total.CacheCreate1h != 4 || total.Speed != nil {
		t.Fatalf("after a standard attempt = %+v, want the 1-hour sum kept (it wrote nothing) and no speed", total)
	}

	// Cache writes without a split make the 1-hour part unknown, whichever
	// order the attempts come in.
	for _, attempts := range [][]Usage{
		{{CacheCreate: 1000}, {CacheCreate: 500, CacheCreate1h: new(int64(500))}},
		{{CacheCreate: 500, CacheCreate1h: new(int64(500))}, {CacheCreate: 1000}},
		{{CacheCreate: 500, CacheCreate1h: new(int64(500))}, {CacheCreate: 1000}, {CacheCreate: 5, CacheCreate1h: new(int64(5))}},
	} {
		var sum Usage
		for _, attempt := range attempts {
			sum.Add(attempt)
		}
		if sum.CacheCreate1h != nil {
			t.Errorf("%+v summed to 1h %d, want unknown", attempts, *sum.CacheCreate1h)
		}
	}
}

// A run that includes an attempt without usage reporting stays unreported,
// and the speed, 1-hour split and prompt range become unknown, whichever
// order the attempts come in.
func TestUsageAddKeepsUnreported(t *testing.T) {
	reported := Usage{Input: 3, CacheCreate: 4, CacheCreate1h: new(int64(4)), Speed: new("fast"), MinPrompt: new(int64(7)), MaxPrompt: new(int64(7))}
	var total Usage
	total.Add(reported)
	if total.Unreported || total.IsZero() {
		t.Fatalf("reported usage = %+v", total)
	}
	for _, attempts := range [][]Usage{
		{reported, {Unreported: true}},
		{{Unreported: true}, reported},
		{reported, {Unreported: true}, reported},
	} {
		var sum Usage
		for _, attempt := range attempts {
			sum.Add(attempt)
		}
		if !sum.Unreported || sum.Input != 3*int64(len(attempts)-1) {
			t.Errorf("%+v summed to %+v, want unreported with the reported buckets", attempts, sum)
		}
		if sum.Speed != nil || sum.CacheCreate1h != nil || sum.MinPrompt != nil || sum.MaxPrompt != nil {
			t.Errorf("%+v summed to %+v, want no speed, 1-hour split or prompt range", attempts, sum)
		}
	}
	if (Usage{Unreported: true}).IsZero() {
		t.Fatal("unreported usage is not zero")
	}
}

func TestUsageAddMergesThePromptRange(t *testing.T) {
	ranged := func(in, lo, hi int64) Usage {
		return Usage{Input: in, MinPrompt: &lo, MaxPrompt: &hi}
	}
	var total Usage
	total.Add(ranged(100, 10, 60))
	total.Add(Usage{})
	total.Add(ranged(100, 5, 40))
	if total.MinPrompt == nil || *total.MinPrompt != 5 || *total.MaxPrompt != 60 {
		t.Fatalf("merged range = %v..%v, want 5..60", total.MinPrompt, total.MaxPrompt)
	}
	total.Add(Usage{Input: 50})
	if total.MinPrompt != nil || total.MaxPrompt != nil {
		t.Fatalf("an attempt without a range left %v..%v, want unknown", total.MinPrompt, total.MaxPrompt)
	}
}
