package locallink

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/baumai/baum/internal/locallink/linktest"
)

func request(f linktest.Fixture) Request {
	return Request{Dir: f.Repo, Repository: linktest.Repository, Head: f.Head, Base: f.Base, Sessions: f.Store}
}

func resolve(t *testing.T, req Request) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := Resolve(ctx, req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return result
}

func TestResolveLinksTheSessionThatCommitted(t *testing.T) {
	f := linktest.New(t)
	var warnings bytes.Buffer
	req := request(f)
	req.Warn = &warnings
	result := resolve(t, req)
	if !result.Available {
		t.Fatalf("unavailable: %s: %s (warnings: %s)", result.Code, result.Reason, warnings.String())
	}
	if len(result.Sessions) != 1 || result.Sessions[0].ID != linktest.SessionID ||
		result.Sessions[0].Source != "claude_code" || result.Sessions[0].Branch != "feature" {
		t.Fatalf("sessions = %+v", result.Sessions)
	}
	if len(result.Commits) != 2 || result.Commits[0].OID != f.Commits[0] || result.Commits[1].OID != f.Commits[1] {
		t.Fatalf("commits = %+v", result.Commits)
	}
	first := result.Commits[0]
	if len(first.Links) != 1 || first.Links[0].Session != linktest.SessionID ||
		first.Links[0].Method != "commit_ref" || first.Links[0].Confidence < 0.9 {
		t.Errorf("first commit links = %+v", first.Links)
	}
	if first.Subject != "Add the feature" || first.Author != "Grace Hopper" || first.CommittedAt == "" {
		t.Errorf("first commit = %+v", first)
	}
	if links := result.Commits[1].Links; links == nil || len(links) != 0 {
		t.Errorf("the unrelated commit must be listed with no links, got %+v", links)
	}
}

func TestResolveRedactsEverythingItReturns(t *testing.T) {
	f := linktest.New(t)
	encoded, err := json.Marshal(resolve(t, request(f)))
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, leaked := range []string{linktest.Email, "ada.lovelace", linktest.Secret, "bob@example.com", "grace@example.com", f.Repo} {
		if strings.Contains(text, leaked) {
			t.Errorf("response leaks %q:\n%s", leaked, text)
		}
	}
	if !strings.Contains(text, "Ask ") || !strings.Contains(text, "about the feature") {
		t.Errorf("the title should survive, masked:\n%s", text)
	}
	if !strings.Contains(text, "Tidy up, reported by") {
		t.Errorf("the second subject should survive, masked:\n%s", text)
	}
}

func TestResolveShapes(t *testing.T) {
	f := linktest.New(t)
	encoded, err := json.Marshal(resolve(t, request(f)))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["available"]) != "true" || got["sessions"] == nil || got["commits"] == nil || got["code"] != nil || got["reason"] != nil {
		t.Errorf("available shape = %s", encoded)
	}
	unavailable, _ := json.Marshal(Unavailable(CodeSample, "a sample"))
	if string(unavailable) != `{"available":false,"code":"sample","reason":"a sample"}` {
		t.Errorf("unavailable shape = %s", unavailable)
	}
}

func TestResolveWithoutSessionsIsReadyAndEmpty(t *testing.T) {
	f := linktest.New(t).NoSession(t)
	result := resolve(t, request(f))
	if !result.Available || result.Sessions == nil || len(result.Sessions) != 0 || len(result.Commits) != 2 {
		t.Fatalf("result = %+v", result)
	}
	encoded, _ := json.Marshal(result)
	if !strings.Contains(string(encoded), `"sessions":[]`) {
		t.Errorf("an empty list must be [], got %s", encoded)
	}
}

func TestResolveUnavailable(t *testing.T) {
	f := linktest.New(t)
	absent := strings.Repeat("a", 40)
	tests := []struct {
		name   string
		change func(*Request)
		code   string
	}{
		{"another repository", func(r *Request) { r.Repository = "acme/other" }, CodeRemoteMismatch},
		{"a missing head", func(r *Request) { r.Head = absent }, CodeCommitsMissing},
		{"a missing base", func(r *Request) { r.Base = absent }, CodeCommitsMissing},
		{"no head recorded", func(r *Request) { r.Head = "" }, CodeNoHead},
		{"a head that is not a commit id", func(r *Request) { r.Head = "feature" }, CodeNoHead},
		{"a missing merge base and base", func(r *Request) { r.MergeBase, r.Base = absent, absent }, CodeCommitsMissing},
		{"outside a clone", func(r *Request) { r.Dir = t.TempDir() }, CodeNotAClone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := request(f)
			tt.change(&req)
			result := resolve(t, req)
			if result.Available || result.Code != tt.code || result.Reason == "" {
				t.Errorf("result = %+v, want unavailable %s", result, tt.code)
			}
			if strings.Contains(result.Reason, f.Repo) {
				t.Errorf("reason leaks a path: %q", result.Reason)
			}
		})
	}
}

func TestResolveWithoutRemotes(t *testing.T) {
	f := linktest.New(t)
	linktest.Git(t, f.Repo, "remote", "remove", "origin")
	if result := resolve(t, request(f)); result.Available || result.Code != CodeRemoteMismatch {
		t.Errorf("result = %+v", result)
	}
}

func TestCoauthoredAgents(t *testing.T) {
	got := coauthoredAgents([]string{
		"Co-Authored-By: Claude <noreply@anthropic.com>",
		"co-authored-by: Cursor Agent <cursoragent@cursor.com>",
		"Co-authored-by: Copilot <12345+Copilot@users.noreply.github.com>",
		"Co-authored-by: Ada <ada@example.com>",
		"Reviewed-by: Claude <noreply@anthropic.com>",
		"Co-Authored-By: Claude <noreply@anthropic.com>",
	})
	want := []string{"claude", "cursor", "copilot"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("agents = %v, want %v", got, want)
	}
}
