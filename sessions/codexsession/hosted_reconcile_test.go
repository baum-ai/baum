package codexsession

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/baum-ai/baum/sessions/codextranscript"
	collectcodex "github.com/baum-ai/baum/sessions/collector/codex"
)

// TestHostedReconcileAgainstTheRollouts independently counts inputs, messages, tool
// calls, and answered call ids. Its invocation count intentionally mirrors the
// documented batch boundaries, so that comparison is a regression check rather
// than independent proof of the grouping rule.
func TestHostedReconcileAgainstTheRollouts(t *testing.T) {
	root := corpusRoot(t)
	index, scan, err := collectcodex.ScanStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if scan.SkippedFiles > 0 {
		t.Logf("store scan skipped %d incomplete rollouts: %v", scan.SkippedFiles, scan.SkippedPaths)
	}

	var source, mapped hostedReconcileCounts
	for _, entry := range index {
		if entry.Class != codextranscript.ThreadRoot {
			continue
		}
		files, err := collectcodex.Find(index, entry)
		if err != nil {
			t.Errorf("finding %s: %v", entry.Path, err)
			continue
		}
		tree, err := codextranscript.DecodeLocalFiles(files)
		if err != nil {
			t.Errorf("decoding %s: %v", entry.Path, err)
			continue
		}
		graph, _, err := hostedBuild(tree)
		if err != nil {
			t.Errorf("building %s: %v", entry.Path, err)
			continue
		}
		fromSource, err := hostedCountRolloutTree(tree)
		if err != nil {
			t.Errorf("counting %s: %v", entry.Path, err)
			continue
		}
		fromGraph := hostedReconcileCounts{
			UserInputs: len(graph.UserInputs), Invocations: len(graph.ModelInvocations),
			Messages: len(graph.AssistantMessages), ToolUses: len(graph.ToolUses),
			AnsweredCalls: len(graph.ToolResults),
		}
		if fromSource != fromGraph {
			t.Errorf("%s source/graph counts differ: source=%+v graph=%+v", entry.Path, fromSource, fromGraph)
		}
		source = source.add(fromSource)
		mapped = mapped.add(fromGraph)
	}

	t.Logf("source=%+v", source)
	t.Logf("mapped=%+v", mapped)
	if source != mapped {
		t.Fatalf("corpus source/graph counts differ: source=%+v graph=%+v", source, mapped)
	}
}

type hostedReconcileCounts struct {
	UserInputs, Invocations, Messages, ToolUses, AnsweredCalls int
}

func (c hostedReconcileCounts) add(other hostedReconcileCounts) hostedReconcileCounts {
	return hostedReconcileCounts{
		UserInputs:    c.UserInputs + other.UserInputs,
		Invocations:   c.Invocations + other.Invocations,
		Messages:      c.Messages + other.Messages,
		ToolUses:      c.ToolUses + other.ToolUses,
		AnsweredCalls: c.AnsweredCalls + other.AnsweredCalls,
	}
}

func hostedCountRolloutTree(tree *codextranscript.Tree) (hostedReconcileCounts, error) {
	var out hostedReconcileCounts
	for _, file := range tree.Files() {
		boundary, err := hostedReconcileBoundary(tree, file)
		if err != nil {
			return out, err
		}
		out = out.add(hostedCountOwnedLines(file, file == tree.Root, boundary))
	}
	return out, nil
}

func hostedReconcileBoundary(tree *codextranscript.Tree, file *codextranscript.File) (int, error) {
	if file == tree.Root {
		return 1, nil
	}
	if file.Meta.SubagentHistoryStartOrdinal != nil {
		return *file.Meta.SubagentHistoryStartOrdinal, nil
	}
	forkTurns := map[string]bool{}
	for _, line := range tree.Rollout(file.Meta.ForkedFromID).Lines {
		if line.EventMsg != nil && line.EventMsg.TaskStarted != nil {
			forkTurns[line.EventMsg.TaskStarted.TurnID] = true
		}
	}
	for _, line := range file.Lines[1:] {
		if line.EventMsg != nil && line.EventMsg.TaskStarted != nil && !forkTurns[line.EventMsg.TaskStarted.TurnID] {
			return line.Ordinal, nil
		}
	}
	return 0, fmt.Errorf("no owned-history boundary for %s", file.Path)
}

func hostedCountOwnedLines(file *codextranscript.File, root bool, boundary int) hostedReconcileCounts {
	var out hostedReconcileCounts
	active, inBatch := false, false
	pendingTrigger := false
	calls, answered := map[string]bool{}, map[string]bool{}

	startModelItem := func() {
		if active && !inBatch {
			out.Invocations++
			inBatch = true
		}
	}
	for i, line := range file.Lines {
		if i > 0 && line.Ordinal < boundary {
			continue
		}
		if line.InterAgentMetadata != nil {
			pendingTrigger = line.InterAgentMetadata.TriggerTurn
			continue
		}
		if item := line.ResponseItem; item != nil {
			switch {
			case item.AgentMessage != nil:
				inBatch = false
				if pendingTrigger && item.AgentMessage.Recipient == file.Meta.AgentPath {
					out.UserInputs++
					active = true
				}
				pendingTrigger = false
			case item.Message != nil && item.Message.Role == "user":
				inBatch = false
			case item.Message != nil && item.Message.Role == "assistant":
				startModelItem()
				if active {
					out.Messages++
				}
			case item.Reasoning != nil:
				startModelItem()
			case item.FunctionCall != nil:
				startModelItem()
				if active {
					out.ToolUses++
					calls[item.FunctionCall.CallID] = true
				}
			case item.CustomCall != nil:
				startModelItem()
				if active {
					out.ToolUses++
					calls[item.CustomCall.CallID] = true
				}
			case item.ToolSearchCall != nil:
				startModelItem()
				if active {
					out.ToolUses++
					callID := item.ToolSearchCall.CallID
					if callID == "" {
						callID = "line-" + strconv.Itoa(line.Ordinal)
					}
					calls[callID] = true
				}
			case item.WebSearchCall != nil:
				startModelItem()
				if active {
					out.ToolUses++
					callID := file.Meta.ID + ":" + strconv.Itoa(line.Ordinal)
					calls[callID] = true
					if hostedSourceTerminal(item.WebSearchCall.Status) {
						answered[callID] = true
					}
				}
			case item.FunctionOutput != nil:
				inBatch = false
				hostedMarkAnswered(calls, answered, item.FunctionOutput.CallID)
			case item.CustomOutput != nil:
				inBatch = false
				hostedMarkAnswered(calls, answered, item.CustomOutput.CallID)
			case item.ToolSearchOutput != nil:
				inBatch = false
				hostedMarkAnswered(calls, answered, item.ToolSearchOutput.CallID)
			}
			continue
		}
		if communication := line.InterAgentCommunication; communication != nil {
			inBatch = false
			if communication.TriggerTurn != nil && *communication.TriggerTurn && communication.Recipient == file.Meta.AgentPath {
				out.UserInputs++
				active = true
			}
			continue
		}
		if event := line.EventMsg; event != nil {
			switch {
			case event.TaskStarted != nil:
				active, inBatch = false, false
			case event.TaskComplete != nil || event.TurnAborted != nil:
				inBatch = false
			case event.UserMessage != nil && root:
				out.UserInputs++
				active, inBatch = true, false
			case event.ExecCommandEnd != nil:
				hostedMarkAnswered(calls, answered, event.ExecCommandEnd.CallID)
			case event.PatchApplyEnd != nil:
				hostedMarkAnswered(calls, answered, event.PatchApplyEnd.CallID)
			case event.MCPToolCallEnd != nil:
				hostedMarkAnswered(calls, answered, event.MCPToolCallEnd.CallID)
			case event.SubAgentActivity != nil:
				hostedMarkAnswered(calls, answered, event.SubAgentActivity.EventID)
			}
		}
	}
	out.AnsweredCalls = len(answered)
	return out
}

func hostedMarkAnswered(calls, answered map[string]bool, callID string) {
	if calls[callID] {
		answered[callID] = true
	}
}

func hostedSourceTerminal(status string) bool {
	switch strings.ToLower(status) {
	case "ok", "completed", "success", "succeeded", "error", "failed", "failure", "interrupted", "cancelled", "canceled":
		return true
	}
	return false
}
