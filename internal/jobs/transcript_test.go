package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/privatefs"
)

func TestTranscriptMasksAndCuts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "transcripts")
	w, err := openTranscript(dir, "job")
	if err != nil {
		t.Fatal(err)
	}
	secret := "sk_" + "live_oHBvRPOIvGrv5iFlbCBFNOgmBjMtpsia"
	ctx := context.Background()
	w.Add(ctx, agent.TranscriptEntry{Kind: "tool", ID: "1", Tool: "command", Title: "curl -H 'Authorization: " + secret + "'", Text: "token " + secret})
	w.Add(ctx, agent.TranscriptEntry{Kind: "message", Text: strings.Repeat("é", maxTranscriptText)})
	w.Add(ctx, agent.TranscriptEntry{Kind: "tool", Title: strings.Repeat("a", 2*maxTranscriptTitle)})
	w.Close()
	w.Add(ctx, agent.TranscriptEntry{Kind: "message", Text: "after close"})
	raw, err := os.ReadFile(filepath.Join(dir, "job.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("secret kept: %s", raw)
	}
	lines, more, err := readTranscript(filepath.Join(dir, "job.jsonl"), 0)
	if err != nil || more || len(lines) != 3 {
		t.Fatalf("lines %d more %v err %v", len(lines), more, err)
	}
	for i, line := range lines {
		if line.Seq != i+1 {
			t.Errorf("line %d has seq %d", i, line.Seq)
		}
	}
	if text := lines[1].Text; len(text) > maxTranscriptText+len("…") || !utf8.ValidString(text) || !strings.HasSuffix(text, "…") {
		t.Errorf("text of %d bytes, valid %v", len(text), utf8.ValidString(text))
	}
	if title := lines[2].Title; len(title) != maxTranscriptTitle+len("…") {
		t.Errorf("title of %d bytes", len(title))
	}
	info, _ := os.Stat(filepath.Join(dir, "job.jsonl"))
	if !privatefs.PrivateFile(filepath.Join(dir, "job.jsonl"), info) {
		t.Errorf("transcript is not private: %v", info.Mode())
	}
}

func TestTranscriptStopsAtItsSizeLimit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "transcripts")
	w, err := openTranscript(dir, "job")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	chunk := strings.Repeat("x", maxTranscriptText)
	for range maxTranscriptBytes/maxTranscriptText + 10 {
		w.Add(context.Background(), agent.TranscriptEntry{Kind: "message", Text: chunk})
	}
	path := filepath.Join(dir, "job.jsonl")
	info, _ := os.Stat(path)
	if info.Size() > maxTranscriptBytes+1<<10 {
		t.Fatalf("size %d", info.Size())
	}
	var all []transcriptLine
	for after := 0; ; {
		lines, more, err := readTranscript(path, after)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, lines...)
		if !more {
			break
		}
		after = lines[len(lines)-1].Seq
	}
	last := all[len(all)-1]
	if last.Kind != "error" || last.Text != transcriptLimitText || last.Seq != len(all) {
		t.Fatalf("last entry %+v of %d", last, len(all))
	}
}

func TestTranscriptKeepsTheNewestFifty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "transcripts")
	now := time.Now()
	for i := range 55 {
		if err := privatefs.MkdirAll(dir); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, fmt.Sprintf("old%02d.jsonl", i))
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, now, now.Add(time.Duration(i-100)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	w, err := openTranscript(dir, "new")
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	entries, _ := os.ReadDir(dir)
	if len(entries) != keepTranscripts {
		t.Fatalf("%d files kept", len(entries))
	}
	for _, name := range []string{"new.jsonl", "old54.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s removed", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "old00.jsonl")); err == nil {
		t.Error("the oldest was kept")
	}
}

func TestHTTPTranscript(t *testing.T) {
	q, _ := newQueue(t)
	w, err := openTranscript(q.transcripts, "fixjob")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for i := range transcriptPage + 2 {
		w.Add(context.Background(), agent.TranscriptEntry{Kind: "message", Text: fmt.Sprint("step ", i+1)})
	}
	// A line still being written is skipped.
	if _, err := w.file.WriteString(`{"seq":503,"kind":"mess`); err != nil {
		t.Fatal(err)
	}
	q.mu.Lock()
	q.jobs = append(q.jobs, Job{ID: "fixjob", State: "running", HasTranscript: true}, Job{ID: "plain", State: "completed"}, Job{ID: "gone", State: "completed", HasTranscript: true})
	q.mu.Unlock()
	handler := q.Handler()
	get := func(path string) (int, struct {
		Entries []transcriptLine `json:"entries"`
		Next    int              `json:"next"`
		More    bool             `json:"more"`
		Done    bool             `json:"done"`
	}) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		var answer struct {
			Entries []transcriptLine `json:"entries"`
			Next    int              `json:"next"`
			More    bool             `json:"more"`
			Done    bool             `json:"done"`
		}
		if recorder.Code == 200 {
			if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
				t.Fatal(err)
			}
		}
		return recorder.Code, answer
	}
	code, first := get("/api/jobs/fixjob/transcript")
	if code != 200 || len(first.Entries) != transcriptPage || first.Next != transcriptPage || !first.More || first.Done {
		t.Fatalf("first page: %d entries, next %d, more %v, done %v", len(first.Entries), first.Next, first.More, first.Done)
	}
	if first.Entries[0].Seq != 1 || first.Entries[0].Text != "step 1" || first.Entries[0].Kind != "message" {
		t.Errorf("first entry %+v", first.Entries[0])
	}
	_, second := get("/api/jobs/fixjob/transcript?after=500")
	if len(second.Entries) != 2 || second.Next != 502 || second.More {
		t.Fatalf("second page: %+v", second)
	}
	_, none := get("/api/jobs/fixjob/transcript?after=502")
	if len(none.Entries) != 0 || none.Next != 502 || none.More {
		t.Fatalf("nothing new: %+v", none)
	}
	q.mu.Lock()
	q.jobs[len(q.jobs)-3].State = "completed"
	q.mu.Unlock()
	if _, done := get("/api/jobs/fixjob/transcript?after=502"); !done.Done {
		t.Error("a finished job is not done")
	}
	for _, path := range []string{"/api/jobs/plain/transcript", "/api/jobs/missing/transcript", "/api/jobs/gone/transcript"} {
		if code, _ := get(path); code != 404 {
			t.Errorf("%s: %d", path, code)
		}
	}
	if code, _ := get("/api/jobs/fixjob/transcript?after=x"); code != 400 {
		t.Errorf("bad after: %d", code)
	}
}
