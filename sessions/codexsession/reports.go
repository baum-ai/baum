package codexsession

import (
	"encoding/json"
	"strings"

	"github.com/baum-ai/baum/sessions/traceprocessing"

	"github.com/baum-ai/baum/sessions/codextranscript"
	"github.com/baum-ai/baum/sessions/session"
)

// A collaboration child reports back with an agent message whose author is its
// agent path and whose text starts with finalAnswerHeader. wait_agent returns
// only whether the wait completed, so the report goes on the call waiting for
// it: the spawn_agent call, or a later followup_task to the same agent, oldest
// first. A spawn_agent call learns its child's path from the
// sub_agent_activity record naming the call, when there is one, else from its
// output: {"task_name": "/root/<name>"} or the plain text "spawned
// /root/<name>". An output naming only {"agent_id": ...} gives no path, so
// without an activity record that call keeps subagent_detail none.

const (
	finalAnswerHeader = "Message Type: FINAL_ANSWER"
	payloadMarker     = "\nPayload:\n"
	spawnedPrefix     = "spawned "
)

// spawnTools are the calls that start a child.
var spawnTools = map[string]bool{"spawn_agent": true}

// trackAgentCall registers a spawn or follow-up call by the agent path it
// addresses, once its output is known.
func (f *threadState) trackAgentCall(evidence *toolEvidence) {
	if f.b.profile != traceprocessing.Hosted {
		return
	}
	switch evidence.use.Name {
	case "spawn_agent":
		if evidence.agentPath == "" {
			f.registerSpawn(evidence, spawnedPath(outputText(evidence.output)))
		}
	case "followup_task":
		var input struct {
			Target string `json:"target"`
		}
		if json.Unmarshal(evidence.use.Input, &input) != nil {
			return
		}
		if path := f.agentPath(input.Target); path != "" {
			f.agentCalls[path] = append(f.agentCalls[path], evidence)
		}
	}
}

// trackSpawnActivity registers a spawn by the agent path its
// sub_agent_activity record names. The record outranks the call's output.
func (f *threadState) trackSpawnActivity(evidence *toolEvidence, activity codextranscript.SubAgentActivity) {
	if f.b.profile != traceprocessing.Hosted {
		return
	}
	if !spawnTools[evidence.use.Name] || activity.AgentPath == "" || evidence.agentPath == activity.AgentPath {
		return
	}
	if previous := evidence.agentPath; previous != "" {
		f.b.report.note("thread %s: spawn %s returned %s but its activity names %s", f.meta.ID, evidence.callID, previous, activity.AgentPath)
		calls := f.agentCalls[previous][:0]
		for _, call := range f.agentCalls[previous] {
			if call != evidence {
				calls = append(calls, call)
			}
		}
		f.agentCalls[previous] = calls
	}
	f.registerSpawn(evidence, activity.AgentPath)
}

// registerSpawn files a spawn under its child's path, and under the short
// name the model gave it when that differs.
func (f *threadState) registerSpawn(evidence *toolEvidence, path string) {
	if path == "" {
		return
	}
	evidence.agentPath = path
	f.agentCalls[path] = append(f.agentCalls[path], evidence)
	var input struct {
		TaskName string `json:"task_name"`
	}
	if json.Unmarshal(evidence.use.Input, &input) == nil && input.TaskName != "" && input.TaskName != path {
		f.agentAliases[input.TaskName] = path
	}
}

// spawnedPath reads the child's agent path from a spawn_agent output:
// {"task_name": "/root/<name>"} or "spawned /root/<name>".
func spawnedPath(text string) string {
	var output struct {
		TaskName string `json:"task_name"`
	}
	if json.Unmarshal([]byte(text), &output) == nil {
		return output.TaskName
	}
	rest, found := strings.CutPrefix(strings.TrimSpace(text), spawnedPrefix)
	if fields := strings.Fields(rest); found && len(fields) > 0 && strings.HasPrefix(fields[0], "/") {
		return fields[0]
	}
	return ""
}

// agentPath resolves a target as the model wrote it to an agent this thread
// started: the full path, or the short name it was spawned with.
func (f *threadState) agentPath(target string) string {
	if len(f.agentCalls[target]) > 0 {
		return target
	}
	if path := f.agentAliases[target]; len(f.agentCalls[path]) > 0 {
		return path
	}
	return ""
}

// attachFinalAnswer links a child's final answer to the oldest call still
// waiting for one. It reports whether a call took it.
func (f *threadState) attachFinalAnswer(message *codextranscript.AgentMessage) bool {
	if f.b.profile != traceprocessing.Hosted {
		return false
	}
	var items []codextranscript.ContentItem
	if json.Unmarshal(message.Content, &items) != nil {
		return false
	}
	var parts []string
	for _, item := range items {
		if item.Type == "input_text" {
			parts = append(parts, item.Text)
		}
	}
	text := strings.Join(parts, "")
	if !strings.HasPrefix(text, finalAnswerHeader) {
		return false
	}
	_, report, found := strings.Cut(text, payloadMarker)
	report = strings.TrimSpace(report)
	if !found || report == "" {
		return false
	}
	for _, evidence := range f.agentCalls[message.Author] {
		if evidence.report == "" {
			evidence.report = report
			// A child's final answer means the call succeeded, also when
			// only its output (no activity record) named the child.
			evidence.ok = true
			return true
		}
	}
	if len(f.agentCalls[message.Author]) > 0 {
		f.b.report.note("thread %s: a final answer from %s arrived when no call was waiting for one", f.meta.ID, message.Author)
	}
	return false
}

// subagentDetail says what the tree holds of a spawned child's work.
func subagentDetail(evidence *toolEvidence) session.SubagentDetail {
	switch {
	case evidence != nil && evidence.use.Verb == session.VerbSpawnSubagent:
		return session.SubagentTranscript
	case evidence != nil && evidence.report != "":
		return session.SubagentReport
	default:
		return session.SubagentNone
	}
}
