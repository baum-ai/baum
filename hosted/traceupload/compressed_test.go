package traceupload

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/baum-ai/baum/sessions/collector"
	"github.com/baum-ai/baum/sessions/collector/model"
	"github.com/baum-ai/baum/sessions/vendors"
)

func buildOne(t *testing.T, path string) (*Bundle, error) {
	t.Helper()
	return BuildWithProgress(context.Background(), []collector.Session{{
		Source: vendors.Codex, Path: path, SessionID: "id", ModifiedAt: time.Unix(1, 0).UTC(),
	}}, Repository{ID: "repo-id", FullName: "acme/demo"}, "test", nil)
}

// A compressed rollout is staged as what it decodes to, under its plain name:
// the bytes and digest are those of the plain rollout, and no zstd leaves the
// machine.
func TestBuildStagesACompressedRolloutAsThePlainOne(t *testing.T) {
	content := `{"type":"event","token":"` + fakeGitHubToken + `","message":"keep me"}` + "\n"
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain", "rollout-2026-07-01T10-00-00-id.jsonl")
	packed := filepath.Join(dir, "packed", "rollout-2026-07-01T10-00-00-id.jsonl.zst")
	for _, path := range []string{plain, packed} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(plain, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(content))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packed, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	want, err := buildOne(t, plain)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = want.Close() }()
	got, err := buildOne(t, packed)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = got.Close() }()

	wantFile, gotFile := want.Manifest.Sessions[0].Files[0], got.Manifest.Sessions[0].Files[0]
	if gotFile.Name != wantFile.Name || gotFile.SHA256 != wantFile.SHA256 || gotFile.SizeBytes != wantFile.SizeBytes {
		t.Fatalf("compressed staged as %+v, plain as %+v", gotFile, wantFile)
	}
	if gotFile.Name != "rollout-2026-07-01T10-00-00-id.jsonl" {
		t.Fatalf("name %q", gotFile.Name)
	}
	wantBytes, gotBytes := readBundle(t, want.File)[wantFile.Member], readBundle(t, got.File)[gotFile.Member]
	if len(gotBytes) == 0 || !bytes.Equal(gotBytes, wantBytes) || bytes.Contains(gotBytes, []byte(fakeGitHubToken)) {
		t.Fatalf("staged %q, want %q", gotBytes, wantBytes)
	}
}

// A compressed rollout that cannot be decompressed fails the build, rather
// than uploading a transcript cut short.
func TestBuildRejectsACorruptCompressedRollout(t *testing.T) {
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(bytes.Repeat([]byte(`{"type":"event"}`+"\n"), 100000))
	_ = w.Close()
	path := filepath.Join(t.TempDir(), "rollout-2026-07-01T10-00-00-id.jsonl.zst")
	if err := os.WriteFile(path, buf.Bytes()[:buf.Len()/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildOne(t, path); err == nil {
		t.Fatal("a truncated compressed rollout was staged")
	}
}

// A rollout listed both plain and compressed would be staged twice under one
// name, so it is refused like any other repeated name.
func TestBuildRejectsARolloutListedPlainAndCompressed(t *testing.T) {
	content := `{"type":"event","message":"keep me"}` + "\n"
	plain := filepath.Join(t.TempDir(), "rollout-2026-07-01T10-00-00-id.jsonl")
	if err := os.WriteFile(plain, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(content))
	_ = w.Close()
	if err := os.WriteFile(plain+".zst", buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = BuildWithProgress(context.Background(), []collector.Session{{
		Source: vendors.Codex, Path: plain, SessionID: "id", ModifiedAt: time.Unix(1, 0).UTC(),
		Files: []model.SessionFile{
			{Path: plain, Name: filepath.Base(plain), Kind: model.KindTranscript},
			{Path: plain + ".zst", Name: filepath.Base(plain) + ".zst", Kind: model.KindSubTranscript},
		},
	}}, Repository{ID: "repo-id", FullName: "acme/demo"}, "test", nil)
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("err = %v, want the repeated name refused", err)
	}
}
