package tilechecks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/tileframes"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/kit"
	"github.com/baum-ai/baum/tiles/library"
)

// starter is the kit's starter type as a candidate, with its samples.
func starter(t *testing.T) Candidate {
	t.Helper()
	entry, err := library.Load(kit.Starter())
	if err != nil {
		t.Fatal(err)
	}
	return candidateOf(entry)
}

func candidateOf(entry library.Entry) Candidate {
	def := entry.Definition
	def.Digest = ""
	samples := slices.Clone(entry.Samples)
	if len(samples) > MaxSamples {
		samples = samples[:MaxSamples]
	}
	return Candidate{Definition: def, Samples: samples}
}

// events records what a checker publishes.
type events struct {
	mu   sync.Mutex
	seen []Event
}

func (e *events) publish(event Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seen = append(e.seen, event)
}

func (e *events) states(id string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var states []string
	for _, event := range e.seen {
		if event.ID == id {
			states = append(states, event.State)
		}
	}
	return states
}

type fixture struct {
	checker *Checker
	frames  *tileframes.Registry
	events  *events
}

func newFixture(t *testing.T, change func(*Options)) fixture {
	t.Helper()
	f := fixture{frames: tileframes.New(tileframes.MaxBytes), events: &events{}}
	opts := Options{Frames: f.frames, Publish: f.events.publish, Subscribed: func() bool { return true },
		Database: filepath.Join(appdbtest.TempDir(t), "app.sqlite"), Wait: 5 * time.Second, Claim: 5 * time.Second, Hang: 5 * time.Second}
	if change != nil {
		change(&opts)
	}
	c, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	f.checker = c
	return f
}

// submit submits candidate in the background.
func (f fixture) submit(candidate Candidate) chan Result {
	done := make(chan Result, 1)
	go func() {
		result, err := f.checker.Submit(context.Background(), candidate)
		if err != nil {
			result = Result{Verdict: "error: " + err.Error()}
		}
		done <- result
	}()
	return done
}

// claim is a runner's poll that must get a job.
func (f fixture) claim(t *testing.T) Job {
	t.Helper()
	job, ok := f.checker.Next(context.Background(), 3*time.Second)
	if !ok {
		t.Fatal("no job came")
	}
	return job
}

func codes(problems []Problem) []string {
	var out []string
	for _, p := range problems {
		out = append(out, p.Code)
	}
	return out
}

func TestStaticProblemsFailWithoutDrawing(t *testing.T) {
	f := newFixture(t, nil)
	for _, tt := range []struct {
		name   string
		change func(*Candidate)
		code   string
	}{
		{"reserved name", func(c *Candidate) { c.Definition.Name = "custom" }, CodeDefinitionInvalid},
		{"no sizes", func(c *Candidate) { c.Definition.Sizes = nil }, CodeDefinitionInvalid},
		{"schema outside the dialect", func(c *Candidate) {
			c.Definition.DataSchema = json.RawMessage(`{"type":"object","properties":{"a":{"type":"string","format":"date"}}}`)
		}, CodeDataSchemaInvalid},
		{"network", func(c *Candidate) {
			c.Definition.Source.HTML = strings.Replace(c.Definition.Source.HTML, "<script>", "<script>\nfetch('https://example.com/x');\n", 1)
		}, CodeLint},
		{"sample against the schema", func(c *Candidate) {
			c.Samples[0].Data = json.RawMessage(`{"summary":"x","items":"no"}`)
		}, CodeSampleInvalid},
		{"too many samples", func(c *Candidate) {
			c.Samples = append(c.Samples, c.Samples[0], c.Samples[0], c.Samples[0])
		}, CodeSampleInvalid},
		{"data against the schema", func(c *Candidate) {
			c.Data = json.RawMessage(`{"summary":""}`)
			c.Context = &Context{Clone: "c", PR: 1, Version: 1}
		}, CodeDataInvalid},
		{"data without its version", func(c *Candidate) { c.Data = c.Samples[1].Data }, CodeDataInvalid},
	} {
		t.Run(tt.name, func(t *testing.T) {
			candidate := starter(t)
			tt.change(&candidate)
			result, err := f.checker.Submit(context.Background(), candidate)
			if err != nil {
				t.Fatal(err)
			}
			if result.Verdict != VerdictFail || !slices.Contains(codes(result.Errors), tt.code) {
				t.Fatalf("result = %s %v, want fail with %s", result.Verdict, result.Errors, tt.code)
			}
			for _, p := range result.Errors {
				if p.Hint == "" || p.Hint != Hints[p.Code] {
					t.Errorf("problem %s has hint %q", p.Code, p.Hint)
				}
			}
			if !strings.HasPrefix(result.Check, "chk_") {
				t.Errorf("check ID %q", result.Check)
			}
		})
	}
	// Nothing was queued or registered.
	if job, ok := f.checker.Next(context.Background(), 0); ok {
		t.Fatalf("a failed candidate was queued: %+v", job)
	}
	if documents, _, _ := f.frames.Stats(); documents != 0 {
		t.Fatalf("%d documents registered", documents)
	}
}

func TestLintPositionAndWarnings(t *testing.T) {
	f := newFixture(t, nil)
	candidate := starter(t)
	candidate.Definition.Source.HTML = "<style>.a { height: 50vh; }</style>\n<script>\nfetch('/x');\n</script>"
	result, err := f.checker.Submit(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Errors) == 0 || result.Errors[0].Code != CodeLint || result.Errors[0].Line == nil || *result.Errors[0].Line != 3 {
		t.Fatalf("errors = %+v, want lint at line 3", result.Errors)
	}
	if !slices.Contains(codes(result.Warnings), CodeLint) {
		t.Fatalf("warnings = %+v, want the vh lint warning", result.Warnings)
	}
}

func TestJobCarriesSamplesAndRegistersOnClaim(t *testing.T) {
	f := newFixture(t, nil)
	candidate := starter(t)
	candidate.Data = candidate.Samples[0].Data
	candidate.Context = &Context{Clone: "clone", PR: 7, Version: 2}
	done := f.submit(candidate)
	job := f.claim(t)
	if job.Kind != JobCheck || !strings.HasPrefix(job.Digest, "sha256:") || job.Kit != 1 || len(job.Sizes) != 3 {
		t.Fatalf("job = %+v", job)
	}
	var names, kinds []string
	for _, s := range job.Samples {
		names, kinds = append(names, s.Name), append(kinds, s.Kind)
	}
	if !slices.Equal(kinds, []string{KindData, KindSample, KindSample, KindMinimal, KindMaximal}) {
		t.Fatalf("samples = %v (%v)", names, kinds)
	}
	if job.Samples[0].Refs != nil || job.Context == nil || job.Context.PR != 7 {
		t.Fatalf("the data's citations are the runner's to resolve: %+v", job.Samples[0])
	}
	// The example sample cites its own hunk; the edge samples cite it too.
	for _, s := range job.Samples[1:] {
		if s.Refs == nil {
			t.Fatalf("sample %s has no refs", s.Name)
		}
	}
	if len(job.Samples[1].Refs.Detail) == 0 || len(job.Samples[4].Refs.Glance) == 0 {
		t.Fatalf("refs = %+v / %+v", job.Samples[1].Refs, job.Samples[4].Refs)
	}
	// The served definition has no HTML; the frame route serves it.
	if strings.Contains(string(job.Definition), "baum.tile") {
		t.Fatal("the job carries the HTML")
	}
	if _, ok := f.frames.Get(job.Digest); !ok {
		t.Fatal("claiming did not register the frame document")
	}
	if err := f.checker.Report(job.ID, Report{Engine: "test"}); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.Verdict != VerdictPass || result.Engine != "test" || result.Check != job.ID {
		t.Fatalf("result = %+v", result)
	}
	if got := f.events.states(job.ID); !slices.Equal(got, []string{StateQueued, StateRunning, StateDone}) {
		t.Fatalf("events = %v", got)
	}
}

func TestReportIsClassified(t *testing.T) {
	f := newFixture(t, nil)
	candidate := starter(t)
	done := f.submit(candidate)
	job := f.claim(t)
	line := 4
	report := Report{Problems: []Problem{
		{Code: CodeOverflowGlance, Message: "260px in 164px", Sample: KindMaximal, Face: "glance", Size: "third", Theme: "light"},
		{Code: CodeOverflowGlance, Message: "again", Sample: KindMaximal, Face: "glance", Size: "third", Theme: "dark"},
		{Code: CodeUnresolvedCitation, Message: "edge", Sample: KindMinimal, Face: "detail", Size: "full"},
		{Code: CodeSlow, Message: "800 ms", Sample: "example", Face: "detail", Size: "full"},
		{Code: CodeScriptError, Message: "TypeError: x is undefined", Sample: "example", Face: "glance", Size: "half", Line: &line},
		{Code: "made_up", Message: "?"},
		{Code: CodeHang, Message: "a runner cannot claim this"},
	}, Runs: []Run{{Sample: "example", Face: "glance", Size: "half", Theme: "light", Width: 488, Height: 180}}}
	if err := f.checker.Report(job.ID, report); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.Verdict != VerdictFail || !slices.Equal(codes(result.Errors), []string{CodeScriptError}) {
		t.Fatalf("errors = %+v", result.Errors)
	}
	if got := codes(result.Warnings); !slices.Equal(got, []string{CodeOverflowGlance, CodeSlow}) {
		t.Fatalf("warnings = %v (deduped by code, sample, face and size)", got)
	}
	if *result.Errors[0].Line != 4 || result.Errors[0].Hint != Hints[CodeScriptError] || len(result.Runs) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if err := f.checker.Report(job.ID, report); !errors.Is(err, ErrUnknown) {
		t.Fatalf("a second report: %v", err)
	}
}

func TestOneRunsAtATimeAndTheQueueIsBounded(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Wait = 30 * time.Second })
	candidate := starter(t)
	first := f.submit(candidate)
	job := f.claim(t)
	var waiting []chan Result
	for range maxQueued {
		waiting = append(waiting, f.submit(candidate))
	}
	// Each Submit queues before it waits; give them a moment to.
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.checker.mu.Lock()
		n := len(f.checker.queue)
		f.checker.mu.Unlock()
		if n == maxQueued || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := f.checker.Submit(context.Background(), candidate); !errors.Is(err, ErrBusy) {
		t.Fatalf("a fifth waiting check: %v", err)
	}
	if _, ok := f.checker.Next(context.Background(), 50*time.Millisecond); ok {
		t.Fatal("a second job ran beside the first")
	}
	if err := f.checker.Report(job.ID, Report{}); err != nil {
		t.Fatal(err)
	}
	if r := <-first; r.Verdict != VerdictPass {
		t.Fatalf("first = %+v", r)
	}
	// The waiting checks run in the order they were queued.
	for range waiting {
		next := f.claim(t)
		if err := f.checker.Report(next.ID, Report{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, done := range waiting {
		if r := <-done; r.Verdict != VerdictPass {
			t.Fatalf("queued = %+v", r)
		}
	}
}

func TestHangBlocksTheDigest(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Hang = 100 * time.Millisecond })
	candidate := starter(t)
	candidate.Data = candidate.Samples[0].Data
	candidate.Context = &Context{Clone: "clone", PR: 7, Version: 1}
	done := f.submit(candidate)
	job := f.claim(t)
	result := <-done
	if result.Verdict != VerdictHang || !slices.Equal(codes(result.Errors), []string{CodeHang}) {
		t.Fatalf("result = %+v", result)
	}
	if !f.frames.Hung(job.Digest) {
		t.Fatal("the digest was not marked hung")
	}
	if _, ok := f.frames.Get(job.Digest); ok {
		t.Fatal("the frame route would still serve a hung type")
	}
	if got := f.events.states(job.ID); !slices.Equal(got, []string{StateQueued, StateRunning, StateHung}) {
		t.Fatalf("events = %v", got)
	}
	if err := f.checker.Report(job.ID, Report{}); !errors.Is(err, ErrUnknown) {
		t.Fatalf("a late report: %v", err)
	}
	// Checking the same type again answers hang at once, drawing nothing.
	again, err := f.checker.Submit(context.Background(), candidate)
	if err != nil || again.Verdict != VerdictHang || !slices.Equal(codes(again.Errors), []string{CodeHang}) {
		t.Fatalf("a second check = %+v, %v", again, err)
	}
	if extra, ok := f.checker.Next(context.Background(), 0); ok {
		t.Fatalf("a hung type was queued again: %+v", extra)
	}
	// A preflight of the same type answers hang at once.
	pre, err := f.checker.Preflight(context.Background(), PreflightRequest{Digest: job.Digest, Data: candidate.Data})
	if err != nil || pre.Verdict != VerdictHang {
		t.Fatalf("preflight = %+v, %v", pre, err)
	}
}

func TestUnavailableWithoutARunner(t *testing.T) {
	t.Run("no app", func(t *testing.T) {
		f := newFixture(t, func(o *Options) { o.Subscribed = func() bool { return false } })
		result, err := f.checker.Submit(context.Background(), starter(t))
		if err != nil || result.Verdict != VerdictUnavailable || result.Reason != reasonNoApp {
			t.Fatalf("result = %+v, %v", result, err)
		}
	})
	t.Run("nobody claims", func(t *testing.T) {
		f := newFixture(t, func(o *Options) { o.Claim = 100 * time.Millisecond })
		result, err := f.checker.Submit(context.Background(), starter(t))
		if err != nil || result.Verdict != VerdictUnavailable || result.Reason != reasonNoRunner {
			t.Fatalf("result = %+v, %v", result, err)
		}
		if _, ok := f.checker.Next(context.Background(), 0); ok {
			t.Fatal("the job stayed queued")
		}
	})
	t.Run("a runner polled lately", func(t *testing.T) {
		f := newFixture(t, func(o *Options) { o.Subscribed = func() bool { return false } })
		f.checker.Next(context.Background(), 0)
		done := f.submit(starter(t))
		job := f.claim(t)
		_ = f.checker.Report(job.ID, Report{})
		if r := <-done; r.Verdict != VerdictPass {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		f := newFixture(t, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if _, err := f.checker.Submit(ctx, starter(t)); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
		if _, ok := f.checker.Next(context.Background(), 0); ok {
			t.Fatal("a cancelled check stayed queued")
		}
	})
}

func TestPreflightRunsOncePerPair(t *testing.T) {
	f := newFixture(t, nil)
	entry, err := library.Load(kit.Starter())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.frames.Add(entry.Definition); err != nil {
		t.Fatal(err)
	}
	digest := entry.Definition.Digest
	data := entry.Samples[0].Data
	req := PreflightRequest{Digest: digest, Data: data, Size: tiles.SizeHalf, Refs: Refs{Glance: map[string]any{}, Detail: map[string]any{}}}
	done := make(chan PreflightResult, 2)
	for range 2 {
		go func() {
			r, err := f.checker.Preflight(context.Background(), req)
			if err != nil {
				r.Verdict = err.Error()
			}
			done <- r
		}()
	}
	// Both wait on one job: the second asks while the first is queued.
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.checker.mu.Lock()
		n := len(f.checker.queue)
		f.checker.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	job := f.claim(t)
	if extra, ok := f.checker.Next(context.Background(), 0); ok {
		t.Fatalf("a second preflight of the same pair was queued: %+v", extra)
	}
	if job.Kind != JobPreflight || len(job.Samples) != 1 || job.Samples[0].Kind != KindData || !slices.Equal(job.Sizes, []tiles.Size{tiles.SizeHalf}) {
		t.Fatalf("job = %+v", job)
	}
	if err := f.checker.Report(job.ID, Report{Problems: []Problem{{Code: CodeScriptError, Message: "x", Sample: KindData}}}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if r := <-done; r.Verdict != VerdictFail || r.Recorded {
			t.Fatalf("preflight = %+v", r)
		}
	}
	// Spelled differently, the same data is the same pair: kept, not drawn.
	var value any
	_ = json.Unmarshal(data, &value)
	respelled, _ := json.MarshalIndent(value, "", "    ")
	r, err := f.checker.Preflight(context.Background(), PreflightRequest{Digest: digest, Data: respelled})
	if err != nil || !r.Recorded || r.Verdict != VerdictFail {
		t.Fatalf("second preflight = %+v, %v", r, err)
	}
	// A type the registry does not hold is shown without one.
	r, err = f.checker.Preflight(context.Background(), PreflightRequest{Digest: "sha256:" + strings.Repeat("0", 64), Data: data})
	if err != nil || r.Verdict != VerdictUnavailable {
		t.Fatalf("unregistered = %+v, %v", r, err)
	}
}

func TestStoreKeepsTheNewest(t *testing.T) {
	defer func(n int) { keptChecks = n }(keptChecks)
	keptChecks = 3
	s, err := openStore(filepath.Join(appdbtest.TempDir(t), "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.close() }()
	ctx := context.Background()
	start := time.Now()
	for i := range 5 {
		if err := s.record(ctx, "sha256:d", string(rune('a'+i)), VerdictPass, start.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range []bool{false, false, true, true, true} {
		if _, ok, err := s.lookup(ctx, "sha256:d", string(rune('a'+i))); err != nil || ok != want {
			t.Errorf("row %d kept = %v, want %v (%v)", i, ok, want, err)
		}
	}
	// Recording a pair again replaces its verdict.
	if err := s.record(ctx, "sha256:d", "e", VerdictHang, start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if verdict, _, _ := s.lookup(ctx, "sha256:d", "e"); verdict != VerdictHang {
		t.Fatalf("verdict = %s", verdict)
	}
}

func TestBuiltinsAndStarterPassTheStaticChecks(t *testing.T) {
	f := newFixture(t, nil)
	candidates := map[string]Candidate{"starter": starter(t)}
	for _, entry := range library.All() {
		candidates[entry.Definition.Name] = candidateOf(entry)
	}
	for name, candidate := range candidates {
		t.Run(name, func(t *testing.T) {
			done := f.submit(candidate)
			job := f.claim(t)
			kinds := map[string]bool{}
			for _, s := range job.Samples {
				kinds[s.Kind] = true
			}
			if !kinds[KindSample] || !kinds[KindMinimal] || !kinds[KindMaximal] {
				t.Fatalf("samples = %+v", job.Samples)
			}
			if err := f.checker.Report(job.ID, Report{}); err != nil {
				t.Fatal(err)
			}
			result := <-done
			if result.Verdict != VerdictPass || len(result.Warnings) != 0 {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestHintsAreTheGuides(t *testing.T) {
	guide, err := os.ReadFile("../../tiles/kit/GUIDE.md")
	if err != nil {
		t.Fatal(err)
	}
	for code, hint := range Hints {
		if !strings.Contains(string(guide), "| `"+code+"` | "+hint+" |") {
			t.Errorf("GUIDE.md has no row for %s with its hint %q", code, hint)
		}
	}
}

func TestDataSHA(t *testing.T) {
	a, err := DataSHA(json.RawMessage(`{"b":1.0,"a":"<x>"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := DataSHA(json.RawMessage(`{ "a": "<x>", "b": 1 }`))
	if a != b {
		t.Fatalf("%s != %s", a, b)
	}
	c, _ := DataSHA(json.RawMessage(`{"a":"<x>","b":10e-1}`))
	if a != c {
		t.Fatalf("1.0 and 10e-1 differ: %s != %s", a, c)
	}
	// Integers a double cannot tell apart are different data.
	big1, _ := DataSHA(json.RawMessage(`{"n":9007199254740993}`))
	big2, _ := DataSHA(json.RawMessage(`{"n":9007199254740992}`))
	if big1 == big2 {
		t.Fatal("integers past a double's precision were named alike")
	}
	if huge, err := DataSHA(json.RawMessage(`{"n":1e999999999}`)); err != nil || huge == "" {
		t.Fatalf("a huge exponent: %q, %v", huge, err)
	}
	if _, err := DataSHA(json.RawMessage(`{`)); err == nil {
		t.Fatal("not JSON was named")
	}
}

func TestAliveKeepsALongCheckFromHanging(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Hang = 200 * time.Millisecond })
	done := f.submit(starter(t))
	job := f.claim(t)
	// Drawing takes three times the hang window, the runner saying it is
	// alive as it goes.
	for range 12 {
		time.Sleep(50 * time.Millisecond)
		if err := f.checker.Alive(job.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.checker.Report(job.ID, Report{Problems: []Problem{{Code: CodeTimeout, Message: "slow", Sample: "example"}}}); err != nil {
		t.Fatalf("the job hung while its runner was alive: %v", err)
	}
	result := <-done
	if result.Verdict != VerdictFail || f.frames.Hung(job.Digest) {
		t.Fatalf("result = %+v", result)
	}
	if err := f.checker.Alive(job.ID); !errors.Is(err, ErrUnknown) {
		t.Fatalf("alive after the end: %v", err)
	}
}

func TestAnAliveCheckStillEndsAtItsLimit(t *testing.T) {
	defer func(d time.Duration) { frameBudget = d }(frameBudget)
	frameBudget = 10 * time.Millisecond
	f := newFixture(t, func(o *Options) { o.Hang = 200 * time.Millisecond })
	done := f.submit(starter(t))
	job := f.claim(t)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(30 * time.Millisecond):
				_ = f.checker.Alive(job.ID)
			}
		}
	}()
	defer close(stop)
	select {
	case result := <-done:
		if result.Verdict != VerdictHang || !f.frames.Hung(job.Digest) {
			t.Fatalf("result = %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a job past its limit did not end")
	}
}

func TestAGoneClientsJobIsQueuedAgain(t *testing.T) {
	f := newFixture(t, nil)
	done := f.submit(starter(t))
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.checker.mu.Lock()
		n := len(f.checker.queue)
		f.checker.mu.Unlock()
		if n == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A long poll whose client has gone claims nothing.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	f.checker.Handler().ServeHTTP(w, httptest.NewRequestWithContext(ctx, "GET", "/api/tile-checks/next?wait=0", nil))
	job := f.claim(t)
	if got := f.events.states(job.ID); len(got) < 4 || got[1] != StateRunning || got[2] != StateQueued || f.frames.Hung(job.Digest) {
		t.Fatalf("events = %v: claimed, put back and claimed again", got)
	}
	_ = f.checker.Report(job.ID, Report{})
	if r := <-done; r.Verdict != VerdictPass {
		t.Fatalf("result = %+v", r)
	}
}

func TestAnAbandonedCheckIsUnavailableNotHung(t *testing.T) {
	f := newFixture(t, nil)
	done := f.submit(starter(t))
	job := f.claim(t)
	if err := f.checker.Report(job.ID, Report{Abandoned: true}); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.Verdict != VerdictUnavailable || f.frames.Hung(job.Digest) {
		t.Fatalf("result = %+v", result)
	}
}

func TestAHangAnnouncesTheWaitingChecksAgain(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Hang = time.Second; o.Wait = 10 * time.Second })
	first := f.submit(starter(t))
	f.claim(t)
	other := starter(t)
	other.Definition.Title = "Another"
	second := f.submit(other)
	for {
		f.checker.mu.Lock()
		n := len(f.checker.queue)
		f.checker.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r := <-first; r.Verdict != VerdictHang {
		t.Fatalf("first = %+v", r)
	}
	f.checker.mu.Lock()
	waitingID := f.checker.queue[0].ID
	f.checker.mu.Unlock()
	// The hang result is delivered before the waiting check is announced
	// again, so wait for the announcement before claiming it.
	for deadline := time.Now().Add(5 * time.Second); len(f.events.states(waitingID)) < 2 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	job := f.claim(t)
	if got := f.events.states(job.ID); len(got) < 2 || !slices.Equal(got[:2], []string{StateQueued, StateQueued}) {
		t.Fatalf("events of the waiting check = %v", got)
	}
	_ = f.checker.Report(job.ID, Report{})
	if r := <-second; r.Verdict != VerdictPass {
		t.Fatalf("second = %+v", r)
	}
}

// A runner already polling claims a job as soon as it is queued; the job's
// queued event still comes before its running.
func TestQueuedIsPublishedBeforeRunning(t *testing.T) {
	f := newFixture(t, func(o *Options) {
		publish := o.Publish
		o.Publish = func(e Event) {
			if e.State == StateQueued {
				// A slow publish widens the window a claim could slip into.
				time.Sleep(50 * time.Millisecond)
			}
			publish(e)
		}
	})
	claimed := make(chan Job, 1)
	go func() {
		job, _ := f.checker.Next(context.Background(), 3*time.Second)
		claimed <- job
	}()
	time.Sleep(20 * time.Millisecond) // the runner is polling
	done := f.submit(starter(t))
	job := <-claimed
	if job.ID == "" {
		t.Fatal("no job came")
	}
	if err := f.checker.Report(job.ID, Report{}); err != nil {
		t.Fatal(err)
	}
	<-done
	if got := f.events.states(job.ID); len(got) < 2 || !slices.Equal(got[:2], []string{StateQueued, StateRunning}) {
		t.Fatalf("events = %v", got)
	}
}

// The runner gets the pull request's data through Options.Inline (a custom
// tile's stored images as data: URLs), for a check and a preflight alike;
// the samples, and the data the verdict is kept under, are left as they are.
func TestTheRunnerGetsTheDataInlined(t *testing.T) {
	inline := func(_ context.Context, data json.RawMessage) json.RawMessage {
		return json.RawMessage(strings.ReplaceAll(string(data), "Raises", "INLINED"))
	}
	f := newFixture(t, func(o *Options) { o.Inline = inline })
	candidate := starter(t)
	for _, s := range candidate.Samples {
		if strings.Contains(string(s.Data), "Raises") {
			candidate.Data = s.Data
		}
	}
	if candidate.Data == nil {
		t.Fatal("the starter has no sample that says Raises")
	}
	candidate.Context = &Context{Clone: "clone", PR: 7, Version: 2}
	done := f.submit(candidate)
	job := f.claim(t)
	if job.Samples[0].Kind != KindData || !strings.Contains(string(job.Samples[0].Data), "INLINED") {
		t.Errorf("the data: %s", job.Samples[0].Data)
	}
	for _, s := range job.Samples[1:] {
		if strings.Contains(string(s.Data), "INLINED") {
			t.Errorf("sample %s was inlined", s.Name)
		}
	}
	if err := f.checker.Report(job.ID, Report{Engine: "test"}); err != nil {
		t.Fatal(err)
	}
	<-done
	// The kept pair is the data as checked: a preflight of it is recorded.
	r, err := f.checker.Preflight(context.Background(), PreflightRequest{Digest: job.Digest, Data: candidate.Data})
	if err != nil || !r.Recorded || r.Verdict != VerdictPass {
		t.Errorf("preflight of the checked data: %+v %v", r, err)
	}
	// A preflight's data is inlined too.
	other := json.RawMessage(strings.Replace(string(candidate.Data), "Raises", "Raises again", 1))
	pre := make(chan PreflightResult, 1)
	go func() {
		r, _ := f.checker.Preflight(context.Background(), PreflightRequest{Digest: job.Digest, Data: other})
		pre <- r
	}()
	job = f.claim(t)
	if job.Kind != JobPreflight || !strings.Contains(string(job.Samples[0].Data), "INLINED again") {
		t.Errorf("the preflight's data: %s", job.Samples[0].Data)
	}
	if err := f.checker.Report(job.ID, Report{Engine: "test"}); err != nil {
		t.Fatal(err)
	}
	<-pre
}
