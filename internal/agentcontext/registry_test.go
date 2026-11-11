package agentcontext

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestTokens(t *testing.T) {
	r := NewRegistry()
	token, id, revoke := r.Mint(Consumer{Kind: Chat, ID: "chosen"})
	other, otherID, _ := r.Mint(Consumer{Kind: Chat})
	for _, value := range []string{token, id, other, otherID} {
		if b, err := base64.RawURLEncoding.DecodeString(value); err != nil || len(b) != 32 {
			t.Fatalf("%q is not 32 random bytes", value)
		}
	}
	if token == other || token == id || id == "chosen" || id == otherID {
		t.Fatal("tokens and ids must be independent")
	}
	c, ok := r.Authenticate(token)
	if !ok || c.ID != id || c.Kind != Chat {
		t.Fatalf("%+v %v", c, ok)
	}
	for _, wrong := range []string{"", id, token[:len(token)-1], token + "x", strings.ToUpper(token)} {
		if _, ok := r.Authenticate(wrong); ok {
			t.Fatalf("%q authenticated", wrong)
		}
	}
	revoke()
	revoke()
	if _, ok := r.Authenticate(token); ok {
		t.Fatal("revoked token authenticated")
	}
	if _, ok := r.Consumer(id); ok {
		t.Fatal("revoked consumer found")
	}
	if _, ok := r.Authenticate(other); !ok {
		t.Fatal("revoking one token revoked another")
	}
}

func TestPageIsACopy(t *testing.T) {
	r := NewRegistry()
	_, id, _ := r.Mint(Consumer{Kind: Chat})
	filters := map[string]string{"q": "a"}
	if err := r.SetPage(id, PageContext{Page: PageInbox, Filters: filters}); err != nil {
		t.Fatal(err)
	}
	filters["q"] = "b"
	page, _ := r.Page(id)
	page.Filters["q"] = "c"
	if page, _ := r.Page(id); page.Filters["q"] != "a" {
		t.Fatal("the stored page was changed from outside")
	}
	for _, bad := range []PageContext{
		{Page: PageInbox, Focus: json.RawMessage(`{`)},
		{Page: PageInbox, Focus: json.RawMessage(`"` + strings.Repeat("x", maxFocus) + `"`)},
		{Page: PageInbox, Repository: strings.Repeat("x", maxPageText+1)},
	} {
		if err := r.SetPage(id, bad); err == nil {
			t.Fatal("accepted a page over the limits")
		}
	}
}

func TestActionIsBoundToItsPullRequest(t *testing.T) {
	s := newServer(t)
	var targets []string
	if err := s.RegisterTool(Tool{
		Name: "read_pr", Description: "Reads a pull request.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"repository":{"type":"string"},"number":{"type":"integer"}}}`),
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				Repository string `json:"repository"`
				Number     int    `json:"number"`
			}
			_ = json.Unmarshal(args, &a)
			repository, number, err := c.Target(a.Repository, a.Number)
			if err != nil {
				return "", err
			}
			targets = append(targets, repository+"#"+strconv.Itoa(number))
			response, err := c.Reader.Get(ctx, "/api/prs/"+repository)
			return string(response.Body), err
		},
	}); err != nil {
		t.Fatal(err)
	}
	token, id, revoke := s.Registry.Mint(Consumer{Kind: Action, Repository: "acme/web", PullRequest: 12})
	defer revoke()
	page, ok := s.Registry.Page(id)
	if !ok || page.Page != PagePullRequest || page.Repository != "acme/web" || page.PullRequest != 12 {
		t.Fatalf("action page %+v", page)
	}
	if err := s.Registry.SetPage(id, PageContext{Page: PageSettings}); err == nil {
		t.Fatal("an action's page moved")
	}
	if r := call(t, s, token, "read_pr", map[string]any{}); r.IsError {
		t.Fatal(r.Content[0].Text)
	}
	if r := call(t, s, token, "read_pr", map[string]any{"repository": "ACME/web", "number": 3}); r.IsError {
		t.Fatal(r.Content[0].Text)
	}
	if r := call(t, s, token, "read_pr", map[string]any{"repository": "evil/secrets"}); !r.IsError || !strings.Contains(r.Content[0].Text, "only its own pull request's repository") {
		t.Fatalf("another repository was read: %+v", r)
	}
	if strings.Join(targets, ",") != "acme/web#12,acme/web#3" {
		t.Fatal(targets)
	}
	// An action cannot propose, whatever page it names.
	if err := s.RegisterPage(PageSpec{Name: PagePullRequest, Proposer: &fakeProposer{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Proposals.Propose(context.Background(), id, Proposal{Page: PagePullRequest, Target: "x", After: json.RawMessage(`1`)}); !errors.Is(err, ErrReadOnly) {
		t.Fatal(err)
	}

	chatToken, chatID := chat(t, s)
	if r := call(t, s, chatToken, "read_pr", map[string]any{}); !r.IsError {
		t.Fatal("a chat without a page read a default repository")
	}
	if err := s.Registry.SetPage(chatID, PageContext{Page: PagePullRequest, Repository: "other/repo", PullRequest: 4}); err != nil {
		t.Fatal(err)
	}
	if r := call(t, s, chatToken, "read_pr", map[string]any{}); r.IsError {
		t.Fatal(r.Content[0].Text)
	}
	if r := call(t, s, chatToken, "read_pr", map[string]any{"repository": "third/repo", "number": 1}); r.IsError {
		t.Fatal(r.Content[0].Text)
	}
	if got := strings.Join(targets[2:], ","); got != "other/repo#4,third/repo#1" {
		t.Fatal(got)
	}
}
