package sessions

import (
	"bytes"
	"encoding/hex"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/baum-ai/baum/sessions/codextranscript"
	"github.com/baum-ai/baum/sessions/collector"
	"github.com/baum-ai/baum/sessions/collector/model"
	"github.com/baum-ai/baum/sessions/vendors"
)

// A compressed rollout whose zstd stream breaks part way maps as a plain
// rollout cut off at that point does: the records before are kept and the
// rest is a torn final line, not an error for the whole session.
func TestCompressedRolloutCutPartWayMapsLikeATruncatedOne(t *testing.T) {
	all, _, _ := compressedLifecycle(t)
	filler := make([]byte, 300<<10)
	rand.New(rand.NewSource(1)).Read(filler)
	big := `{"timestamp":"2026-06-20T08:00:00Z","type":"event_msg","payload":{"type":"agent_message","message":"` + hex.EncodeToString(filler) + `"}}` + "\n"
	data := append(bytes.Join(all, nil), big...)

	dir := t.TempDir()
	path := filepath.Join(dir, compressedRolloutName+".zst")
	zstdFile(t, path, data)
	whole, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, whole[:len(whole)*6/10], 0o644); err != nil {
		t.Fatal(err)
	}

	file, err := codextranscript.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Lines) != len(all) || !file.TornFinalLine {
		t.Fatalf("%d lines, torn %v; want the %d lines before the cut and a torn final line", len(file.Lines), file.TornFinalLine, len(all))
	}

	s := collector.Session{Source: vendors.Codex, Files: []model.SessionFile{{Path: path, Name: compressedRolloutName, Kind: model.KindTranscript}}}
	want, _, err := mapSessionWith(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	useTails(t, DefaultTailCacheBytes)
	got, _, err := mapSessionWith(s, tails)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("the tail cache and a whole read map the cut rollout differently")
	}
}
