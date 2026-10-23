package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/bundle"
)

const taskSchema = `{"type":"object","properties":{"features":{"type":"array","items":{"type":"string"}}},"required":["features"],"additionalProperties":false}`

func taskBudgets() Budgets {
	return Budgets{MaxCalls: 10, MaxTokens: 1000, MaxProviderAttempts: 2, MaxRepairAttempts: 2}
}

// The first reply fails the schema, the second the task's check, and the
// third is accepted: each rejection is written to the run directory and sent
// back for a repair, and nothing the model wrote reaches the log.
func TestRunTaskRepairsSchemaAndCheckRejections(t *testing.T) {
	dir := t.TempDir()
	replies := []string{`{"features":"one"}`, `{"features":["Secret feature"]}`, `{"features":["search"]}`}
	backend := &stubBackend{run: func(_ context.Context, request Request, n int) (Result, error) {
		if request.Directory != dir || string(request.Schema) != taskSchema {
			t.Errorf("request %d: directory %q schema %q", n, request.Directory, request.Schema)
		}
		return Result{Document: []byte(replies[n]), Model: "fixture-model", Calls: 1, Tokens: 10}, nil
	}}
	var logs bytes.Buffer
	g := Generator{Backend: backend, Logger: slog.New(slog.NewTextHandler(&logs, nil))}
	check := func(document []byte) string {
		if bytes.Contains(document, []byte("Secret")) {
			return "feature names must be lowercase"
		}
		return ""
	}
	outcome, document, err := g.RunTask(context.Background(), Task{Directory: dir, Prompt: "Name the features.", Schema: []byte(taskSchema), Check: check, Budgets: taskBudgets()})
	if err != nil || outcome.Reason != "" || string(document) != replies[2] {
		t.Fatalf("%+v %s %v", outcome, document, err)
	}
	if outcome.ValidationRepairs != 2 || outcome.Calls != 3 || outcome.Tokens != 30 || outcome.Model != "fixture-model" || outcome.Findings != "" {
		t.Fatalf("outcome = %+v", outcome)
	}
	for i, name := range []string{"rejected-1.json", "rejected-2.json"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(body) != replies[i] {
			t.Fatalf("%s: %s %v", name, body, err)
		}
	}
	if body, err := os.ReadFile(filepath.Join(dir, "schema.json")); err != nil || string(body) != taskSchema {
		t.Fatalf("schema.json: %s %v", body, err)
	}
	if prompt := backend.calls[0].Prompt; prompt != "Name the features." {
		t.Fatalf("first prompt = %q", prompt)
	}
	repair := backend.calls[2].Prompt
	for _, want := range []string{"Name the features.", "validation repair", replies[1], "feature names must be lowercase", "repair attempt 2 of 2"} {
		if !strings.Contains(repair, want) {
			t.Fatalf("repair prompt lacks %q:\n%s", want, repair)
		}
	}
	if strings.Contains(logs.String(), "Secret") || strings.Contains(logs.String(), "lowercase") {
		t.Fatalf("logs carry model text:\n%s", logs.String())
	}
}

func TestRunTaskKeepsTheLastRejectedReply(t *testing.T) {
	backend := &stubBackend{run: func(context.Context, Request, int) (Result, error) {
		return Result{Document: []byte(`{"features":[]} trailing`), Calls: 1, Tokens: 10}, nil
	}}
	g := Generator{Backend: backend}
	budgets := taskBudgets()
	budgets.MaxRepairAttempts = 1
	outcome, document, err := g.RunTask(context.Background(), Task{Directory: t.TempDir(), Prompt: "p", Schema: []byte(taskSchema), Budgets: budgets})
	if err != nil || outcome.Reason != ReasonValidationRejected || outcome.ValidationRepairs != 1 || len(backend.calls) != 2 || outcome.Findings == "" {
		t.Fatalf("%+v %v", outcome, err)
	}
	// Not JSON, so it is returned but not retained.
	if string(document) != `{"features":[]} trailing` || outcome.RejectedDocument != nil {
		t.Fatalf("document %s rejected %s", document, outcome.RejectedDocument)
	}
}

func TestRunTaskRefusesAnInvalidSchema(t *testing.T) {
	backend := &stubBackend{}
	g := Generator{Backend: backend}
	if _, _, err := g.RunTask(context.Background(), Task{Directory: t.TempDir(), Schema: []byte(`{"type":7}`), Budgets: taskBudgets()}); err == nil || len(backend.calls) != 0 {
		t.Fatalf("err = %v, calls = %d", err, len(backend.calls))
	}
}

func TestRedactTaskOutputMasksEveryString(t *testing.T) {
	redactor, err := bundle.NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	document := []byte(`{"features":["uses ` + leakedDatabaseURL + `"],"` + leakedProviderKey + `":1}`)
	outcome := Outcome{Findings: "quotes " + leakedProviderKey, RejectedDocument: document, Model: "fixture-model"}
	redacted, err := RedactTaskOutput(context.Background(), redactor, document, &outcome)
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"document": string(redacted), "rejected document": string(outcome.RejectedDocument), "findings": outcome.Findings} {
		if strings.Contains(text, "Sup3rS3cretPassw0rd") || strings.Contains(text, leakedProviderKey) {
			t.Errorf("%s keeps a secret: %s", name, text)
		}
	}
	if outcome.OutputMaskedSpans == 0 || outcome.Model != "fixture-model" || !json.Valid(redacted) {
		t.Fatalf("redacted %s, outcome %+v", redacted, outcome)
	}
}
