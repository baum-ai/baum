package repos

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
)

func directoryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestOpenReadOnlyLeavesAnEmptyFileAlone(t *testing.T) {
	dir := appdbtest.TempDir(t)
	path := filepath.Join(dir, "repos.sqlite")
	if err := os.WriteFile(path, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	names := directoryNames(t, dir)

	if r, err := OpenReadOnly(path); err == nil {
		_ = r.Close()
		t.Fatal("opened an unsupported empty database")
	}
	after, _ := os.Stat(path)
	if after.Size() != 0 || after.Size() != before.Size() || (runtime.GOOS != "windows" && after.Mode() != before.Mode()) || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("file changed: %v %v -> %v %v", before.Size(), before.Mode(), after.Size(), after.Mode())
	}
	if got := directoryNames(t, dir); !slices.Equal(got, names) {
		t.Errorf("directory changed: %v -> %v", names, got)
	}
}

func TestOpenReadOnlyListsWithoutWriting(t *testing.T) {
	ctx := context.Background()
	dir := appdbtest.TempDir(t)
	path := filepath.Join(dir, "repos.sqlite")
	writable, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	work := clone(t, "https://github.com/Owner/Repo.git")
	added, err := writable.Add(ctx, work, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = writable.Close()
	before, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	names := directoryNames(t, dir)

	r, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	clones, err := r.List(ctx)
	if err != nil || len(clones) != 1 || clones[0].ID != added.ID || !clones[0].Available {
		t.Fatalf("clones = %+v, err = %v", clones, err)
	}
	if r.Remove(ctx, added.ID) == nil {
		t.Error("a read-only registry accepted a write")
	}
	after, _ := os.ReadFile(path)
	afterInfo, _ := os.Stat(path)
	if !bytes.Equal(before, after) || !afterInfo.ModTime().Equal(info.ModTime()) {
		t.Error("the registry file changed")
	}
	if got := directoryNames(t, dir); !slices.Equal(got, names) {
		t.Errorf("directory changed: %v -> %v", names, got)
	}
}

func TestOpenReadOnlyDoesNotCreateAMissingRegistry(t *testing.T) {
	dir := appdbtest.TempDir(t)
	_, err := OpenReadOnly(filepath.Join(dir, "sub", "repos.sqlite"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want not exist", err)
	}
	if got := directoryNames(t, dir); len(got) != 0 {
		t.Errorf("created %v", got)
	}
}

func TestOpenReadOnlyReportsAFileThatIsNotADatabase(t *testing.T) {
	path := filepath.Join(appdbtest.TempDir(t), "repos.sqlite")
	if err := os.WriteFile(path, bytes.Repeat([]byte("not a database "), 100), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := OpenReadOnly(path); err == nil {
		_ = r.Close()
		t.Fatal("opened garbage")
	}
}
