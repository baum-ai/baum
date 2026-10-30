package cursorsession

import (
	"os"
	"testing"

	"github.com/baum-ai/baum/sessions/cursortranscript"
)

func TestHostedReconcile(t *testing.T) {
	path := os.Getenv(corpusEnv)
	if path == "" {
		t.Skipf("set %s to a Cursor state.vscdb to run this", corpusEnv)
	}
	store, err := cursortranscript.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	discovery, err := store.Discover()
	if err != nil {
		t.Fatal(err)
	}
	var roots int
	for _, ref := range discovery.Roots() {
		if !ref.HasData {
			continue
		}
		tree, err := store.Tree(ref.ComposerID)
		if err != nil {
			t.Errorf("%s: %v", ref.ComposerID, err)
			continue
		}
		graph, _, err := hostedBuild(tree)
		if err != nil {
			t.Errorf("%s: %v", ref.ComposerID, err)
			continue
		}
		want := hostedRecount(tree)
		got := hostedSourceCounts{
			sessions: len(graph.Sessions), inputs: len(graph.UserInputs), invocations: len(graph.ModelInvocations),
			messages: len(graph.AssistantMessages), tools: len(graph.ToolUses), results: len(graph.ToolResults),
		}
		if got != want {
			t.Errorf("%s: graph counts %+v, source recount %+v", ref.ComposerID, got, want)
		}
		roots++
	}
	if roots == 0 {
		t.Error("no roots were reconciled")
	}
	t.Logf("reconciled roots=%d", roots)
}

type hostedSourceCounts struct {
	sessions, inputs, invocations, messages, tools, results int
}

// recount is an independent second implementation of the mapping's counting
// rules, written directly against the mapping doc rather than the builder.
func hostedRecount(tree *cursortranscript.Tree) hostedSourceCounts {
	counts := hostedSourceCounts{sessions: 1}
	active := false
	hasBatch := false
	spawnMatches := map[string]int{}
	var previous *cursortranscript.Bubble
	for _, bubble := range tree.Composer.Bubbles {
		if bubble.Type == cursortranscript.BubbleTypeUser {
			counts.inputs++
			active, hasBatch, previous = true, false, nil
			continue
		}
		if !active {
			continue
		}
		tool := bubble.Tool != nil && !bubble.Tool.Degenerate()
		thinking := len(bubble.Thinking) > 0 && string(bubble.Thinking) != "null"
		if bubble.Text == "" && !tool && !thinking && !bubble.TokenCount.HasUsage() {
			continue // contentless bubbles join no batch (traceprocessing/README.md, "Cursor")
		}
		boundary := !hasBatch
		if previous != nil {
			previousTool := previous.Tool != nil && !previous.Tool.Degenerate()
			boundary = previousTool && !tool ||
				previousTool && tool && previous.Tool.ModelCallID != "" &&
					bubble.Tool.ModelCallID != "" && previous.Tool.ModelCallID != bubble.Tool.ModelCallID
		}
		if boundary {
			counts.invocations++
			hasBatch = true
		}
		if bubble.Text != "" {
			counts.messages++
		}
		if tool {
			counts.tools++
			if bubble.Tool.Status != "loading" {
				counts.results++
			}
			if bubble.Tool.ToolName() == "task_v2" && bubble.Tool.AdditionalData != nil {
				if child := bubble.Tool.AdditionalData.SubagentComposerID; child != "" {
					spawnMatches[child]++
				}
			}
		}
		previous = bubble
	}
	for _, child := range tree.Children {
		if spawnMatches[child.ComposerID()] != 1 {
			continue // unresolved spawn: the child subtree is skipped (traceprocessing/README.md, "Cursor")
		}
		childCounts := hostedRecount(child)
		counts.sessions += childCounts.sessions
		counts.inputs += childCounts.inputs
		counts.invocations += childCounts.invocations
		counts.messages += childCounts.messages
		counts.tools += childCounts.tools
		counts.results += childCounts.results
	}
	return counts
}
