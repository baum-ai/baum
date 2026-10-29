package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/jsonschema-go/jsonschema"
)

// Task is a generic run besides a review: a prompt over a run directory whose
// reply is a JSON document of the task's schema. Only the Generator's Backend,
// Logger and RetryDelay take part in it.
type Task struct {
	// Directory is the run directory: the checkout and the task's input
	// files. RunTask adds schema.json and the rejected-N.json files of the
	// repairs, and the backend runs there.
	Directory string
	Prompt    string
	// Schema is the JSON Schema of the reply, given to the backend as the
	// output schema and checked against every reply.
	Schema []byte
	// Check is the workflow's own check of a reply the schema accepts; it
	// returns findings, empty when the reply is valid. Nil checks nothing more.
	Check    func(document []byte) (findings string)
	Budgets  Budgets
	Endpoint *Endpoint
	// PassPrompt, when set, is the prompt of each pass instead of Prompt and,
	// on a repair, the standard repair text.
	PassPrompt func(TaskPass) (string, error)
	// Accept, when set, checks each reply instead of the schema and Check:
	// it returns the document the reply stands for, the findings that send it
	// back for a repair, or Stop to reject it without another repair.
	Accept func(ctx context.Context, pass TaskPass, document []byte, outcome *Outcome) (Verdict, error)
}

// TaskPass is one pass of a task: Number 1 is the first, each later one a
// repair of the Rejected document with the Findings of the pass before it.
// Repairs is the repair budget.
type TaskPass struct {
	Number, Repairs int
	Rejected        []byte
	Findings        string
}

// Verdict is a task's Accept on a reply: the Document it stands for, with
// Findings when it is rejected; Stop rejects it without another repair.
type Verdict struct {
	Document []byte
	Findings string
	Stop     bool
}

// The text around a task's validation repair, before the rejected document
// and the findings. The ending is the backend's RepairText ending.
const taskRepairInstruction = "\n\nThis is validation repair, not a new task. Preserve valid content and correct only the reported problems.\nRejected document:\n"

// RunTask runs a task through the same loop as Generate: provider retries,
// the stall watchdog and the budgets of call, and a reply rejected by the
// schema or the task's Check (or its Accept) is written to the run directory
// and sent back for a repair, up to the repair budget or a Verdict that stops. The returned document is the accepted
// one, or the last rejected one when the Outcome carries a Reason of
// ReasonValidationRejected; any other failure comes back as an error with the
// Outcome of what was spent. The Outcome's text is not redacted: the host
// applies RedactTaskOutput before it stores or shows it. Nothing the model
// wrote is logged.
func (g *Generator) RunTask(ctx context.Context, task Task) (outcome Outcome, document []byte, err error) {
	var schema jsonschema.Schema
	if err := json.Unmarshal(task.Schema, &schema); err != nil {
		return outcome, nil, fmt.Errorf("task schema: %w", err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return outcome, nil, fmt.Errorf("task schema: %w", err)
	}
	if err := os.WriteFile(filepath.Join(task.Directory, "schema.json"), task.Schema, 0400); err != nil {
		return outcome, nil, err
	}
	logger := g.Logger
	_, repairEnding := RepairText(g.Backend.Name())
	prompt := task.PassPrompt
	if prompt == nil {
		prompt = func(pass TaskPass) (string, error) {
			if pass.Number == 1 {
				return task.Prompt, nil
			}
			return task.Prompt + taskRepairInstruction + string(pass.Rejected) + "\nThe validator found:\n" + pass.Findings + fmt.Sprintf(repairEnding, pass.Number-1, pass.Repairs), nil
		}
	}
	accept := task.Accept
	if accept == nil {
		accept = func(_ context.Context, _ TaskPass, document []byte, _ *Outcome) (Verdict, error) {
			return Verdict{Document: document, Findings: taskFindings(resolved, task.Check, document)}, nil
		}
	}
	stalls := 0
	budgets := task.Budgets
	pass := TaskPass{Number: 1, Repairs: max(budgets.MaxRepairAttempts, 1)}
	for ; ; pass.Number++ {
		text, err := prompt(pass)
		if err != nil {
			return outcome, nil, err
		}
		result, callErr := g.call(ctx, budgets, Request{Directory: task.Directory, Prompt: text, Schema: task.Schema, Logger: logger, Endpoint: task.Endpoint, Task: true}, &outcome, &stalls)
		if callErr != nil {
			if pass.Number > 1 {
				outcome.RejectedDocument = RetainedDocument(document)
			}
			return outcome, nil, callErr
		}
		verdict, err := accept(ctx, pass, result.Document, &outcome)
		if err != nil {
			return outcome, nil, err
		}
		document, outcome.Findings = verdict.Document, verdict.Findings
		if logger != nil {
			logger.InfoContext(ctx, "task document validated", "pass", pass.Number, "valid", outcome.Findings == "", "findings_bytes", len(outcome.Findings), "document_bytes", len(document))
		}
		if outcome.Findings == "" {
			break
		}
		if pass.Number > pass.Repairs || verdict.Stop {
			outcome.Reason = ReasonValidationRejected
			outcome.RejectedDocument = RetainedDocument(document)
			break
		}
		outcome.ValidationRepairs++
		if err := os.WriteFile(filepath.Join(task.Directory, fmt.Sprintf("rejected-%d.json", pass.Number)), document, 0400); err != nil {
			return outcome, nil, err
		}
		pass.Rejected, pass.Findings = document, outcome.Findings
	}
	if err := ctx.Err(); err != nil {
		return outcome, nil, err
	}
	return outcome, document, nil
}

// taskFindings checks a reply against the task's schema and then its Check.
func taskFindings(schema *jsonschema.Resolved, check func([]byte) string, document []byte) string {
	var value any
	if err := json.Unmarshal(document, &value); err != nil {
		return "The reply is not a single JSON document."
	}
	if err := schema.Validate(value); err != nil {
		return err.Error()
	}
	if check == nil {
		return ""
	}
	return check(document)
}
