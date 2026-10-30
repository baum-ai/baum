package github

import (
	"context"
	"errors"
	"testing"
)

func TestCurrentNameFollowsARenamedRepository(t *testing.T) {
	fake := newFakeGitHub(t, map[string]route{"CurrentName": func(request gqlRequest, _ int) fakeResponse {
		if request.Variables["owner"] != "old-owner" || request.Variables["name"] != "app" {
			t.Errorf("variables = %v", request.Variables)
		}
		return data(map[string]any{"repository": map[string]any{"nameWithOwner": "New-Owner/App"}})
	}})
	got, err := fake.client(nil).CurrentName(context.Background(), Repository{Host: "github.com", Owner: "old-owner", Name: "app"})
	if err != nil || got != "new-owner/app" {
		t.Fatalf("CurrentName = %q, %v", got, err)
	}
}

func TestCurrentNameReportsARepositoryTheTokenCannotSee(t *testing.T) {
	fake := newFakeGitHub(t, map[string]route{"CurrentName": func(gqlRequest, int) fakeResponse {
		return graphQLError(map[string]any{"repository": nil}, "NOT_FOUND", "Could not resolve to a Repository with the name 'acme/gone'.")
	}})
	if _, err := fake.client(nil).CurrentName(context.Background(), Repository{Host: "github.com", Owner: "acme", Name: "gone"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
