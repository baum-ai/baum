package refresh

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/schema"
)

type sources struct {
	evidence               github.Evidence
	checksHead, base, head string
	err                    error
}

func (s *sources) Fetch(_ context.Context, _ github.Repository, _ int, opts github.FetchOptions) (github.Evidence, error) {
	s.checksHead = opts.ChecksHead
	return s.evidence, s.err
}
func (s *sources) Commits(_ context.Context, base, head string) ([]gitlocal.Commit, error) {
	s.base, s.head = base, head
	return []gitlocal.Commit{{OID: head}}, nil
}
func (s *sources) Evidence(_ context.Context, commits []gitlocal.Commit, _, _ int) ([]bundle.Session, error) {
	if len(commits) != 1 || commits[0].OID != s.head {
		return nil, errors.New("wrong linked commits")
	}
	return nil, nil
}

func object(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestRefreshLocalSourcesAndCurrencyPreserveAuthorship(t *testing.T) {
	before := object(t, schema.Example)
	header := before["review"].(map[string]any)
	saved := store.Version{Document: schema.Example, Metadata: store.Metadata{Version: 3, Head: header["head"].(string), Base: header["base"].(string), MergeBase: header["merge_base"].(string)}}
	for _, tc := range []struct{ name, head, base, conclusion, state, status string }{
		{"complete", saved.Metadata.Head, saved.Metadata.Base, "success", "current", "verified"},
		{"failed", saved.Metadata.Head, saved.Metadata.Base, "failure", "current", "failed"},
		{"head moved", "aaaaaaa", saved.Metadata.Base, "success", "outdated", "asserted"},
		{"base moved", saved.Metadata.Head, "bbbbbbb", "success", "outdated", "verified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &sources{evidence: github.Evidence{Repository: "example-org/taskline", PullRequest: bundle.PullRequest{Number: 212, State: "open", HeadSHA: tc.head, BaseSHA: tc.base}, Checks: []bundle.Check{{ID: "chk_9001", Kind: "check_run", Name: "Tests", Status: "completed", Conclusion: tc.conclusion}}, Sources: bundle.Sources{"checks": true, "review_threads": true}}}
			now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
			result, err := (Refresher{GitHub: source, Git: source, Sessions: source}).Refresh(context.Background(), github.Repository{Host: "github.com", Owner: "Example-Org", Name: "Taskline"}, 212, saved, assemble.DefaultLimits().Caps, now)
			if err != nil {
				t.Fatal(err)
			}
			after := object(t, result)
			gotHeader := after["review"].(map[string]any)
			if gotHeader["generation_state"] != tc.state || gotHeader["evidence_refreshed_at"] != now.Format(time.RFC3339Nano) {
				t.Fatalf("header = %v", gotHeader)
			}
			for _, key := range []string{"version", "previous_version", "generated_at", "head", "base", "merge_base"} {
				if !reflect.DeepEqual(header[key], gotHeader[key]) {
					t.Errorf("changed generation field %s", key)
				}
			}
			if source.checksHead != "" || source.head != saved.Metadata.Head || source.base != saved.Metadata.MergeBase {
				t.Fatalf("read wrong revisions: %+v", source)
			}
			oldSections, sections := before["sections"].([]any), after["sections"].([]any)
			for i, old := range oldSections {
				for _, key := range []string{"id", "title", "kind", "hunks", "summary", "attention", "assessment", "risks", "blocks"} {
					if !reflect.DeepEqual(old.(map[string]any)[key], sections[i].(map[string]any)[key]) {
						t.Errorf("changed authored section %d field %s", i, key)
					}
				}
			}
			status := sections[0].(map[string]any)["evidence"].([]any)[0].(map[string]any)["status"]
			if status != tc.status {
				t.Fatalf("evidence = %v, want %s", status, tc.status)
			}
			oldIndex, index := before["index"].(map[string]any), after["index"].(map[string]any)
			if !reflect.DeepEqual(index["attachments"], oldIndex["attachments"]) {
				t.Error("attachments changed without any image input")
			}
			if len(index["sessions"].([]any)) != 0 {
				t.Error("unlinked sessions retained")
			}
			if index["checks"].([]any)[0].(map[string]any)["head"] != tc.head {
				t.Error("checks indexed under wrong head")
			}
		})
	}
}

func TestRefreshSourceFailureReturnsNoDocument(t *testing.T) {
	doc := object(t, schema.Example)["review"].(map[string]any)
	saved := store.Version{Document: schema.Example, Metadata: store.Metadata{Version: 3, Head: doc["head"].(string), Base: doc["base"].(string), MergeBase: doc["merge_base"].(string)}}
	source := &sources{err: errors.New("offline")}
	result, err := (Refresher{GitHub: source, Git: source}).Refresh(context.Background(), github.Repository{Host: "github.com", Owner: "Example-Org", Name: "Taskline"}, 212, saved, assemble.DefaultLimits().Caps, time.Now())
	if err == nil || result != nil {
		t.Fatalf("result %s, error %v", result, err)
	}
}

func TestRefreshBotReportsAndResolvedReopenedFindings(t *testing.T) {
	doc := object(t, schema.Example)
	header := doc["review"].(map[string]any)
	saved := store.Version{Document: schema.Example, Metadata: store.Metadata{Version: 3, Head: header["head"].(string), Base: header["base"].(string), MergeBase: header["merge_base"].(string)}}
	// Two sections contain this path. The current thread line picks the later hunk.
	path, line := "api/handler.go", 38
	source := &sources{evidence: github.Evidence{Repository: "example-org/taskline", PullRequest: bundle.PullRequest{Number: 212, State: "open", HeadSHA: saved.Metadata.Head, BaseSHA: saved.Metadata.Base}, Sources: bundle.Sources{"review_threads": true}, Threads: []bundle.Thread{{ID: "PRRT_1", Path: path, Line: &line}}, Conversation: []bundle.ConversationItem{
		{ID: "100", Kind: "review", Bot: true, BotTool: "Cursor", AuthorLogin: "cursor[bot]", State: "commented", Commit: saved.Metadata.Head},
		{ID: "200", Kind: "review_comment", Bot: true, BotTool: "Cursor", AuthorLogin: "cursor[bot]", Path: path, Review: "100", Thread: "PRRT_1", ThreadRoot: true, Body: "### Guard the parser\n\n**High Severity**\n\nIt can be nil."},
		{ID: "300", Kind: "comment", Bot: true, BotReport: true, BotTool: "CI", Body: "Checks completed."},
	}}}
	for _, resolved := range []bool{false, true, false} {
		source.evidence.Threads[0].Resolved = resolved
		source.evidence.Conversation[1].Resolved = &resolved
		result, err := (Refresher{GitHub: source, Git: source}).Refresh(context.Background(), github.Repository{Host: "github.com", Owner: "example-org", Name: "taskline"}, 212, saved, assemble.DefaultLimits().Caps, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		after := object(t, result)
		reviews := after["agent_reviews"].([]any)
		if len(reviews) != 1 {
			t.Fatalf("agent reviews %v", reviews)
		}
		finding := reviews[0].(map[string]any)["findings"].([]any)[0].(map[string]any)
		state := "open"
		if resolved {
			state = "resolved"
		}
		if finding["state"] != state {
			t.Fatalf("finding state %v, want %s", finding["state"], state)
		}
		if finding["section"] != "sec_mechanical" {
			t.Fatalf("finding lost section location: %v", finding)
		}
		located := false
		for _, anchor := range finding["anchors"].([]any) {
			a := anchor.(map[string]any)
			located = located || a["type"] == "hunk" && a["id"] == "h_a0480f808203"
		}
		if !located {
			t.Fatalf("finding lost line-specific hunk anchor: %v", finding)
		}
		wantOpen := float64(1)
		if resolved {
			wantOpen = 0
		}
		for _, entry := range after["sections"].([]any) {
			section := entry.(map[string]any)
			if section["id"] == "sec_mechanical" && section["derived"].(map[string]any)["open_findings"] != wantOpen {
				t.Fatalf("wrong section finding count: %v", section["derived"])
			}
		}
		reports := after["index"].(map[string]any)["reports"].([]any)
		if len(reports) != 1 || reports[0].(map[string]any)["summary"] != "Checks completed." {
			t.Fatalf("bot reports %v", reports)
		}
		saved.Document = result
	}
}
