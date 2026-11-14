package replay_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/gitlocal"
)

// source is the pull request the synthetic cases are made from, assembled by
// the real assembler, with the trees it exported.
type source struct {
	result assemble.Result
	// trees maps "head/<path>" and "merge-base/<path>" to the file's content.
	trees  map[string]string
	images map[string][]byte
}

func assembleSource(t *testing.T) source {
	t.Helper()
	repo := assembletest.NewRepo(t, assembletest.Options{})
	clone, err := gitlocal.Open(context.Background(), repo.Dir, gitlocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clone.Close() })
	assembler := &assemble.Assembler{
		GitHub: &assembletest.GitHub{Evidence: repo.Evidence()}, Git: clone,
		Sessions: &assembletest.Sessions{Found: assembletest.SessionTree()},
		Images:   func(context.Context, string, int64) ([]byte, error) { return assembletest.PNG, nil },
	}
	var checkout, attachments bytes.Buffer
	request := assemble.Request{Repository: assembletest.Repository, Number: assembletest.Number, Limits: assemble.DefaultLimits(), Checkout: &checkout, Attachments: &attachments}
	result, err := assembler.Assemble(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	s := source{result: result, trees: map[string]string{}, images: map[string][]byte{}}
	zip, err := gzip.NewReader(&checkout)
	if err != nil {
		t.Fatal(err)
	}
	for r := tar.NewReader(zip); ; {
		header, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			content, _ := io.ReadAll(r)
			s.trees[header.Name] = string(content)
		}
	}
	for r := tar.NewReader(&attachments); ; {
		header, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		s.images[filepath.Base(header.Name)], _ = io.ReadAll(r)
	}
	if len(s.trees) == 0 || len(s.images) == 0 {
		t.Fatalf("the source has %d files and %d images", len(s.trees), len(s.images))
	}
	return s
}

// dataset is a synthetic dataset directory.
type dataset struct {
	dir string
	src source
	// entries are the index lines.
	entries []map[string]any
}

func newDataset(t *testing.T, src source) *dataset {
	t.Helper()
	d := &dataset{dir: t.TempDir(), src: src}
	git(t, "", "init", "-q", "--bare", filepath.Join(d.dir, "checkouts.git"))
	return d
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return gitWith(t, nil, dir, args...)
}

func gitWith(t *testing.T, extra []string, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), extra...), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_AUTHOR_DATE=2026-07-01T10:00:00Z", "GIT_COMMITTER_DATE=2026-07-01T10:00:00Z")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// addCase writes cases/<id>/ with the source's bundle, images and case.json,
// the two refs in checkouts.git and an index line made of facts.
func (d *dataset) addCase(t *testing.T, id string, facts map[string]any) {
	t.Helper()
	d.addCaseJSON(t, id, facts, d.src.result.JSON)
}

// addCaseJSON is addCase with the case's bundle bytes given.
func (d *dataset) addCaseJSON(t *testing.T, id string, facts map[string]any, bundleJSON []byte) {
	t.Helper()
	dir := filepath.Join(d.dir, "cases", id)
	if err := os.MkdirAll(filepath.Join(dir, "attachments"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path string, content []byte) {
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "bundle.json"), bundleJSON)
	for name, content := range d.src.images {
		write(filepath.Join(dir, "attachments", name), content)
	}
	bare := filepath.Join(d.dir, "checkouts.git")
	trees := map[string]string{}
	for _, root := range []string{"head", "merge-base"} {
		work := t.TempDir()
		for name, content := range d.src.trees {
			if rest, ok := strings.CutPrefix(name, root+"/"); ok {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(work, rest)), 0o755); err != nil {
					t.Fatal(err)
				}
				write(filepath.Join(work, rest), []byte(content))
			}
		}
		index := filepath.Join(t.TempDir(), "index")
		env := []string{"GIT_INDEX_FILE=" + index}
		gitWith(t, env, work, "--git-dir="+bare, "--work-tree="+work, "add", "-A")
		tree := gitWith(t, env, work, "--git-dir="+bare, "write-tree")
		commit := gitWith(t, env, work, "--git-dir="+bare, "commit-tree", tree, "-m", root)
		gitWith(t, env, work, "--git-dir="+bare, "update-ref", "refs/cases/"+id+"/"+root, commit)
		trees[root] = tree
	}
	sum := sha256.Sum256(bundleJSON)
	meta, _ := json.Marshal(map[string]string{"id": id, "bundle_sha256": hex.EncodeToString(sum[:]), "head_tree": trees["head"], "merge_base_tree": trees["merge-base"]})
	write(filepath.Join(dir, "case.json"), meta)

	entry := map[string]any{"id": id, "repository": "acme/demo", "number": 7, "split": "calibration", "representative": false, "primary": true, "size": "s", "session_vendors": []string{}, "attachments": 0, "threads": 0}
	for k, v := range facts {
		entry[k] = v
	}
	d.entries = append(d.entries, entry)
	var lines []string
	for _, e := range d.entries {
		line, _ := json.Marshal(e)
		lines = append(lines, string(line))
	}
	if err := os.MkdirAll(filepath.Join(d.dir, "index"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(d.dir, "index", "cases.jsonl"), []byte(strings.Join(lines, "\n")+"\n"))
}

// snapshot lists every file of dir with its size, to prove nothing wrote there.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err == nil {
			info, _ := entry.Info()
			lines = append(lines, path+" "+info.ModTime().String()+" "+fmt.Sprint(info.Size()))
		}
		return nil
	})
	return strings.Join(lines, "\n")
}
