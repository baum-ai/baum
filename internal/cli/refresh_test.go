package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/store"
)

func TestRefreshCommandUpdatesLatestWithoutCallingModel(t *testing.T) {
	backend, sessions := generateEnv(t)
	root := filepath.Join(t.TempDir(), "reviews")
	args := append([]string{"internal", "generate", "--pr", "7", "--store", root}, sessions...)
	for range 2 {
		if _, stderr, err := run(t, args...); err != nil {
			t.Fatalf("generate: %v %s", err, stderr)
		}
	}
	versions, err := store.Open("", root, false)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: "acme/demo", Number: 7}
	ctx := context.Background()
	previous, err := versions.Get(ctx, key, 1)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := versions.Latest(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	calls := len(backend.Requests)
	fake := newGitHub(config.Config{}).(*fakeGitHub)
	fake.Evidence.PullRequest.HeadSHA = "aaaaaaa"
	stdout, stderr, err := run(t, append([]string{"internal", "refresh", "--pr", "7", "--store", root, "--repo", "acme/demo"}, sessions...)...)
	if err != nil {
		t.Fatalf("refresh: %v %s", err, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, "version 2") || !strings.Contains(stderr, "--store ") || !strings.Contains(stderr, "--repo ") {
		t.Fatalf("output: %q %q", stdout, stderr)
	}
	if len(backend.Requests) != calls {
		t.Fatal("refresh called model")
	}
	updated, err := versions.Latest(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if string(updated.Document) == string(latest.Document) || !reflect.DeepEqual(updated.Metadata, latest.Metadata) {
		t.Fatalf("wrong refreshed version: %+v", updated.Metadata)
	}
	old, err := versions.Get(ctx, key, 1)
	if err != nil || string(old.Document) != string(previous.Document) {
		t.Fatal("changed replaced version")
	}
	all, err := versions.Versions(ctx, key)
	if err != nil || len(all) != 2 {
		t.Fatal("refresh created version")
	}
	fake.Err = errors.New("offline")
	if _, _, err := run(t, append([]string{"internal", "refresh", "--pr", "7", "--store", root}, sessions...)...); err == nil {
		t.Fatal("accepted source failure")
	}
	unchanged, err := versions.Latest(ctx, key)
	if err != nil || string(unchanged.Document) != string(updated.Document) {
		t.Fatal("failure changed document")
	}
}

func TestRefreshMissingDocumentDoesNotGenerateOrCreateStore(t *testing.T) {
	backend, sessions := generateEnv(t)
	root := filepath.Join(t.TempDir(), "absent")
	_, _, err := run(t, append([]string{"internal", "refresh", "--pr", "7", "--store", root}, sessions...)...)
	if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("created missing store: %v", statErr)
	}
	if !errors.Is(err, store.ErrNotFound) || len(backend.Requests) != 0 {
		t.Fatalf("missing review: %v", err)
	}
}

func TestRefreshRejectsHistoricalVersionSelector(t *testing.T) {
	if _, _, err := run(t, "internal", "refresh", "--pr", "7", "--version", "1"); err == nil {
		t.Fatal("accepted historical refresh")
	}
}

func TestRefreshEnterpriseReviewKeepsHostScopedHistory(t *testing.T) {
	_, sessions := generateEnv(t)
	root := filepath.Join(t.TempDir(), "reviews")
	repository := "acme.ghe.com/acme/demo"
	flags := append([]string{"--pr", "7", "--repo", repository, "--store", root}, sessions...)
	if _, stderr, err := run(t, append([]string{"internal", "generate"}, flags...)...); err != nil {
		t.Fatalf("generate: %v\n%s", err, stderr)
	}
	if _, stderr, err := run(t, append([]string{"internal", "refresh"}, flags...)...); err != nil {
		t.Fatalf("refresh: %v\n%s", err, stderr)
	}
	versions, err := store.Open("", root, false)
	if err != nil {
		t.Fatal(err)
	}
	all, err := versions.Versions(context.Background(), store.Key{Repository: repository, Number: 7})
	if err != nil || len(all) != 1 {
		t.Fatalf("Enterprise history: %+v %v", all, err)
	}
	if _, _, err := run(t, "internal", "validate", "--pr", "7", "--repo", repository, "--store", root); err != nil {
		t.Fatal(err)
	}
}
