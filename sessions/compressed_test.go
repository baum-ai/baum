package sessions

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/baum-ai/baum/sessions/codextranscript"
	"github.com/baum-ai/baum/sessions/collector"
	collectcodex "github.com/baum-ai/baum/sessions/collector/codex"
	"github.com/baum-ai/baum/sessions/collector/model"
	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/vendors"
)

// zstdFile writes data to path compressed the way Codex compresses an old
// rollout.
func zstdFile(t *testing.T, path string, data []byte) {
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
	write(t, path, buf.String())
}

const compressedRolloutName = "rollout-2026-06-20T08-00-00-019e9999-1111-7000-8000-aaaabbbbcccc.jsonl"

func compressedLifecycle(t *testing.T) (all [][]byte, plain, packed collector.Session) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("codexsession", "testdata", "lifecycle", "basic_rollout.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	all = lines(data)
	dir := t.TempDir()
	plainPath := filepath.Join(dir, "plain", compressedRolloutName)
	packedPath := filepath.Join(dir, "packed", compressedRolloutName+".zst")
	write(t, plainPath, string(data))
	zstdFile(t, packedPath, data)
	file := func(path string) collector.Session {
		return collector.Session{Source: vendors.Codex, Files: []model.SessionFile{{Path: path, Name: compressedRolloutName, Kind: model.KindTranscript}}}
	}
	return all, file(plainPath), file(packedPath)
}

// A compressed rollout is decoded whole once, and again only after it changed.
func TestMapDecodesACompressedRolloutOnlyWhenItChanged(t *testing.T) {
	c := useTails(t, DefaultTailCacheBytes)
	all, plain, packed := compressedLifecycle(t)
	path := packed.Files[0].Path

	first, _, err := mapSessionWith(packed, tails)
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := mapSessionWith(plain, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatal("the graph of the compressed rollout differs from the plain one's")
	}
	decoded := c.counts()
	if decoded.full != 1 || decoded.appended != 0 {
		t.Fatalf("first read = %+v, want one whole read", decoded)
	}

	again, _, err := mapSessionWith(packed, tails)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, want) {
		t.Fatal("the unchanged rollout maps differently the second time")
	}
	if after := c.counts(); after != decoded || c.reused != 1 {
		t.Fatalf("unchanged read = %+v, %d reused; want nothing decoded after %+v", after, c.reused, decoded)
	}

	// Rewritten with fewer lines and a later modification time.
	zstdFile(t, path, bytes.Join(all[:len(all)-1], nil))
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	shorter := collector.Session{Source: vendors.Codex, Files: []model.SessionFile{{Path: filepath.Join(t.TempDir(), compressedRolloutName), Name: compressedRolloutName, Kind: model.KindTranscript}}}
	write(t, shorter.Files[0].Path, string(bytes.Join(all[:len(all)-1], nil)))
	got, _, err := mapSessionWith(packed, tails)
	if err != nil {
		t.Fatal(err)
	}
	wantShorter, _, err := mapSessionWith(shorter, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, wantShorter) || reflect.DeepEqual(got, want) {
		t.Fatal("the rewritten rollout did not map to its new content")
	}
	if after := c.counts(); after.full != 2 || after.appended != 0 {
		t.Fatalf("rewritten read = %+v, want a second whole read", after)
	}
	if c.len() != 1 || c.covered <= 0 {
		t.Fatalf("entries %d, covered %d", c.len(), c.covered)
	}
}

// With the cache off a compressed rollout is decoded whole each time.
func TestMapReadsACompressedRolloutWithoutTheCache(t *testing.T) {
	useTails(t, 0)
	_, plain, packed := compressedLifecycle(t)
	got, _, err := mapSessionWith(packed, tails)
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := mapSessionWith(plain, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("graphs differ")
	}
}

// The decode budget counts a compressed rollout by its decoded size, and the
// mapping cache key still follows the file on disk.
func TestMapKeyCostsACompressedRolloutByItsDecodedSize(t *testing.T) {
	_, plain, packed := compressedLifecycle(t)
	plainKey, plainCost, ok := mapKey(plain)
	if !ok {
		t.Fatal("no plain key")
	}
	key, cost, ok := mapKey(packed)
	if !ok || cost != plainCost || key == plainKey || !strings.Contains(key, packed.Files[0].Path) {
		t.Fatalf("cost %d, want %d; key %q", cost, plainCost, key)
	}
	if only, ok := mapKeyOnly(packed); !ok || only != key {
		t.Fatalf("key without cost %q, want %q", only, key)
	}
}

// copyStore copies the Codex fixture store into home/sessions, compressing
// every rollout when compress is set.
func copyStore(t *testing.T, home string, compress bool) {
	t.Helper()
	src := filepath.Join("codextranscript", "testdata", "store", "sessions")
	if err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if compress {
			zstdFile(t, filepath.Join(home, "sessions", rel+".zst"), data)
		} else {
			write(t, filepath.Join(home, "sessions", rel), string(data))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func mapStoreRoot(t *testing.T, home string) (collector.Session, *session.Graph) {
	t.Helper()
	index, _, err := collectcodex.ScanStore(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range index {
		if entry.Class != codextranscript.ThreadRoot {
			continue
		}
		files, err := collectcodex.Find(index, entry)
		if err != nil {
			t.Fatal(err)
		}
		s := collector.Session{Source: vendors.Codex, SessionID: entry.Meta.ID, Files: files}
		graph, _, err := mapSessionWith(s, tails)
		if err != nil {
			t.Fatal(err)
		}
		return s, graph
	}
	t.Fatal("the store has no root rollout")
	return collector.Session{}, nil
}

// A store whose rollouts were all compressed gives the graph the plain store
// gives, with the same source refs, and its files keep their plain names.
func TestCompressedCodexStoreMapsAsThePlainStoreDoes(t *testing.T) {
	useTails(t, DefaultTailCacheBytes)
	plainHome, packedHome := t.TempDir(), t.TempDir()
	copyStore(t, plainHome, false)
	copyStore(t, packedHome, true)

	plain, want := mapStoreRoot(t, plainHome)
	packed, got := mapStoreRoot(t, packedHome)
	if len(packed.Files) != len(plain.Files) || len(plain.Files) < 2 {
		t.Fatalf("files: %d plain, %d compressed; want the same, at least 2", len(plain.Files), len(packed.Files))
	}
	for i, f := range packed.Files {
		if !codextranscript.Compressed(f.Path) || f.Name != plain.Files[i].Name || f.Kind != plain.Files[i].Kind {
			t.Fatalf("file %d = %+v, want a compressed file named %s", i, f, plain.Files[i].Name)
		}
	}
	if len(want.VendorIDs) == 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("the compressed store's graph differs from the plain store's (%d vendor ids)", len(want.VendorIDs))
	}
	for _, id := range got.VendorIDs {
		if id.SourceRef != nil && strings.Contains(*id.SourceRef, ".zst") {
			t.Fatalf("source ref %q names the compressed file", *id.SourceRef)
		}
	}
}

// A rollout with both copies is read from the plain one, and is not a
// duplicate thread.
func TestPlainRolloutWinsOverItsCompressedCopy(t *testing.T) {
	useTails(t, DefaultTailCacheBytes)
	home := t.TempDir()
	copyStore(t, home, false)
	_, want := mapStoreRoot(t, home)
	copyStore(t, home, true)

	s, got := mapStoreRoot(t, home)
	for _, f := range s.Files {
		if codextranscript.Compressed(f.Path) {
			t.Fatalf("%s is read although a plain copy exists", f.Path)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("the graph changed when compressed copies appeared")
	}
}
