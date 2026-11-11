package locallink

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/locallink/linktest"
	"github.com/baum-ai/baum/internal/workstats"
)

func counter(name string) int64 { return workstats.Take().Counters[name] }

// useEvidenceMemo gives the test its own memo.
func useEvidenceMemo(t *testing.T) *evidenceCache {
	t.Helper()
	previous, previousFrozen := evidenceMemo, frozenMemo
	evidenceMemo, frozenMemo = newEvidenceCache(EvidenceMaxAge), newEvidenceCache(0)
	t.Cleanup(func() { evidenceMemo, frozenMemo = previous, previousFrozen })
	return evidenceMemo
}

func TestBuildSessionEvidenceReusesAnUnchangedAnswer(t *testing.T) {
	useEvidenceMemo(t)
	f := linktest.New(t)
	req := evidenceRequest(t, f)
	first := buildEvidence(t, req)
	if len(first) != 1 {
		t.Fatalf("sessions = %+v", first)
	}

	loads := counter("sessions.load.calls")
	second := buildEvidence(t, req)
	if got := counter("sessions.load.calls") - loads; got != 0 {
		t.Errorf("an unchanged pull request loaded sessions %d times, want 0", got)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("reused evidence differs:\n%+v\n%+v", first, second)
	}

	// Callers cap and redact what they get in place: that must not reach
	// the next caller.
	second[0].Prompts[0].Text = "changed by a caller"
	second[0].Models[0] = "changed"
	third := buildEvidence(t, req)
	if third[0].Prompts[0].Text != "add the feature" || third[0].Models[0] != "claude-opus-5" {
		t.Errorf("a caller's change leaked into the memo: %+v", third[0])
	}

	// A transcript that grew is read again.
	transcripts, err := filepath.Glob(filepath.Join(f.Store.ClaudeProjectsDir, "*", "*.jsonl"))
	if err != nil || len(transcripts) == 0 {
		t.Fatalf("transcripts = %v, %v", transcripts, err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(transcripts[0], later, later); err != nil {
		t.Fatal(err)
	}
	loads = counter("sessions.load.calls")
	buildEvidence(t, req)
	if got := counter("sessions.load.calls") - loads; got != 1 {
		t.Errorf("after a transcript changed: %d loads, want 1", got)
	}

	// An index rewrite, as "git status" in an editor does, changes nothing.
	buildEvidence(t, req)
	index := filepath.Join(f.Repo, ".git", "index")
	if err := os.Chtimes(index, later, later); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(f.Repo, ".git"), later, later); err != nil {
		t.Fatal(err)
	}
	loads = counter("sessions.load.calls")
	buildEvidence(t, req)
	if got := counter("sessions.load.calls") - loads; got != 0 {
		t.Errorf("after an index rewrite: %d loads, want 0", got)
	}

	// So is the evidence of a checkout git changed (a checkout, a commit).
	cmd := exec.Command("git", "-C", f.Repo, "checkout", "-q", "--detach")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout: %v\n%s", err, out)
	}
	loads = counter("sessions.load.calls")
	buildEvidence(t, req)
	if got := counter("sessions.load.calls") - loads; got != 1 {
		t.Errorf("after a checkout: %d loads, want 1", got)
	}

	// Other caps are another answer: the linked session's evidence is built
	// again, which loads a second time to map it. (Its transcript is first
	// settled and read, so that it has a digest to reuse.)
	rebuild(t, req)
	other := req
	other.MaxPromptsPerSession = 1
	loads = counter("sessions.load.calls")
	buildEvidence(t, other)
	if got := counter("sessions.load.calls") - loads; got != 2 {
		t.Errorf("other caps: %d loads, want 2", got)
	}
}

func TestFrozenEvidenceIsReadOnce(t *testing.T) {
	useEvidenceMemo(t)
	f := linktest.New(t)
	req := evidenceRequest(t, f)
	req.Frozen = true
	first := buildEvidence(t, req)
	if len(first) != 1 {
		t.Fatalf("sessions = %+v", first)
	}
	// A frozen answer stands even when the stores change and the ordinary
	// answer expired.
	transcripts, err := filepath.Glob(filepath.Join(f.Store.ClaudeProjectsDir, "*", "*.jsonl"))
	if err != nil || len(transcripts) == 0 {
		t.Fatalf("transcripts = %v, %v", transcripts, err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(transcripts[0], later, later); err != nil {
		t.Fatal(err)
	}
	evidenceMemo = newEvidenceCache(EvidenceMaxAge)
	loads, frozen := counter("sessions.load.calls"), counter("locallink.session_evidence_frozen")
	second := buildEvidence(t, req)
	if got := counter("sessions.load.calls") - loads; got != 0 {
		t.Errorf("a frozen pull request loaded sessions %d times", got)
	}
	if counter("locallink.session_evidence_frozen") != frozen+1 || !reflect.DeepEqual(first, second) {
		t.Errorf("the frozen answer was not reused: %+v", second)
	}
	// An ordinary request still reads the changed store.
	req.Frozen = false
	loads = counter("sessions.load.calls")
	buildEvidence(t, req)
	if got := counter("sessions.load.calls") - loads; got != 1 {
		t.Errorf("an open pull request: %d loads, want 1", got)
	}
}

func TestEvidenceMemoExpires(t *testing.T) {
	c := newEvidenceCache(EvidenceMaxAge)
	now := time.Now()
	c.now = func() time.Time { return now }
	c.put("k", []bundle.Session{{RootSessionID: "s"}})
	if _, ok := c.get("k"); !ok {
		t.Fatal("a fresh answer was not kept")
	}
	now = now.Add(EvidenceMaxAge)
	if _, ok := c.get("k"); ok {
		t.Error("an answer older than EvidenceMaxAge was reused")
	}
	for i := range maxEvidenceEntries + 5 {
		c.put(string(rune('a'+i%26))+string(rune(i)), nil)
	}
	if c.order.Len() != maxEvidenceEntries {
		t.Errorf("entries = %d, want at most %d", c.order.Len(), maxEvidenceEntries)
	}
}

func TestDeepCopyKeepsNilAndEmptyApart(t *testing.T) {
	at := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	decisions := []bundle.SessionDecision{}
	in := []bundle.Session{{
		RootSessionID: "s", Models: []string{}, StartedAt: &at, Confidence: 0.9, HasStartedAt: true, StartedAtValue: at,
		Decisions: &decisions,
		Prompts:   []bundle.Prompt{{Text: "p"}},
	}, {RootSessionID: "t"}}
	out := deepCopy(in)
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("copy differs:\n%#v\n%#v", in, out)
	}
	if out[0].Models == nil || out[1].Models != nil || out[0].Decisions == nil || *out[0].Decisions == nil {
		t.Errorf("nil and empty were mixed up: %#v", out)
	}
	*out[0].StartedAt = at.Add(time.Hour)
	out[0].Prompts[0].Text = "changed"
	if !in[0].StartedAt.Equal(at) || in[0].Prompts[0].Text != "p" {
		t.Error("the copy shares memory with the original")
	}
}
