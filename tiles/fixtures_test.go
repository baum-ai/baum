package tiles_test

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/baum-ai/baum/schema"
	"github.com/baum-ai/baum/tiles"
)

// fixture is a synthetic baum.review/1 input for one shape the converter must
// handle: the base document (testdata/base.review.json) changed by mutate, or
// the schema's example document. Its tile document is the golden
// testdata/<name>.tiles.json.
type fixture struct {
	name string
	opts tiles.Options
	// invalid marks an input the stored review schema rejects but the viewer
	// still reads, such as a shape older documents kept.
	invalid bool
	example bool
	mutate  func(doc map[string]any)
}

var fixtures = []fixture{
	{name: "base"},
	{name: "example", example: true},
	{name: "context-missing", mutate: func(doc map[string]any) {
		delete(object(doc, "summary"), "context")
	}},
	{name: "draft-context-missing", opts: tiles.Options{Draft: true}, mutate: func(doc map[string]any) {
		delete(object(doc, "summary"), "context")
	}},
	{name: "recommendation-null", mutate: func(doc map[string]any) {
		object(doc, "summary")["recommendation"] = nil
	}},
	{name: "recommendation-set", mutate: func(doc map[string]any) {
		object(doc, "summary")["recommendation"] = map[string]any{
			"decision": "look_first",
			"text":     "Read the retry loop before approving.",
			"refs":     []any{"sec_retry_policy"},
		}
	}},
	{name: "risk-score-not-assessed", mutate: func(doc map[string]any) {
		object(doc, "summary")["risk_score"] = map[string]any{
			"rubric": "risk-v1", "status": "not_assessed", "score": nil, "reasons": []any{},
			"limitations": []any{map[string]any{"kind": "unassessed_sections", "count": 2}},
		}
	}},
	{name: "risk-score-lower-bound", mutate: func(doc map[string]any) {
		score := object(doc, "summary", "risk_score")
		score["status"] = "lower_bound"
		score["limitations"] = []any{map[string]any{"kind": "omitted_source", "count": 1}}
	}},
	{name: "assessment-and-score-missing", mutate: func(doc map[string]any) {
		flag := sectionOf(doc, "sec_config_flag")
		delete(flag, "assessment")
		delete(object(flag, "derived"), "review_score")
	}},
	{name: "unscored", mutate: func(doc map[string]any) {
		for _, raw := range array(doc, "sections") {
			s := raw.(map[string]any)
			delete(s, "assessment")
			delete(object(s, "derived"), "review_score")
			delete(object(s, "derived"), "review_reason")
		}
		delete(object(doc, "summary"), "risk_score")
	}},
	{name: "stale-without-hunks", mutate: func(doc map[string]any) {
		insertSection(doc, 1, staleSection("sec_old_approach", "logical"))
		insertSection(doc, 3, staleSection("sec_old_formatting", "mechanical"))
	}},
	{name: "no-current-logical", mutate: func(doc map[string]any) {
		for _, id := range []string{"sec_retry_policy", "sec_config_flag"} {
			s := sectionOf(doc, id)
			s["hunks"] = []any{}
			derived := object(s, "derived")
			derived["state"] = "stale"
			delete(derived, "review_score")
			delete(derived, "review_reason")
		}
		doc["unassigned_hunks"] = []any{"h_000000000001", "h_000000000002", "h_000000000003", "h_000000000006", "h_000000000007"}
	}},
	{name: "no-mechanical", mutate: func(doc map[string]any) {
		removeSection(doc, "sec_generated_client")
		doc["unassigned_hunks"] = []any{"h_000000000004", "h_000000000005", "h_000000000006", "h_000000000007"}
	}},
	{name: "no-everything-else", mutate: func(doc map[string]any) {
		removeSection(doc, "sec_generated_client")
		doc["unassigned_hunks"] = []any{}
		index := object(doc, "index")
		index["hunks"] = array(index, "hunks")[:3]
	}},
	{name: "duplicate-ids", mutate: func(doc map[string]any) {
		// A later section repeats an earlier section's risk, evidence and
		// decision IDs for different items and cites its own. The summary
		// mentions the name the risk would be renamed to, which the renaming
		// must skip.
		object(doc, "summary")["text"] = "Retries are added; rsk_retry_storm_queue_limits is not an ID."
		queue := map[string]any{
			"id": "sec_queue_limits", "title": "Bound the retry queue", "kind": "logical",
			"hunks": []any{"h_000000000006"},
			"summary": map[string]any{
				"text":    "The queue holds at most 1000 retries (rsk_retry_storm, ev_retry_test, dec_doubling_delay).",
				"anchors": []any{map[string]any{"type": "hunk", "id": "h_000000000006"}},
			},
			"attention": map[string]any{"text": "See rsk_retry_storm.", "refs": []any{"rsk_retry_storm", "ev_retry_test"}},
			"risks": []any{map[string]any{
				"id": "rsk_retry_storm", "tier": "medium", "category": "capacity", "source": "model",
				"text": "A full queue drops new retries; ev_retry_test does not cover it.", "anchors": []any{map[string]any{"type": "hunk", "id": "h_000000000006"}},
				"state": "active", "introduced_version": 3, "mitigated_version": nil,
			}},
			"evidence": []any{map[string]any{
				"id": "ev_retry_test", "kind": "test", "claim": "A test fills the queue.", "status": "asserted", "source": "model",
				"anchors": []any{map[string]any{"type": "hunk", "id": "h_000000000006"}},
			}},
			"blocks": []any{
				map[string]any{"kind": "behavior", "steps": []any{map[string]any{
					"id": "beh_queue_full", "text": "A retry past the bound is dropped (dec_doubling_delay).",
					"anchors": []any{map[string]any{"type": "hunk", "id": "h_000000000006"}}, "evidence": []any{"ev_retry_test"},
				}}},
				map[string]any{"kind": "decisions", "items": []any{map[string]any{
					"id": "dec_doubling_delay", "by": "human", "text": "Bound the queue at 1000.",
					"anchors": []any{map[string]any{"type": "hunk", "id": "h_000000000006"}},
				}}},
			},
			"derived": map[string]any{
				"state": "new", "risk_tier": "medium", "additions": 3, "deletions": 0, "role_loc": map[string]any{"docs": 3},
				"reviewers": []any{}, "evidence_summary": map[string]any{"asserted": 1}, "open_findings": 0, "review_score": 3,
				"review_reason": map[string]any{"risk": "rsk_retry_storm", "text": "The queue can fill."},
			},
		}
		insertSection(doc, 2, queue)
		doc["unassigned_hunks"] = []any{"h_000000000007"}
		reasons := array(object(doc, "summary", "risk_score"), "reasons")
		object(doc, "summary", "risk_score")["reasons"] = append(reasons, map[string]any{"section": "sec_queue_limits", "risk": "rsk_retry_storm", "text": "The queue can fill."})
	}},
	{name: "obsolete-duplicate-decision", mutate: func(doc map[string]any) {
		// An obsolete section repeats a current decision's ID and cites it.
		// Its blocks stay in its own tile, so its decision keeps the ID.
		stale := staleSection("sec_old_approach", "logical")
		decisions := array(stale, "blocks")[1].(map[string]any)
		array(decisions, "items")[0].(map[string]any)["id"] = "dec_doubling_delay"
		object(stale, "summary")["text"] = "The old approach (dec_doubling_delay) is dropped."
		doc["sections"] = append(array(doc, "sections"), stale)
	}},
	{name: "duplicate-ids-verbatim", mutate: func(doc map[string]any) {
		// A later section repeats a risk ID and cites it in prose, in an
		// inline code span, in a URL and in verbatim quotes; only the prose
		// citation is rewritten.
		flag := sectionOf(doc, "sec_config_flag")
		risk := clone(array(sectionOf(doc, "sec_retry_policy"), "risks")[0]).(map[string]any)
		risk["text"] = "Flag flips cause bursts too."
		risk["anchors"] = []any{map[string]any{"type": "prompt", "session": "11111111-2222-4333-8444-555555555555", "index": 1, "input": "please avoid rsk_retry_storm"}}
		flag["risks"] = append(array(flag, "risks"), risk)
		object(flag, "summary")["text"] = "See rsk_retry_storm, `rsk_retry_storm` and https://example.com/rsk_retry_storm/notes."
		flag["blocks"] = append(array(flag, "blocks"), map[string]any{"kind": "decisions", "items": []any{map[string]any{
			"id": "dec_flag_default", "by": "agent", "text": "Default to on despite rsk_retry_storm.",
			"rejected": "Off by default, as rsk_retry_storm suggested.", "evidence": "The prompt said rsk_retry_storm.",
			"anchors": []any{map[string]any{"type": "hunk", "id": "h_000000000003"}},
		}}})
	}},
	{name: "two-mechanical", mutate: func(doc map[string]any) {
		second := clone(sectionOf(doc, "sec_generated_client")).(map[string]any)
		second["id"] = "sec_vendored_client"
		second["title"] = "Update the vendored client"
		second["hunks"] = []any{"h_000000000006"}
		second["risks"] = []any{}
		doc["sections"] = append(array(doc, "sections"), second)
		doc["unassigned_hunks"] = []any{"h_000000000007"}
	}},
	{name: "draft-outline", opts: tiles.Options{Draft: true}, mutate: func(doc map[string]any) {
		// A live draft as a staged review serves it before any details are
		// written: every section is its outline with pending details, and
		// the summary has no context or reading time yet.
		summary := object(doc, "summary")
		delete(summary, "context")
		delete(summary, "review_minutes")
		summary["recommendation"] = nil
		summary["attention"] = nil
		object(summary, "risk_score")["reasons"] = []any{}
		for _, raw := range array(doc, "sections") {
			s := raw.(map[string]any)
			s["details"] = "pending"
			s["summary"] = nil
			s["attention"] = nil
			s["risks"], s["evidence"], s["blocks"] = []any{}, []any{}, []any{}
			delete(object(s, "derived"), "review_reason")
		}
	}},
	{name: "within-section-duplicate", invalid: true, mutate: func(doc map[string]any) {
		flag := sectionOf(doc, "sec_config_flag")
		risk := clone(array(flag, "risks")[0]).(map[string]any)
		risk["text"] = "A second risk that reuses the first one's ID."
		flag["risks"] = append(array(flag, "risks"), risk)
		object(flag, "summary")["text"] = "A new setting defaults to true (rsk_flag_default)."
	}},
	{name: "rare-blocks", mutate: func(doc map[string]any) {
		flag := sectionOf(doc, "sec_config_flag")
		source := []any{map[string]any{"type": "hunk", "id": "h_000000000003"}}
		flag["blocks"] = append(array(flag, "blocks"),
			map[string]any{"kind": "code", "language": "go", "content": "if !cfg.RetriesEnabled {\n\treturn drop(d)\n}", "caption": "The early return.", "source": source},
			map[string]any{"kind": "table", "columns": []any{"Setting", "Default"}, "rows": []any{[]any{"retries_enabled", "true"}}, "source": source},
			map[string]any{"kind": "repro", "steps": []any{"Set retries_enabled to false.", "Fail a delivery."}, "source": source},
			map[string]any{"kind": "entities", "items": []any{map[string]any{"kind": "config", "name": "retries_enabled", "change": "added", "hunks": []any{"h_000000000003"}}}},
			map[string]any{"kind": "dependency", "items": []any{map[string]any{"name": "backoff", "ecosystem": "go", "from": nil, "to": "v1.2.0", "hunks": []any{"h_000000000003"}}}},
		)
	}},
	{name: "diagram-media-included-prs", mutate: func(doc map[string]any) {
		retry := sectionOf(doc, "sec_retry_policy")
		source := []any{map[string]any{"type": "hunk", "id": "h_000000000001"}}
		retry["blocks"] = append(array(retry, "blocks"),
			map[string]any{"kind": "diagram", "format": "mermaid", "content": "graph LR\n  fail --> wait --> retry", "source": source},
			map[string]any{"kind": "media", "url": "https://example.com/retry.mp4", "caption": "A recorded outage.", "source": source},
			map[string]any{"kind": "included_prs", "items": []any{map[string]any{"pr": 40, "note": "The queue it builds on."}}},
		)
	}},
	{name: "details-states", mutate: func(doc map[string]any) {
		retry := sectionOf(doc, "sec_retry_policy")
		retry["details"] = "pending"
		retry["summary"] = nil
		retry["attention"] = nil
		retry["evidence"] = []any{}
		retry["blocks"] = []any{}
		flag := sectionOf(doc, "sec_config_flag")
		flag["details"] = "failed"
		delete(flag, "summary")
		flag["attention"] = nil
		flag["evidence"] = []any{}
		flag["blocks"] = []any{}
		stalePending := staleSection("sec_dropped_pending", "logical")
		stalePending["details"] = "pending"
		stalePending["summary"] = nil
		stalePending["blocks"] = []any{}
		staleFailed := staleSection("sec_dropped_failed", "logical")
		staleFailed["details"] = "failed"
		delete(staleFailed, "summary")
		staleFailed["blocks"] = []any{}
		doc["sections"] = append(array(doc, "sections"), stalePending, staleFailed)
		object(doc, "summary")["attention"] = nil
		object(doc, "summary", "risk_score")["reasons"] = []any{}
	}},
	{name: "stack", mutate: func(doc map[string]any) {
		doc["stack"] = map[string]any{"title": "Webhook retries", "members": []any{
			map[string]any{"number": 41, "head_sha": "b1b2c3d4e5f60718293a4b5c6d7e8f9012345678", "review_version": 2},
			map[string]any{"number": 42, "head_sha": "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", "review_version": 3},
		}}
		sectionOf(doc, "sec_retry_policy")["pull_requests"] = []any{41, 42}
		sectionOf(doc, "sec_config_flag")["pull_requests"] = []any{42}
		sectionOf(doc, "sec_generated_client")["pull_requests"] = []any{42}
		object(doc, "index")["file_comparisons"] = []any{map[string]any{
			"base_revision": "0f1e2d3c4b5a69788796a5b4c3d2e1f001234567",
			"changes":       []any{map[string]any{"path": "worker/deliver.go", "change": "modified"}},
		}}
		delete(object(doc, "summary"), "review_minutes")
	}},
	{name: "review-exchange-string", invalid: true, mutate: func(doc map[string]any) {
		object(doc, "summary")["review_exchange"] = "One reviewer asked for jitter; the author agreed."
	}},
	{name: "critical-risks", mutate: func(doc map[string]any) {
		flag := sectionOf(doc, "sec_config_flag")
		flag["risks"] = append(array(flag, "risks"),
			map[string]any{
				"id": "rsk_flag_secret", "tier": "critical", "category": "security", "source": "agent_review",
				"text": "The flag is read from a world-writable file.", "anchors": []any{map[string]any{"type": "hunk", "id": "h_000000000003"}},
				"finding": "fnd_add_jitter", "state": "active", "introduced_version": 3, "mitigated_version": nil,
			},
			map[string]any{
				"id": "rsk_flag_reload", "tier": "medium", "category": "operability", "source": "model",
				"text": "Changing the flag needs a restart.", "anchors": []any{map[string]any{"type": "hunk", "id": "h_000000000003"}},
				"state": "active", "introduced_version": 3, "mitigated_version": nil,
			})
	}},
	{name: "many-changes", mutate: func(doc map[string]any) {
		hunks := array(object(doc, "index"), "hunks")
		for i := range 7 {
			id := fmt.Sprintf("h_00000000010%d", i)
			hunks = append(hunks, map[string]any{"id": id, "path": fmt.Sprintf("worker/part%d.go", i), "change": "added", "role": "code", "new_range": []any{1, 5}, "additions": 5, "deletions": 0})
			s := clone(sectionOf(doc, "sec_config_flag")).(map[string]any)
			s["id"] = fmt.Sprintf("sec_part_%d", i)
			s["title"] = fmt.Sprintf("Part %d of the worker", i)
			s["hunks"] = []any{id}
			s["risks"] = []any{}
			s["evidence"] = []any{}
			s["blocks"] = []any{}
			s["summary"] = map[string]any{"text": fmt.Sprintf("Adds part %d.", i), "anchors": []any{map[string]any{"type": "hunk", "id": id}}}
			object(s, "derived")["review_score"] = 1 + i%4
			if i == 5 {
				s["attention"] = map[string]any{"text": "Look at this part.", "refs": []any{"rsk_part_5"}}
				s["risks"] = []any{map[string]any{
					"id": "rsk_part_5", "tier": "medium", "category": "correctness", "source": "model",
					"text": "Part 5 runs before the queue is ready.", "anchors": []any{map[string]any{"type": "hunk", "id": id}},
					"state": "active", "introduced_version": 3, "mitigated_version": nil,
				}}
			}
			doc["sections"] = append(array(doc, "sections"), s)
		}
		object(doc, "index")["hunks"] = hunks
	}},
}

func staleSection(id, kind string) map[string]any {
	s := map[string]any{
		"id": id, "title": "Dropped: " + id, "kind": kind, "hunks": []any{},
		"summary":   map[string]any{"text": "An approach the pull request no longer takes.", "anchors": []any{map[string]any{"type": "file", "path": "worker/old.go"}}},
		"attention": nil,
		"risks": []any{map[string]any{
			"id": "rsk_" + id[len("sec_"):], "tier": "low", "category": "correctness", "source": "model",
			"text": "Kept from the version that had it.", "anchors": []any{map[string]any{"type": "file", "path": "worker/old.go"}},
			"state": "active", "introduced_version": 1, "mitigated_version": nil,
		}},
		"evidence": []any{},
		"blocks": []any{
			map[string]any{"kind": "intent", "text": "Keep the old approach readable.", "anchors": []any{}},
			map[string]any{"kind": "decisions", "items": []any{map[string]any{
				"id": "dec_" + id[len("sec_"):], "by": "agent", "text": "Dropped in favour of retries.",
				"anchors": []any{map[string]any{"type": "file", "path": "worker/old.go"}},
			}}},
		},
		"derived": map[string]any{
			"state": "stale", "risk_tier": "low", "additions": 0, "deletions": 0, "role_loc": map[string]any{},
			"reviewers": []any{}, "evidence_summary": map[string]any{}, "open_findings": 0,
		},
	}
	if kind == "mechanical" {
		s["blocks"] = []any{}
		array(s, "risks")[0].(map[string]any)["tier"] = "mechanical"
	}
	return s
}

// input returns the fixture's baum.review/1 document.
func (f fixture) input(t *testing.T) []byte {
	t.Helper()
	if f.example {
		return schema.Example
	}
	raw, err := os.ReadFile("testdata/base.review.json")
	if err != nil {
		t.Fatal(err)
	}
	if f.mutate == nil {
		return raw
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	f.mutate(doc)
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func object(value map[string]any, path ...string) map[string]any {
	for _, key := range path {
		value = value[key].(map[string]any)
	}
	return value
}

func array(value map[string]any, key string) []any {
	items, _ := value[key].([]any)
	return items
}

func sectionOf(doc map[string]any, id string) map[string]any {
	for _, raw := range array(doc, "sections") {
		if s := raw.(map[string]any); s["id"] == id {
			return s
		}
	}
	panic("no section " + id)
}

func insertSection(doc map[string]any, at int, s map[string]any) {
	sections := array(doc, "sections")
	doc["sections"] = append(sections[:at:at], append([]any{s}, sections[at:]...)...)
}

func removeSection(doc map[string]any, id string) {
	var kept []any
	for _, raw := range array(doc, "sections") {
		if raw.(map[string]any)["id"] != id {
			kept = append(kept, raw)
		}
	}
	doc["sections"] = kept
}

func clone(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var copied any
	if err := json.Unmarshal(encoded, &copied); err != nil {
		panic(err)
	}
	return copied
}
