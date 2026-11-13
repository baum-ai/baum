package codextranscript

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func zstdBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRolloutNames(t *testing.T) {
	for name, want := range map[string]bool{
		"rollout-a.jsonl":     true,
		"rollout-a.jsonl.zst": true,
		"rollout-a.json":      false,
		"rollout-a.zst":       false,
		"history.jsonl":       false,
		"x-rollout-a.jsonl":   false,
	} {
		if got := IsRolloutName(name); got != want {
			t.Errorf("IsRolloutName(%q) = %v, want %v", name, got, want)
		}
	}
	if PlainName("rollout-a.jsonl.zst") != "rollout-a.jsonl" || PlainName("rollout-a.jsonl") != "rollout-a.jsonl" {
		t.Error("PlainName")
	}
	if !Compressed("/x/rollout-a.jsonl.zst") || Compressed("/x/rollout-a.jsonl") {
		t.Error("Compressed")
	}
}

func TestOpenRolloutReadsPlainAndCompressed(t *testing.T) {
	dir := t.TempDir()
	content := bytes.Repeat([]byte(`{"type":"x"}`+"\n"), 1000)
	plain := filepath.Join(dir, "rollout-a.jsonl")
	packed := plain + ".zst"
	if err := os.WriteFile(plain, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packed, zstdBytes(t, content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{plain, packed} {
		r, err := OpenRollout(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		if err != nil || !bytes.Equal(got, content) {
			t.Fatalf("%s: read %d bytes, %v", path, len(got), err)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := OpenRollout(filepath.Join(dir, "missing.jsonl.zst")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
}

func TestCorruptCompressedRolloutIsMarked(t *testing.T) {
	dir := t.TempDir()
	good := zstdBytes(t, bytes.Repeat([]byte("0123456789\n"), 100000))
	for name, data := range map[string][]byte{
		"garbage":   []byte("not zstd at all"),
		"truncated": good[:len(good)/2],
	} {
		path := filepath.Join(dir, name+".jsonl.zst")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		r, err := OpenRollout(path)
		if err == nil {
			_, err = io.ReadAll(r)
			_ = r.Close()
		}
		if !errors.Is(err, ErrDecompress) {
			t.Errorf("%s: error %v, want ErrDecompress", name, err)
		}
	}
}

func TestDecodedSize(t *testing.T) {
	dir := t.TempDir()
	content := bytes.Repeat([]byte("abc\n"), 5000)
	plain := filepath.Join(dir, "rollout-a.jsonl")
	if err := os.WriteFile(plain, content, 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(plain)
	if got := DecodedSize(plain, info); got != int64(len(content)) {
		t.Errorf("plain = %d", got)
	}

	packed := plain + ".zst"
	if err := os.WriteFile(packed, zstdBytes(t, content), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(packed)
	if got := DecodedSize(packed, info); got != int64(len(content)) {
		t.Errorf("compressed = %d, want the frame content size %d", got, len(content))
	}

	// A streamed frame has no content size: the estimate is eight times the file.
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		_, _ = w.Write(content[:1000])
		_ = w.Flush()
	}
	_ = w.Close()
	streamed := filepath.Join(dir, "rollout-b.jsonl.zst")
	if err := os.WriteFile(streamed, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(streamed)
	if got := DecodedSize(streamed, info); got != info.Size()*8 {
		t.Errorf("streamed = %d, want %d", got, info.Size()*8)
	}
}
