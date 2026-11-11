package redact

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/goredact"
)

// memoCorpus holds texts with secrets and addresses, and some without.
var memoCorpus = []string{
	"mail " + email + " and use " + plantedURL + " for the tests",
	plantedKey,
	"export AWS_ACCESS_KEY_ID=AKIA" + "Z3MQ5TNV2XWB7KDL",
	"Authorization: Bearer " + "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.c2lnbmF0dXJlLXZhbHVl",
	"ghp_" + "Xq3vT7mK9pL2wR5nB8cF1hJ4dG6sA0eY1uZ3",
	"just an ordinary line about the diff",
	"Edit src/main.go",
	strings.Repeat("a long pasted prompt line ", 400) + plantedURL,
}

// secretsIn are what memoCorpus plants, none of which a mask may keep.
var secretsIn = []string{"Tr0ub4dor", "ada.lovelace", "MIIEpAIBAAKCAQEA7Qx1", "Z3MQ5TNV2XWB7KDL", "eyJzdWIiOiIxMjM0In0", "Xq3vT7mK9pL2wR5nB8cF1hJ4dG6sA0eY1uZ3"}

func TestMemoMatchesTheEngine(t *testing.T) {
	e, err := Shared()
	if err != nil {
		t.Fatal(err)
	}
	m := NewMemo(64, 1<<20)
	ctx := context.Background()
	for round := range 3 {
		for _, text := range memoCorpus {
			want, _, err := e.RedactContext(ctx, text)
			if err != nil {
				t.Fatal(err)
			}
			got, err := m.Redact(ctx, e, text)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("round %d: memo = %q, engine = %q", round, got, want)
			}
			for _, secret := range secretsIn {
				if strings.Contains(got, secret) {
					t.Fatalf("round %d: %q survived in %q", round, secret, got)
				}
			}
		}
	}
	if got := m.Len(); got != len(memoCorpus) {
		t.Fatalf("remembered %d texts, want %d", got, len(memoCorpus))
	}
}

// The memo is keyed by a hash: no entry holds an input.
func TestMemoKeepsNoInput(t *testing.T) {
	e, err := Shared()
	if err != nil {
		t.Fatal(err)
	}
	m := NewMemo(64, 1<<20)
	for _, text := range memoCorpus {
		if _, err := m.Redact(context.Background(), e, text); err != nil {
			t.Fatal(err)
		}
	}
	for el := m.order.Front(); el != nil; el = el.Next() {
		for _, secret := range secretsIn {
			if strings.Contains(el.Value.(*memoEntry).masked, secret) {
				t.Fatalf("an entry holds %q", secret)
			}
		}
	}
}

func TestMemoIsBounded(t *testing.T) {
	e, err := Shared()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m := NewMemo(2, 1<<20)
	for _, text := range []string{"one", "two", "three"} {
		if _, err := m.Redact(ctx, e, text); err != nil {
			t.Fatal(err)
		}
	}
	if m.Len() != 2 {
		t.Fatalf("Len = %d, want 2", m.Len())
	}
	if _, ok := m.lookup(e.rules, m.key("one")); ok {
		t.Fatal("the least recently used text was kept")
	}

	m = NewMemo(100, 40)
	for _, text := range []string{"0123456789", "abcdefghij", "ABCDEFGHIJ", "klmnopqrst", "KLMNOPQRST"} {
		if _, err := m.Redact(ctx, e, text); err != nil {
			t.Fatal(err)
		}
	}
	if m.bytes > 40 || m.Len() != 4 {
		t.Fatalf("%d bytes in %d entries, want at most 40 in 4", m.bytes, m.Len())
	}
	// A text over a quarter of the budget is masked, not remembered.
	long := strings.Repeat("z", 11)
	if got, err := m.Redact(ctx, e, long); err != nil || got != long {
		t.Fatalf("Redact(long) = %q, %v", got, err)
	}
	if _, ok := m.lookup(e.rules, m.key(long)); ok {
		t.Fatal("a text over a quarter of the budget was kept")
	}
}

// A call that fails returns no text and leaves nothing behind.
func TestMemoRemembersNoFailure(t *testing.T) {
	e, err := New()
	if err != nil {
		t.Fatal(err)
	}
	// Every finder busy: the call waits for one until ctx ends.
	for range cap(e.slots) {
		if _, err := e.borrow(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := NewMemo(64, 1<<20)
	got, err := m.Redact(ctx, e, memoCorpus[0])
	if err == nil || got != "" {
		t.Fatalf("Redact with ctx ended = %q, %v; want no text and an error", got, err)
	}
	if m.Len() != 0 {
		t.Fatal("a failed call was remembered")
	}
}

func TestNilMemoMasks(t *testing.T) {
	e, err := Shared()
	if err != nil {
		t.Fatal(err)
	}
	var m *Memo
	got, err := m.Redact(context.Background(), e, memoCorpus[0])
	want, _, _ := e.Redact(memoCorpus[0])
	if err != nil || got != want {
		t.Fatalf("nil memo = %q, %v; want %q", got, err, want)
	}
}

// A change of rules compiles a new shared engine, and the memo forgets what
// the old rules masked.
func TestRuleChangeTakesEffectThroughTheMemo(t *testing.T) {
	before, err := Shared()
	if err != nil {
		t.Fatal(err)
	}
	m := NewMemo(64, 1<<20)
	ctx := context.Background()
	text := "the codeword is zebra-quartz-cobalt"
	if got, err := m.Redact(ctx, before, text); err != nil || got != text {
		t.Fatalf("before the change: %q, %v", got, err)
	}

	original := Config
	t.Cleanup(func() { Config = original })
	Config.CustomRules = append(append([]goredact.CustomRule(nil), original.CustomRules...), goredact.CustomRule{
		ID:            "test-codeword",
		Triggers:      []string{"zebra-quartz-cobalt"},
		Confidence:    goredact.ConfidenceHigh,
		MaxLookbehind: 0,
		MaxLookahead:  0,
		Validate: func(window []byte, trigStart, trigEnd int) (int, int, bool) {
			return trigStart, trigEnd, true
		},
	})
	after, err := Shared()
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("Shared kept the engine of the old rules")
	}
	got, err := m.Redact(ctx, after, text)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "zebra") {
		t.Fatalf("the memo answered with the old rules' mask: %q", got)
	}
	want, _, _ := after.Redact(text)
	if got != want {
		t.Fatalf("memo = %q, engine = %q", got, want)
	}
	if m.Len() != 1 {
		t.Fatalf("Len = %d after the change, want 1", m.Len())
	}

	Config = original
	again, err := Shared()
	if err != nil {
		t.Fatal(err)
	}
	if again == after {
		t.Fatal("Shared kept the engine of the test rules")
	}
	if got, err := m.Redact(ctx, again, text); err != nil || got != text {
		t.Fatalf("after restoring the rules: %q, %v", got, err)
	}
}

func TestSharedIsOneEngineForConcurrentCallers(t *testing.T) {
	first, err := Shared()
	if err != nil {
		t.Fatal(err)
	}
	m := NewMemo(16, 1<<20)
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := range cap(errs) {
		wg.Go(func() {
			e, err := Shared()
			if err != nil {
				errs <- err
				return
			}
			if e != first {
				t.Error("Shared returned another engine")
			}
			text := memoCorpus[i%len(memoCorpus)]
			got, err := m.Redact(context.Background(), e, text)
			if err != nil {
				errs <- err
				return
			}
			if want, _, _ := e.Redact(text); got != want {
				t.Errorf("memo = %q, engine = %q", got, want)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// BenchmarkEngine compares compiling an engine for each text, as callers did,
// with reusing the shared one and with a memo that saw the text before.
func BenchmarkEngine(b *testing.B) {
	ctx := context.Background()
	text := memoCorpus[0]
	b.Run("new-per-use", func(b *testing.B) {
		for b.Loop() {
			e, err := New()
			if err != nil {
				b.Fatal(err)
			}
			if _, _, err := e.RedactContext(ctx, text); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("shared", func(b *testing.B) {
		for b.Loop() {
			e, err := Shared()
			if err != nil {
				b.Fatal(err)
			}
			if _, _, err := e.RedactContext(ctx, text); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("shared-memo-hit", func(b *testing.B) {
		m := NewMemo(16, 1<<20)
		for b.Loop() {
			e, err := Shared()
			if err != nil {
				b.Fatal(err)
			}
			if _, err := m.Redact(ctx, e, text); err != nil {
				b.Fatal(err)
			}
		}
	})
}
