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
}

// The text around a task's validation repair, before the rejected document
// and the findings. The ending is the backend's RepairText ending.
const taskRepairInstruction = "\n\nThis is validation repair, not a new task. Preserve valid content and correct only the reported problems.\nRejected document:\n"

// RunTask runs a task through the same loop as Generate: provider retries,
// the stall watchdog and the budgets of call, and a reply rejected by the
// schema or the task's Check is written to the run directory and sent back for
// a repair, up to the repair budget. The returned document is the accepted
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
	text := task.Prompt
	stalls := 0
	budgets := task.Budgets
	repairs := max(budgets.MaxRepairAttempts, 1)
	for attempt := 0; ; attempt++ {
		result, callErr := g.call(ctx, budgets, Request{Directory: task.Directory, Prompt: text, Schema: task.Schema, Logger: logger, Endpoint: task.Endpoint, Task: true}, &outcome, &stalls)
		if callErr != nil {
			if attempt > 0 {
				outcome.RejectedDocument = RetainedDocument(document)
			}
			return outcome, nil, callErr
		}
		document = result.Document
		outcome.Findings = taskFindings(resolved, task.Check, document)
		if logger != nil {
			logger.InfoContext(ctx, "task document validated", "pass", attempt+1, "valid", outcome.Findings == "", "findings_bytes", len(outcome.Findings), "document_bytes", len(document))
		}
		if outcome.Findings == "" {
			break
		}
		if attempt == repairs {
			outcome.Reason = ReasonValidationRejected
			outcome.RejectedDocument = RetainedDocument(document)
			break
		}
		outcome.ValidationRepairs++
		if err := os.WriteFile(filepath.Join(task.Directory, fmt.Sprintf("rejected-%d.json", attempt+1)), document, 0400); err != nil {
			return outcome, nil, err
		}
		text = task.Prompt + taskRepairInstruction + string(document) + "\nThe validator found:\n" + outcome.Findings + fmt.Sprintf(repairEnding, attempt+1, repairs)
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
