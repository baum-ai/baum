// Package generatetest is a stub model backend for tests of "the generation pipeline":
// it returns canned documents without calling a model, and records the run
// directories it was given.
package generatetest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/review"
)

// Backend is an agent.Backend and a generate.CheckedBackend whose runs a test
// scripts. With no RunFunc it returns Document(request.Directory) as the model
// "stub-model", with usage.
type Backend struct {
	// BackendName defaults to claude.
	BackendName string
	CheckErr    error
	Version     string
	RunFunc     func(ctx context.Context, request agent.Request, n int) (agent.Result, error)

	mu sync.Mutex
	// Requests are the requests received, in order. Directories are the run
	// directories of them, and Existed whether each held a bundle.json and a
	// checkout when the backend ran.
	Requests []agent.Request
	Existed  []bool
}

// Name implements agent.Backend.
func (b *Backend) Name() string {
	if b.BackendName == "" {
		return agent.BackendClaude
	}
	return b.BackendName
}

// Check implements generate.CheckedBackend.
func (b *Backend) Check(context.Context) (string, error) { return b.Version, b.CheckErr }

// Run implements agent.Backend.
func (b *Backend) Run(ctx context.Context, request agent.Request) (agent.Result, error) {
	b.mu.Lock()
	n := len(b.Requests)
	b.Requests = append(b.Requests, request)
	_, bundleErr := os.Stat(filepath.Join(request.Directory, "bundle.json"))
	_, headErr := os.Stat(filepath.Join(request.Directory, "head"))
	b.Existed = append(b.Existed, bundleErr == nil && headErr == nil)
	b.mu.Unlock()
	if b.RunFunc != nil {
		return b.RunFunc(ctx, request, n)
	}
	document, err := Document(request.Directory, nil)
	if err != nil {
		return agent.Result{}, err
	}
	result := agent.Result{Document: ForStage(request, document), Model: "stub-model", Calls: 1, Tokens: 100, Usage: agent.Usage{Input: 60, Output: 40}}
	if request.PersistSession {
		result.SessionID = StageSessionID(request)
	}
	return result, nil
}

// SessionID is the session the default Run reports when asked to persist one
// for a review in one pass or a staged review's details; StageSessionID
// names the others.
const SessionID = "11111111-2222-4333-8444-555555555555"

// The sessions the default Run reports for the outline and context stages
// of a staged review.
const (
	OutlineSessionID = "11111111-2222-4333-8444-000000000001"
	ContextSessionID = "11111111-2222-4333-8444-000000000003"
)

// StageSessionID is the session the default Run reports for a request.
func StageSessionID(request agent.Request) string {
	switch Stage(request) {
	case agent.StageOutline:
		return OutlineSessionID
	case agent.StageContext:
		return ContextSessionID
	}
	return SessionID
}

// Stage names the stage of a staged review a request is for, by the title of
// its schema: agent.StageOutline, StageDetails or StageContext, or "" for a
// review in one pass.
func Stage(request agent.Request) string {
	var schema struct {
		Title string `json:"title"`
	}
	_ = json.Unmarshal(request.Schema, &schema)
	switch schema.Title {
	case "The outline of a baum review":
		return agent.StageOutline
	case "Section details of a baum review":
		return agent.StageDetails
	case "The codebase context of a baum review":
		return agent.StageContext
	}
	return ""
}

// AskedSections are the sections a details request asks for: the list of
// its first pass, or the sections a repair names.
func AskedSections(request agent.Request) []string {
	if _, list, ok := strings.Cut(request.Prompt, "in this order: "); ok {
		list, _, _ = strings.Cut(list, ".\n")
		return strings.Split(list, ", ")
	}
	var ids []string
	for _, line := range strings.Split(request.Prompt, "\n") {
		if id, ok := strings.CutPrefix(line, "Section "); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// ForStage is the part of a whole model document that a request's stage
// writes: the outline, the details of the sections it asks for, or the
// context; the document itself for a review in one pass. A document that
// cannot be read is returned as it is.
func ForStage(request agent.Request, document []byte) []byte {
	stage := Stage(request)
	if stage == "" {
		return document
	}
	var doc map[string]any
	if json.Unmarshal(document, &doc) != nil {
		return document
	}
	var part any
	switch stage {
	case agent.StageOutline:
		return review.OutlineOf(document)
	case agent.StageDetails:
		sections := map[string]json.RawMessage{}
		var all struct {
			Sections []json.RawMessage `json:"sections"`
		}
		_ = json.Unmarshal(document, &all)
		for _, section := range all.Sections {
			sections[review.SectionID(section)] = review.DetailsOf(section)
		}
		var asked []json.RawMessage
		for _, id := range AskedSections(request) {
			if section, ok := sections[id]; ok {
				asked = append(asked, section)
			}
		}
		part = map[string]any{"sections": asked}
	case agent.StageContext:
		summary, _ := doc["summary"].(map[string]any)
		part = map[string]any{"context": summary["context"]}
	}
	encoded, err := json.Marshal(part)
	if err != nil {
		return document
	}
	return encoded
}

// Document builds a valid model document for the bundle the loop wrote into
// dir: one section holding every hunk, anchored at the first. edit, when not
// nil, changes the decoded document before it is encoded.
func Document(dir string, edit func(map[string]any)) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		return nil, err
	}
	return DocumentFor(raw, edit)
}

// DocumentFor is Document for the encoded bundle, for a stub that sees only
// the prompt.
func DocumentFor(raw []byte, edit func(map[string]any)) ([]byte, error) {
	var b bundle.Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	if len(b.Diff) == 0 {
		return nil, fmt.Errorf("the bundle has no hunks")
	}
	ids := make([]string, len(b.Diff))
	for i, hunk := range b.Diff {
		ids[i] = hunk.ID
	}
	document := map[string]any{
		"schema": "baum.review/1",
		"summary": map[string]any{
			"text": "Adds notes, settings and a renamed guide.", "review_exchange": map[string]any{"summary": "No reviews available."},
			"context": map[string]any{"area": map[string]any{
				"text": "The demo app keeps notes and settings beside its guide.",
				"refs": []any{map[string]any{"path": b.Diff[0].Path, "symbol": nil}},
			}},
			"change_kind": "feature", "implements": []any{}, "release_note": nil, "breaking_change": nil, "attention": nil,
			"recommendation": nil,
			"authorship": map[string]any{"declared": "unknown", "declared_tools": []any{}, "anchors": []any{}},
		},
		"relations": []any{},
		"sections": []any{map[string]any{
			"id": "sec_changes", "title": "Changes", "kind": "logical", "hunks": ids,
			"summary":   map[string]any{"text": "The whole change.", "anchors": []any{map[string]any{"type": "hunk", "id": ids[0]}}},
			"attention": map[string]any{"text": "Check the settings handling.", "refs": []any{"rsk_settings", "ev_unit"}},
			"assessment": map[string]any{
				"level": 3, "factors": []any{"untrusted_input"},
				"reasons": []any{map[string]any{"text": "Settings come from the environment.", "anchors": []any{map[string]any{"type": "hunk", "id": ids[0]}}}},
			},
			"risks": []any{map[string]any{
				"id": "rsk_settings", "tier": "medium", "category": "data_integrity", "source": "model",
				"text": "Settings are read from the environment without validation.", "finding": nil,
				"anchors": []any{map[string]any{"type": "hunk", "id": ids[0]}},
			}},
			"evidence": []any{map[string]any{
				"id": "ev_unit", "kind": "test", "claim": "The change is covered by the demo tests.", "status": "asserted", "source": "model",
				"anchors": []any{map[string]any{"type": "hunk", "id": ids[0]}}, "detail": nil,
			}},
			"blocks": []any{},
		}},
	}
	if edit != nil {
		edit(document)
	}
	return json.Marshal(document)
}

// TwoSections splits the stub document into a section of the first hunk,
// sec_changes, and one of the others, sec_rest, so one can fail while the
// other is written. Pass it as Document's edit.
func TwoSections(d map[string]any) {
	section := d["sections"].([]any)[0].(map[string]any)
	hunks := section["hunks"].([]string)
	first, second := map[string]any{}, map[string]any{}
	for key, value := range section {
		first[key], second[key] = value, value
	}
	first["hunks"] = hunks[:1]
	second["id"], second["title"], second["hunks"] = "sec_rest", "The rest", hunks[1:]
	anchor := []any{map[string]any{"type": "hunk", "id": hunks[1]}}
	second["summary"] = map[string]any{"text": "The rest of the change.", "anchors": anchor}
	second["attention"] = nil
	second["risks"], second["evidence"] = []any{}, []any{}
	second["assessment"] = map[string]any{"level": 2, "factors": []any{}, "reasons": []any{map[string]any{"text": "Small.", "anchors": anchor}}}
	d["sections"] = []any{first, second}
}

// Answer answers a request of a stage itself, or reports false to leave it
// to StagedReplies.
type Answer func(stage string, request agent.Request) (agent.Result, error, bool)

// StagedReplies is a RunFunc that answers every stage, and a review in one
// pass, from the two-section document (TwoSections), with the stage's
// session when one is asked to persist, unless answer answers first.
func StagedReplies(answer Answer) func(context.Context, agent.Request, int) (agent.Result, error) {
	return func(_ context.Context, request agent.Request, _ int) (agent.Result, error) {
		if answer != nil {
			if result, err, ok := answer(Stage(request), request); ok {
				return result, err
			}
		}
		document, err := Document(request.Directory, TwoSections)
		result := agent.Result{Document: ForStage(request, document), Model: "stub-model", Calls: 1, Tokens: 100}
		if request.PersistSession {
			result.SessionID = StageSessionID(request)
		}
		return result, err
	}
}

// WithoutSection is an Answer whose section details never include the
// section id, which therefore fails after its repairs.
func WithoutSection(id string) Answer {
	return func(stage string, request agent.Request) (agent.Result, error, bool) {
		if stage != agent.StageDetails {
			return agent.Result{}, nil, false
		}
		document, err := Document(request.Directory, TwoSections)
		var all struct {
			Sections []json.RawMessage `json:"sections"`
		}
		_ = json.Unmarshal(ForStage(request, document), &all)
		kept := []json.RawMessage{}
		for _, section := range all.Sections {
			if review.SectionID(section) != id {
				kept = append(kept, section)
			}
		}
		if len(kept) == 0 {
			// The schema needs one section: a stray one is not the section.
			kept = append(kept, json.RawMessage(`{"id":"sec_stray"}`))
		}
		encoded, _ := json.Marshal(map[string]any{"sections": kept})
		return agent.Result{Document: encoded, Model: "stub-model", Calls: 1, Tokens: 100}, err, true
	}
}
