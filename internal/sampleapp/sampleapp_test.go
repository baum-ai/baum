package sampleapp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkspaceServesEveryPage builds the sample project and reads what each
// page of the app reads, as the app asks for it: every read answers, and
// the pages the tour stops on have something to show.
func TestWorkspaceServesEveryPage(t *testing.T) {
	var own []string
	w := &Workspace{Dir: filepath.Join(t.TempDir(), "sample"), Own: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		own = append(own, r.Method+" "+r.URL.Path)
		_, _ = rw.Write([]byte(`{"revision":1,"value":{"seen":true}}`))
	})}
	t.Cleanup(w.Close)
	get := func(path string, into any) {
		t.Helper()
		rec := httptest.NewRecorder()
		w.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
		}
		if into != nil {
			if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
				t.Fatalf("GET %s: %v: %s", path, err, rec.Body)
			}
		}
	}
	var clones []struct{ ID, Repository string }
	get("api/repos", &clones)
	if len(clones) != 1 || clones[0].Repository != repository {
		t.Fatalf("clones = %+v", clones)
	}
	clone := url.QueryEscape(clones[0].ID)
	repo := url.QueryEscape(repository)

	var index struct {
		Entries []struct {
			VersionCount int    `json:"version_count"`
			Status       string `json:"status"`
		} `json:"entries"`
	}
	get("api/index?history=1", &index)
	reviewed := 0
	for _, e := range index.Entries {
		if e.VersionCount > 0 {
			reviewed++
		}
	}
	if len(index.Entries) < 4 || reviewed != len(reviews) {
		t.Fatalf("index has %d entries, %d reviewed: %+v", len(index.Entries), reviewed, index.Entries)
	}
	for _, path := range []string{"api/jobs", "api/prs", "api/automatic", "api/settings", "api/github-auth", "api/repositories",
		"api/groups", "api/inboxes", "api/plan-usage?refresh=1", "api/ui-state/inbox",
		"api/versions?clone=" + clone + "&pr=12", "api/prs/chat?clone=" + clone + "&pr=12",
		"api/prs/checks?repository=" + repo + "&pr=16", "api/prs/merge-options?repository=" + repo + "&pr=14",
		"api/prs/review-threads?repository=" + repo + "&pr=16&unresolved=2", "api/decisions?repository=" + repo + "&pr=14",
		"api/timeline/repositories"} {
		get(path, nil)
	}

	var document struct {
		Sections []struct{ ID string } `json:"sections"`
	}
	get("api/document?clone="+clone+"&pr=12", &document)
	if len(document.Sections) != 5 {
		t.Fatalf("review of #12 has %d sections", len(document.Sections))
	}
	var patch struct{ Patch string }
	get("api/patch?clone="+clone+"&pr=12&path=tags.go", &patch)
	if !strings.Contains(patch.Patch, "func CleanTags") {
		t.Fatalf("patch of tags.go = %q", patch.Patch)
	}
	// The pull request the tour opens links its author's sessions, from the
	// Entire checkpoints its commits name, and their decisions.
	var linked struct {
		Sessions []struct{ ID string }
		Commits  []struct{ Links []struct{ Session string } }
	}
	get("api/sessions?clone="+clone+"&pr=12", &linked)
	if len(linked.Sessions) != 2 {
		t.Fatalf("#12 links %d sessions: %+v", len(linked.Sessions), linked)
	}
	for _, c := range linked.Commits {
		if len(c.Links) == 0 {
			t.Fatalf("a commit of #12 links no session: %+v", linked.Commits)
		}
	}
	var captured struct {
		Sessions []struct{ Decisions []struct{ Title string } }
	}
	get("api/decisions?repository="+repo+"&pr=12", &captured)
	if len(captured.Sessions) != 2 || len(captured.Sessions[0].Decisions) == 0 {
		t.Fatalf("decisions of #12 = %+v", captured)
	}

	var features struct {
		Features []struct{ Title string } `json:"features"`
	}
	get("api/timeline/features?repository="+repo, &features)
	var decided struct {
		Decisions []struct{ Summary string } `json:"decisions"`
	}
	get("api/timeline/decisions?repository="+repo, &decided)
	if len(features.Features) != 3 || len(decided.Decisions) < 6 {
		t.Fatalf("timeline has %d features and %d decisions", len(features.Features), len(decided.Decisions))
	}

	var live struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			State     string `json:"state"`
			PR        int    `json:"pr"`
			Focus     string `json:"focus"`
		} `json:"sessions"`
	}
	get("api/live-sessions", &live)
	states := map[string]bool{}
	for _, s := range live.Sessions {
		states[s.State] = true
		// The tour names Focus session, which a running session offers.
		if s.State == "running" && s.Focus != "session" {
			t.Errorf("running session offers focus %q", s.Focus)
		}
	}
	if len(live.Sessions) != 3 || !states["running"] || !states["ended"] {
		t.Fatalf("live sessions = %+v", live.Sessions)
	}
	get("api/live-sessions/claude-code/"+agentSessions[0].id+"/transcript", nil)

	var report struct {
		Summary struct {
			Sessions int `json:"sessions"`
		} `json:"summary"`
	}
	get("api/analytics?days=30", &report)
	if report.Summary.Sessions < len(agentSessions) {
		t.Fatalf("analytics counts %d sessions", report.Summary.Sessions)
	}

	// Changes are refused, marked; the sample's own view state and the
	// tour's state, which is the person's, are not.
	rec := httptest.NewRecorder()
	w.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/prs/merge", strings.NewReader(`{}`)))
	if rec.Code != http.StatusConflict || rec.Header().Get("X-Baum-Sample") != Refused {
		t.Fatalf("merge: %d %q", rec.Code, rec.Header().Get("X-Baum-Sample"))
	}
	rec = httptest.NewRecorder()
	w.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/ui-state/tour", strings.NewReader(`{}`)))
	if len(own) != 1 || own[0] != "PUT /api/ui-state/tour" {
		t.Fatalf("own API got %q", own)
	}
}
