package uiserver_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/tilechecks"
	"github.com/baum-ai/baum/internal/tileframes"
	"github.com/baum-ai/baum/tiles"
)

// clock is a clock a test moves.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// runCheck submits a candidate of the env_vars type to the real render
// checker and answers its job as the app's runner would, with problems.
func runCheck(t *testing.T, checker *tilechecks.Checker, problems ...tilechecks.Problem) tilechecks.Result {
	t.Helper()
	c := typeChange(t, "env_vars", "")
	var def tiles.TypeDefinition
	if err := json.Unmarshal(c.Definition, &def); err != nil {
		t.Fatal(err)
	}
	def.Digest = ""
	samples := c.Samples[:min(len(c.Samples), tilechecks.MaxSamples)]
	done := make(chan tilechecks.Result, 1)
	go func() {
		result, err := checker.Submit(context.Background(), tilechecks.Candidate{Definition: def, Samples: samples})
		if err != nil {
			result.Verdict = err.Error()
		}
		done <- result
	}()
	job, ok := checker.Next(context.Background(), 5*time.Second)
	if !ok {
		t.Fatalf("no job came: %+v", <-done)
	}
	if err := checker.Report(job.ID, tilechecks.Report{Problems: problems, Engine: "test"}); err != nil {
		t.Fatal(err)
	}
	return <-done
}

// propose_tile_type rests on the daemon's render checker: a check that
// passed on a synthetic sample makes a proposal; a failed, unknown or
// expired check is refused.
func TestProposeTileTypeRestsOnTheRenderCheck(t *testing.T) {
	now := &clock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	checker, err := tilechecks.New(tilechecks.Options{Frames: tileframes.New(tileframes.MaxBytes), Subscribed: func() bool { return true },
		Wait: 5 * time.Second, Claim: 5 * time.Second, Hang: 5 * time.Second, Now: now.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = checker.Close() })
	f := newTileTypeFixtureWith(t, checker)
	f.page(t, prPage)
	propose := func(check string) (string, bool) {
		return f.call(t, "propose_tile_type", map[string]any{"from": map[string]any{"tile": "env_vars"}, "check": check, "glance": "New variables."})
	}

	passed := runCheck(t, checker)
	if passed.Verdict != tilechecks.VerdictPass {
		t.Fatalf("the check: %+v", passed)
	}
	if text, bad := propose(passed.Check); bad {
		t.Fatalf("a passed check was refused: %s", text)
	}
	if p := f.proposal(t); !strings.Contains(string(p.After), "env_vars") {
		t.Errorf("after: %.200s", p.After)
	}

	failed := runCheck(t, checker, tilechecks.Problem{Code: tilechecks.CodeScriptError, Message: "TypeError", Sample: "sample_1", Face: "glance", Size: "full"})
	if failed.Verdict != tilechecks.VerdictFail {
		t.Fatalf("the failed check: %+v", failed)
	}
	if text, bad := propose(failed.Check); !bad || !strings.Contains(text, "did not pass") {
		t.Errorf("a failed check: %v %s", bad, text)
	}
	if text, bad := propose("chk_nothing"); !bad || !strings.Contains(text, "unknown or has expired") {
		t.Errorf("an unknown check: %v %s", bad, text)
	}

	now.add(time.Hour)
	if text, bad := propose(passed.Check); !bad || !strings.Contains(text, "unknown or has expired") {
		t.Errorf("an expired check: %v %s", bad, text)
	}
}
