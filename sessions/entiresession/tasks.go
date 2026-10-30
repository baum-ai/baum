package entiresession

import "github.com/baum-ai/baum/sessions/entirecheckpoint"

// accountTasks accounts for every subagent directory once per checkpoint: it
// became a nested session in some snapshot, or it is discarded under a named
// reason. Directories are checkpoint-wide, so a snapshot that does not claim a
// task says nothing about it.
func accountTasks(report *Report, tasks []*entirecheckpoint.Task) {
	claimed := map[string]bool{}
	nested := map[string]bool{}
	for _, delegated := range report.Delegated {
		for _, task := range delegated.Tasks {
			claimed[task.Directory] = true
			nested[task.Directory] = nested[task.Directory] || task.Nested
		}
	}
	for _, task := range tasks {
		report.Tasks.Total++
		switch {
		case task.Problem != "":
			report.Tasks.discard("unreadable task", 1)
		case !task.TranscriptPresent:
			report.Tasks.discard("task.json only, no transcript", 1)
		case nested[task.Directory]:
			report.Tasks.Mapped++
		case claimed[task.Directory]:
			report.Tasks.discard("transcript not reached from a tool call", 1)
		case codexRollout(task.Transcript):
			report.Tasks.discard("Codex child rollout", 1)
		default:
			report.Tasks.discard("transcript claimed by no delegated session", 1)
		}
	}
}
