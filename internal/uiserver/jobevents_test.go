package uiserver

import (
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/jobs"
)

func lastEvent(t *testing.T, s *Server) event {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		t.Fatal("nothing published")
	}
	return s.events[len(s.events)-1]
}

// A usage update travels as a `job` event while it fits in one; usage too
// large for an event, and a job too large for a `progress` event, become a
// `progress` event without the job, so clients still read everything again
// rather than miss the change.
func TestJobEventsFallBackToAnEmptyProgressEvent(t *testing.T) {
	s, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	usage := jobs.Usage{ID: "a", State: "running", Phase: "generating", Calls: 3, Tokens: 1200, Accounting: &agent.Accounting{ChargedTokens: 1200}}
	if err := s.PublishUsage(usage); err != nil {
		t.Fatal(err)
	}
	if e := lastEvent(t, s); e.Kind != "job" || !strings.Contains(string(e.Data), `"tokens":1200`) || !strings.Contains(string(e.Data), `"id":"a"`) {
		t.Fatalf("%s %s", e.Kind, e.Data)
	}

	// A long run's attempts can outgrow an event.
	big := &agent.Accounting{}
	for range 400 {
		big.Attempts = append(big.Attempts, agent.UsageAttempt{ModelSource: "provider_reported", Model: "claude-sonnet-4-5", Calls: 1, ChargedTokens: 1000})
	}
	usage.Accounting = big
	if err := s.PublishUsage(usage); err != nil {
		t.Fatal(err)
	}
	if e := lastEvent(t, s); e.Kind != "progress" || string(e.Data) != "{}" {
		t.Fatalf("%s %s", e.Kind, e.Data)
	}

	job := jobs.Job{ID: "a", State: "running", Phase: "generating"}
	if err := s.PublishProgress(job); err != nil {
		t.Fatal(err)
	}
	if e := lastEvent(t, s); e.Kind != "progress" || !strings.Contains(string(e.Data), `"id":"a"`) {
		t.Fatalf("%s %s", e.Kind, e.Data)
	}
	job.Accounting = big
	if err := s.PublishProgress(job); err != nil {
		t.Fatal(err)
	}
	if e := lastEvent(t, s); e.Kind != "progress" || string(e.Data) != "{}" {
		t.Fatalf("%s %s", e.Kind, e.Data)
	}
}
