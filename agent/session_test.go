package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSessionMissing(t *testing.T) {
	resume := Request{ResumeSession: "00000000-0000-4000-8000-000000000001"}
	for text, want := range map[string]bool{
		"claude code failed: exit status 1 (No conversation found with session ID: 0000)": true,
		"codex failed: exit status 1 (Error: thread not found)":                           true,
		"codex failed: exit status 1 (no rollout found for thread id)":                    true,
		"codex failed: exit status 1 (session not found)":                                 true,
		"the model provider was unavailable":                                              false,
		"claude code failed: exit status 1 (model not found)":                             false,
	} {
		if got := sessionMissing(resume, Result{}, errors.New(text)); got != want {
			t.Errorf("%q: %v", text, got)
		}
	}
	text := errors.New("No conversation found with session ID: x")
	if sessionMissing(Request{}, Result{}, text) {
		t.Error("a fresh call cannot miss its session")
	}
	if sessionMissing(resume, Result{Calls: 1}, text) {
		t.Error("a call that reached the model did not miss its session")
	}
}

type scriptedBackend struct {
	calls int
	err   error
}

func (b *scriptedBackend) Name() string { return BackendClaude }
func (b *scriptedBackend) Run(context.Context, Request) (Result, error) {
	b.calls++
	return Result{}, b.err
}

// Continue fails a missing session at once instead of retrying it as a
// provider failure.
func TestContinueDoesNotRetryAMissingSession(t *testing.T) {
	budgets := Budgets{MaxCalls: 5, MaxTokens: 100, MaxProviderAttempts: 3}
	backend := &scriptedBackend{err: errors.New("claude code failed: exit status 1 (No conversation found with session ID: x)")}
	g := Generator{Backend: backend, RetryDelay: func(int) time.Duration { return 0 }}
	var outcome Outcome
	_, err := g.Continue(context.Background(), budgets, Request{ResumeSession: "00000000-0000-4000-8000-000000000001"}, &outcome)
	if !errors.Is(err, ErrSessionNotFound) || backend.calls != 1 {
		t.Errorf("err %v after %d calls", err, backend.calls)
	}
	backend.calls = 0
	_, err = g.Continue(context.Background(), budgets, Request{}, &outcome)
	if err == nil || errors.Is(err, ErrSessionNotFound) || backend.calls != 3 {
		t.Errorf("a fresh call: err %v after %d calls", err, backend.calls)
	}
	if _, err := g.Continue(context.Background(), budgets, Request{ForkSession: true}, &outcome); err == nil {
		t.Error("a fork without a session was accepted")
	}
}
