package uiserver

import (
	"errors"

	"github.com/baum-ai/baum/internal/jobs"
)

// PublishProgress announces a change to a job, other than to its usage alone,
// as a `progress` event, on which clients read everything again. The job goes
// with it when it fits in an event.
func (s *Server) PublishProgress(job jobs.Job) error {
	if err := s.Publish("progress", map[string]any{"job": job}); !errors.Is(err, errEventTooLarge) {
		return err
	}
	return s.Publish("progress", struct{}{})
}

// PublishUsage announces a running job's new usage as a `job` event, which
// clients apply to their copy of the job without reading anything again.
// Usage too large for an event is announced as a `progress` event instead.
func (s *Server) PublishUsage(usage jobs.Usage) error {
	if err := s.Publish("job", map[string]any{"job": usage}); !errors.Is(err, errEventTooLarge) {
		return err
	}
	return s.Publish("progress", struct{}{})
}
