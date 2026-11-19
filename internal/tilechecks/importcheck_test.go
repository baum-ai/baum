package tilechecks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/tiles/kit"
	"github.com/baum-ai/baum/tiles/library"
)

func starterEntry(t *testing.T) library.Entry {
	t.Helper()
	entry, err := library.Load(kit.Starter())
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

// An import's check draws the type's samples through the queue: a pass is
// the pass verdict, a failure its own verdict, and no runner or a stopped
// checker is tilelibrary.ErrNoRunner, which saves the type as not checked yet.
func TestImportCheck(t *testing.T) {
	entry := starterEntry(t)
	t.Run("pass", func(t *testing.T) {
		f := newFixture(t, nil)
		type answer struct {
			verdict string
			err     error
		}
		done := make(chan answer, 1)
		go func() {
			v, err := f.checker.ImportCheck(context.Background(), entry)
			done <- answer{v, err}
		}()
		job := f.claim(t)
		if len(job.Samples) == 0 {
			t.Error("the job has no samples")
		}
		_ = f.checker.Report(job.ID, Report{})
		if got := <-done; got.err != nil || got.verdict != tilelibrary.VerdictPass {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("fail", func(t *testing.T) {
		f := newFixture(t, nil)
		done := make(chan string, 1)
		go func() {
			v, _ := f.checker.ImportCheck(context.Background(), entry)
			done <- v
		}()
		job := f.claim(t)
		_ = f.checker.Report(job.ID, Report{Problems: []Problem{{Code: CodeScriptError, Message: "TypeError", Sample: "example", Face: "glance", Size: "half"}}})
		if v := <-done; v != VerdictFail {
			t.Fatalf("verdict %q", v)
		}
	})
	t.Run("no app", func(t *testing.T) {
		f := newFixture(t, func(o *Options) { o.Subscribed = func() bool { return false } })
		if _, err := f.checker.ImportCheck(context.Background(), entry); !errors.Is(err, tilelibrary.ErrNoRunner) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("nobody claims", func(t *testing.T) {
		f := newFixture(t, func(o *Options) { o.Claim = 100 * time.Millisecond })
		if _, err := f.checker.ImportCheck(context.Background(), entry); !errors.Is(err, tilelibrary.ErrNoRunner) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("stopped", func(t *testing.T) {
		f := newFixture(t, nil)
		_ = f.checker.Close()
		if _, err := f.checker.ImportCheck(context.Background(), entry); !errors.Is(err, tilelibrary.ErrNoRunner) {
			t.Fatalf("err = %v", err)
		}
	})
}
