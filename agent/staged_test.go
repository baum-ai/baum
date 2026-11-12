package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/bundle"
)

const stagedTestHunk = "h_abcdefabcdef"

// The test prompts: each stage's text is a marker the stub reads back.
const (
	stagedShared  = "SHARED-INSTRUCTIONS"
	stagedOutline = "OUTLINE-INSTRUCTIONS"
	stagedDetails = "DETAILS-INSTRUCTIONS"
	stagedContext = "CONTEXT-INSTRUCTIONS"
)

type staticStagedPrompts StagedPrompt

func (p staticStagedPrompts) StagedInstructions(context.Context, string) (StagedPrompt, error) {
	return StagedPrompt(p), nil
}

var stagedTestPrompts = staticStagedPrompts{Version: "staged-test-v1", Shared: stagedShared, Outline: stagedOutline, Details: stagedDetails, Context: stagedContext}

func stagedFixtureBundle() *bundle.Bundle {
	b := fixtureBundle()
	b.Diff[0].LineCount = 10
	b.Diff = append(b.Diff, bundle.Hunk{ID: stagedTestHunk, Path: "main_test.go", Role: "test", Change: "added", StartLine: 1, LineCount: 5, Additions: 5})
	return b
}

// stagedFixture is a Generator over a stub backend that answers every stage
// validly, unless script answers first, and a staged run over a two-file
// checkout with budgets for 12 calls, 1200 tokens and one repair.
func stagedFixture(t *testing.T, b *bundle.Bundle, script func(stage string, request Request, n int) (Result, error, bool)) (*Generator, StagedRun, *stubBackend) {
	t.Helper()
	if b == nil {
		b = stagedFixtureBundle()
	}
	generator, run, backend := loopFixture(t, b)
	for _, root := range []string{"head", "merge-base"} {
		if err := os.WriteFile(filepath.Join(run.Directory, root, "main_test.go"), []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run.Budgets.MaxCalls, run.Budgets.MaxTokens = 12, 1200
	backend.run = func(_ context.Context, request Request, n int) (Result, error) {
		stage := stageOf(t, request.Prompt)
		if script != nil {
			if result, err, answered := script(stage, request, n); answered {
				return result, err
			}
		}
		return stagedReply(t, stage, request), nil
	}
	return generator, StagedRun{Run: run, Prompts: stagedTestPrompts}, backend
}

func stageOf(t *testing.T, prompt string) string {
	t.Helper()
	for stage, marker := range map[string]string{StageOutline: stagedOutline, StageDetails: stagedDetails, StageContext: stagedContext} {
		if strings.Contains(prompt, marker) {
			return stage
		}
	}
	t.Errorf("a prompt of no stage: %.200s", prompt)
	return ""
}

// askedSections are the sections a details prompt asks for: the list of a
// first pass, or the sections a repair names.
func askedSections(prompt string) []string {
	if _, list, ok := strings.Cut(prompt, "in this order: "); ok {
		list, _, _ = strings.Cut(list, ".\n")
		return strings.Split(list, ", ")
	}
	var ids []string
	for _, line := range strings.Split(prompt, "\n") {
		if id, ok := strings.CutPrefix(line, "Section "); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

func repairing(prompt string) bool { return strings.Contains(prompt, "This is validation repair") }

func stagedOutlineDocument(sections ...string) []byte {
	if len(sections) == 0 {
		sections = []string{"sec_parser", "sec_tests"}
	}
	hunks := map[string]string{"sec_parser": testHunk, "sec_tests": stagedTestHunk}
	var items []string
	for _, id := range sections {
		items = append(items, fmt.Sprintf(`{"id":%q,"title":"Section %s","kind":"logical","hunks":[%q],"assessment":{"level":2,"factors":[],"reasons":[{"text":"A local change.","anchors":[{"type":"hunk","id":%q}]}]}}`, id, id, hunks[id], hunks[id]))
	}
	return []byte(`{"schema":"baum.review/1","summary":{"text":"Adds a parser and its tests.","review_exchange":{"summary":"No reviews yet."},"change_kind":"feature","implements":[],"release_note":null,"breaking_change":null,"attention":null,"recommendation":null,"authorship":{"declared":"none","declared_tools":[],"anchors":[]}},"relations":[],"sections":[` + strings.Join(items, ",") + `]}`)
}

func stagedSectionDetails(id, text string) string {
	hunk := map[string]string{"sec_parser": testHunk, "sec_tests": stagedTestHunk}[id]
	return fmt.Sprintf(`{"id":%q,"summary":{"text":%q,"anchors":[{"type":"hunk","id":%q}]},"attention":null,"risks":[],"evidence":[],"blocks":[]}`, id, text, hunk)
}

// invalidSectionDetails has an attention reference to no item.
func invalidSectionDetails(id string) string {
	hunk := map[string]string{"sec_parser": testHunk, "sec_tests": stagedTestHunk}[id]
	return fmt.Sprintf(`{"id":%q,"summary":{"text":"Writes tests.","anchors":[{"type":"hunk","id":%q}]},"attention":{"text":"Look here.","refs":["rsk_missing"]},"risks":[],"evidence":[],"blocks":[]}`, id, hunk)
}

const stagedContextDocument = `{"context":{"area":{"text":"The parser turns source text into a syntax tree.","refs":[]}}}`

func stagedResult(document string, session string) Result {
	return Result{Document: []byte(document), Model: "fixture-model", Calls: 1, Tokens: 10, SessionID: session}
}

// stagedReply answers a stage validly.
func stagedReply(t *testing.T, stage string, request Request) Result {
	switch stage {
	case StageOutline:
		return stagedResult(string(stagedOutlineDocument()), "session-outline")
	case StageDetails:
		var sections []string
		for _, id := range askedSections(request.Prompt) {
			sections = append(sections, stagedSectionDetails(id, "Writes "+id+"."))
		}
		return stagedResult(`{"sections":[`+strings.Join(sections, ",")+`]}`, "session-details")
	case StageContext:
		return stagedResult(stagedContextDocument, "session-context")
	}
	return Result{}
}

func stagedSections(t *testing.T, document []byte) map[string]map[string]any {
	t.Helper()
	var doc struct {
		Sections []map[string]any `json:"sections"`
	}
	if err := json.Unmarshal(document, &doc); err != nil {
		t.Fatal(err)
	}
	sections := map[string]map[string]any{}
	for _, section := range doc.Sections {
		sections[section["id"].(string)] = section
	}
	return sections
}

func stagedSummary(t *testing.T, document []byte) map[string]any {
	t.Helper()
	var doc struct {
		Summary map[string]any `json:"summary"`
	}
	if err := json.Unmarshal(document, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Summary
}

// completesStaged checks the document completes and stores as the host
// completes it.
func completesStaged(t *testing.T, run StagedRun, document []byte) {
	t.Helper()
	validator := sharedValidator(t)
	completed, findings := validator.CompleteStaged(document, run.Bundle, run.Directory, "fixture-model", "staged-test-v1", time.Now())
	if findings != "" {
		t.Fatalf("the staged document does not complete: %s", findings)
	}
	if stored := validator.ValidateStored(completed, run.Bundle); stored != nil {
		t.Fatalf("the completed document is not a stored one: %q", stored)
	}
}

// The outline comes first; the details and the context follow it, each with
// the shared prefix, its own instructions, its schema file and its share of
// the budgets, and the document assembles their parts.
func TestStagedReviewWritesTheStages(t *testing.T) {
	generator, run, backend := stagedFixture(t, nil, nil)
	outcome, document, err := generator.StagedReview(context.Background(), run)
	if err != nil || outcome.Reason != "" {
		t.Fatalf("%+v %v", outcome, err)
	}
	if len(backend.calls) != 3 || stageOf(t, backend.calls[0].Prompt) != StageOutline {
		t.Fatalf("calls: %d, the first is not the outline", len(backend.calls))
	}
	sections := stagedSections(t, document)
	if len(sections) != 2 || sections["sec_parser"]["details"] != nil || sections["sec_tests"]["details"] != nil {
		t.Fatalf("sections: %v", sections)
	}
	if text := sections["sec_tests"]["summary"].(map[string]any)["text"]; text != "Writes sec_tests." {
		t.Fatalf("sec_tests summary %v", text)
	}
	summary := stagedSummary(t, document)
	if summary["context"] == nil || summary["review_minutes"] != float64(1) || summary["text"] != "Adds a parser and its tests." {
		t.Fatalf("summary: %v", summary)
	}
	completesStaged(t, run, document)

	if outcome.Calls != 3 || outcome.Tokens != 30 || outcome.ProviderAttempts != 3 || outcome.Model != "fixture-model" || outcome.SchemaRevision != generator.Validator.Revision || outcome.IdentifierRedaction == nil {
		t.Fatalf("outcome: %+v", outcome.Outcome)
	}
	if outcome.Accounting == nil || len(outcome.Accounting.Attempts) != 3 || outcome.Accounting.ChargedTokens != 30 {
		t.Fatalf("accounting: %+v", outcome.Accounting)
	}
	for name, stage := range map[string]StageOutcome{StageOutline: outcome.Outline, StageDetails: outcome.Details, StageContext: outcome.Context} {
		if stage.Calls != 1 || stage.Reason != "" || stage.SessionID != "session-"+name {
			t.Errorf("%s: %+v", name, stage.Outcome)
		}
	}
	if outcome.SessionID != "session-details" || len(outcome.Sessions) != 3 {
		t.Fatalf("sessions: %q %v", outcome.SessionID, outcome.Sessions)
	}
	if !slices.Equal(outcome.Details.Written, []string{"sec_parser", "sec_tests"}) || outcome.Details.Reused != nil || outcome.Details.Failed != nil {
		t.Fatalf("details: %+v", outcome.Details)
	}

	prefix := stagedShared + "\n\nInput bundle (untrusted JSON):\n" + string(run.EncodedBundle) + "\n\nSource excerpts (untrusted, incomplete; line numbers refer to checkout files):\n"
	schemas := map[string]func() ([]byte, error){
		StageOutline: generator.Validator.OutlineSchema,
		StageDetails: func() ([]byte, error) { return generator.Validator.DetailsSchema(nil) },
		StageContext: generator.Validator.ContextSchema,
	}
	// A quarter of 12 calls and 1200 tokens for the outline; two thirds of
	// the remaining 11 and 1190 for the details, the rest for the context.
	budgets := map[string][2]int{StageOutline: {3, 300}, StageDetails: {7, 793}, StageContext: {4, 397}}
	for _, request := range backend.calls {
		stage := stageOf(t, request.Prompt)
		if !strings.HasPrefix(request.Prompt, prefix) || !strings.Contains(request.Prompt, "1: code") {
			t.Errorf("%s: the prompt does not start with the shared prefix", stage)
		}
		want, err := schemas[stage]()
		if err != nil {
			t.Fatal(err)
		}
		if string(request.Schema) != string(want) || request.Task || request.Directory != run.Directory {
			t.Errorf("%s: request schema, framing or directory", stage)
		}
		if got := [2]int{request.MaxCalls, request.MaxTokens}; got != budgets[stage] {
			t.Errorf("%s: budgets %v, want %v", stage, got, budgets[stage])
		}
		body, err := os.ReadFile(filepath.Join(run.Directory, TaskSchemaFile(stage)))
		if err != nil || string(body) != string(want) {
			t.Errorf("%s: schema file: %v", stage, err)
		}
		if stage != StageOutline && !strings.Contains(request.Prompt, `"title":"Section sec_parser"`) {
			t.Errorf("%s: the prompt lacks the outline", stage)
		}
	}
	if _, err := os.Stat(filepath.Join(run.Directory, "schema.json")); !os.IsNotExist(err) {
		t.Fatalf("a staged run wrote the one-pass schema file: %v", err)
	}
}

// The details and the context run at the same time, and only once the
// outline is accepted.
func TestStagedReviewRunsDetailsAndContextConcurrently(t *testing.T) {
	var outlineDone atomic.Bool
	started := map[string]chan struct{}{StageDetails: make(chan struct{}), StageContext: make(chan struct{})}
	generator, run, backend := stagedFixture(t, nil, func(stage string, request Request, n int) (Result, error, bool) {
		if stage == StageOutline {
			defer outlineDone.Store(true)
			return Result{}, nil, false
		}
		if !outlineDone.Load() {
			t.Errorf("%s started before the outline was accepted", stage)
		}
		close(started[stage])
		other := StageContext
		if stage == StageContext {
			other = StageDetails
		}
		select {
		case <-started[other]:
		case <-time.After(10 * time.Second):
			t.Errorf("%s ran alone: %s never started beside it", stage, other)
		}
		return Result{}, nil, false
	})
	generator.Backend = concurrentBackend{backend}
	if _, _, err := generator.StagedReview(context.Background(), run); err != nil {
		t.Fatal(err)
	}
}

// concurrentBackend is a stub that may run calls over one run directory at
// once.
type concurrentBackend struct{ *stubBackend }

func (concurrentBackend) ConcurrentCalls() bool { return true }

// A backend that does not say its calls may share the run directory runs
// one stage at a time: the details, then the context.
func TestStagedReviewRunsStagesInTurnOnOtherBackends(t *testing.T) {
	var running atomic.Int32
	var order []string
	var mu sync.Mutex
	generator, run, _ := stagedFixture(t, nil, func(stage string, request Request, n int) (Result, error, bool) {
		if running.Add(1) > 1 {
			t.Errorf("%s ran beside another stage", stage)
		}
		defer running.Add(-1)
		mu.Lock()
		order = append(order, stage)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		return Result{}, nil, false
	})
	if _, _, err := generator.StagedReview(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order, []string{StageOutline, StageDetails, StageContext}) {
		t.Fatalf("order %v", order)
	}
}

// A repair resends only the failing sections, each with its own rejected
// details and findings, after the same prefix and instructions.
func TestStagedReviewRepairsOnlyFailingSections(t *testing.T) {
	var repair Request
	generator, run, backend := stagedFixture(t, nil, func(stage string, request Request, n int) (Result, error, bool) {
		if stage != StageDetails {
			return Result{}, nil, false
		}
		if repairing(request.Prompt) {
			repair = request
			return stagedResult(`{"sections":[`+stagedSectionDetails("sec_tests", "Tests the parser.")+`]}`, "session-repair"), nil, true
		}
		return stagedResult(`{"sections":[`+stagedSectionDetails("sec_parser", "Writes the parser.")+`,`+invalidSectionDetails("sec_tests")+`]}`, "session-details"), nil, true
	})
	outcome, document, err := generator.StagedReview(context.Background(), run)
	if err != nil || outcome.Reason != "" {
		t.Fatalf("%+v %v", outcome, err)
	}
	if len(backend.calls) != 4 || outcome.Details.ValidationRepairs != 1 || outcome.ValidationRepairs != 1 {
		t.Fatalf("calls %d, repairs %d", len(backend.calls), outcome.Details.ValidationRepairs)
	}
	if got := askedSections(repair.Prompt); !slices.Equal(got, []string{"sec_tests"}) {
		t.Fatalf("the repair asks for %v", got)
	}
	for _, want := range []string{stagedShared, stagedDetails, "rsk_missing", `section 2 (sec_tests) attention ref "rsk_missing"`, "repair attempt 1 of 1"} {
		if !strings.Contains(repair.Prompt, want) {
			t.Errorf("the repair prompt lacks %q", want)
		}
	}
	if strings.Contains(repair.Prompt, "Writes the parser.") || strings.Contains(repair.Prompt, "\nSection sec_parser\n") {
		t.Fatal("the repair resends a valid section")
	}
	rejected, err := os.ReadFile(filepath.Join(run.Directory, "rejected-details-1.json"))
	if err != nil || !strings.Contains(string(rejected), "rsk_missing") || strings.Contains(string(rejected), "sec_parser") {
		t.Fatalf("rejected-details-1.json: %s %v", rejected, err)
	}
	sections := stagedSections(t, document)
	if sections["sec_tests"]["summary"].(map[string]any)["text"] != "Tests the parser." || sections["sec_parser"]["summary"].(map[string]any)["text"] != "Writes the parser." {
		t.Fatalf("sections: %v", sections)
	}
	// The session of the pass that wrote most of the document is kept.
	if outcome.SessionID != "session-details" || outcome.Details.SessionID != "session-details" {
		t.Fatalf("session %q", outcome.SessionID)
	}
	completesStaged(t, run, document)
}

// A section still invalid after the repairs is published without its
// details; the rest of the review is published as written.
func TestStagedReviewFailsASectionAfterRepairs(t *testing.T) {
	var progress []StagedProgress
	generator, run, backend := stagedFixture(t, nil, func(stage string, request Request, n int) (Result, error, bool) {
		if stage != StageDetails {
			return Result{}, nil, false
		}
		var sections []string
		for _, id := range askedSections(request.Prompt) {
			if id == "sec_tests" {
				sections = append(sections, invalidSectionDetails(id))
			} else {
				sections = append(sections, stagedSectionDetails(id, "Writes the parser."))
			}
		}
		return stagedResult(`{"sections":[`+strings.Join(sections, ",")+`]}`, "session-details"), nil, true
	})
	run.Progress = func(p StagedProgress) { progress = append(progress, p) }
	outcome, document, err := generator.StagedReview(context.Background(), run)
	if err != nil || outcome.Reason != "" {
		t.Fatalf("%+v %v", outcome, err)
	}
	if len(backend.calls) != 4 || outcome.Details.Reason != ReasonValidationRejected || !slices.Equal(outcome.Details.Failed, []string{"sec_tests"}) || !slices.Equal(outcome.Details.Written, []string{"sec_parser"}) {
		t.Fatalf("calls %d, details %+v", len(backend.calls), outcome.Details)
	}
	sections := stagedSections(t, document)
	if sections["sec_tests"]["details"] != "failed" || sections["sec_tests"]["summary"] != nil || sections["sec_tests"]["assessment"] == nil {
		t.Fatalf("the failed section: %v", sections["sec_tests"])
	}
	if sections["sec_parser"]["details"] != nil || stagedSummary(t, document)["context"] == nil {
		t.Fatal("the rest of the review was not published")
	}
	failed := slices.IndexFunc(progress, func(p StagedProgress) bool { return p.Stage == StageDetails && p.Section == "sec_tests" && p.Failed })
	if failed < 0 || stagedSections(t, progress[failed].Document)["sec_tests"]["details"] != "failed" {
		t.Fatalf("no failed progress for sec_tests: %+v", progress)
	}
	completesStaged(t, run, document)
}

// A context that cannot be written leaves the review without one.
func TestStagedReviewSucceedsWithoutContext(t *testing.T) {
	for name, test := range map[string]struct {
		reply  func(n int) (Result, error)
		reason string
	}{
		"refused": {func(int) (Result, error) {
			return Result{Calls: 1, Tokens: 5}, &RejectedError{Status: "refusal", Err: errors.New("declined")}
		}, ProviderRejectedReason("refusal")},
		"invalid": {func(int) (Result, error) {
			return stagedResult(`{"context":{"related":{"text":"Other code.","refs":[]}}}`, ""), nil
		}, ReasonValidationRejected},
	} {
		t.Run(name, func(t *testing.T) {
			generator, run, _ := stagedFixture(t, nil, func(stage string, request Request, n int) (Result, error, bool) {
				if stage != StageContext {
					return Result{}, nil, false
				}
				result, err := test.reply(n)
				return result, err, true
			})
			var contextProgress []StagedProgress
			run.Progress = func(p StagedProgress) {
				if p.Stage == StageContext {
					contextProgress = append(contextProgress, p)
				}
			}
			outcome, document, err := generator.StagedReview(context.Background(), run)
			if err != nil || outcome.Reason != "" || outcome.Context.Reason != test.reason {
				t.Fatalf("%v: run %q, context %q", err, outcome.Reason, outcome.Context.Reason)
			}
			if _, present := stagedSummary(t, document)["context"]; present {
				t.Fatal("the document has a context")
			}
			if len(contextProgress) != 1 || !contextProgress[0].Failed {
				t.Fatalf("context progress: %+v", contextProgress)
			}
			completesStaged(t, run, document)
		})
	}
}

// A run whose outline fails fails as a one-pass run does, and writes nothing
// else.
func TestStagedReviewFailsWithItsOutline(t *testing.T) {
	generator, run, backend := stagedFixture(t, nil, func(stage string, request Request, n int) (Result, error, bool) {
		// An unassessed logical section.
		return stagedResult(strings.Replace(string(stagedOutlineDocument()), `"assessment"`, `"unassessed"`, 1), ""), nil, true
	})
	outcome, document, err := generator.StagedReview(context.Background(), run)
	if err != nil || outcome.Reason != ReasonValidationRejected || !strings.Contains(outcome.Findings, "needs an assessment") || len(outcome.RejectedDocument) == 0 {
		t.Fatalf("%+v %v", outcome.Outcome, err)
	}
	if len(backend.calls) != 2 || !repairing(backend.calls[1].Prompt) || !strings.Contains(backend.calls[1].Prompt, "Rejected outline:") {
		t.Fatalf("calls %d", len(backend.calls))
	}
	if strings.Contains(string(document), `"details"`) || !strings.Contains(string(document), "sec_parser") {
		t.Fatalf("the rejected outline: %s", document)
	}
	if _, err := os.Stat(filepath.Join(run.Directory, "rejected-outline-1.json")); err != nil {
		t.Fatal(err)
	}

	generator, run, backend = stagedFixture(t, nil, func(stage string, request Request, n int) (Result, error, bool) {
		return Result{Calls: 1}, &RejectedError{Status: "authentication_error", Err: errors.New("bad key")}, true
	})
	outcome, _, err = generator.StagedReview(context.Background(), run)
	var rejected *RejectedError
	if !errors.As(err, &rejected) || len(backend.calls) != 1 || outcome.Calls != 1 {
		t.Fatalf("%v, %d calls", err, len(backend.calls))
	}
}

// A run fails when the details stage writes none of the sections it was
// asked for, as a one-pass run whose document has no sections would.
func TestStagedReviewFailsWhenNoDetailsAreWritten(t *testing.T) {
	generator, run, _ := stagedFixture(t, nil, func(stage string, request Request, n int) (Result, error, bool) {
		if stage != StageDetails {
			return Result{}, nil, false
		}
		return Result{Calls: 1}, errors.New("unavailable"), true
	})
	_, _, err := generator.StagedReview(context.Background(), run)
	if err == nil || err.Error() != ReasonProviderUnavailable {
		t.Fatalf("err = %v", err)
	}
}

// A section whose hunks are unchanged since a previous version with complete
// details keeps those details and is not sent to the model.
func TestStagedReviewReusesUnchangedSections(t *testing.T) {
	previousSection := func(id, text string, hunks []string, failed bool) bundle.PreviousSection {
		var content map[string]any
		if err := json.Unmarshal([]byte(stagedSectionDetails(id, text)), &content); err != nil {
			t.Fatal(err)
		}
		content["title"], content["kind"], content["hunks"] = "Section "+id, "logical", hunks
		if failed {
			content["details"], content["summary"] = "failed", nil
		}
		encoded, err := json.Marshal(content)
		if err != nil {
			t.Fatal(err)
		}
		return bundle.PreviousSection{ID: id, Title: "Section " + id, Kind: "logical", HunkContentHashes: hunks, Content: encoded}
	}
	for name, test := range map[string]struct {
		tests              bundle.PreviousSection
		reused, written    []string
		calls              int
		session, testsText string
	}{
		"changed hunks":  {previousSection("sec_tests", "Old tests.", []string{testHunk, stagedTestHunk}, false), []string{"sec_parser"}, []string{"sec_tests"}, 3, "session-details", "Writes sec_tests."},
		"failed details": {previousSection("sec_tests", "Old tests.", []string{stagedTestHunk}, true), []string{"sec_parser"}, []string{"sec_tests"}, 3, "session-details", "Writes sec_tests."},
		"all unchanged":  {previousSection("sec_tests", "Old tests.", []string{stagedTestHunk}, false), []string{"sec_parser", "sec_tests"}, nil, 2, "session-outline", "Old tests."},
	} {
		t.Run(name, func(t *testing.T) {
			b := stagedFixtureBundle()
			b.Previous = &bundle.PreviousVersion{Version: 1, Sections: []bundle.PreviousSection{
				previousSection("sec_parser", "Previously reviewed parser.", []string{testHunk}, false), test.tests,
			}}
			generator, run, backend := stagedFixture(t, b, nil)
			outcome, document, err := generator.StagedReview(context.Background(), run)
			if err != nil || outcome.Reason != "" {
				t.Fatalf("%+v %v", outcome, err)
			}
			if len(backend.calls) != test.calls || !slices.Equal(outcome.Details.Reused, test.reused) || !slices.Equal(outcome.Details.Written, test.written) {
				t.Fatalf("calls %d, details %+v", len(backend.calls), outcome.Details)
			}
			for _, request := range backend.calls {
				if stageOf(t, request.Prompt) == StageDetails && !slices.Equal(askedSections(request.Prompt), test.written) {
					t.Fatalf("the details stage was asked for %v", askedSections(request.Prompt))
				}
			}
			sections := stagedSections(t, document)
			if sections["sec_parser"]["summary"].(map[string]any)["text"] != "Previously reviewed parser." || sections["sec_tests"]["summary"].(map[string]any)["text"] != test.testsText {
				t.Fatalf("sections: %v", sections)
			}
			if outcome.SessionID != test.session {
				t.Fatalf("session %q", outcome.SessionID)
			}
			completesStaged(t, run, document)
		})
	}
}

// Progress reports each stage result as it lands with the document so far:
// sections not yet written are pending in a draft, never in the result.
func TestStagedReviewReportsProgress(t *testing.T) {
	var mu sync.Mutex
	var progress []StagedProgress
	generator, run, _ := stagedFixture(t, nil, nil)
	run.Progress = func(p StagedProgress) {
		mu.Lock()
		defer mu.Unlock()
		progress = append(progress, p)
	}
	_, document, err := generator.StagedReview(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if len(progress) != 4 || progress[0].Stage != StageOutline {
		t.Fatalf("progress: %+v", progress)
	}
	first := stagedSections(t, progress[0].Document)
	if first["sec_parser"]["details"] != "pending" || first["sec_tests"]["details"] != "pending" {
		t.Fatalf("the outline draft: %s", progress[0].Document)
	}
	landed := map[string]bool{}
	for _, p := range progress[1:] {
		if p.Failed || p.Reused {
			t.Fatalf("progress: %+v", p)
		}
		landed[p.Stage+" "+p.Section] = true
		sections := stagedSections(t, p.Document)
		for id, section := range sections {
			if done := landed[StageDetails+" "+id]; done == (section["details"] == "pending") {
				t.Fatalf("after %s %s, section %s: %v", p.Stage, p.Section, id, section["details"])
			}
		}
		if _, present := stagedSummary(t, p.Document)["context"]; present != landed[StageContext+" "] {
			t.Fatalf("after %s %s, the context is present: %v", p.Stage, p.Section, present)
		}
		if _, findings := generator.Validator.CompleteStaged(p.Document, run.Bundle, run.Directory, "m", "v", time.Now()); findings != "" {
			t.Fatalf("a draft does not complete: %s", findings)
		}
	}
	if !landed[StageDetails+" sec_parser"] || !landed[StageDetails+" sec_tests"] || !landed[StageContext+" "] {
		t.Fatalf("landed: %v", landed)
	}
	if strings.Contains(string(document), `"pending"`) {
		t.Fatal("the result holds pending details")
	}
}

func TestStageBudgets(t *testing.T) {
	run := Budgets{MaxCalls: 2, MaxTokens: 100, MaxProviderAttempts: 3, MaxRepairAttempts: 2, StallTimeout: time.Second}
	outline := outlineBudgets(run)
	if outline.MaxCalls != 1 || outline.MaxTokens != 25 || outline.MaxProviderAttempts != 3 || outline.MaxRepairAttempts != 2 || outline.StallTimeout != time.Second {
		t.Fatalf("outline %+v", outline)
	}
	for _, test := range []struct {
		calls, tokens                       int
		details                             bool
		detailsCalls, contextCalls, tokens0 int
	}{
		{11, 90, true, 7, 4, 60},
		{1, 90, true, 1, 0, 60},
		{0, 0, true, 0, 0, 0},
		{11, 90, false, 0, 11, 0},
	} {
		details, context := stageBudgets(run, test.calls, test.tokens, test.details)
		if details.MaxCalls != test.detailsCalls || context.MaxCalls != test.contextCalls || details.MaxTokens != test.tokens0 || details.MaxCalls+context.MaxCalls != max(test.calls, 0) || details.MaxTokens+context.MaxTokens != test.tokens {
			t.Errorf("%+v: details %d/%d, context %d/%d", test, details.MaxCalls, details.MaxTokens, context.MaxCalls, context.MaxTokens)
		}
		if details.MaxProviderAttempts != 3 || context.MaxRepairAttempts != 2 {
			t.Errorf("per-call budgets changed")
		}
	}
}

func TestStagedReviewDoesNotRaiseExhaustedOutlineBudgets(t *testing.T) {
	for _, field := range []string{"calls", "tokens"} {
		t.Run(field, func(t *testing.T) {
			generator, run, backend := stagedFixture(t, nil, nil)
			// A negative budget is one already spent; zero would be no limit.
			if field == "calls" {
				run.Budgets.MaxCalls = -1
			} else {
				run.Budgets.MaxTokens = -1
			}
			_, _, err := generator.StagedReview(context.Background(), run)
			want := GenerationBudgetExceededReason("model calls")
			if field == "tokens" {
				want = GenerationBudgetExceededReason("tokens")
			}
			if err == nil || err.Error() != want || len(backend.calls) != 0 {
				t.Fatalf("err = %v, calls = %d", err, len(backend.calls))
			}
		})
	}
}

// A run without enough calls for the context still writes the review.
func TestStagedReviewWithoutBudgetForTheContext(t *testing.T) {
	generator, run, backend := stagedFixture(t, nil, nil)
	run.Budgets.MaxCalls = 2
	outcome, document, err := generator.StagedReview(context.Background(), run)
	if err != nil || outcome.Reason != "" || len(backend.calls) != 2 || outcome.Context.Reason != GenerationBudgetExceededReason("model calls") {
		t.Fatalf("%v %q %d %q", err, outcome.Reason, len(backend.calls), outcome.Context.Reason)
	}
	if _, present := stagedSummary(t, document)["context"]; present {
		t.Fatal("a context without budget")
	}
}

// A secret written into an identifier is masked in every stage and counted
// over the run, so the host rejects it (RejectSecrets).
func TestStagedReviewMasksSecretsInIdentifiers(t *testing.T) {
	generator, run, _ := stagedFixture(t, nil, func(stage string, request Request, n int) (Result, error, bool) {
		if stage != StageDetails {
			return Result{}, nil, false
		}
		leak := strings.Replace(stagedSectionDetails("sec_parser", "Writes the parser."), `"risks":[]`, `"risks":[{"id":"rsk_leak","tier":"low","category":"`+identifierSecret+`","source":"model","text":"A concrete risk.","anchors":[{"type":"hunk","id":"`+testHunk+`"}]}]`, 1)
		return stagedResult(`{"sections":[`+leak+`,`+stagedSectionDetails("sec_tests", "Writes tests.")+`]}`, ""), nil, true
	})
	outcome, document, err := generator.StagedReview(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.IdentifierRedaction == nil || outcome.IdentifierRedaction.MaskedClosed == 0 || outcome.Details.IdentifierRedaction.MaskedClosed == 0 {
		t.Fatalf("identifier redaction: %+v", outcome.IdentifierRedaction)
	}
	if strings.Contains(strings.ToLower(string(document)), identifierSecret) {
		t.Fatal("the document keeps the secret")
	}
	if kept, rejected := RejectSecrets(&outcome.Outcome, document, outcome.IdentifierRedaction.MaskedClosed); !rejected || kept != nil {
		t.Fatal("the host would keep the run")
	}
}

// A regeneration that reuses some sections succeeds even when every section
// sent to the model fails: the reused ones keep their details.
func TestStagedReviewSucceedsWithOnlyReusedSections(t *testing.T) {
	b := stagedFixtureBundle()
	var content map[string]any
	if err := json.Unmarshal([]byte(stagedSectionDetails("sec_parser", "Previously reviewed parser.")), &content); err != nil {
		t.Fatal(err)
	}
	content["title"], content["kind"], content["hunks"] = "Section sec_parser", "logical", []string{testHunk}
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	b.Previous = &bundle.PreviousVersion{Version: 1, Sections: []bundle.PreviousSection{{ID: "sec_parser", Title: "Section sec_parser", Kind: "logical", HunkContentHashes: []string{testHunk}, Content: encoded}}}
	generator, run, _ := stagedFixture(t, b, func(stage string, request Request, n int) (Result, error, bool) {
		if stage != StageDetails {
			return Result{}, nil, false
		}
		return Result{Calls: 1}, errors.New("unavailable"), true
	})
	outcome, document, err := generator.StagedReview(context.Background(), run)
	if err != nil || outcome.Reason != "" || outcome.Details.Reason != ReasonProviderUnavailable {
		t.Fatalf("%+v %v", outcome.Outcome, err)
	}
	if !slices.Equal(outcome.Details.Reused, []string{"sec_parser"}) || !slices.Equal(outcome.Details.Failed, []string{"sec_tests"}) {
		t.Fatalf("details %+v", outcome.Details)
	}
	sections := stagedSections(t, document)
	if sections["sec_tests"]["details"] != "failed" || sections["sec_parser"]["details"] != nil {
		t.Fatalf("sections %v", sections)
	}
	completesStaged(t, run, document)
}

// Every stage outcome is masked as the run's is: a secret the context stage
// wrote into its rejected reply does not survive RedactStagedOutput.
func TestRedactStagedOutputMasksEveryStage(t *testing.T) {
	generator, run, _ := stagedFixture(t, nil, func(stage string, request Request, n int) (Result, error, bool) {
		if stage != StageContext {
			return Result{}, nil, false
		}
		return stagedResult(`{"context":{"related":{"text":"Calls the API with `+identifierSecret+`.","refs":[]}}}`, ""), nil, true
	})
	outcome, document, err := generator.StagedReview(context.Background(), run)
	if err != nil || outcome.Context.Reason != ReasonValidationRejected {
		t.Fatalf("%v %q", err, outcome.Context.Reason)
	}
	if !strings.Contains(string(outcome.Context.RejectedDocument), identifierSecret) {
		t.Fatal("the fixture's secret did not reach the stage outcome")
	}
	redactor, err := bundle.NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	outcome.Details.Reason = "failed with " + identifierSecret
	redacted, _, err := RedactStagedOutput(context.Background(), generator.Validator, redactor, document, run.Bundle, nil, &outcome)
	if err != nil || len(redacted) == 0 {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"rejected context": string(outcome.Context.RejectedDocument), "details reason": outcome.Details.Reason, "context findings": outcome.Context.Findings} {
		if strings.Contains(text, identifierSecret) {
			t.Errorf("the %s keeps the secret", name)
		}
	}
	if outcome.Context.OutputMaskedSpans == 0 || outcome.Context.IdentifierRedaction == nil {
		t.Fatalf("context outcome: %+v", outcome.Context.Outcome)
	}
}

// The identifier pass runs on every context reply, one without a context
// key included, before it is written as a rejected reply.
func TestStagedReviewMasksEveryContextReply(t *testing.T) {
	generator, run, _ := stagedFixture(t, nil, func(stage string, request Request, n int) (Result, error, bool) {
		if stage != StageContext {
			return Result{}, nil, false
		}
		return stagedResult(`{"area":{"text":"Misplaced.","refs":[]}}`, ""), nil, true
	})
	outcome, _, err := generator.StagedReview(context.Background(), run)
	if err != nil || outcome.Context.Reason != ReasonValidationRejected || outcome.Context.IdentifierRedaction == nil {
		t.Fatalf("%v %+v", err, outcome.Context.Outcome)
	}
	if _, err := os.Stat(filepath.Join(run.Directory, "rejected-context-1.json")); err != nil {
		t.Fatal(err)
	}
}

// A secret masked in a closed value of the outline stops the later stages:
// the host rejects the run whatever they would write.
func TestStagedReviewStopsAfterASecretInTheOutline(t *testing.T) {
	generator, run, backend := stagedFixture(t, nil, func(stage string, request Request, n int) (Result, error, bool) {
		if stage != StageOutline {
			return Result{}, nil, false
		}
		return stagedResult(strings.Replace(string(stagedOutlineDocument()), `"id":"sec_tests"`, `"id":"sec_`+identifierSecret+`"`, 1), ""), nil, true
	})
	outcome, document, err := generator.StagedReview(context.Background(), run)
	if err != nil || len(backend.calls) != 1 || outcome.IdentifierRedaction.MaskedClosed == 0 {
		t.Fatalf("%v, %d calls, %+v", err, len(backend.calls), outcome.IdentifierRedaction)
	}
	if strings.Contains(strings.ToLower(string(document)), identifierSecret) || strings.Contains(string(document), `"pending"`) {
		t.Fatalf("document: %s", document)
	}
	if _, rejected := RejectSecrets(&outcome.Outcome, document, outcome.IdentifierRedaction.MaskedClosed); !rejected {
		t.Fatal("the host would keep the run")
	}
}

func TestStagedReviewRefusesDecoration(t *testing.T) {
	generator, run, backend := stagedFixture(t, nil, nil)
	run.Decorate = func(document []byte) []byte { return document }
	if _, _, err := generator.StagedReview(context.Background(), run); err == nil || len(backend.calls) != 0 {
		t.Fatalf("err %v, %d calls", err, len(backend.calls))
	}
}

// A run given an accepted outline continues it: no outline or context call,
// the given context kept, the sections Reuse holds complete reused, and only
// the others written, with every call of the budgets.
func TestStagedReviewContinuesAnOutline(t *testing.T) {
	previousSection := func(id string, failed bool) bundle.PreviousSection {
		var content map[string]any
		if err := json.Unmarshal([]byte(stagedSectionDetails(id, "Saved "+id+".")), &content); err != nil {
			t.Fatal(err)
		}
		hunk := map[string]string{"sec_parser": testHunk, "sec_tests": stagedTestHunk}[id]
		content["title"], content["kind"], content["hunks"] = "Section "+id, "logical", []string{hunk}
		if failed {
			content["details"], content["summary"] = "failed", nil
		}
		encoded, err := json.Marshal(content)
		if err != nil {
			t.Fatal(err)
		}
		return bundle.PreviousSection{ID: id, Title: "Section " + id, Kind: "logical", HunkContentHashes: []string{hunk}, Content: encoded}
	}
	generator, run, backend := stagedFixture(t, nil, nil)
	run.Outline = stagedOutlineDocument()
	run.Context = []byte(`{"area":{"text":"The parser turns source text into a syntax tree.","refs":[]}}`)
	run.Reuse = &bundle.PreviousVersion{Version: 1, Sections: []bundle.PreviousSection{previousSection("sec_parser", false), previousSection("sec_tests", true)}}
	run.Budgets.MaxCalls = 1
	outcome, document, err := generator.StagedReview(context.Background(), run)
	if err != nil || outcome.Reason != "" {
		t.Fatalf("%+v %v", outcome.Outcome, err)
	}
	if len(backend.calls) != 1 || stageOf(t, backend.calls[0].Prompt) != StageDetails || !slices.Equal(askedSections(backend.calls[0].Prompt), []string{"sec_tests"}) {
		t.Fatalf("calls %d", len(backend.calls))
	}
	if !slices.Equal(outcome.Details.Reused, []string{"sec_parser"}) || !slices.Equal(outcome.Details.Written, []string{"sec_tests"}) || outcome.SessionID != "session-details" {
		t.Fatalf("details %+v, session %q", outcome.Details, outcome.SessionID)
	}
	sections := stagedSections(t, document)
	if sections["sec_parser"]["summary"].(map[string]any)["text"] != "Saved sec_parser." || sections["sec_tests"]["summary"].(map[string]any)["text"] != "Writes sec_tests." {
		t.Fatalf("sections %v", sections)
	}
	if stagedSummary(t, document)["context"] == nil {
		t.Fatal("the given context was not kept")
	}
	completesStaged(t, run, document)

	// An outline that no longer validates fails the run before any call.
	generator, run, backend = stagedFixture(t, nil, nil)
	run.Outline = []byte(`{"schema":"baum.review/1","sections":[]}`)
	if _, _, err := generator.StagedReview(context.Background(), run); err == nil || len(backend.calls) != 0 {
		t.Fatalf("err %v, calls %d", err, len(backend.calls))
	}
}
