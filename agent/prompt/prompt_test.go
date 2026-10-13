package prompt_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lastpersonlabs/baum/agent"
	"github.com/lastpersonlabs/baum/agent/prompt"
	"github.com/lastpersonlabs/baum/review"
)

type neverRun struct{ ran bool }

func (*neverRun) Name() string { return agent.BackendClaude }
func (b *neverRun) Run(context.Context, agent.Request) (agent.Result, error) {
	b.ran = true
	return agent.Result{}, nil
}

func TestPromptNotAuthoredSurfacesFromTheLoop(t *testing.T) {
	for _, backend := range []string{agent.BackendClaude, agent.BackendCodex, agent.BackendAnthropic} {
		if _, err := (prompt.Prompts{}).Instructions(context.Background(), backend); !errors.Is(err, prompt.ErrNotAuthored) {
			t.Fatalf("%s: %v", backend, err)
		}
	}
	validator, err := review.NewDocumentValidator()
	if err != nil {
		t.Fatal(err)
	}
	backend := &neverRun{}
	generator := agent.Generator{Backend: backend, Prompts: prompt.Prompts{}, Validator: validator}
	_, document, err := generator.Generate(context.Background(), agent.Run{})
	if !errors.Is(err, prompt.ErrNotAuthored) || document != nil || backend.ran {
		t.Fatalf("%v ran=%v", err, backend.ran)
	}
}
