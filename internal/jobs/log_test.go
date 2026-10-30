package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestJobLogNamesTheJobAndHowItEnded(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	j := Job{ID: "j1", Prepared: Prepared{Item: Item{PR: 7, Kind: KindFix}, Repository: "o/r", Backend: "claude", Model: "opus"}}
	logStarted(log, j)
	j.State, j.Reason, j.Detail, j.Calls = "failed", "backend", "the agent failed", 11
	logFinished(log, j, time.Now().Add(-2*time.Minute), errors.New("provider down"))
	out := buf.String()
	for _, want := range []string{"msg=\"job started\" job=j1 kind=fix repository=o/r pr=7 backend=claude model=opus", "level=WARN msg=\"job finished\"", "state=failed", "reason=backend", "calls=11", "duration=2m0s", "error=\"provider down\""} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	buf.Reset()
	j = Job{ID: "j2", Prepared: Prepared{Item: Item{Group: "g"}, Repository: "o/r"}, State: "completed"}
	logFinished(log, j, time.Now(), nil)
	if out := buf.String(); !strings.Contains(out, "level=INFO") || !strings.Contains(out, "kind=review") || !strings.Contains(out, "group=g") || strings.Contains(out, "error=") {
		t.Errorf("completed job:\n%s", out)
	}
	// Without a logger nothing is written, and nothing panics.
	logStarted(nil, j)
	logFinished(nil, j, time.Now(), nil)
	if jobLogger(context.Background(), nil, j.Prepared) != nil {
		t.Error("jobLogger without a log")
	}
}

func TestJobLoggerNamesTheRunningJob(t *testing.T) {
	var buf bytes.Buffer
	ctx := context.WithValue(context.Background(), transcriptTargetKey{}, transcriptTarget{JobID: "j3"})
	jobLogger(ctx, slog.New(slog.NewTextHandler(&buf, nil)), Prepared{Item: Item{PR: 4, Kind: KindFix}, Repository: "o/r"}).Info("x")
	if !strings.Contains(buf.String(), "job=j3 kind=fix repository=o/r pr=4") {
		t.Errorf("log: %s", buf.String())
	}
}
