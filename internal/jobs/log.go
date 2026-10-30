package jobs

import (
	"cmp"
	"context"
	"log/slog"
	"time"
)

// SetLogger has the queue log when each job starts and how it ended, the
// error the job keeps no text of included; nil logs nothing. The daemon log
// is the user's own file, so a job's error goes there in full.
func (q *Queue) SetLogger(log *slog.Logger) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.log = log
}

// jobAttrs name a job in the log.
func jobAttrs(j Job) []any {
	attrs := []any{"job", j.ID, "kind", cmp.Or(j.Kind, "review"), "repository", j.Repository}
	if j.Group != "" {
		attrs = append(attrs, "group", j.Group)
	} else {
		attrs = append(attrs, "pr", j.PR)
	}
	return append(attrs, "backend", j.Backend, "model", j.Model, "automatic", j.Automatic)
}

func logStarted(log *slog.Logger, j Job) {
	if log != nil {
		log.Info("job started", jobAttrs(j)...)
	}
}

// logFinished records how a job ended: a failed or interrupted one as a
// warning, with the executor's error.
func logFinished(log *slog.Logger, j Job, started time.Time, err error) {
	if log == nil {
		return
	}
	attrs := append(jobAttrs(j), "state", j.State, "duration", time.Since(started).Round(time.Second), "calls", j.Calls, "tokens", j.Tokens)
	if j.Reason != "" {
		attrs = append(attrs, "reason", j.Reason)
	}
	if j.Detail != "" {
		attrs = append(attrs, "detail", j.Detail)
	}
	if j.Commit != "" {
		attrs = append(attrs, "commit", j.Commit)
	}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	level := slog.LevelInfo
	if j.State == "failed" || j.State == "interrupted" {
		level = slog.LevelWarn
	}
	log.Log(context.Background(), level, "job finished", attrs...)
}

// jobLogger is the executor's logger for the job running under ctx, naming
// it, or nil without one.
func jobLogger(ctx context.Context, log *slog.Logger, p Prepared) *slog.Logger {
	if log == nil {
		return nil
	}
	attrs := []any{"kind", cmp.Or(p.Kind, "review"), "repository", p.Repository, "pr", p.PR}
	if target, ok := ctx.Value(transcriptTargetKey{}).(transcriptTarget); ok && target.JobID != "" {
		attrs = append([]any{"job", target.JobID}, attrs...)
	}
	return log.With(attrs...)
}
