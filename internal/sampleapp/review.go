package sampleapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/review"
)

// section is one section of a sample review, as a model would write it:
// the files it covers stand for their hunks.
type section struct {
	id, title, summary, intent string
	files                      []string
	level                      int
	reason                     string
	risk                       *risk
	behavior                   []string
	test                       string
}

type risk struct{ tier, category, text string }

// reviewed is a sample review: the pull request and what the review says.
type reviewed struct {
	pr             pull
	summary, kind  string
	releaseNote    string
	area, attended string
	sections       []section
}

var reviews = []reviewed{
	{pr: tags, kind: "feature",
		summary:     "Notes gain tags, cleaned when a note is saved, and the store can list the notes that carry one.",
		releaseNote: "Notes can be tagged and filtered by tag.",
		area:        "The notes library's model and in-memory store.",
		attended:    "ByTag reads every note on each call.",
		sections: []section{
			{id: "sec_tags", title: "Tags on notes", files: []string{"note.go", "store.go"}, level: 2,
				summary:  "Each note carries a list of tags, which Add cleans before saving: trimmed, lowercased and without empty or repeated tags.",
				intent:   "Tags should behave the same however people type them, so \"Work\" and \" work\" are one tag.",
				reason:   "A note saved with only blank tags ends up with none, without an error.",
				risk:     &risk{"low", "data_integrity", "Blank tags are dropped silently, so a typo in an importer would go unnoticed"},
				behavior: []string{"Add trims and lowercases every tag of the note", "Repeated and empty tags are dropped before the note is stored"}},
			{id: "sec_filter", title: "Filter notes by tag", files: []string{"tags.go"}, level: 3,
				summary:  "CleanTags holds the cleaning rules, and ByTag returns the notes with a tag, newest first, cleaning the tag it is asked for the same way.",
				intent:   "People should be able to see just the notes for one topic.",
				reason:   "ByTag scans every note on each call.",
				risk:     &risk{"medium", "performance", "ByTag reads every note on each call, so filtering gets slower as the list grows"},
				behavior: []string{"ByTag(\"Work\") finds notes tagged work", "Results keep the list's newest-first order"},
				test:     "tags_test.go"},
			{id: "sec_docs", title: "Tests and README", files: []string{"tags_test.go", "README.md"}, level: 1,
				summary: "Tests cover cleaning and filtering, and the README shows tags in its example.",
				reason:  "Tests and documentation only."},
		}},
	{pr: accents, kind: "feature",
		summary:     "Search ignores accents as well as case, so \"cafe\" finds \"Café\".",
		releaseNote: "Search matches words with or without accents.",
		area:        "The notes library's search.",
		sections: []section{
			{id: "sec_fold", title: "Search ignores accents", files: []string{"search.go", "search_test.go"}, level: 2,
				summary:  "Search folds the query and each note with a small table of accented Latin letters before comparing them.",
				intent:   "Searching should find a note however its words were accented.",
				reason:   "Only the letters in the table are folded.",
				risk:     &risk{"low", "behavior", "Letters outside the table, such as ø or ł, still have to match exactly"},
				behavior: []string{"\"cafe\" finds a note titled \"Café\"", "Case is still ignored"},
				test:     "search_test.go"},
		}},
}

// writeReviews saves a review of each reviewed pull request into the
// clone's store, finished the way generated reviews are.
func writeReviews(ctx context.Context, p project, commonDir string, now time.Time) error {
	repo, err := gitlocal.Open(ctx, p.dir, gitlocal.Options{})
	if err != nil {
		return err
	}
	defer func() { _ = repo.Close() }()
	validator, err := review.NewDocumentValidator()
	if err != nil {
		return err
	}
	versions, err := store.Open(commonDir, "", true)
	if err != nil {
		return err
	}
	for _, r := range reviews {
		head := p.heads[r.pr.number]
		comparison, err := repo.Compare(ctx, p.main, head)
		if err != nil {
			return err
		}
		hunks, err := bundle.BuildDiffIndex(comparison, repo.PatchFetcher(ctx, comparison), bundle.RoleForPath)
		if err != nil {
			return err
		}
		opened := now.Add(-r.pr.opened)
		b := bundle.Bundle{Repository: repository, Diff: hunks, Sources: bundle.Sources{"diff": true},
			PullRequest: bundle.PullRequest{Number: r.pr.number, State: "open", Title: r.pr.title, Body: r.pr.body,
				AuthorLogin: r.pr.author.login, AuthorAssociation: "MEMBER", BaseRef: "main", BaseSHA: p.main,
				HeadRef: r.pr.branch, HeadSHA: head, MergeBaseSHA: comparison.MergeBaseOID, OpenedAt: &opened}}
		authored, err := r.authored(hunks, head)
		if err != nil {
			return err
		}
		document, findings := validator.Complete(authored, &b, "", "sample", now.Add(-r.pr.updated+time.Hour/2))
		if findings != "" {
			return fmt.Errorf("sample review of #%d: %s", r.pr.number, findings)
		}
		version := store.Version{Metadata: store.Metadata{Version: 1, Head: head, GeneratedAt: now.UTC().Format(time.RFC3339), Backend: "claude", Model: "sample"}, Document: document}
		if err := versions.Put(ctx, store.Key{Repository: repository, Number: r.pr.number}, store.Version{}, version); err != nil {
			return err
		}
	}
	return nil
}

// authored writes the review as the model writes it.
func (r reviewed) authored(hunks []bundle.Hunk, head string) ([]byte, error) {
	byPath := map[string][]string{}
	for _, h := range hunks {
		byPath[h.Path] = append(byPath[h.Path], h.ID)
	}
	anchor := func(id string) map[string]any { return map[string]any{"type": "hunk", "id": id} }
	var sections []any
	for _, s := range r.sections {
		var ids []string
		for _, file := range s.files {
			ids = append(ids, byPath[file]...)
		}
		if len(ids) == 0 {
			return nil, errors.New("sample section " + s.id + " covers no hunk")
		}
		first := []any{anchor(ids[0])}
		blocks := []any{}
		if s.intent != "" {
			blocks = append(blocks, map[string]any{"kind": "intent", "text": s.intent, "anchors": []any{map[string]any{"type": "commit", "sha": head}}})
		}
		if len(s.behavior) > 0 {
			var steps []any
			for i, text := range s.behavior {
				steps = append(steps, map[string]any{"id": fmt.Sprintf("beh_%s_%d", s.id[4:], i+1), "text": text, "anchors": first, "evidence": []any{}})
			}
			blocks = append(blocks, map[string]any{"kind": "behavior", "steps": steps})
		}
		risks, evidence := []any{}, []any{}
		var attention any
		if s.risk != nil {
			id := "rsk_" + s.id[4:]
			risks = append(risks, map[string]any{"id": id, "tier": s.risk.tier, "category": s.risk.category, "source": "model", "text": s.risk.text, "finding": nil, "anchors": first})
			if s.risk.tier == "medium" {
				attention = map[string]any{"text": s.risk.text + ".", "refs": []any{id}}
			}
		}
		if s.test != "" && len(byPath[s.test]) > 0 {
			evidence = append(evidence, map[string]any{"id": "ev_" + s.id[4:], "kind": "test", "claim": "Unit tests cover the change.", "status": "asserted", "source": "model",
				"anchors": []any{anchor(byPath[s.test][0])}, "detail": nil})
		}
		factors := []any{}
		sections = append(sections, map[string]any{
			"id": s.id, "title": s.title, "kind": "logical", "hunks": ids,
			"summary":    map[string]any{"text": s.summary, "anchors": first},
			"attention":  attention,
			"assessment": map[string]any{"level": s.level, "factors": factors, "reasons": []any{map[string]any{"text": s.reason, "anchors": first}}},
			"risks":      risks, "evidence": evidence, "blocks": blocks,
		})
	}
	var attention any
	if r.attended != "" {
		attention = map[string]any{"text": r.attended, "refs": []any{"rsk_filter"}}
	}
	return json.Marshal(map[string]any{
		"schema": "baum.review/1",
		"summary": map[string]any{
			"text": r.summary, "review_exchange": map[string]any{"summary": "No reviews yet."},
			"context":     map[string]any{"area": map[string]any{"text": r.area, "refs": []any{map[string]any{"path": hunks[0].Path, "symbol": nil}}}},
			"change_kind": r.kind, "implements": []any{}, "release_note": r.releaseNote, "breaking_change": nil, "attention": attention,
			"authorship": map[string]any{"declared": "ai_assisted", "declared_tools": []any{}, "anchors": []any{}},
		},
		"relations": []any{},
		"sections":  sections,
	})
}
