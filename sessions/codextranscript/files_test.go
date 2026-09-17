package codextranscript

import (
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"
)

type source struct{ name, path, kind string }
type sources []source

func (f sources) Len() int { return len(f) }
func (f sources) At(i int) (string, string, string) {
	return f[i].name, f[i].path, f[i].kind
}

func TestDecodeLocalFilesUsesListedKinds(t *testing.T) {
	dir := filepath.Join("testdata", "store", "sessions", "2026", "08", "01")
	files := sources{
		{"root", filepath.Join(dir, "rollout-2026-08-01T12-00-00-11111111-1111-4111-8111-111111111111.jsonl"), "transcript"},
		{"child", filepath.Join(dir, "rollout-2026-08-01T12-01-00-22222222-2222-4222-8222-222222222222.jsonl"), "sub_transcript"},
		{"fork", filepath.Join(dir, "rollout-2026-08-01T12-02-00-33333333-3333-4333-8333-333333333333.jsonl"), "fork_source"},
	}
	tree, err := DecodeLocalFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Children) != 1 || len(tree.ForkSources) != 1 || len(tree.Files()) != 2 {
		t.Fatalf("decoded tree = root %v, children %d, forks %d", tree.Root != nil, len(tree.Children), len(tree.ForkSources))
	}
	for _, file := range append(tree.Files(), tree.ForkSources...) {
		if tree.Rollout(file.Meta.ID) != file {
			t.Errorf("rollout %s is not indexed by thread id", file.Path)
		}
	}
}

type countingFS struct {
	fs.FS
	opens map[string]int
}

func (f *countingFS) Open(name string) (fs.File, error) {
	f.opens[name]++
	return f.FS.Open(name)
}

func TestDecodeFilesOpensEachListedRolloutOnce(t *testing.T) {
	const body = `{"timestamp":"2026-08-01T12:00:00Z","type":"session_meta","payload":{"id":"root","session_id":"root","source":"cli"}}` + "\n"
	store := &countingFS{FS: fstest.MapFS{"root.jsonl": {Data: []byte(body)}}, opens: map[string]int{}}
	if _, err := DecodeFiles(store, sources{{"root.jsonl", "root.jsonl", "transcript"}}); err != nil {
		t.Fatal(err)
	}
	if store.opens["root.jsonl"] != 1 {
		t.Fatalf("root opens = %d, want 1", store.opens["root.jsonl"])
	}
}
