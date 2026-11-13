package livesessions

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/sessions/codextranscript"
)

// writeZstd writes data to path compressed as Codex compresses an old rollout.
func writeZstd(t *testing.T, path, data string) {
	t.Helper()
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	write(t, path, buf.String())
}

func trackCodex(t *testing.T, f *transcriptFixture, id string) {
	t.Helper()
	e := hookevents.New(hookevents.KindAgent, "codex", "user-prompt-submit", "/work/demo", "test")
	e.Time = f.clock.now
	e.Session = &hookevents.Session{ID: id, Cwd: "/work/demo"}
	if !apply(f.tracker, e) {
		t.Fatal("not tracked")
	}
}

// A session whose only rollout is compressed is found by its id, located,
// sized by what it decodes to, described and its images served.
func TestACompressedCodexRolloutIsFoundDescribedAndItsImagesServed(t *testing.T) {
	f := newTranscriptFixture(t)
	const id = "019f0000-0000-7000-8000-000000000012"
	const turn = "019f0000-0000-7000-8000-0000000000bb"
	url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pixel)
	content := `{"timestamp":"2026-10-30T15:42:21.557Z","ordinal":0,"type":"session_meta","payload":{"session_id":"` + id + `","id":"` + id + `","cwd":"/work/demo","cli_version":"0.166.0","model_provider":"openai","history_mode":"paginated"}}
{"timestamp":"2026-10-30T15:42:37.900Z","ordinal":1,"type":"event_msg","payload":{"type":"task_started","turn_id":"` + turn + `","root_turn_id":"` + turn + `","started_at":1793374957}}
{"timestamp":"2026-10-30T15:42:37.966Z","ordinal":2,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<image name=[Image #1] path=\"/tmp/codex-clipboard.png\">"},{"type":"input_image","image_url":"` + url + `","detail":"high"},{"type":"input_text","text":"</image>"},{"type":"input_text","text":"Rename the flag [Image #1]"}],"internal_chat_message_metadata_passthrough":{"turn_id":"` + turn + `"}}}
{"timestamp":"2026-10-30T15:42:37.970Z","ordinal":3,"type":"event_msg","payload":{"type":"item_completed","thread_id":"` + id + `","turn_id":"` + turn + `","item":{"type":"UserMessage","id":"item-1","content":[{"type":"local_image","path":"/tmp/codex-clipboard.png"},{"type":"text","text":"Rename the flag [Image #1]"}]}}}
{"timestamp":"2026-10-30T15:42:41.000Z","ordinal":4,"type":"event_msg","payload":{"type":"task_complete","turn_id":"` + turn + `","root_turn_id":"` + turn + `","last_agent_message":"I see it.","started_at":1793374957,"completed_at":1793374961}}
`
	path := filepath.Join(f.home, ".codex", "sessions", "2026", "10", "30", "rollout-2026-10-30T15-42-13-"+id+".jsonl.zst")
	writeZstd(t, path, content)
	trackCodex(t, f, id)

	got, err := findRollout(filepath.Join(f.home, ".codex"), id)
	if err != nil || got != path {
		t.Fatalf("found %q, %v", got, err)
	}
	location, err := Locate(f.home, "", AgentCodex, id, "")
	if err != nil || location.Path() != path {
		t.Fatalf("located %q, %v", location.Path(), err)
	}
	if size, err := location.Size(); err != nil || size != int64(len(content)) {
		t.Fatalf("size %d, %v; want the %d decoded bytes", size, err, len(content))
	}
	if title := titles(t, f)["codex/"+id]; !strings.Contains(title, "Rename the flag") {
		t.Fatalf("title %q", title)
	}
	base := "/api/live-sessions/codex/" + id + "/transcript"
	servedImage(t, f.live.Handler(), base, imageAnswer(t, f.live.Handler(), base))
}

// A compressed rollout whose frame does not record its size is described to
// its end, past the estimate of its size: a prompt there still names it.
func TestACompressedRolloutIsDescribedPastItsEstimatedSize(t *testing.T) {
	f := newTranscriptFixture(t)
	const id = "019f0000-0000-7000-8000-000000000014"
	const turn = "019f0000-0000-7000-8000-0000000000cc"
	var b strings.Builder
	b.WriteString(`{"timestamp":"2026-10-30T15:42:21.557Z","ordinal":0,"type":"session_meta","payload":{"session_id":"` + id + `","id":"` + id + `","cwd":"/work/demo","cli_version":"0.166.0","model_provider":"openai","history_mode":"paginated"}}` + "\n")
	for range 20000 {
		b.WriteString(`{"timestamp":"2026-10-30T15:42:30.000Z","type":"event_msg","payload":{"type":"token_count","info":null}}` + "\n")
	}
	b.WriteString(`{"timestamp":"2026-10-30T15:50:00.000Z","type":"event_msg","payload":{"type":"user_message","message":"Rename the late flag"}}` + "\n")
	b.WriteString(`{"timestamp":"2026-10-30T15:50:01.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"` + turn + `"}}` + "\n")
	content := b.String()
	path := filepath.Join(f.home, ".codex", "sessions", "2026", "10", "30", "rollout-2026-10-30T15-42-13-"+id+".jsonl.zst")
	writeZstd(t, path, content)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if estimate := codextranscript.DecodedSize(path, info); estimate >= int64(len(content)) {
		t.Fatalf("estimated %d of %d bytes: the frame records its size or compresses too little to test this", estimate, len(content))
	}
	trackCodex(t, f, id)
	if title := titles(t, f)["codex/"+id]; !strings.Contains(title, "Rename the late flag") {
		t.Fatalf("title %q, want the prompt past the estimated size", title)
	}
}

// A compressed rollout is read by decoded offset: lines before from are
// skipped, and a read that has nothing to read does not open the file.
func TestEachLineReadsACompressedRolloutByDecodedOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-2026-10-30T15-42-13-x.jsonl.zst")
	lines := []string{`{"a":1}`, `{"b":2}`, `{"c":3}`}
	writeZstd(t, path, strings.Join(lines, "\n")+"\n")
	from := int64(len(lines[0]) + 1)
	total := int64(len(strings.Join(lines, "\n")) + 1)

	var got []string
	var scan scanState
	err := eachLine(path, from, total, &scan, func(line []byte, _ bool) { got = append(got, string(line)) })
	if err != nil || strings.Join(got, "|") != lines[1]+"|"+lines[2] || scan.offset != total {
		t.Fatalf("lines %q, offset %d of %d, %v", got, scan.offset, total, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	scan = scanState{}
	if err := eachLine(path, total, total, &scan, func([]byte, bool) { t.Fatal("a line") }); err != nil || scan.offset != total {
		t.Fatalf("nothing to read: offset %d, %v", scan.offset, err)
	}
}

// A plain rollout is preferred to a compressed copy beside it, and a corrupt
// copy does not stop the lookup.
func TestAPlainRolloutIsFoundBeforeItsCompressedCopy(t *testing.T) {
	home := t.TempDir()
	const id = "019f0000-0000-7000-8000-000000000013"
	day := filepath.Join(home, ".codex", "sessions", "2026", "10", "20")
	name := "rollout-2026-10-20T09-00-00-" + id + ".jsonl"
	meta := `{"timestamp":"2026-10-20T09:00:00.000Z","type":"session_meta","payload":{"id":"` + id + `","cwd":"/work/demo","source":"cli"}}` + "\n"
	write(t, filepath.Join(day, name), meta)
	write(t, filepath.Join(day, name+".zst"), "not zstd")
	got, err := findRollout(filepath.Join(home, ".codex"), id)
	if err != nil || got != filepath.Join(day, name) {
		t.Fatalf("found %q, %v", got, err)
	}
	if !codextranscript.IsRolloutName(filepath.Base(got)) {
		t.Fatal("not a rollout name")
	}
}

// A path the hooks recorded before Codex compressed the rollout leads to the
// compressed copy once the plain file is gone.
func TestARecordedRolloutPathFollowsItsCompressedCopy(t *testing.T) {
	plain := filepath.Join(t.TempDir(), "rollout-2026-07-01T10-00-00-id.jsonl")
	if got := compressedSince(plain); got != plain {
		t.Fatalf("nothing there: %s, want the path as given", got)
	}
	writeZstd(t, plain+".zst", "{}\n")
	if got := compressedSince(plain); got != plain+".zst" {
		t.Fatalf("compressed since: %s, want the copy", got)
	}
	write(t, plain, "{}\n")
	if got := compressedSince(plain); got != plain {
		t.Fatalf("plain still there: %s, want the plain rollout", got)
	}
}
