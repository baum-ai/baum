package generate_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/sessions"
)

func stagedOptions(t *testing.T, f *fixture) generate.Options {
	o := f.options(t)
	o.Staged = true
	o.Budgets.MaxCalls = 16
	return o
}

func sectionStates(t *testing.T, document []byte) map[string]any {
	t.Helper()
	var doc struct {
		Sections []map[string]any `json:"sections"`
	}
	if err := json.Unmarshal(document, &doc); err != nil {
		t.Fatal(err)
	}
	states := map[string]any{}
	for _, section := range doc.Sections {
		states[section["id"].(string)] = section["details"]
	}
	return states
}

// A staged run sends the outline, then the details and the context, each
// prompt starting with the generation-run marker; the drafts it reports are
// completed, with the sections not yet written pending, and the document is
// published with the staged prompt's version and digest.
func TestStagedRunPublishesWithDrafts(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = generatetest.StagedReplies(nil)
	o := stagedOptions(t, f)
	var mu sync.Mutex
	var drafts [][]byte
	o.OnDraft = func(document []byte) {
		mu.Lock()
		defer mu.Unlock()
		drafts = append(drafts, document)
	}
	result, err := generate.Run(context.Background(), o)
	if err != nil {
		t.Fatalf("%v\n%s", err, f.progress.String())
	}
	var stages []string
	for _, request := range f.backend.Requests {
		stages = append(stages, generatetest.Stage(request))
		if !strings.HasPrefix(request.Prompt, sessions.GenerationRunMarker) {
			t.Errorf("a %s prompt lacks the marker: %.60q", generatetest.Stage(request), request.Prompt)
		}
	}
	if !slices.Equal(stages, []string{agent.StageOutline, agent.StageDetails, agent.StageContext}) {
		t.Fatalf("stages %v", stages)
	}
	if len(drafts) == 0 {
		t.Fatal("no draft was reported")
	}
	if states := sectionStates(t, drafts[0]); states["sec_changes"] != "pending" || states["sec_rest"] != "pending" {
		t.Errorf("first draft %v", states)
	}
	staged, _ := prompt.StagedPrompts{}.StagedInstructions(context.Background(), "claude")
	if result.Metadata.PromptVersion != staged.Version || result.Metadata.PromptDigest == staged.Digest || result.Metadata.PromptDigest == "" {
		t.Errorf("prompt %q %q", result.Metadata.PromptVersion, result.Metadata.PromptDigest)
	}
	if states := sectionStates(t, result.Document); states["sec_changes"] != nil || states["sec_rest"] != nil || len(result.FailedSections) != 0 || result.ContextFailed {
		t.Errorf("published %v, gaps %v %v", states, result.FailedSections, result.ContextFailed)
	}
	for _, line := range []string{"outline, pass 1: the outline is valid", "details, pass 1: the details is valid", "context, pass 1: the context is valid"} {
		if !strings.Contains(f.progress.String(), line) {
			t.Errorf("progress lacks %q:\n%s", line, f.progress.String())
		}
	}
	f.requireClean(t)
}

// A section whose details stay invalid is published as failed, a context
// that fails is left out, and the run succeeds; an outline that fails fails
// it.
func TestStagedRunPublishesWithGaps(t *testing.T) {
	t.Run("failed section", func(t *testing.T) {
		f := newFixture(t)
		f.backend.RunFunc = generatetest.StagedReplies(generatetest.WithoutSection("sec_rest"))
		result, err := generate.Run(context.Background(), stagedOptions(t, f))
		if err != nil {
			t.Fatalf("%v\n%s", err, f.progress.String())
		}
		if states := sectionStates(t, result.Document); states["sec_rest"] != "failed" || states["sec_changes"] != nil || !slices.Equal(result.FailedSections, []string{"sec_rest"}) {
			t.Fatalf("states %v, failed %v", states, result.FailedSections)
		}
		if !strings.Contains(f.progress.String(), "1 section(s) without their details") {
			t.Errorf("progress:\n%s", f.progress.String())
		}
	})
	t.Run("failed context", func(t *testing.T) {
		f := newFixture(t)
		f.backend.RunFunc = generatetest.StagedReplies(func(stage string, _ agent.Request) (agent.Result, error, bool) {
			return agent.Result{Calls: 1}, errors.New("unavailable"), stage == agent.StageContext
		})
		result, err := generate.Run(context.Background(), stagedOptions(t, f))
		if err != nil {
			t.Fatalf("%v\n%s", err, f.progress.String())
		}
		if !result.ContextFailed || len(result.FailedSections) != 0 || strings.Contains(string(result.Document), `"context":{`) {
			t.Fatalf("gaps %v %v", result.FailedSections, result.ContextFailed)
		}
	})
	t.Run("failed outline", func(t *testing.T) {
		f := newFixture(t)
		f.backend.RunFunc = generatetest.StagedReplies(func(stage string, _ agent.Request) (agent.Result, error, bool) {
			return agent.Result{Document: []byte(`{"schema":"baum.review/1","sections":[]}`), Model: "stub-model", Calls: 1}, nil, stage == agent.StageOutline
		})
		_, err := generate.Run(context.Background(), stagedOptions(t, f))
		if generate.CategoryOf(err) != generate.CategoryValidation {
			t.Fatalf("err %v", err)
		}
		for _, request := range f.backend.Requests {
			if generatetest.Stage(request) != agent.StageOutline {
				t.Fatalf("a %s call after a failed outline", generatetest.Stage(request))
			}
		}
		f.requireClean(t)
	})
}

// A saved version with a failed section is completed: only the details of
// that section are written again, and the result is the same version, with
// its metadata and generation time, which CompleteCurrent stores in place.
func TestContinueWritesOnlyTheFailedDetails(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	failing := true
	without := generatetest.WithoutSection("sec_rest")
	f.backend.RunFunc = generatetest.StagedReplies(func(stage string, request agent.Request) (agent.Result, error, bool) {
		if !failing {
			return agent.Result{}, nil, false
		}
		return without(stage, request)
	})
	s, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	if _, err := generate.Publish(ctx, s, key, stagedOptions(t, f), nil); err != nil {
		t.Fatalf("%v\n%s", err, f.progress.String())
	}
	saved, err := s.Latest(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if states := sectionStates(t, saved.Document); states["sec_rest"] != "failed" {
		t.Fatalf("saved %v", states)
	}
	failing = false
	f.backend.Requests = nil
	o := stagedOptions(t, f)
	o.Continue = &saved
	result, err := generate.Run(ctx, o)
	if err != nil {
		t.Fatalf("%v\n%s", err, f.progress.String())
	}
	if len(f.backend.Requests) != 1 || generatetest.Stage(f.backend.Requests[0]) != agent.StageDetails || !slices.Equal(generatetest.AskedSections(f.backend.Requests[0]), []string{"sec_rest"}) {
		t.Fatalf("%d request(s)", len(f.backend.Requests))
	}
	if states := sectionStates(t, result.Document); states["sec_rest"] != nil || states["sec_changes"] != nil || len(result.FailedSections) != 0 {
		t.Fatalf("completed %v", states)
	}
	if result.Metadata != saved.Metadata {
		t.Errorf("metadata changed: %+v", result.Metadata)
	}
	if err := s.CompleteCurrent(ctx, key, saved, result.Document); err != nil {
		t.Fatal(err)
	}
	// A pull request that moved since is refused before any call.
	moved := saved
	moved.Metadata.Head = strings.Repeat("0", 40)
	f.backend.Requests = nil
	o.Continue = &moved
	if _, err := generate.Run(ctx, o); generate.CategoryOf(err) != generate.CategoryInput || len(f.backend.Requests) != 0 {
		t.Fatalf("err %v, %d request(s)", err, len(f.backend.Requests))
	}
}

// A stale section carried from an earlier version is not a gap: nothing
// writes its details again.
func TestGapsSkipStaleSections(t *testing.T) {
	failed, noContext := generate.Gaps([]byte(`{"summary":{"context":{"area":{"text":"A.","refs":[]}}},"sections":[{"id":"sec_a","details":"failed","derived":{"state":"new"}},{"id":"sec_old","details":"failed","derived":{"state":"stale"}},{"id":"sec_b","derived":{"state":"unchanged"}}]}`))
	if !slices.Equal(failed, []string{"sec_a"}) || noContext {
		t.Fatalf("failed %v, no context %v", failed, noContext)
	}
}

// A draft is masked before anyone sees it, and a run that masked a secret in
// a closed value, which the host rejects, shows no draft at all.
func TestStagedDraftsAreMasked(t *testing.T) {
	outline := func(edit func(string) string) generatetest.Answer {
		return func(stage string, request agent.Request) (agent.Result, error, bool) {
			if stage != agent.StageOutline {
				return agent.Result{}, nil, false
			}
			document, err := generatetest.Document(request.Directory, generatetest.TwoSections)
			return agent.Result{Document: []byte(edit(string(generatetest.ForStage(request, document)))), Model: "stub-model", Calls: 1, Tokens: 100}, err, true
		}
	}
	run := func(t *testing.T, answer generatetest.Answer) ([][]byte, error) {
		f := newFixture(t)
		f.backend.RunFunc = generatetest.StagedReplies(answer)
		o := stagedOptions(t, f)
		var mu sync.Mutex
		var drafts [][]byte
		o.OnDraft = func(document []byte) {
			mu.Lock()
			defer mu.Unlock()
			drafts = append(drafts, document)
		}
		_, err := generate.Run(context.Background(), o)
		return drafts, err
	}
	drafts, err := run(t, outline(func(reply string) string {
		return strings.Replace(reply, "Adds notes, settings and a renamed guide.", "Adds notes; the token is "+assembletest.PlantedSecret+".", 1)
	}))
	if err != nil || len(drafts) == 0 {
		t.Fatalf("%v, %d draft(s)", err, len(drafts))
	}
	for _, draft := range drafts {
		if strings.Contains(string(draft), assembletest.PlantedSecret) {
			t.Fatal("a draft holds the secret")
		}
	}
	const closed = "sk_live_4ec3b2a1f9d84c7e6b5a3f2e1d0c9b8a"
	drafts, err = run(t, outline(func(reply string) string {
		return strings.Replace(reply, `"id":"sec_rest"`, `"id":"sec_`+closed+`"`, 1)
	}))
	if generate.CategoryOf(err) != generate.CategoryValidation || len(drafts) != 0 {
		t.Fatalf("%v, %d draft(s)", err, len(drafts))
	}
}
