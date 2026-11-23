package locallink

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/locallink/linktest"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/session"
)

func TestEvidenceBasisProjectsExactlyAcrossContexts(t *testing.T) {
	s := evidenceTree()
	s.Graph.AssistantMessages = append(s.Graph.AssistantMessages, session.AssistantMessage{ID: "zz", UserInputID: "u-1", Text: text("Tie-winning reply"), Timestamp: minute(2)})
	s.Graph.ToolResults = append(s.Graph.ToolResults, session.ToolResult{ToolUseID: "e-1", Status: session.StatusError})
	// None of these large bodies is read by sessionEvidenceWith.
	huge := strings.Repeat("IRRELEVANT-OUTPUT", 1<<18)
	s.Graph.ToolResults[0].OutputText = &huge
	s.Graph.ToolResults[0].Output = json.RawMessage(`"` + huge + `"`)
	s.Graph.AssistantMessages[0].Content = json.RawMessage(`"` + huge + `"`)
	s.Graph.FileTouches[0].Hunks = []session.Hunk{{Lines: []string{huge}}}
	s.Graph.UserInputs[0].Metadata = json.RawMessage(`"` + huge + `"`)
	basis, cost := newEvidenceBasis(s.Graph, maxEvidenceBasisBytes)
	if basis == nil || cost > maxEvidenceBasisBytes || cost != estimateCost(reflect.ValueOf(basis)) {
		t.Fatalf("basis cost %d", cost)
	}
	encoded, _ := json.Marshal(basis)
	if strings.Contains(string(encoded), "IRRELEVANT-OUTPUT") || int64(len(encoded)) > cost {
		t.Fatal("basis retained irrelevant graph data or undercounted bytes")
	}
	for _, roots := range [][]string{nil, {"/home/dev/widgets"}, {"/home/dev/widgets", "/home/dev/widgets/web"}} {
		for _, known := range []bool{false, true} {
			for _, prompts := range []int{0, 1, 2, 4, 10} {
				for _, commands := range []int{0, 1, 2, 20} {
					thin := s
					thin.Graph = basis
					predicate := func(string) bool { return known }
					got, ok := sessionEvidence(thin, .9, roots, predicate, prompts, commands)
					want, wantOK := sessionEvidence(s, .9, roots, predicate, prompts, commands)
					if ok != wantOK {
						t.Fatal("availability differs")
					}
					sameEvidence(t, fmt.Sprint(roots, known, prompts, commands), []bundle.Session{got}, []bundle.Session{want})
				}
			}
		}
	}
	// The owned basis must share no mutable data or backing strings with the
	// graph. Changing all input classes afterward cannot change its answer.
	thin := s
	thin.Graph = basis
	before, _ := sessionEvidence(thin, 1, []string{"/home/dev/widgets"}, func(string) bool { return true }, 5, 5)
	*s.Graph.Sessions[0].Title = "changed"
	s.Graph.UserInputs[0].Text = "changed"
	*s.Graph.AssistantMessages[1].Text = "changed"
	*s.Graph.ModelInvocations[0].Model = "changed"
	s.Graph.ToolUses[2].Input[0] = 'x'
	s.Graph.ToolResults[0].Status = session.StatusError
	s.Graph.FileTouches[0].Path = "changed"
	after, _ := sessionEvidence(thin, 1, []string{"/home/dev/widgets"}, func(string) bool { return true }, 5, 5)
	sameEvidence(t, "detached", []bundle.Session{after}, []bundle.Session{before})
}

func TestEvidenceBasisPreservesTextSelectionAtScanBoundary(t *testing.T) {
	for _, prefix := range []string{strings.Repeat(" ", bundle.PromptScanChars), strings.Repeat("界", bundle.PromptScanChars), strings.Repeat("\u2003", bundle.PromptScanChars)} {
		s := evidenceTree()
		s.Graph.UserInputs[0].Text = prefix + "nonblank suffix"
		s.Graph.AssistantMessages[1].Text = text(prefix + "nonblank reply")
		// Two equally dated replies use the lexicographically later id.
		s.Graph.AssistantMessages = append(s.Graph.AssistantMessages, session.AssistantMessage{ID: "zz", UserInputID: "u-1", Text: text(prefix + "chosen"), Timestamp: minute(2)})
		basis, _ := newEvidenceBasis(s.Graph, 16<<20)
		if basis == nil {
			t.Fatal("test basis did not fit")
		}
		thin := s
		thin.Graph = basis
		got, _ := sessionEvidence(thin, 1, nil, nil, 1, 0)
		want, _ := sessionEvidence(s, 1, nil, nil, 1, 0)
		sameEvidence(t, "unicode/blank prefix", []bundle.Session{got}, []bundle.Session{want})
	}
}

func TestEvidenceBasisThreadNamesAndProjectionEviction(t *testing.T) {
	s := evidenceTree()
	s.Key = "thread-key"
	s.Source, s.Vendor = "codex", "codex"
	home := t.TempDir()
	s.Path = filepath.Join(home, "sessions", "2026", "rollout.jsonl")
	s.Graph.Sessions[0].Title = nil
	s.Graph.VendorIDs = []session.VendorID{{OurID: "s-root", VendorIDKind: "thread_id", VendorID: "thread-1"}}
	cache := newDigestCache(DefaultDigestBytes)
	first := newDigestBook(cache)
	first.evidence(s, 1, []string{"/home/dev/widgets"}, func(string) bool { return false }, 5, 5)
	for i := 0; i < maxProjections+4; i++ {
		name := fmt.Sprintf("current thread name %d", i)
		if err := os.WriteFile(filepath.Join(home, "session_index.jsonl"), []byte(fmt.Sprintf("{\"id\":\"thread-1\",\"thread_name\":%q}\n", name)), 0600); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(filepath.Join(home, "session_index.jsonl"), at, at); err != nil {
			t.Fatal(err)
		}
		book := newDigestBook(cache)
		if _, ok := book.reuse(s.Key); !ok {
			t.Fatal("facts missing")
		}
		thin := s
		thin.Graph = nil
		thin.Unmapped = true
		got, ok, needs := book.evidence(thin, 1, []string{"/home/dev/widgets"}, func(string) bool { return i%2 == 0 }, i+1, i+1)
		want, wantOK := sessionEvidence(s, 1, []string{"/home/dev/widgets"}, func(string) bool { return i%2 == 0 }, i+1, i+1)
		if needs || ok != wantOK || got.Title != name {
			t.Fatalf("thread/caps projection stale or requested graph: %q", got.Title)
		}
		sameEvidence(t, "thread name/caps", []bundle.Session{got}, []bundle.Session{want})
	}
	if err := os.Remove(filepath.Join(home, "session_index.jsonl")); err != nil {
		t.Fatal(err)
	}
	book := newDigestBook(cache)
	book.reuse(s.Key)
	thin := s
	thin.Graph = nil
	thin.Unmapped = true
	got, _, needs := book.evidence(thin, 1, []string{"/home/dev/widgets"}, func(string) bool { return false }, 5, 5)
	want, _ := sessionEvidence(s, 1, []string{"/home/dev/widgets"}, func(string) bool { return false }, 5, 5)
	if needs {
		t.Fatal("thread index disappearance requested graph")
	}
	sameEvidence(t, "missing thread name", []bundle.Session{got}, []bundle.Session{want})
	if cache.held > cache.limit {
		t.Fatal("cache budget exceeded")
	}
}

func TestEvidenceBasisPrioritizesFactsAndRefillsHotBasis(t *testing.T) {
	s := evidenceTree()
	basis, cost := newEvidenceBasis(s.Graph, maxEvidenceBasisBytes)
	cache := newDigestCache(cost + 1000)
	a := &sessionDigest{key: "a", facts: sessionFacts{root: true}, cost: 200}
	b := &sessionDigest{key: "b", facts: sessionFacts{root: true}, cost: 300}
	cache.put(a)
	cache.addBasis(a, basis, cost)
	cache.put(b)
	cache.addBasis(b, basis, cost)
	if a.basis != nil || b.basis == nil {
		t.Fatal("hot basis did not replace optional LRU basis")
	}
	if _, ok := cache.get(a.key); !ok {
		t.Fatal("optional admission evicted compact facts")
	}
	// An existing digest whose basis was shed can seed it again after its
	// next real mapped read, shedding the colder optional basis instead.
	s.Key = a.key
	book := newDigestBook(cache)
	book.facts(s)
	if a.basis == nil || b.basis != nil {
		t.Fatal("hot mapped digest did not refill its optional basis")
	}
	cache.put(&sessionDigest{key: "essential", cost: 1000})
	if a.basis != nil || cache.held != 1500 || len(cache.entries) != 3 {
		t.Fatal("essential facts did not take priority over optional data")
	}
	if cache.held > cache.limit {
		t.Fatal("byte bound exceeded")
	}
	cache.limit = 0
	cache.evict()
	if cache.held != 0 || len(cache.entries) != 0 {
		t.Fatal("zero budget retained data")
	}
}

func TestEvidenceBasisOversizeAndMissingFallBack(t *testing.T) {
	s := evidenceTree()
	if basis, _ := newEvidenceBasis(s.Graph, 1); basis != nil {
		t.Fatal("tiny bound accepted basis")
	}
	huge, _ := json.Marshal(map[string]string{"command": strings.Repeat("x", int(maxEvidenceBasisBytes))})
	s.Graph.ToolUses[2].Input = huge
	if basis, _ := newEvidenceBasis(s.Graph, maxEvidenceBasisBytes); basis != nil {
		t.Fatal("oversized execute input retained or truncated")
	}
	cache := newDigestCache(DefaultDigestBytes)
	d := &sessionDigest{key: "missing-basis", facts: sessionFacts{root: true}, cost: 200}
	cache.put(d)
	s.Key = d.key
	s.Unmapped = true
	s.Graph = nil
	book := newDigestBook(cache)
	if _, ok := book.reuse(s.Key); !ok {
		t.Fatal("facts missing")
	}
	if _, _, needs := book.evidence(s, 1, nil, func(string) bool { return false }, 5, 5); !needs {
		t.Fatal("absent basis failed to request safe remap")
	}
}

func TestOldRichEvidenceReprojectsAcrossPRContextsWithoutDecoding(t *testing.T) {
	useEvidenceMemo(t)
	useSessionDigests(t)
	sessions.SetTailCacheLimit(0)
	t.Cleanup(func() { sessions.SetTailCacheLimit(sessions.DefaultTailCacheBytes) })
	f := linktest.New(t)
	const runner = "bbbbbbbb-0000-4000-8000-000000000001"
	f.AddSessionRunning(t, runner, f.Repo, "read notes", "cat notes/one.md notes/two.md")
	req := evidenceRequest(t, f)
	req.MaxCommandEventsPerSession = 27
	rebuild(t, req)
	for i := 0; i < maxProjections+4; i++ {
		next := req
		next.Commits = deepCopy(req.Commits)
		next.Commits[0].Files = append(next.Commits[0].Files, fmt.Sprintf("notes/%s.md", []string{"one", "two"}[i%2]))
		next.MaxPromptsPerSession = i + 1
		next.MaxCommandEventsPerSession = i + 1
		before := decodes()
		got := rebuild(t, next)
		if decodes() != before {
			t.Fatal("unchanged old rich transcript decoded for another PR projection")
		}
		sameEvidence(t, "another PR context", got, fullBuild(t, next))
	}
	before := decodes()
	got := rebuild(t, req)
	if decodes() != before {
		t.Fatal("evicted old projection did not reuse basis")
	}
	sameEvidence(t, "evicted context", got, fullBuild(t, req))
	// Optional bases may be shed independently of compact facts. The
	// existing full-read fallback refills them without changing output.
	sessionDigests.mu.Lock()
	for _, e := range sessionDigests.entries {
		sessionDigests.dropBasis(e.Value.(*sessionDigest))
	}
	sessionDigests.mu.Unlock()
	missing := req
	missing.MaxCommandEventsPerSession = 28
	before = decodes()
	got = rebuild(t, missing)
	if decodes() == before {
		t.Fatal("missing bases did not request safe remap")
	}
	sameEvidence(t, "missing bases", got, fullBuild(t, missing))
	missing.MaxCommandEventsPerSession = 29
	before = decodes()
	got = rebuild(t, missing)
	if decodes() != before {
		t.Fatal("mapped fallback did not refill basis")
	}
	sameEvidence(t, "refilled bases", got, fullBuild(t, missing))
	retitle(t, transcriptOf(f, runner), runner, "updated source")
	before = decodes()
	got = rebuild(t, req)
	if decodes()-before != 1 {
		t.Fatal("changed source did not invalidate its basis")
	}
	sameEvidence(t, "changed source", got, fullBuild(t, req))
	if counter("locallink.evidence_basis.reused") == 0 {
		t.Fatal("test did not exercise basis reuse")
	}
}
