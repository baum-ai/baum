package jobs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"unicode/utf8"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/privatefs"
)

// A fix job's transcript is what its agent said and did, kept in
// transcripts/<job id>.jsonl beside the journal, one {"seq":N,...entry} line
// per agent.TranscriptEntry, so the app can show the run live. It is local
// only and masked like the job's summary, but it is the model's own text and
// tool output, so it is bounded and only the latest fixes keep one.
const (
	maxTranscriptText  = 16 << 10
	maxTranscriptTitle = 1 << 10
	// maxTranscriptBytes is where a transcript stops growing.
	maxTranscriptBytes = 8 << 20
	// keepTranscripts is how many fixes' transcripts are kept.
	keepTranscripts = 50
	// redactInput bounds what the redactor reads: far above what is kept.
	redactInput = 256 << 10
	// transcriptPage is the most entries one request returns.
	transcriptPage = 500
)

const transcriptLimitText = "The transcript reached its size limit; later steps are not shown."

// transcriptTarget is where the queue asks the executor to keep the running
// job's transcript, carried by the context. Without one none is kept.
type transcriptTarget struct{ Dir, JobID string }

type transcriptTargetKey struct{}

func transcriptTargetOf(ctx context.Context) (transcriptTarget, bool) {
	target, ok := ctx.Value(transcriptTargetKey{}).(transcriptTarget)
	return target, ok && target.Dir != ""
}

// transcriptLine is a line of the file.
type transcriptLine struct {
	Seq int `json:"seq"`
	agent.TranscriptEntry
}

// transcriptWriter appends entries to one job's file. Writes are synchronous:
// an entry is one small append to a local file, far cheaper than the model
// step it describes, and never dropping or reordering one is worth more than
// a queue and a goroutine. The agent's reader calls it from one goroutine.
type transcriptWriter struct {
	mu       sync.Mutex
	file     *os.File
	redactor *bundle.Redactor
	seq      int
	size     int
	full     bool
}

// openTranscript creates the job's file in dir and removes the transcripts
// beyond the newest keepTranscripts.
func openTranscript(dir, jobID string) (*transcriptWriter, error) {
	if err := privatefs.MkdirAll(dir); err != nil {
		return nil, err
	}
	redactor, err := bundle.NewRedactor()
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(transcriptPath(dir, jobID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	pruneTranscripts(dir, keepTranscripts)
	return &transcriptWriter{file: file, redactor: redactor}, nil
}

// pruneTranscripts removes all but the keep most recently written
// transcripts.
func pruneTranscripts(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type found struct {
		path string
		time int64
	}
	var files []found
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		files = append(files, found{filepath.Join(dir, entry.Name()), info.ModTime().UnixNano()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].time > files[j].time })
	for _, old := range files[min(keep, len(files)):] {
		_ = os.Remove(old.path)
	}
}

// cut shortens s to at most limit bytes at a character boundary, marking the cut.
func cut(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}

func (w *transcriptWriter) mask(ctx context.Context, s string, limit int) string {
	if s == "" {
		return ""
	}
	masked, _, err := w.redactor.RedactString(ctx, cut(s, redactInput))
	if err != nil {
		return "[not shown: it could not be checked for secrets]"
	}
	return cut(masked, limit)
}

// Add appends an entry, masked and cut. It stops at maxTranscriptBytes, with
// one last entry saying so.
func (w *transcriptWriter) Add(ctx context.Context, entry agent.TranscriptEntry) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil || w.full {
		return
	}
	entry.Title = w.mask(ctx, entry.Title, maxTranscriptTitle)
	entry.Text = w.mask(ctx, entry.Text, maxTranscriptText)
	w.seq++
	raw, err := json.Marshal(transcriptLine{w.seq, entry})
	if err != nil {
		return
	}
	if w.size+len(raw) > maxTranscriptBytes {
		w.full = true
		entry = agent.TranscriptEntry{Time: entry.Time, Kind: "error", Text: transcriptLimitText}
		if raw, err = json.Marshal(transcriptLine{w.seq, entry}); err != nil {
			return
		}
	}
	// One write per line, so a reader sees whole lines or a partial last one.
	if _, err := w.file.Write(append(raw, '\n')); err != nil {
		w.full = true
		return
	}
	w.size += len(raw) + 1
}

// Close ends the file. Add does nothing afterwards.
func (w *transcriptWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
}

// transcriptPath is the file of job id in dir.
func transcriptPath(dir, id string) string { return filepath.Join(dir, id+".jsonl") }

// readTranscript returns the entries of the file after seq after, at most
// transcriptPage, and whether more follow. It reads the file afresh and skips
// a last line still being written.
func readTranscript(path string, after int) (lines []transcriptLine, more bool, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64<<10)
	for {
		raw, err := reader.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return lines, more, nil
			}
			return lines, more, err
		}
		var line transcriptLine
		if json.Unmarshal(bytes.TrimSpace(raw), &line) != nil || line.Seq <= after {
			continue
		}
		if len(lines) == transcriptPage {
			return lines, true, nil
		}
		lines = append(lines, line)
	}
}
