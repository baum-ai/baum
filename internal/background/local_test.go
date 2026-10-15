package background

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/schema"
)

type source struct {
	evidence github.Evidence
	before   func()
	heads    []string
}

func (s *source) Fetch(_ context.Context, _ github.Repository, _ int, opts github.FetchOptions) (github.Evidence, error) {
	s.heads = append(s.heads, opts.ChecksHead)
	if s.before != nil {
		s.before()
	}
	return s.evidence, nil
}
func TestLocalRefreshPreservesVersionsAndRejectsConcurrentGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, output, err)
		}
		return strings.TrimSpace(string(output))
	}
	git("init")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "--allow-empty", "-m", "fixture")
	head := git("rev-parse", "HEAD")
	common := filepath.Join(dir, ".git")
	versions, err := store.Open(common, "", true)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(schema.Example, &doc); err != nil {
		t.Fatal(err)
	}
	header := doc["review"].(map[string]any)
	header["version"] = 1
	header["previous_version"] = nil
	header["head"] = head
	header["base"] = head
	header["merge_base"] = head
	raw, _ := json.Marshal(doc)
	key := store.Key{Repository: "example-org/taskline", Number: 212}
	saved := store.Version{Document: raw, Metadata: store.Metadata{Version: 1, Head: head, Base: head, MergeBase: head, Backend: "codex", Model: "priced", GeneratedAt: header["generated_at"].(string)}}
	if err := versions.Put(ctx, key, 0, saved); err != nil {
		t.Fatal(err)
	}
	remote := &source{evidence: github.Evidence{Repository: key.Repository, PullRequest: bundle.PullRequest{Number: 212, State: "closed", MergedSHA: head, HeadSHA: head, BaseSHA: head}, Checks: []bundle.Check{{ID: "chk_9001", Kind: "check_run", Name: "Tests", Status: "completed", Conclusion: "failure"}}, Sources: bundle.Sources{"checks": true, "review_threads": true}}}
	local := Local{Client: remote}
	clone := repos.Clone{Repository: key.Repository, Path: dir, CommonDir: common}
	observation, err := local.Refresh(ctx, clone, 212)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := versions.Latest(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	var after map[string]any
	_ = json.Unmarshal(latest.Document, &after)
	if observation.State != "MERGED" || observation.Checks != "failure" || !reflect.DeepEqual(saved.Metadata, latest.Metadata) {
		t.Fatalf("observation=%+v metadata=%+v", observation, latest.Metadata)
	}
	beforeSections := doc["sections"].([]any)
	afterSections := after["sections"].([]any)
	for i, section := range beforeSections {
		for _, field := range []string{"title", "summary", "blocks", "attention", "assessment"} {
			if !reflect.DeepEqual(section.(map[string]any)[field], afterSections[i].(map[string]any)[field]) {
				t.Fatalf("authored %s changed", field)
			}
		}
	}
	doc2 := after
	doc2["review"].(map[string]any)["version"] = 2
	doc2["review"].(map[string]any)["previous_version"] = 1
	raw2, _ := json.Marshal(doc2)
	second := store.Version{Document: raw2, Metadata: latest.Metadata}
	second.Metadata.Version = 2
	remote.before = func() {
		remote.before = nil
		if err := versions.Put(ctx, key, 1, second); err != nil {
			t.Fatal(err)
		}
	}
	_, err = local.Refresh(ctx, clone, 212)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale refresh overwrote generation: %v", err)
	}
	current, err := versions.Latest(ctx, key)
	if err != nil || current.Metadata.Version != 2 || !bytes.Equal(current.Document, raw2) {
		t.Fatal("new version overwritten")
	}
	old, err := versions.Get(ctx, key, 1)
	if err != nil || !bytes.Equal(old.Document, latest.Document) {
		t.Fatal("older version mutated")
	}
}
func TestLocalNoReviewMetadataNeverCreatesStore(t *testing.T) {
	common := t.TempDir()
	remote := &source{evidence: github.Evidence{PullRequest: bundle.PullRequest{Number: 7, State: "open", HeadSHA: "new"}}}
	observation, err := (Local{Client: remote}).Refresh(context.Background(), repos.Clone{Repository: "acme/app", CommonDir: common}, 7)
	if err != nil || observation.Number != 7 || len(remote.heads) != 1 || remote.heads[0] != "" {
		t.Fatalf("%+v %v", observation, err)
	}
	versions, _ := store.Open(common, "", false)
	if _, err := versions.Latest(context.Background(), store.Key{Repository: "acme/app", Number: 7}); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("no-review refresh created a version")
	}
}
