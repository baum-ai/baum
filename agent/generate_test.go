package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/review/tilecatalogue"
)

const testHunk = "h_0123456789ab"

// authoredFixture is a valid document for the fixture bundle.
func authoredFixture() []byte {
	return []byte(`{"schema":"baum.review/1","summary":{"text":"Changes parser behavior.","context":{"area":{"text":"The parser turns source text into a syntax tree.","refs":[]}},"review_exchange":{"summary":"No reviews available."},"change_kind":"feature","implements":[],"release_note":null,"breaking_change":null,"attention":null,"authorship":{"declared":"unknown","declared_tools":[],"anchors":[]}},"relations":[],"sections":[{"id":"sec_parser","title":"Parser","kind":"logical","hunks":["h_0123456789ab"],"summary":{"text":"Adds the parser.","anchors":[{"type":"hunk","id":"h_0123456789ab"}]},"attention":null,"risks":[],"evidence":[],"blocks":[]}]}`)
}

// stubBackend is a Backend whose runs a test scripts.
type stubBackend struct {
	name  string
	mu    sync.Mutex
	calls []Request
	run   func(ctx context.Context, request Request, n int) (Result, error)
}

func (b *stubBackend) Name() string {
	if b.name == "" {
		return BackendClaude
	}
	return b.name
}

func (b *stubBackend) Run(ctx context.Context, request Request) (Result, error) {
	b.mu.Lock()
	n := len(b.calls)
	b.calls = append(b.calls, request)
	b.mu.Unlock()
	if b.run != nil {
		return b.run(ctx, request, n)
	}
	return Result{Document: authoredFixture(), Model: "fixture-model", Calls: 1, Tokens: 10}, nil
}

const testBody = "Review this change for a maintainer."

var testPrompts = StaticPrompts{"": {Version: "test-v1", Body: testBody, Digest: "digest"}}

var (
	validatorOnce sync.Once
	validator     *review.DocumentValidator
	validatorErr  error
)

// sharedValidator compiles the document schema once; a validator is read-only
// after construction.
func sharedValidator(t *testing.T) *review.DocumentValidator {
	t.Helper()
	validatorOnce.Do(func() { validator, validatorErr = review.NewDocumentValidator() })
	if validatorErr != nil {
		t.Fatal(validatorErr)
	}
	return validator
}

func fixtureBundle() *bundle.Bundle {
	return &bundle.Bundle{
		Repository: "org/repo", PullRequest: bundle.PullRequest{Number: 1, State: "open", HeadSHA: "aaaaaaa", BaseSHA: "bbbbbbb", MergeBaseSHA: "bbbbbbb"},
		Sources: bundle.Sources{"diff": true},
		Diff:    []bundle.Hunk{{ID: testHunk, Path: "main.go", Role: "code", Change: "added", StartLine: 1, LineCount: 1, Additions: 1}},
	}
}

// loopFixture is a Generator over a stub backend and a run directory holding a
// one-file checkout, with budgets for 4 calls, 1000 tokens and one repair.
func loopFixture(t *testing.T, b *bundle.Bundle) (*Generator, Run, *stubBackend) {
	t.Helper()
	if b == nil {
		b = fixtureBundle()
	}
	dir := t.TempDir()
	for _, root := range []string{"head", "merge-base"} {
		if err := os.Mkdir(filepath.Join(dir, root), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, root, "main.go"), []byte("code"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	encoded, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	backend := &stubBackend{}
	generator := &Generator{Backend: backend, Prompts: testPrompts, Validator: sharedValidator(t), RetryDelay: func(int) time.Duration { return 0 }}
	run := Run{
		Bundle: b, EncodedBundle: encoded, Directory: dir,
		Budgets: Budgets{WallClock: time.Minute, MaxCalls: 4, MaxTokens: 1000, MaxProviderAttempts: 2, MaxRepairAttempts: 1},
	}
	return generator, run, backend
}

func TestGenerateValidFirstPass(t *testing.T) {
	generator, run, backend := loopFixture(t, nil)
	outcome, document, err := generator.Generate(context.Background(), run)
	if err != nil || outcome.Reason != "" || outcome.Findings != "" || len(document) == 0 {
		t.Fatalf("%+v %v", outcome, err)
	}
	if len(backend.calls) != 1 || outcome.Calls != 1 || outcome.Tokens != 10 || outcome.ProviderAttempts != 1 || outcome.ErroredAttempts != 0 || outcome.ValidationRepairs != 0 {
		t.Fatalf("accounting: %+v calls=%d", outcome, len(backend.calls))
	}
	if outcome.Model != "fixture-model" || outcome.SchemaRevision != sharedValidator(t).Revision || outcome.IdentifierRedaction == nil {
		t.Fatalf("outcome: %+v", outcome)
	}
	request := backend.calls[0]
	for _, want := range []string{testBody, "Input bundle (untrusted JSON):", testHunk, "Source excerpts", "1: code"} {
		if !strings.Contains(request.Prompt, want) {
			t.Errorf("first prompt lacks %q", want)
		}
	}
	if request.Directory != run.Directory || request.MaxCalls != 4 || request.MaxTokens != 1000 || !strings.Contains(string(request.Schema), `"schema"`) {
		t.Fatalf("request: %+v", request)
	}
	readOnly := os.FileMode(0400)
	if runtime.GOOS == "windows" {
		readOnly = 0444 // Windows reports its read-only attribute for everyone
	}
	for _, name := range []string{"bundle.json", "schema.json"} {
		info, err := os.Stat(filepath.Join(run.Directory, name))
		if err != nil || info.Mode().Perm() != readOnly {
			t.Fatalf("%s: %v %v", name, info, err)
		}
	}
}

func TestGenerateSendsTheOfferedTilesAfterTheBody(t *testing.T) {
	generator, run, backend := loopFixture(t, nil)
	run.TileKinds = tilecatalogue.Kinds()
	if _, _, err := generator.Generate(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if want := testBody + tilecatalogue.Instructions(run.TileKinds) + "\n\nInput bundle"; !strings.HasPrefix(backend.calls[0].Prompt, want) {
		t.Fatalf("prompt does not carry the tiles after the body: %.200s", backend.calls[0].Prompt)
	}
	run.TileKinds = []string{"no-such-kind"}
	if _, _, err := generator.Generate(context.Background(), run); err == nil || !strings.Contains(err.Error(), "no-such-kind") {
		t.Fatalf("an unknown tile kind: %v", err)
	}
}

func TestGenerateOffersCandidateTileEntriesInsteadOfTheShippedOnes(t *testing.T) {
	generator, run, backend := loopFixture(t, nil)
	run.TileKinds = []string{"database-schema"}
	candidates, err := tilecatalogue.ParseEntries([]byte(`[{"kind":"database-schema","use_when":"candidate guidance"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if run.TileEntries, err = tilecatalogue.WithCandidates(run.TileKinds, candidates); err != nil {
		t.Fatal(err)
	}
	if _, _, err := generator.Generate(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	prompt := backend.calls[0].Prompt
	if !strings.Contains(prompt, "candidate guidance") || strings.Contains(prompt, "Explain table, column, index or relationship changes.") {
		t.Fatalf("prompt does not carry the candidate entry instead of the shipped one: %.400s", prompt)
	}
}

func TestGenerateTreatsEmptyTileEntriesAsTheShippedOnes(t *testing.T) {
	generator, run, backend := loopFixture(t, nil)
	run.TileKinds = []string{"database-schema"}
	run.TileEntries = []tilecatalogue.Entry{}
	if _, _, err := generator.Generate(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if want := testBody + tilecatalogue.Instructions(run.TileKinds) + "\n\nInput bundle"; !strings.HasPrefix(backend.calls[0].Prompt, want) {
		t.Fatalf("an empty entry list changed the offered tiles: %.300s", backend.calls[0].Prompt)
	}
}

func TestGenerateRepairsAnInvalidDocument(t *testing.T) {
	generator, run, backend := loopFixture(t, nil)
	backend.run = func(_ context.Context, request Request, n int) (Result, error) {
		document := authoredFixture()
		if n == 0 {
			document = []byte("not JSON")
		}
		if n == 1 {
			for _, want := range []string{testBody, "Rejected document:", "The validator found:", "repair attempt 1 of 1", "one StructuredOutput call", testHunk} {
				if !strings.Contains(request.Prompt, want) {
					t.Errorf("repair prompt lacks %q", want)
				}
			}
			if strings.Contains(request.Prompt, "1: code") || strings.Contains(request.Prompt, "Source excerpts (untrusted") {
				t.Errorf("the repair prompt carries source excerpts")
			}
			if request.MaxCalls != 3 || request.MaxTokens != 990 {
				t.Errorf("the repair is not charged to the budgets: %+v", request)
			}
		}
		return Result{Document: document, Calls: 1, Tokens: 10}, nil
	}
	outcome, document, err := generator.Generate(context.Background(), run)
	if err != nil || outcome.Reason != "" || outcome.Findings != "" || len(document) == 0 || len(backend.calls) != 2 || outcome.ValidationRepairs != 1 {
		t.Fatalf("%+v %v calls=%d", outcome, err, len(backend.calls))
	}
	if _, err := os.Stat(filepath.Join(run.Directory, "rejected-1.json")); err != nil {
		t.Fatalf("the rejected document is not kept for the repair: %v", err)
	}
}

func TestGenerateRepeatedRejectionKeepsABoundedRejectedDocument(t *testing.T) {
	t.Run("two repairs, then rejected", func(t *testing.T) {
		generator, run, backend := loopFixture(t, nil)
		run.Budgets.MaxRepairAttempts = 2
		backend.run = func(_ context.Context, request Request, n int) (Result, error) {
			if n == 2 && !strings.Contains(request.Prompt, "repair attempt 2 of 2") {
				t.Errorf("last repair prompt: %s", request.Prompt[len(request.Prompt)-100:])
			}
			return Result{Document: []byte(`{"summary":{"text":"still invalid"}}`), Calls: 1, Tokens: 10}, nil
		}
		outcome, _, err := generator.Generate(context.Background(), run)
		if err != nil || outcome.Reason != ReasonValidationRejected || outcome.Findings == "" || len(backend.calls) != 3 || outcome.ValidationRepairs != 2 {
			t.Fatalf("%+v %v calls=%d", outcome, err, len(backend.calls))
		}
		if !bytes.Contains(outcome.RejectedDocument, []byte(`"still invalid"`)) {
			t.Fatalf("rejected document = %s", outcome.RejectedDocument)
		}
		encoded, _ := json.Marshal(outcome)
		if !bytes.Contains(encoded, []byte(`"rejected_document":{`)) {
			t.Fatalf("the rejected document is not part of the encoded outcome: %s", encoded)
		}
	})
	t.Run("a malformed document is not retained", func(t *testing.T) {
		// Prepare shrinks an oversized document before it is kept, so only the
		// bounds of RetainedDocument itself, below, can drop one.
		for name, document := range map[string][]byte{
			"malformed": []byte("not JSON"),
		} {
			generator, run, backend := loopFixture(t, nil)
			backend.run = func(context.Context, Request, int) (Result, error) {
				return Result{Document: document, Calls: 1, Tokens: 10}, nil
			}
			outcome, _, err := generator.Generate(context.Background(), run)
			if err != nil || outcome.Reason != ReasonValidationRejected || outcome.Findings == "" {
				t.Fatalf("%s: %+v %v", name, outcome, err)
			}
			if len(outcome.RejectedDocument) != 0 {
				t.Fatalf("%s: a malformed document was retained (%d bytes)", name, len(outcome.RejectedDocument))
			}
		}
	})
	t.Run("RetainedDocument bounds", func(t *testing.T) {
		if RetainedDocument(bytes.Repeat([]byte(" "), MaxRetainedDocumentBytes+1)) != nil || RetainedDocument([]byte("not JSON")) != nil || RetainedDocument(nil) != nil || string(RetainedDocument([]byte(`{}`))) != `{}` {
			t.Fatal("retention bounds are wrong")
		}
	})
}

func TestGenerateBudgets(t *testing.T) {
	t.Run("calls", func(t *testing.T) {
		generator, run, backend := loopFixture(t, nil)
		backend.run = func(context.Context, Request, int) (Result, error) { return Result{Calls: 5}, nil }
		_, _, err := generator.Generate(context.Background(), run)
		if err == nil || err.Error() != GenerationBudgetExceededReason("model calls") {
			t.Fatal(err)
		}
	})
	t.Run("tokens", func(t *testing.T) {
		generator, run, backend := loopFixture(t, nil)
		backend.run = func(context.Context, Request, int) (Result, error) { return Result{Tokens: 1001}, nil }
		_, _, err := generator.Generate(context.Background(), run)
		if err == nil || err.Error() != GenerationBudgetExceededReason("tokens") {
			t.Fatal(err)
		}
	})
	t.Run("a repair the budget cannot pay for keeps the rejected document", func(t *testing.T) {
		generator, run, backend := loopFixture(t, nil)
		backend.run = func(_ context.Context, _ Request, n int) (Result, error) {
			if n == 0 {
				return Result{Document: []byte(`{"summary":{"text":"first invalid"}}`), Calls: 1, Tokens: 600}, nil
			}
			// The repair is attempted although the 400 tokens left cannot pay for it.
			return Result{Calls: 1, Tokens: 700}, nil
		}
		outcome, document, err := generator.Generate(context.Background(), run)
		if err == nil || err.Error() != GenerationBudgetExceededReason("tokens") || len(backend.calls) != 2 || document != nil {
			t.Fatalf("%+v %v calls=%d", outcome, err, len(backend.calls))
		}
		if !bytes.Contains(outcome.RejectedDocument, []byte(`"first invalid"`)) || outcome.Findings == "" {
			t.Fatalf("the failure lost the rejected document or the findings: %+v", outcome)
		}
	})
	t.Run("no calls left for a retry", func(t *testing.T) {
		generator, run, backend := loopFixture(t, nil)
		run.Budgets.MaxCalls = 1
		backend.run = func(context.Context, Request, int) (Result, error) {
			return Result{Calls: 1, Tokens: 10}, errors.New("provider unavailable")
		}
		if _, _, err := generator.Generate(context.Background(), run); err == nil || err.Error() != GenerationBudgetExceededReason("model calls") || len(backend.calls) != 1 {
			t.Fatalf("%v calls=%d", err, len(backend.calls))
		}
	})
	t.Run("zero calls and tokens are no limit", func(t *testing.T) {
		generator, run, backend := loopFixture(t, nil)
		run.Budgets.MaxCalls, run.Budgets.MaxTokens = 0, 0
		backend.run = func(context.Context, Request, int) (Result, error) {
			return Result{Document: authoredFixture(), Model: "fixture-model", Calls: 500, Tokens: 10_000_000}, nil
		}
		if _, document, err := generator.Generate(context.Background(), run); err != nil || len(document) == 0 {
			t.Fatal(err)
		}
		if request := backend.calls[0]; request.MaxCalls != 0 || request.MaxTokens != 0 {
			t.Fatalf("an unbounded run bounded its request: %+v", request)
		}
	})
}

func TestGenerateCancellation(t *testing.T) {
	generator, run, backend := loopFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend.run = func(context.Context, Request, int) (Result, error) {
		cancel()
		return Result{}, context.Canceled
	}
	outcome, document, err := generator.Generate(ctx, run)
	if !errors.Is(err, context.Canceled) || document != nil || outcome.Reason != "" || len(backend.calls) != 1 {
		t.Fatalf("%+v %v", outcome, err)
	}
	// A context cancelled once the document is valid is not a success either.
	generator, run, backend = loopFixture(t, nil)
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	backend.run = func(context.Context, Request, int) (Result, error) {
		cancel()
		return Result{Document: authoredFixture(), Calls: 1, Tokens: 10}, nil
	}
	if _, document, err = generator.Generate(ctx, run); !errors.Is(err, context.Canceled) || document != nil {
		t.Fatalf("%v", err)
	}
}

func TestGenerateRetriesProviderFailures(t *testing.T) {
	t.Run("a retry succeeds", func(t *testing.T) {
		generator, run, backend := loopFixture(t, nil)
		var delays []int
		generator.RetryDelay = func(attempt int) time.Duration { delays = append(delays, attempt); return 0 }
		backend.run = func(_ context.Context, _ Request, n int) (Result, error) {
			if n == 0 {
				return Result{Model: "first-model", Usage: Usage{Input: 1, Output: 1, CacheRead: 100}, Calls: 1, Tokens: 2, Events: []Event{{Kind: "error", Status: "rate_limit"}}}, errors.New("private provider failure")
			}
			return Result{Document: authoredFixture(), Model: "second-model", Usage: Usage{Input: 2, Output: 8, CacheRead: 200}, Calls: 1, Tokens: 10}, nil
		}
		outcome, _, err := generator.Generate(context.Background(), run)
		if outcome.Accounting == nil || len(outcome.Accounting.Attempts) != 2 || outcome.Accounting.Usage.CacheRead != 300 || !outcome.Accounting.Attempts[0].Failed || outcome.Accounting.Attempts[1].Model != "second-model" {
			t.Fatalf("retry usage lost: %+v", outcome.Accounting)
		}
		if err != nil || outcome.Reason != "" || outcome.ProviderAttempts != 2 || outcome.ErroredAttempts != 1 || outcome.Calls != 2 || outcome.Tokens != 12 || len(outcome.Diagnostics) != 1 {
			t.Fatalf("%+v %v", outcome, err)
		}
		if len(delays) != 1 || delays[0] != 0 {
			t.Fatalf("retry delays = %v", delays)
		}
	})
	t.Run("every attempt fails", func(t *testing.T) {
		generator, run, backend := loopFixture(t, nil)
		backend.run = func(context.Context, Request, int) (Result, error) {
			return Result{Calls: 1, Tokens: 2}, errors.New("private provider failure")
		}
		outcome, _, err := generator.Generate(context.Background(), run)
		if err == nil || err.Error() != ReasonProviderUnavailable || len(backend.calls) != 2 || outcome.ProviderAttempts != 2 || outcome.ErroredAttempts != 2 {
			t.Fatalf("%+v %v", outcome, err)
		}
	})
	t.Run("a rejection is not retried", func(t *testing.T) {
		for _, status := range []string{"authentication_error", "invalid_request", "refusal", "context_window_exceeded"} {
			generator, run, backend := loopFixture(t, nil)
			backend.run = func(context.Context, Request, int) (Result, error) {
				return Result{Events: []Event{{Kind: "error", Status: status}}}, &RejectedError{Status: status, Err: errors.New("the key was refused")}
			}
			outcome, _, err := generator.Generate(context.Background(), run)
			var rejected *RejectedError
			if !errors.As(err, &rejected) || err.Error() != ProviderRejectedReason(status)+": the key was refused" || len(backend.calls) != 1 || outcome.ProviderAttempts != 1 {
				t.Errorf("%s: %v, %d calls", status, err, len(backend.calls))
			}
		}
	})
	t.Run("a budget error is not retried", func(t *testing.T) {
		generator, run, backend := loopFixture(t, nil)
		backend.run = func(context.Context, Request, int) (Result, error) {
			return Result{}, errors.New(GenerationBudgetExceededReason("model calls"))
		}
		if _, _, err := generator.Generate(context.Background(), run); err == nil || len(backend.calls) != 1 {
			t.Fatalf("%v calls=%d", err, len(backend.calls))
		}
	})
	t.Run("the delay is cancellable and defaults to seconds", func(t *testing.T) {
		generator, run, backend := loopFixture(t, nil)
		generator.RetryDelay = nil
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		backend.run = func(context.Context, Request, int) (Result, error) {
			go func() { time.Sleep(10 * time.Millisecond); cancel() }()
			return Result{}, errors.New("private provider failure")
		}
		if _, _, err := generator.Generate(ctx, run); !errors.Is(err, context.Canceled) || len(backend.calls) != 1 {
			t.Fatalf("%v calls=%d", err, len(backend.calls))
		}
		for attempt, want := range map[int]time.Duration{0: time.Second, 1: 2 * time.Second, 4: 16 * time.Second, 9: 16 * time.Second} {
			if got := generator.retryDelay(attempt); got != want {
				t.Errorf("retry delay %d = %v, want %v", attempt, got, want)
			}
		}
	})
}

func historySection(id string, risks ...string) map[string]any {
	values := []any{}
	for _, risk := range risks {
		values = append(values, map[string]any{"id": risk, "tier": "high", "category": "correctness", "source": "model", "text": "A concrete risk.", "anchors": []any{map[string]any{"type": "hunk", "id": testHunk}}})
	}
	return map[string]any{"id": "sec_" + id, "title": id, "kind": "logical", "hunks": []any{testHunk}, "summary": map[string]any{"text": "Changes behavior.", "anchors": []any{map[string]any{"type": "hunk", "id": testHunk}}}, "attention": nil, "risks": values, "evidence": []any{}, "blocks": []any{}}
}

func candidate(t *testing.T, sections ...any) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(authoredFixture(), &document); err != nil {
		t.Fatal(err)
	}
	document["sections"] = sections
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestGenerateRepairDoesNotReactivateOmittedRisks(t *testing.T) {
	prior := historySection("a", "rsk_old")
	content, _ := json.Marshal(prior)
	first := historySection("a")
	// The first pass fails on something only the model can fix.
	first["kind"] = "not-a-kind"
	run := func(previous *bundle.PreviousVersion) []byte {
		b := fixtureBundle()
		b.Previous = previous
		generator, run, backend := loopFixture(t, b)
		backend.run = func(_ context.Context, _ Request, n int) (Result, error) {
			section := first
			if n == 1 {
				section = historySection("a", "rsk_old")
			}
			return Result{Document: candidate(t, section), Calls: 1, Tokens: 10}, nil
		}
		outcome, document, err := generator.Generate(context.Background(), run)
		if err != nil || outcome.Reason != "" || len(backend.calls) != 2 {
			t.Fatalf("%+v %v calls=%d", outcome, err, len(backend.calls))
		}
		return document
	}
	withHistory := run(&bundle.PreviousVersion{Version: 1, Sections: []bundle.PreviousSection{{ID: "sec_a", HunkContentHashes: []string{testHunk}, Content: content}}})
	if bytes.Contains(withHistory, []byte("rsk_old")) {
		t.Fatalf("the repair reactivated a risk the first pass omitted: %s", withHistory)
	}
	if without := run(nil); !bytes.Contains(without, []byte("rsk_old")) {
		t.Fatalf("with no history the repaired risk is kept: %s", without)
	}
}

func TestGenerateIdentifierSecretStopsRepairs(t *testing.T) {
	generator, run, backend := loopFixture(t, nil)
	run.Budgets.MaxRepairAttempts = 3
	document := bytes.Replace(authoredFixture(), []byte(`"risks":[]`), []byte(`"risks":[{"id":"rsk_leak","tier":"high","category":"`+identifierSecret+`","source":"model","text":"A concrete risk.","anchors":[{"type":"hunk","id":"`+testHunk+`"}]}]`), 1)
	document = bytes.Replace(document, []byte(`"kind":"logical"`), []byte(`"kind":"not-a-kind"`), 1)
	backend.run = func(context.Context, Request, int) (Result, error) {
		return Result{Document: document, Calls: 1, Tokens: 10}, nil
	}
	outcome, _, err := generator.Generate(context.Background(), run)
	if err != nil || outcome.Reason != ReasonValidationRejected || len(backend.calls) != 1 || outcome.ValidationRepairs != 0 {
		t.Fatalf("%+v %v calls=%d", outcome, err, len(backend.calls))
	}
	if outcome.IdentifierRedaction == nil || outcome.IdentifierRedaction.Masked == 0 || outcome.IdentifierRedaction.MaskedClosed == 0 {
		t.Fatalf("identifier redaction = %+v", outcome.IdentifierRedaction)
	}
	if strings.Contains(strings.ToLower(string(outcome.RejectedDocument)), identifierSecret) {
		t.Fatalf("the rejected document keeps the secret: %s", outcome.RejectedDocument)
	}
}

func TestGenerateDiagnosticsAreCapped(t *testing.T) {
	generator, run, backend := loopFixture(t, nil)
	run.Budgets.MaxRepairAttempts = 2
	backend.run = func(context.Context, Request, int) (Result, error) {
		events := make([]Event, 300)
		for i := range events {
			events[i] = Event{Kind: "tool_use", Tool: "Read"}
		}
		return Result{Document: []byte("not JSON"), Calls: 1, Tokens: 10, Events: events}, nil
	}
	outcome, _, err := generator.Generate(context.Background(), run)
	if err != nil || len(backend.calls) != 3 || len(outcome.Diagnostics) != 512 {
		t.Fatalf("diagnostics = %d, calls = %d, %v", len(outcome.Diagnostics), len(backend.calls), err)
	}
}

func TestGenerateReportsUnreportedUsage(t *testing.T) {
	generator, run, backend := loopFixture(t, nil)
	backend.run = func(context.Context, Request, int) (Result, error) {
		return Result{Document: authoredFixture(), Calls: 1, Tokens: 10, Usage: Usage{Unreported: true}, StructuredOutputRejections: 2}, nil
	}
	outcome, _, err := generator.Generate(context.Background(), run)
	if err != nil || !outcome.Usage.Unreported || outcome.StructuredOutputRejections != 2 {
		t.Fatalf("%+v %v", outcome, err)
	}
}

func TestGenerateRepairWording(t *testing.T) {
	invalidThenValid := func(backend *stubBackend) {
		backend.run = func(_ context.Context, _ Request, n int) (Result, error) {
			if n == 0 {
				return Result{Document: []byte("not JSON"), Calls: 1, Tokens: 10}, nil
			}
			return Result{Document: authoredFixture(), Calls: 1, Tokens: 10}, nil
		}
	}
	t.Run("codex", func(t *testing.T) {
		generator, run, backend := loopFixture(t, nil)
		backend.name = BackendCodex
		invalidThenValid(backend)
		if _, _, err := generator.Generate(context.Background(), run); err != nil || len(backend.calls) != 2 {
			t.Fatal(err)
		}
		prompt := backend.calls[1].Prompt
		if !strings.Contains(prompt, "repair attempt 1 of 1") || !strings.Contains(prompt, "final message") {
			t.Fatalf("codex repair prompt: %s", prompt)
		}
		for _, claudeOnly := range []string{"StructuredOutput", "Read", "Glob", "Grep"} {
			if strings.Contains(prompt, claudeOnly) {
				t.Errorf("codex repair prompt mentions %q", claudeOnly)
			}
		}
	})
	t.Run("the prompt overrides the wording", func(t *testing.T) {
		generator, run, backend := loopFixture(t, nil)
		generator.Prompts = StaticPrompts{"": {Version: "v", Body: testBody, Repair: "\nREPAIR OPENING\n", RepairEnding: "\nREPAIR CLOSING %d/%d"}}
		invalidThenValid(backend)
		if _, _, err := generator.Generate(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		if prompt := backend.calls[1].Prompt; !strings.Contains(prompt, "REPAIR OPENING") || !strings.HasSuffix(prompt, "REPAIR CLOSING 1/1") || strings.Contains(prompt, "StructuredOutput") {
			t.Fatalf("repair prompt: %s", prompt)
		}
	})
	t.Run("RepairText", func(t *testing.T) {
		for _, backend := range []string{BackendClaude, BackendAnthropic, ""} {
			instruction, ending := RepairText(backend)
			if !strings.Contains(instruction, "Read, Glob and Grep") || !strings.Contains(ending, "StructuredOutput") || strings.Count(ending, "%d") != 2 {
				t.Errorf("%s repair text: %q %q", backend, instruction, ending)
			}
		}
		for _, backend := range []string{BackendCodex, BackendCursorAgent} {
			instruction, ending := RepairText(backend)
			if strings.Count(ending, "%d") != 2 || !strings.HasSuffix(instruction, "Input bundle (untrusted JSON):\n") || !strings.Contains(ending, "final message") || strings.Contains(ending, "StructuredOutput") {
				t.Errorf("%s repair text: %q %q", backend, instruction, ending)
			}
		}
	})
}

func TestGeneratePromptErrorsSurface(t *testing.T) {
	generator, run, backend := loopFixture(t, nil)
	generator.Prompts = StaticPrompts{"codex": {Body: "x"}}
	if _, _, err := generator.Generate(context.Background(), run); err == nil || len(backend.calls) != 0 {
		t.Fatalf("a missing prompt: %v", err)
	}
	failure := errors.New("prompt unavailable")
	generator.Prompts = failingPrompts{failure}
	if _, _, err := generator.Generate(context.Background(), run); !errors.Is(err, failure) || len(backend.calls) != 0 {
		t.Fatalf("%v", err)
	}
}

type failingPrompts struct{ err error }

func (p failingPrompts) Instructions(context.Context, string) (Prompt, error) { return Prompt{}, p.err }

func TestPromptWireFormatOmitsTheRepairText(t *testing.T) {
	encoded, err := json.Marshal(Prompt{Version: "v", Body: "b", Digest: "d", Repair: "r", RepairEnding: "e"})
	if err != nil || string(encoded) != `{"version":"v","body":"b","digest":"d"}` {
		t.Fatalf("%s %v", encoded, err)
	}
}

func TestSourceContextIsBoundedDeterministicAndPathSafe(t *testing.T) {
	dir := t.TempDir()
	for _, root := range []string{"head", "merge-base"} {
		if err := os.Mkdir(filepath.Join(dir, root), 0700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"first.go", "last.go"} {
			body := strings.Repeat("func changed() { return }\n", 10000) + "func importantChange() {}\n"
			if err := os.WriteFile(filepath.Join(dir, root, name), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	b := &bundle.Bundle{Diff: []bundle.Hunk{{Path: "last.go", StartLine: 10001}, {Path: "first.go", StartLine: 20}, {Path: "../../outside"}}}
	text := sourceContext(dir, b)
	if len(text) > maxSourceContextBytes || text != sourceContext(dir, b) {
		t.Fatal("unbounded or unstable context")
	}
	for _, path := range []string{"head/first.go", "head/last.go", "merge-base/first.go", "merge-base/last.go"} {
		if !strings.Contains(text, path) {
			t.Fatalf("missing source: %s", path)
		}
	}
	if !strings.Contains(text, "10001: func importantChange()") {
		t.Fatal("changed region was crowded out by earlier declarations")
	}
	if strings.Contains(text, "outside") {
		t.Fatal("unsafe path included")
	}
}

func TestBoundedText(t *testing.T) {
	if got := boundedText("a\nb\tc", 10); got != "a b c" {
		t.Errorf("control characters: %q", got)
	}
	if got := boundedText(strings.Repeat("x", 20), 5); got != "xxxxx…" {
		t.Errorf("limit: %q", got)
	}
}
