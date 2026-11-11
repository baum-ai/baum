package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
)

// bigReview is a realistic stored review: forty sections, each with its own
// canary in its details, a canary in the overview and one in the context. A
// part of it must never carry another part's canary.
func bigReview(head string) string {
	var sections, hunks []string
	for i := 1; i <= 40; i++ {
		id := fmt.Sprintf("sec_%d", i)
		hunkID := fmt.Sprintf("h_%d", i)
		hunks = append(hunks, fmt.Sprintf(`{"id":%q,"path":"pkg/file%d.go","change":"modified","additions":3,"deletions":1}`, hunkID, i))
		state := ""
		details := fmt.Sprintf(`"summary":{"text":"CANARY-DETAILS-%s %s"},"attention":null,"risks":[{"id":"r1","text":"CANARY-RISK-%s"}],"evidence":[{"id":"e1","claim":"CANARY-EVIDENCE-%s"}],"blocks":[],"derived":{"state":"fresh"}`, id, strings.Repeat("padding ", 400), id, id)
		switch i {
		case 3:
			state = `"details":"failed",`
			details = `"summary":null,"attention":null,"risks":[],"evidence":[],"blocks":[],"derived":{"state":"fresh"}`
		case 4:
			state = `"details":"pending",`
			details = `"summary":null,"attention":null,"risks":[],"evidence":[],"blocks":[],"derived":{"state":"fresh"}`
		}
		sections = append(sections, fmt.Sprintf(`{"id":%q,"title":"Section %d title","kind":"logic","hunks":[%q],"assessment":"Assessment %d",%s%s}`, id, i, hunkID, i, state, details))
	}
	return fmt.Sprintf(`{"schema":"baum.review/1","review":{"repository":"acme/web","pull_request":13,"version":2,"head":%q,"base":"b13","generated_at":"2026-11-01T10:00:00Z","relations":[]},
 "summary":{"title":"T","text":"CANARY-OVERVIEW","context":{"text":"CANARY-CONTEXT"},"change_kind":"feature","review_exchange":{},"implements":[],"release_note":null,"breaking_change":null,"attention":null,"authorship":{},"review_minutes":9},
 "sections":[%s],"relations":[],"index":{"hunks":[%s]}}`, head, strings.Join(sections, ","), strings.Join(hunks, ","))
}

const reviewDraftDocument = `{"schema":"baum.review/1","review":{"version":3,"head":"h12","base":"b12"},
 "summary":{"text":"DRAFT-OVERVIEW","change_kind":"feature","review_exchange":{},"implements":[],"authorship":{}},
 "sections":[{"id":"sec_1","title":"First","kind":"logic","hunks":["h_1"],"assessment":"a","details":"pending","summary":null,"risks":[]},
  {"id":"sec_2","title":"Second","kind":"logic","hunks":["h_2"],"assessment":"b","summary":{"text":"DRAFT-DETAILS-2"},"risks":[]}],"relations":[],"index":{"hunks":[{"id":"h_1","path":"a.go"},{"id":"h_2","path":"b.go"}]}}`

// reviewDaemon answers the routes the review tools read.
type reviewDaemon struct {
	mu      sync.Mutex
	seen    []string
	running bool // pull request 12 has a review running with a draft
	draftN  int
}

func (d *reviewDaemon) requests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.seen...)
}

func (d *reviewDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.seen = append(d.seen, r.URL.RequestURI())
	running, draftN := d.running, d.draftN
	d.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()
	p := r.URL.Path
	reply := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
	switch {
	case p == "/api/index":
		reply(`{"entries":[
 {"pull_request":{"repository":"acme/web","number":12,"head_sha":"h12"},"clone_id":"c-web","version_count":0},
 {"pull_request":{"repository":"acme/web","number":13,"head_sha":"h13"},"clone_id":"c-web","version_count":2},
 {"pull_request":{"repository":"evil/secrets","number":1,"head_sha":"hx"},"clone_id":"c-evil","version_count":1}]}`)
	case p == "/api/facts":
		reply(`{"schema":"baum.facts/1","review":{"head":%q,"clone":%q}}`, q.Get("head"), q.Get("clone"))
	case p == "/api/document":
		switch {
		case q.Get("clone") == "c-evil":
			reply(`{"review":{"head":"FOREIGN"},"summary":{"text":"FOREIGN-OVERVIEW"},"sections":[]}`)
		case q.Get("pr") == "13" && q.Get("clone") == "c-web":
			reply("%s", bigReview("h13"))
		case q.Get("pr") == "14":
			reply(`["CANARY-RAW"]`)
		default:
			http.NotFound(w, r)
		}
	case p == "/api/versions":
		reply(`[{"version":2,"head":"h13","base":"b13","merge_base":"m13","generated_at":"2026-11-01T10:00:00Z","backend":"claude","model":"opus","recorded_cost_usd":0.5,"session":{"id":"SECRET-SESSION"},"accounting":{"usage":1},"bundle_digest":"DIGEST","prompt_digest":"PD","policy":"POLICY"},{"version":1,"head":"h13a","base":"b13","model":"sonnet","recorded_cost_usd":null}]`)
	case p == "/api/prs/chat":
		reply(`{"conversations":[{"id":"c1","version":2,"created":"2026-11-02T10:00:00Z","updated":"2026-11-02T11:00:00Z","turns":[
  {"seq":1,"question":%q,"focus":{"section":"sec_1"},"answer":{"answer_markdown":"It **is** fine.","citations":[{"path":"a.go","start_line":1,"end_line":4}],"suggests_fix":null},"job_id":"JOB-1","state":"done","cost_usd":0.2,"cost_reason":"COST","created":"2026-11-02T10:00:00Z"},
  {"seq":2,"question":"And then?","focus":null,"job_id":"JOB-2","state":"failed","reason":"agent_failed","note":"Try again.","created":"2026-11-02T11:00:00Z"}]}]}`, strings.Repeat("why ", 100))
	case p == "/api/jobs":
		switch q.Get("pr") {
		case "12":
			if running {
				reply(`{"available":true,"jobs":[{"id":"j-fix","kind":"fix","state":"running","draft":0},{"id":"j-run","state":"running","draft":%d,"head_sha":"h12"},{"id":"j-chat","kind":"chat","state":"running","draft":5,"head_sha":"h12"},{"id":"j-fix2","kind":"fix","state":"running","draft":6},{"id":"j-group","group":"g-web","state":"running","draft":7}]}`, draftN)
			} else {
				reply(`{"available":true,"jobs":[{"id":"j-old","state":"done","draft":0}]}`)
			}
		case "13":
			reply(`{"available":true,"jobs":[{"id":"j-done","state":"completed","head_sha":"h13","context_failed":true}]}`)
		default:
			reply(`{"available":true,"jobs":[]}`)
		}
	case strings.HasPrefix(p, "/api/jobs/"):
		id, rest, _ := strings.Cut(strings.TrimPrefix(p, "/api/jobs/"), "/")
		repo := map[string]string{"j-run": "acme/web", "j-evil": "evil/secrets"}[id]
		switch {
		case repo == "":
			http.NotFound(w, r)
		case rest == "":
			reply(`{"id":%q,"repository":%q,"pr":12}`, id, repo)
		case rest == "draft" && id == "j-run":
			if !running {
				http.NotFound(w, r)
				return
			}
			reply(`{"draft":4,"document":%s}`, reviewDraftDocument)
		case rest == "draft":
			reply(`{"draft":1,"document":{"summary":{"text":"FOREIGN-DRAFT"},"sections":[]}}`)
		default:
			http.NotFound(w, r)
		}
	case p == "/api/groups":
		reply(`{"available":true,"groups":[{"id":"g-web","repository":"acme/web","name":"Search","members":[13,20]}]}`)
	case strings.HasPrefix(p, "/api/groups/"):
		id, rest, _ := strings.Cut(strings.TrimPrefix(p, "/api/groups/"), "/")
		repo := map[string]string{"g-web": "acme/web", "g-evil": "evil/secrets"}[id]
		switch {
		case repo == "":
			http.NotFound(w, r)
		case rest == "":
			reply(`{"id":%q,"repository":%q,"name":"G","members":[13,20]}`, id, repo)
		case rest == "document" && id == "g-web":
			version := q.Get("version")
			if version == "" {
				version = "1"
			}
			reply(`{"review":{"version":%s,"head":"gh","base":"gb"},"summary":{"text":"GROUP-OVERVIEW","context":{"text":"GROUP-CONTEXT"}},"sections":[{"id":"gsec_1","title":"Group one","kind":"logic","hunks":[],"assessment":"x","summary":{"text":"GROUP-DETAILS"},"risks":[]}],"relations":[],"index":{"hunks":[]}}`, version)
		case rest == "document":
			reply(`{"summary":{"text":"FOREIGN-GROUP-REVIEW"},"sections":[]}`)
		case rest == "versions":
			reply(`[{"version":1,"head":"gh","model":"opus","session":{"id":"SECRET-SESSION"},"policy":"POLICY"}]`)
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func reviewServer(t *testing.T) (*Server, *reviewDaemon) {
	t.Helper()
	d := &reviewDaemon{}
	clones := map[string]string{"c-web": "acme/web", "c-evil": "evil/secrets"}
	s := New(Options{API: d, Version: "test", Clone: func(_ context.Context, id string) (string, error) {
		if repository, ok := clones[id]; ok {
			return repository, nil
		}
		return "", errors.New("unknown clone")
	}})
	return s, d
}

// direct runs a tool as a chat, without the envelope, for results over one
// chunk.
func direct(t *testing.T, s *Server, name string, args any) (string, error) {
	t.Helper()
	s.mu.RLock()
	tool, ok := s.tool(name)
	s.mu.RUnlock()
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	b, _ := json.Marshal(args)
	if len(b) == 0 || string(b) == "null" {
		b = []byte("{}")
	}
	c := CallContext{Consumer: Consumer{Kind: Chat}, Reader: s.reader.within(tool.Reads, "")}
	return tool.Call(context.Background(), c, b)
}

func absent(t *testing.T, what, text string, markers ...string) {
	t.Helper()
	for _, m := range markers {
		if strings.Contains(text, m) {
			t.Errorf("%s carries %q: %.300s", what, m, text)
		}
	}
}

// Each review tool returns its part and no other, however large the review.
func TestReviewToolsReturnOnePartPerCall(t *testing.T) {
	s, _ := reviewServer(t)
	if len(bigReview("h")) < MaxChunk {
		t.Fatal("the review is not large")
	}
	pr := map[string]any{"repository": "acme/web", "pr": 13}
	outline, err := direct(t, s, "review_outline", pr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outline, "CANARY-OVERVIEW") || !strings.Contains(outline, `"Section 40 title"`) || !strings.Contains(outline, `"files":["pkg/file7.go"]`) {
		t.Errorf("outline lacks its part: %.400s", outline)
	}
	// No details, no context: the outline is the outline stage's projection.
	absent(t, "the outline", outline, "CANARY-DETAILS", "CANARY-RISK", "CANARY-EVIDENCE", "CANARY-CONTEXT", "padding", `"derived"`, `"risks"`, `"summary":{"text":"CANARY`)
	var o struct {
		Sections []struct {
			ID      string
			Details string
		}
	}
	if err := json.Unmarshal([]byte(outline), &o); err != nil || len(o.Sections) != 40 || o.Sections[2].Details != "failed" || o.Sections[3].Details != "pending" || o.Sections[0].Details != "" {
		t.Errorf("sections %v: %v", o.Sections[:5], err)
	}

	section, err := direct(t, s, "review_section", map[string]any{"repository": "acme/web", "pr": 13, "id": "sec_7"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(section, "CANARY-DETAILS-sec_7") || !strings.Contains(section, "CANARY-RISK-sec_7") || !strings.Contains(section, "CANARY-EVIDENCE-sec_7") {
		t.Errorf("section lacks its details: %.400s", section)
	}
	absent(t, "a section", section, "CANARY-DETAILS-sec_6", "CANARY-DETAILS-sec_8", "sec_8", "CANARY-OVERVIEW", "CANARY-CONTEXT", "Section 8 title")

	context, err := direct(t, s, "review_context", pr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(context, "CANARY-CONTEXT") {
		t.Errorf("context lacks it: %s", context)
	}
	absent(t, "the context", context, "CANARY-DETAILS", "CANARY-OVERVIEW", "sec_1", "Section 1 title", `"sections"`)

	// Whatever a tool returns is far smaller than the document.
	for name, text := range map[string]string{"outline": outline, "section": section, "context": context} {
		if len(text) > len(bigReview("h"))/4 {
			t.Errorf("the %s is %d bytes of a %d byte review", name, len(text), len(bigReview("h")))
		}
	}
}

// A document the tools cannot read is an error that quotes nothing of it, so
// a projection never falls back to the raw document.
func TestReviewToolsNeverEchoAnUnreadableDocument(t *testing.T) {
	s, _ := reviewServer(t)
	// A pull request whose stored document is no object: every tool fails
	// without quoting it.
	s = New(Options{API: documentOverride(&reviewDaemon{}, `["CANARY-RAW"]`)})
	for _, tool := range []string{"review_outline", "review_section", "review_context"} {
		text, err := direct(t, s, tool, map[string]any{"repository": "acme/web", "pr": 13, "id": "sec_1"})
		if err == nil || strings.Contains(text+err.Error(), "CANARY-RAW") {
			t.Errorf("%s: %q %v", tool, text, err)
		}
	}
	for name, view := range map[string]func([]byte) (any, error){
		"outline": func(b []byte) (any, error) { return outlineView(b) },
		"section": func(b []byte) (any, error) { return sectionView(b, "x") },
		"context": func(b []byte) (any, error) { v, _, err := contextView(b); return v, err },
	} {
		for _, doc := range []string{`["CANARY-RAW"]`, `"CANARY-RAW"`, `{"sections":"CANARY-RAW"`, `CANARY-RAW`} {
			out, err := view([]byte(doc))
			if err == nil && name != "context" {
				t.Errorf("%s of %s: %v", name, doc, out)
			}
			if b, _ := json.Marshal(out); strings.Contains(string(b)+fmt.Sprint(err), "CANARY-RAW") {
				t.Errorf("%s of %s echoed it: %s %v", name, doc, b, err)
			}
		}
	}
}

func TestReviewToolsSayWhenAPartIsFailedPendingOrAbsent(t *testing.T) {
	s, _ := reviewServer(t)
	for id, want := range map[string]string{"sec_3": "could not be written", "sec_4": "still being written"} {
		text, err := direct(t, s, "review_section", map[string]any{"repository": "acme/web", "pr": 13, "id": id})
		state := map[string]string{"sec_3": "failed", "sec_4": "pending"}[id]
		if err != nil || !strings.Contains(text, want) || !strings.Contains(text, `"details":"`+state+`"`) || strings.Contains(text, `"risks"`) {
			t.Errorf("%s: %v %s", id, err, text)
		}
	}
	if _, err := direct(t, s, "review_section", map[string]any{"repository": "acme/web", "pr": 13, "id": "nope"}); err == nil || !strings.Contains(err.Error(), "sec_1, sec_2") {
		t.Errorf("an unknown section: %v", err)
	}
	// The job of that head published without its context.
	failed := strings.Replace(bigReview("h13"), `"context":{"text":"CANARY-CONTEXT"},`, "", 1)
	s2 := New(Options{API: documentOverride(&reviewDaemon{}, failed)})
	text, err := direct(t, s2, "review_context", map[string]any{"repository": "acme/web", "pr": 13})
	if err != nil || !strings.Contains(text, `"state":"failed"`) || !strings.Contains(text, "could not be written") {
		t.Errorf("failed context: %v %s", err, text)
	}
	// Only a completed job says its context failed, as the page does.
	for _, state := range []string{"running", "failed", "queued"} {
		s4 := New(Options{API: documentOverride(jobsOverride(&reviewDaemon{}, `{"available":true,"jobs":[{"id":"j","state":"`+state+`","head_sha":"h13","context_failed":true}]}`), failed)})
		text, err = direct(t, s4, "review_context", map[string]any{"repository": "acme/web", "pr": 13})
		if err != nil || !strings.Contains(text, `"state":"absent"`) {
			t.Errorf("a %s job with context_failed: %v %s", state, err, text)
		}
	}
	// With no failure recorded it is absent.
	s3 := New(Options{API: documentOverride(rewriteJobs(&reviewDaemon{}), failed)})
	text, err = direct(t, s3, "review_context", map[string]any{"repository": "acme/web", "pr": 13})
	if err != nil || !strings.Contains(text, `"state":"absent"`) {
		t.Errorf("absent context: %v %s", err, text)
	}
}

// documentOverride serves doc for pull request 13's document.
func documentOverride(next http.Handler, doc string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/document" && r.URL.Query().Get("pr") == "13" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(doc))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// jobsOverride answers every jobs list with body.
func jobsOverride(next http.Handler, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/jobs" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rewriteJobs answers every jobs list with no jobs.
func rewriteJobs(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/jobs" {
			_, _ = w.Write([]byte(`{"available":true,"jobs":[]}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func TestReviewDraftReadsARunningReviewOnly(t *testing.T) {
	s, d := reviewServer(t)
	pr := map[string]any{"repository": "acme/web", "pr": 12}
	text, err := direct(t, s, "review_draft", pr)
	if err != nil || text != noRunningReview {
		t.Fatalf("no review running: %v %q", err, text)
	}
	d.mu.Lock()
	d.running = true
	d.mu.Unlock()
	if text, _ := direct(t, s, "review_draft", pr); !strings.Contains(text, "has not written a draft yet") {
		t.Errorf("no draft yet: %s", text)
	}
	d.mu.Lock()
	d.draftN = 4
	d.mu.Unlock()
	outline, err := direct(t, s, "review_draft", pr)
	if err != nil || !strings.Contains(outline, "DRAFT-OVERVIEW") || !strings.Contains(outline, `"details":"pending"`) || !strings.Contains(outline, `"draft":4`) {
		t.Fatalf("draft outline: %v %s", err, outline)
	}
	absent(t, "the draft outline", outline, "DRAFT-DETAILS-2")
	pending, _ := direct(t, s, "review_draft", map[string]any{"repository": "acme/web", "pr": 12, "part": "section", "id": "sec_1"})
	if !strings.Contains(pending, "still being written") {
		t.Errorf("a pending draft section: %s", pending)
	}
	written, _ := direct(t, s, "review_draft", map[string]any{"repository": "acme/web", "pr": 12, "part": "section", "id": "sec_2"})
	if !strings.Contains(written, "DRAFT-DETAILS-2") {
		t.Errorf("a written draft section: %s", written)
	}
	absent(t, "a draft section", written, "DRAFT-OVERVIEW", "Section", `"First"`)
	if ctx, _ := direct(t, s, "review_draft", map[string]any{"repository": "acme/web", "pr": 12, "part": "context"}); !strings.Contains(ctx, `"state":"pending"`) || strings.Contains(ctx, "DRAFT-DETAILS") {
		t.Errorf("a draft context: %s", ctx)
	}
	// A section part without its id, and an unknown part.
	if _, err := direct(t, s, "review_draft", map[string]any{"repository": "acme/web", "pr": 12, "part": "section"}); err == nil {
		t.Error("a section without an id")
	}
	// A review of another pull request is not this one's.
	if text, _ := direct(t, s, "review_draft", map[string]any{"repository": "acme/web", "pr": 99}); text != noRunningReview {
		t.Errorf("another pull request: %s", text)
	}
	// The review ended between the list and the draft.
	d.mu.Lock()
	d.running = false
	d.mu.Unlock()
	if text, _ := direct(t, s, "review_draft", pr); text != noRunningReview {
		t.Errorf("ended: %s", text)
	}
}

func TestReviewVersionsAndConversations(t *testing.T) {
	s, _ := reviewServer(t)
	pr := map[string]any{"repository": "acme/web", "pr": 13}
	text, err := direct(t, s, "review_versions", pr)
	if err != nil || !strings.Contains(text, `"model":"opus"`) || !strings.Contains(text, `"merge_base":"m13"`) || !strings.Contains(text, `"recorded_cost_usd":0.5`) {
		t.Fatalf("versions: %v %s", err, text)
	}
	absent(t, "the versions", text, "SECRET-SESSION", "DIGEST", "POLICY", "accounting", `"session"`, "PD")
	text, err = direct(t, s, "review_versions", map[string]any{"group": "g-web", "repository": "acme/web"})
	if err != nil || !strings.Contains(text, `"group":"g-web"`) || !strings.Contains(text, `"model":"opus"`) {
		t.Fatalf("group versions: %v %s", err, text)
	}
	absent(t, "the group versions", text, "SECRET-SESSION", "POLICY")

	list, err := direct(t, s, "review_conversations", pr)
	if err != nil || !strings.Contains(list, `"id":"c1"`) || !strings.Contains(list, `"turns":2`) || !strings.Contains(list, "why why") || !strings.Contains(list, "…") {
		t.Fatalf("conversations: %v %s", err, list)
	}
	absent(t, "the list", list, "It **is** fine", "And then?", "JOB-1", "COST")
	one, err := direct(t, s, "review_conversations", map[string]any{"repository": "acme/web", "pr": 13, "id": "c1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"answer_markdown":"It **is** fine."`, `"path":"a.go"`, `"section":"sec_1"`, `"state":"failed"`, `"note":"Try again."`, `"reason":"agent_failed"`, "And then?"} {
		if !strings.Contains(one, want) {
			t.Errorf("conversation lacks %s: %s", want, one)
		}
	}
	absent(t, "a conversation", one, "JOB-1", "JOB-2", "cost", "COST", "job_id")
	if _, err := direct(t, s, "review_conversations", map[string]any{"repository": "acme/web", "pr": 13, "id": "zzz"}); err == nil {
		t.Error("an unknown conversation")
	}
}

func TestReviewToolsReadAGroupsCombinedReview(t *testing.T) {
	s, d := reviewServer(t)
	for tool, want := range map[string]string{"review_outline": "GROUP-OVERVIEW", "review_context": "GROUP-CONTEXT"} {
		text, err := direct(t, s, tool, map[string]any{"repository": "acme/web", "group": "g-web", "version": 2})
		if err != nil || !strings.Contains(text, want) || !strings.Contains(text, `"group":"g-web"`) {
			t.Errorf("%s: %v %s", tool, err, text)
		}
	}
	if text, err := direct(t, s, "review_section", map[string]any{"repository": "acme/web", "group": "g-web", "id": "gsec_1"}); err != nil || !strings.Contains(text, "GROUP-DETAILS") {
		t.Errorf("group section: %v %s", err, text)
	}
	found := false
	for _, r := range d.requests() {
		found = found || strings.Contains(r, "/api/groups/g-web/document?version=2")
	}
	if !found {
		t.Errorf("requests %v", d.requests())
	}
	// A chat on a group page reads the group by default.
	token, id := chat(t, s)
	if err := s.Registry.SetPage(id, PageContext{Page: PageGroup, Repository: "acme/web", Group: "g-web"}); err != nil {
		t.Fatal(err)
	}
	if text, failed := data(t, s, token, "review_outline", nil); failed || !strings.Contains(text, "GROUP-OVERVIEW") {
		t.Errorf("a group page's outline: %v %s", failed, text)
	}
	// An action's "this" is the one group its pull request is in.
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 13})
	defer revoke()
	if text, failed := data(t, s, action, "review_outline", map[string]any{"group": "this"}); failed || !strings.Contains(text, "GROUP-OVERVIEW") {
		t.Errorf("an action's group: %v %s", failed, text)
	}
	// Without a group an action reads its pull request's own review.
	if text, failed := data(t, s, action, "review_context", nil); failed || !strings.Contains(text, "CANARY-CONTEXT") {
		t.Errorf("an action's own review: %v %s", failed, text)
	}
}

// An action is refused another repository's group, job, clone and pull
// request for each family of review tools.
func TestReviewToolsAreBoundForActions(t *testing.T) {
	s, d := reviewServer(t)
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 13})
	defer revoke()
	names := listed(t, s, action)
	for _, tool := range []string{"facts", "review_outline", "review_section", "review_context", "review_draft", "review_versions", "review_conversations"} {
		if !names[tool] {
			t.Errorf("an action cannot list %s", tool)
		}
	}
	// Each tool gets the arguments its schema takes, so a refusal is the
	// binding's, not the schema's.
	foreignArgs := map[string]map[string]any{
		"facts":                {"repository": "evil/secrets", "pr": 1},
		"review_outline":       {"repository": "evil/secrets", "pr": 1, "version": 1, "group": ""},
		"review_section":       {"repository": "evil/secrets", "pr": 1, "version": 1, "id": "sec_1"},
		"review_context":       {"repository": "evil/secrets", "pr": 1, "version": 1},
		"review_draft":         {"repository": "evil/secrets", "pr": 1, "part": "outline", "id": "d1"},
		"review_versions":      {"repository": "evil/secrets", "pr": 1},
		"review_conversations": {"repository": "evil/secrets", "pr": 1, "id": "c1"},
	}
	for tool, args := range foreignArgs {
		d.reset()
		text, failed := data(t, s, action, tool, args)
		if !failed || strings.Contains(text, "FOREIGN") || strings.Contains(text, "input schema") || !strings.Contains(text, "own pull request's repository") {
			t.Errorf("%s of another repository: %v %s", tool, failed, text)
		}
		if strings.Contains(strings.Join(d.requests(), " "), "evil") || strings.Contains(strings.Join(d.requests(), " "), "c-evil") {
			t.Errorf("%s read %v", tool, d.requests())
		}
	}
	// Another repository's group, in the action's own repository's name: the
	// reader (or the tool) refuses the group, and nothing of it is read.
	foreignGroup := map[string]map[string]any{
		"review_outline":  {"group": "g-evil"},
		"review_section":  {"group": "g-evil", "id": "x"},
		"review_context":  {"group": "g-evil", "version": 1},
		"review_versions": {"group": "g-evil"},
	}
	for tool, args := range foreignGroup {
		d.reset()
		text, failed := data(t, s, action, tool, args)
		if !failed || strings.Contains(text, "FOREIGN") || strings.Contains(text, "input schema") ||
			(!strings.Contains(text, "own pull request's repository") && !strings.Contains(text, "is not a group of")) {
			t.Errorf("%s of a foreign group: %v %s", tool, failed, text)
		}
		for _, r := range d.requests() {
			if strings.Contains(r, "/document") || strings.Contains(r, "/versions") {
				t.Errorf("%s read %s", tool, r)
			}
		}
	}
	// The reader binds the routes without help from a tool: another
	// repository's clone, job and group.
	reader := s.reader.within([]string{"/api"}, "acme/web")
	for _, path := range []string{
		"/api/document?clone=c-evil&pr=1", "/api/versions?clone=c-evil&pr=1", "/api/facts?clone=c-evil&pr=1", "/api/prs/chat?clone=c-evil&pr=1",
		"/api/jobs/j-evil/draft", "/api/groups/g-evil/document", "/api/groups/g-evil/document?version=1", "/api/groups/g-evil/versions",
	} {
		d.reset()
		if response, err := reader.Get(context.Background(), path); err == nil {
			t.Errorf("%s was read: %s", path, response.Body)
		}
		for _, r := range d.requests() {
			if strings.Contains(r, "/draft") || strings.HasSuffix(strings.Split(r, "?")[0], "/document") || strings.HasSuffix(strings.Split(r, "?")[0], "/versions") {
				if strings.Contains(r, "g-evil") || strings.Contains(r, "j-evil") {
					t.Errorf("%s reached %s", path, r)
				}
			}
		}
	}
	// What an action may read of its own.
	for _, path := range []string{"/api/jobs/j-run/draft", "/api/groups/g-web/document?version=1", "/api/prs/chat?clone=c-web&pr=13", "/api/versions?clone=c-web&pr=13"} {
		if _, err := reader.Get(context.Background(), path); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
	if _, err := reader.Get(context.Background(), "/api/groups/g-web/document?version=1&extra=1"); err == nil {
		t.Error("an unlisted parameter was read")
	}
}

func (d *reviewDaemon) reset() {
	d.mu.Lock()
	d.seen = nil
	d.mu.Unlock()
}

func TestFactsToolReadsTheFactsAtTheIndexHead(t *testing.T) {
	s, d := reviewServer(t)
	text, err := direct(t, s, "facts", map[string]any{"repository": "acme/web", "pr": 12})
	if err != nil || !strings.Contains(text, `"schema":"baum.facts/1"`) || !strings.Contains(text, `"head":"h12"`) || !strings.Contains(text, `"clone":"c-web"`) {
		t.Fatalf("facts: %v %s", err, text)
	}
	if got := strings.Join(d.requests(), " "); !strings.Contains(got, "/api/facts?clone=c-web&head=h12&pr=12") {
		t.Errorf("requests %v", d.requests())
	}
}

// A running review is found among the jobs by its kind: a fix, a chat and a
// group's job with drafts are not it.
func TestReviewDraftIgnoresOtherKindsOfJob(t *testing.T) {
	s, d := reviewServer(t)
	d.mu.Lock()
	d.running, d.draftN = true, 4
	d.mu.Unlock()
	d.reset()
	text, err := direct(t, s, "review_draft", map[string]any{"repository": "acme/web", "pr": 12})
	if err != nil || !strings.Contains(text, "DRAFT-OVERVIEW") {
		t.Fatalf("draft: %v %s", err, text)
	}
	for _, r := range d.requests() {
		if strings.Contains(r, "j-chat") || strings.Contains(r, "j-fix") || strings.Contains(r, "j-group") {
			t.Errorf("read the draft of another kind of job: %s", r)
		}
	}
}

// What the jobs say is not hidden: an unreadable or unavailable list is an
// error, not "no running review".
func TestReviewDraftReportsUnavailableJobs(t *testing.T) {
	for name, body := range map[string]string{"off": `{"available":false,"code":"runner_unavailable"}`, "garbled": `{"jobs":`} {
		s := New(Options{API: jobsOverride(&reviewDaemon{}, body)})
		text, err := direct(t, s, "review_draft", map[string]any{"repository": "acme/web", "pr": 12})
		if err == nil || text == noRunningReview {
			t.Errorf("%s: %q %v", name, text, err)
		}
	}
	// A list with no jobs is no running review.
	s := New(Options{API: jobsOverride(&reviewDaemon{}, `{"available":true,"jobs":null}`)})
	if text, err := direct(t, s, "review_draft", map[string]any{"repository": "acme/web", "pr": 12}); err != nil || text != noRunningReview {
		t.Errorf("no jobs: %q %v", text, err)
	}
}

// A group's versions route failing is said as it is.
func TestReviewVersionsPassOnErrors(t *testing.T) {
	failing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/groups/g-web":
			_, _ = w.Write([]byte(`{"id":"g-web","repository":"acme/web"}`))
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	})
	s := New(Options{API: failing})
	_, err := direct(t, s, "review_versions", map[string]any{"repository": "acme/web", "group": "g-web"})
	if err == nil || !strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), "no versions route") {
		t.Errorf("a failing versions route: %v", err)
	}
	// The binding's refusal reaches the agent as it is.
	s, _ = reviewServer(t)
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 13})
	defer revoke()
	if text, failed := data(t, s, action, "review_versions", map[string]any{"group": "g-evil"}); !failed || strings.Contains(text, "no versions route") || strings.Contains(text, "no saved versions") {
		t.Errorf("a foreign group's versions: %v %s", failed, text)
	}
}

// A draft is as large as a document can be.
func TestDraftReadsUpToTheDocumentBound(t *testing.T) {
	if readLimit("/api/jobs/j/draft") != maxDocumentRead || readLimit("/api/jobs/j/transcript") != maxRead || readLimit("/api/jobs/j") != maxRead {
		t.Error("read limits")
	}
	big := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"pad":%q}`, strings.Repeat("x", 9<<20))
	})
	reader := NewReader(big, nil).within([]string{"/api/jobs"}, "")
	if _, err := reader.Get(context.Background(), "/api/jobs/j/draft"); err != nil {
		t.Errorf("a 9 MiB draft: %v", err)
	}
	if _, err := reader.Get(context.Background(), "/api/jobs/j/transcript"); err == nil {
		t.Error("a 9 MiB transcript was read")
	}
}

// The section view keeps the outline's own fields, and the tool schemas take
// only what the tools read.
func TestReviewToolSchemasAndPages(t *testing.T) {
	s := New(Options{API: http.NotFoundHandler()})
	s.mu.RLock()
	defer s.mu.RUnlock()
	take := map[string][]string{
		"review_outline": {"group", "version"}, "review_section": {"group", "version"}, "review_context": {"group", "version"},
		"review_versions": {"group"}, "review_draft": {}, "review_conversations": {}, "facts": {},
	}
	for tool, args := range take {
		tl, _ := s.tool(tool)
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(tl.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"group", "version"} {
			_, has := schema.Properties[name]
			if want := slices.Contains(args, name); has != want {
				t.Errorf("%s takes %s: %v", tool, name, has)
			}
		}
	}
	group := s.pages[PageGroup].Tools
	for _, tool := range []string{"facts", "review_draft", "review_conversations"} {
		if slices.Contains(group, tool) {
			t.Errorf("the group page lists %s", tool)
		}
	}
	for _, tool := range []string{"group", "group_versions", "review_outline", "review_section", "review_context", "review_versions", "focus"} {
		if !slices.Contains(group, tool) {
			t.Errorf("the group page lacks %s", tool)
		}
	}
	for _, tool := range []string{"facts", "review_draft", "review_conversations", "review_outline"} {
		if !slices.Contains(s.pages[PagePullRequest].Tools, tool) {
			t.Errorf("the pull request page lacks %s", tool)
		}
	}
}
