package entiresession

import (
	"fmt"
	"os"
	"testing"

	"github.com/baum-ai/baum/sessions/entirecheckpoint"
)

// TestReconcile independently counts the compact projection and compares it to
// graphs produced by the compact mapper. Delegated native graphs can be richer,
// so for them it asserts non-erasure rather than false one-to-one equality.
func TestHostedReconcile(t *testing.T) {
	root := os.Getenv(entireCorpusEnv)
	if root == "" {
		t.Skipf("set %s to an extracted entire/checkpoints/v1 tree", entireCorpusEnv)
	}
	var exactSource, exactGraph hostedReconcileCounts
	var checkpoints, snapshots, compactSnapshots int
	err := walkCheckpointDirs(root, func(path, rel string) error {
		checkpoint, err := entirecheckpoint.ParseHostedCheckpoint(os.DirFS(path))
		if err != nil {
			return fmt.Errorf("%s reader: %w", rel, err)
		}
		results, report, err := hostedBuild(checkpoint)
		if err != nil {
			return fmt.Errorf("%s mapper: %w", rel, err)
		}
		if len(results) != len(checkpoint.Sessions) {
			return fmt.Errorf("%s session count source=%d graph=%d", rel, len(checkpoint.Sessions), len(results))
		}
		if report.FailedSnapshots != 0 {
			return fmt.Errorf("%s has %d failed session snapshots: %v", rel, report.FailedSnapshots, report.Findings)
		}
		metadataFiles := 0
		for _, snapshot := range checkpoint.Sessions {
			seen := map[string]bool{}
			for _, path := range snapshot.Metadata.FilesTouched {
				if path != "" {
					seen[path] = true
				}
			}
			metadataFiles += len(seen)
		}
		if report.Envelope.FilesConsidered != metadataFiles {
			return fmt.Errorf("%s metadata files source=%d envelope=%d", rel, metadataFiles, report.Envelope.FilesConsidered)
		}
		for index, result := range results {
			if result.Err != nil {
				return fmt.Errorf("%s session %s: %w", rel, result.SessionID, result.Err)
			}
			source := hostedCountCompact(checkpoint.Sessions[index])
			mapped := hostedReconcileCounts{
				Inputs: len(result.Graph.UserInputs), Invocations: len(result.Graph.ModelInvocations),
				Messages: len(result.Graph.AssistantMessages), Tools: len(result.Graph.ToolUses),
				Results: len(result.Graph.ToolResults),
			}
			if result.Fidelity == FidelityCompact {
				compactSnapshots++
				if source != mapped {
					return fmt.Errorf("%s session %s compact source=%+v graph=%+v", rel, result.SessionID, source, mapped)
				}
				exactSource = exactSource.add(source)
				exactGraph = exactGraph.add(mapped)
			} else if source.hasConversation() && !mapped.hasConversation() {
				return fmt.Errorf("%s session %s has compact conversation evidence but delegated graph is empty", rel, result.SessionID)
			}
			snapshots++
		}
		checkpoints++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checkpoints == 0 || snapshots == 0 || compactSnapshots == 0 {
		t.Fatalf("insufficient reconcile coverage: checkpoints=%d snapshots=%d compact=%d", checkpoints, snapshots, compactSnapshots)
	}
	if exactSource != exactGraph {
		t.Fatalf("compact corpus source=%+v graph=%+v", exactSource, exactGraph)
	}
	t.Logf("checkpoints=%d snapshots=%d compact-snapshots=%d source=%+v graph=%+v", checkpoints, snapshots, compactSnapshots, exactSource, exactGraph)
}

type hostedReconcileCounts struct {
	Inputs, Invocations, Messages, Tools, Results int
}

func (c hostedReconcileCounts) add(other hostedReconcileCounts) hostedReconcileCounts {
	return hostedReconcileCounts{
		Inputs: c.Inputs + other.Inputs, Invocations: c.Invocations + other.Invocations,
		Messages: c.Messages + other.Messages, Tools: c.Tools + other.Tools, Results: c.Results + other.Results,
	}
}

func (c hostedReconcileCounts) hasConversation() bool {
	return c.Inputs != 0 || c.Invocations != 0 || c.Messages != 0 || c.Tools != 0 || c.Results != 0
}

func hostedCountCompact(snapshot *entirecheckpoint.SessionSnapshot) hostedReconcileCounts {
	var out hostedReconcileCounts
	activeInput := 0
	invocations := map[string]bool{}
	for _, record := range snapshot.Compact {
		switch record.Type {
		case entirecheckpoint.CompactUser:
			out.Inputs++
			activeInput++
		case entirecheckpoint.CompactAssistant:
			identity := record.ID
			if identity == "" {
				identity = fmt.Sprintf("line:%d", record.Ordinal)
			}
			key := fmt.Sprintf("%d:%s", activeInput, identity)
			if !invocations[key] {
				invocations[key] = true
				out.Invocations++
			}
			for _, block := range record.Content {
				switch block.Kind {
				case entirecheckpoint.CompactAssistantText:
					out.Messages++
				case entirecheckpoint.CompactToolUse:
					out.Tools++
					if block.ToolUse.Result != nil {
						out.Results++
					}
				}
			}
		}
	}
	return out
}
