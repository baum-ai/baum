package generate_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
)

const taskSchema = `{"type":"object","additionalProperties":false,"required":["note"],"properties":{"note":{"type":"string"}}}`

func runTask(t *testing.T, reply func(n int) (agent.Result, error), budgets agent.Budgets) (agent.Outcome, []byte, error) {
	t.Helper()
	backend := &generatetest.Backend{RunFunc: func(ctx context.Context, _ agent.Request, n int) (agent.Result, error) { return reply(n) }}
	return generate.RunTask(context.Background(), generate.TaskOptions{Backend: backend, Task: agent.Task{
		Directory: t.TempDir(), Prompt: "Do it.", Schema: []byte(taskSchema), Budgets: budgets,
		Check: func(document []byte) string {
			if strings.Contains(string(document), "bad") {
				return "bad note"
			}
			return ""
		},
	}})
}

var taskBudgets = agent.Budgets{WallClock: time.Minute, MaxCalls: 4, MaxTokens: 10000, MaxProviderAttempts: 1, MaxRepairAttempts: 1}

// A task's reply is masked before it is returned, and one the check still
// rejects after its repairs is a validation failure with no document.
func TestRunTaskMasksAndClassifies(t *testing.T) {
	secret := "Sup3rS3cretPassw0rd"
	outcome, document, err := runTask(t, func(int) (agent.Result, error) {
		return agent.Result{Document: []byte(`{"note":"uses postgres://admin:` + secret + `@db.internal.example.com:5432/app"}`), Model: "m", Calls: 1, Tokens: 10}, nil
	}, taskBudgets)
	if err != nil || strings.Contains(string(document), secret) || outcome.Calls != 1 {
		t.Fatalf("outcome %+v, document %s, %v", outcome, document, err)
	}
	outcome, document, err = runTask(t, func(int) (agent.Result, error) {
		return agent.Result{Document: []byte(`{"note":"bad"}`), Model: "m", Calls: 1, Tokens: 10}, nil
	}, taskBudgets)
	if generate.CategoryOf(err) != generate.CategoryValidation || document != nil || outcome.ValidationRepairs != 1 {
		t.Fatalf("a rejected reply gives %+v, %s, %v", outcome, document, err)
	}
}

// A spent wall clock is a budget failure.
func TestRunTaskWallClock(t *testing.T) {
	budgets := taskBudgets
	budgets.WallClock = 10 * time.Millisecond
	backend := &generatetest.Backend{RunFunc: func(ctx context.Context, _ agent.Request, _ int) (agent.Result, error) {
		<-ctx.Done()
		return agent.Result{}, ctx.Err()
	}}
	_, _, err := generate.RunTask(context.Background(), generate.TaskOptions{Backend: backend, Task: agent.Task{
		Directory: t.TempDir(), Prompt: "Do it.", Schema: []byte(taskSchema), Budgets: budgets}})
	if generate.CategoryOf(err) != generate.CategoryBudget || !strings.Contains(err.Error(), "wall_clock") {
		t.Fatalf("a spent wall clock gives %v", err)
	}
}
