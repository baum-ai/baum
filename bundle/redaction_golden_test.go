package bundle

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/goredact"

	"github.com/baum-ai/baum/internal/goldentest"
	"github.com/baum-ai/baum/redact"
)

// corpusBundle places text in the named bundle field of an otherwise empty
// bundle, and returns a getter for the field after redaction.
func corpusBundle(t *testing.T, field, text string) (*Bundle, func() string) {
	t.Helper()
	b := &Bundle{}
	switch field {
	case "pull_request.title":
		b.PullRequest.Title = text
		return b, func() string { return b.PullRequest.Title }
	case "pull_request.body":
		b.PullRequest.Body = text
		return b, func() string { return b.PullRequest.Body }
	case "pull_request.head_ref":
		b.PullRequest.HeadRef = text
		return b, func() string { return b.PullRequest.HeadRef }
	case "commit.subject":
		b.PullRequest.Commits = []Commit{{SHA: "c1", Subject: text}}
		return b, func() string { return b.PullRequest.Commits[0].Subject }
	case "check.url":
		b.Checks = []Check{{ID: "k1", Name: "build", URL: text}}
		return b, func() string { return b.Checks[0].URL }
	case "conversation.body":
		b.Conversation = []ConversationItem{{ID: "cm1", Kind: "comment", AuthorLogin: "octocat", Body: text}}
		return b, func() string { return b.Conversation[0].Body }
	case "diff.patch":
		b.Diff = []Hunk{{ID: testHunk, Path: "main.go", Patch: text}}
		return b, func() string { return b.Diff[0].Patch }
	case "session.title":
		b.Sessions = []Session{{RootSessionID: "s1", Title: text}}
		return b, func() string { return b.Sessions[0].Title }
	case "session.prompt":
		b.Sessions = []Session{{RootSessionID: "s1", Prompts: []Prompt{{ID: "p1", Text: text}}}}
		return b, func() string { return b.Sessions[0].Prompts[0].Text }
	case "session.reply":
		b.Sessions = []Session{{RootSessionID: "s1", Prompts: []Prompt{{ID: "p1", Text: "go", Reply: text}}}}
		return b, func() string { return b.Sessions[0].Prompts[0].Reply }
	case "command.target":
		b.Sessions = []Session{{RootSessionID: "s1", CommandEvents: []CommandEvent{{ID: "e1", Commands: []CommandEntry{{Program: "sh", Category: "other", Target: text}}}}}}
		return b, func() string { return b.Sessions[0].CommandEvents[0].Commands[0].Target }
	case "reference.value":
		b.References = []Reference{{Kind: "authorship_disclosure", Value: text, Source: "body"}}
		return b, func() string { return b.References[0].Value }
	case "attachment.alt":
		b.Attachments = []Attachment{{ID: "a1", Alt: text}}
		return b, func() string { return b.Attachments[0].Alt }
	}
	t.Fatalf("unknown corpus field %q", field)
	return nil, nil
}

// span is a half-open byte range of the input that redaction masked.
type span struct{ start, end int }

// mergeSpans sorts spans and joins the ones that overlap or touch.
func mergeSpans(spans []span) []span {
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var merged []span
	for _, s := range spans {
		if n := len(merged); n > 0 && s.start <= merged[n-1].end {
			merged[n-1].end = max(merged[n-1].end, s.end)
			continue
		}
		merged = append(merged, s)
	}
	return merged
}

func formatSpans(spans []span) string {
	if len(spans) == 0 {
		return "none"
	}
	parts := make([]string, len(spans))
	for i, s := range spans {
		parts[i] = fmt.Sprintf("[%d,%d)", s.start, s.end)
	}
	return strings.Join(parts, " ")
}

// spanRecorder is an engine with the redact package's configuration that
// reports where each finding was, which the bundle API does not expose.
type spanRecorder struct {
	mu       sync.Mutex
	engine   *goredact.Engine
	findings []span
}

func newSpanRecorder(t *testing.T) *spanRecorder {
	t.Helper()
	r := &spanRecorder{}
	cfg := redact.Config
	cfg.OnFinding = func(f goredact.Finding) {
		r.findings = append(r.findings, span{int(f.Start), int(f.End)})
	}
	engine, err := goredact.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.engine = engine
	return r
}

// maskedSpans returns the merged input byte spans redaction masks in text,
// which the bundle redacts as one text whatever the field, and the number
// of findings, which is the masked-span count.
func (r *spanRecorder) maskedSpans(t *testing.T, text string) ([]span, int) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.findings = nil
	if _, err := r.engine.Redact(context.Background(), io.Discard, strings.NewReader(text)); err != nil {
		t.Fatal(err)
	}
	return mergeSpans(append([]span(nil), r.findings...)), len(r.findings)
}

func parseSpans(t *testing.T, s string) []span {
	t.Helper()
	if s == "none" {
		return nil
	}
	var spans []span
	for _, field := range strings.Fields(s) {
		var sp span
		if _, err := fmt.Sscanf(field, "[%d,%d)", &sp.start, &sp.end); err != nil {
			t.Fatalf("parse span %q: %v", field, err)
		}
		spans = append(spans, sp)
	}
	return spans
}

// formatMask is goredact's format-preserving mask of one byte: a masked
// byte is always this, an unmasked one the input byte.
func formatMask(b byte) byte {
	switch {
	case b >= 'A' && b <= 'Z':
		return 'X'
	case b >= 'a' && b <= 'z':
		return 'x'
	case b >= '0' && b <= '9':
		return '9'
	case strings.IndexByte("-_.:/+=@", b) >= 0:
		return b
	}
	return '*'
}

// covered reports whether every byte of inner lies in one of outer's spans.
func covered(inner, outer []span) bool {
	for _, in := range inner {
		for pos := in.start; pos < in.end; {
			i := sort.Search(len(outer), func(i int) bool { return outer[i].end > pos })
			if i == len(outer) || outer[i].start > pos {
				return false
			}
			pos = outer[i].end
		}
	}
	return true
}

// TestRedactionGoldenCorpus runs bundle redaction over every corpus input
// and compares the masked text, masked-span count and masked byte spans with
// testdata/redaction. Regenerate with -update and review the diff; a change
// that masks less also fails TestRedactionMasksWhatItMaskedBefore.
func TestRedactionGoldenCorpus(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	recorder := newSpanRecorder(t)
	for _, item := range redactionCorpus {
		t.Run(item.name, func(t *testing.T) {
			b, get := corpusBundle(t, item.field, item.text)
			if err := Redact(context.Background(), b, redactor); err != nil {
				t.Fatal(err)
			}
			spans, findings := recorder.maskedSpans(t, item.text)
			if findings != b.MaskedSpanCount {
				t.Fatalf("span recorder found %d findings, bundle counted %d", findings, b.MaskedSpanCount)
			}
			// Redaction rewrites the masked spans and nothing else.
			out := get()
			if len(out) != len(item.text) {
				t.Fatalf("redaction changed the length: %d -> %d", len(item.text), len(out))
			}
			for i := range len(out) {
				masked := covered([]span{{i, i + 1}}, spans)
				if !masked && out[i] != item.text[i] || item.text[i] == '\n' && out[i] != '\n' {
					t.Fatalf("byte %d changed outside the masked spans or a newline was lost: %q", i, out)
				}
			}
			got := fmt.Sprintf("field: %s\nmasked_spans: %d\nmasked_bytes: %s\n-- output --\n%s\n", item.field, b.MaskedSpanCount, formatSpans(spans), get())
			goldentest.Text(t, filepath.Join("testdata", "redaction", item.name+".golden"), got)
		})
	}
}

// TestRedactionMasksWhatItMaskedBefore proves that moving bundle redaction
// onto the redact package's engine unmasked nothing: every input byte the
// previous bundle redaction masked (its own goredact setup over patch
// lines, then e-mail replacement) is masked now, both in the engine's
// findings and in the bundle's final output. The previous spans were
// recorded with that implementation and are kept in
// testdata/redaction/legacy-masked-bytes.txt; they are not regenerated.
func TestRedactionMasksWhatItMaskedBefore(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "redaction", "legacy-masked-bytes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	legacy := map[string][]span{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		name, spans, ok := strings.Cut(line, ": ")
		if !ok {
			t.Fatalf("malformed line %q", line)
		}
		legacy[name] = parseSpans(t, spans)
	}
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	recorder := newSpanRecorder(t)
	for _, item := range redactionCorpus {
		before, ok := legacy[item.name]
		if !ok {
			t.Errorf("%s has no legacy spans", item.name)
			continue
		}
		delete(legacy, item.name)
		now, _ := recorder.maskedSpans(t, item.text)
		if !covered(before, now) {
			t.Errorf("%s: previously masked %s, engine findings now only %s", item.name, formatSpans(before), formatSpans(now))
		}
		// The bundle's own output, after every pass over it, has each of
		// those bytes masked: replaced by its format-preserving mask. A
		// newline is the exception: it is kept, where the old output had
		// it masked inside a multiline key.
		b, get := corpusBundle(t, item.field, item.text)
		if err := Redact(context.Background(), b, redactor); err != nil {
			t.Fatal(err)
		}
		out := get()
		for _, sp := range before {
			for i := sp.start; i < sp.end; i++ {
				if item.text[i] == '\n' {
					continue
				}
				if i >= len(out) || out[i] != formatMask(item.text[i]) {
					t.Errorf("%s: byte %d was masked before and is %q in the bundle output", item.name, i, out[min(i, len(out)-1)])
					break
				}
			}
		}
	}
	for name := range legacy {
		t.Errorf("legacy spans for %q have no corpus item", name)
	}
}
