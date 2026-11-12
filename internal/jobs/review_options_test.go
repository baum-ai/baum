package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/gitlocal"
)

// reviewOptions generates a review of the fixture pull request with backend.
func reviewOptions(t *testing.T, backend *generatetest.Backend) generate.Options {
	t.Helper()
	repo := assembletest.NewRepo(t, assembletest.Options{})
	clone, err := gitlocal.Open(context.Background(), repo.Dir, gitlocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clone.Close() })
	return generate.Options{
		Assembler: &assemble.Assembler{
			GitHub: &assembletest.GitHub{Evidence: repo.Evidence()}, Git: clone,
			Sessions: &assembletest.Sessions{Found: assembletest.SessionTree()},
			Images:   func(context.Context, string, int64) ([]byte, error) { return assembletest.PNG, nil },
		},
		Request: assemble.Request{Repository: assembletest.Repository, Number: assembletest.Number, Limits: assemble.DefaultLimits()},
		Backend: backend, BackendVersion: "9.9.9",
		Budgets: agent.Budgets{WallClock: time.Minute, MaxCalls: 4, MaxTokens: 10000, MaxProviderAttempts: 1, MaxRepairAttempts: 2},
	}
}
