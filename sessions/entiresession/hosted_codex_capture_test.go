package entiresession

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/baum-ai/baum/sessions/entirecheckpoint"
	"github.com/baum-ai/baum/sessions/session"
)

func TestHostedCodexCaptureCorpus(t *testing.T) {
	root := os.Getenv(entireCorpusEnv)
	if root == "" {
		t.Skip("set BAUM_ENTIRE_CORPUS to an extracted checkpoint tree")
	}
	counts := map[FidelityRank]int{}
	reasons := map[string]int{}
	distinct := map[string]bool{}
	var snapshots, spawns, reports int
	err := walkCheckpointDirs(root, func(path, rel string) error {
		checkpoint, err := entirecheckpoint.ParseHostedCheckpoint(os.DirFS(path))
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		for _, snapshot := range checkpoint.Sessions {
			if snapshot.Metadata.Agent != "Codex" {
				continue
			}
			copy := *checkpoint
			copy.Sessions = []*entirecheckpoint.SessionSnapshot{snapshot}
			results, report, err := hostedBuild(&copy)
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			if !report.Balanced() || len(results) != 1 || results[0].Err != nil {
				return fmt.Errorf("%s: failed mapping: %+v", rel, report)
			}
			result := results[0]
			if err := session.Validate(result.Graph); err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			snapshots++
			distinct[snapshot.Metadata.SessionID] = true
			counts[result.Fidelity]++
			for _, finding := range report.Findings {
				if _, reason, ok := strings.Cut(finding, "used compact fallback: "); ok {
					reasons[reason]++
				}
			}
			for _, tool := range result.Graph.ToolUses {
				if tool.Name != "spawn_agent" {
					continue
				}
				spawns++
				if hostedDetailOf(t, tool) == session.SubagentReport {
					reports++
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshots == 0 {
		t.Fatal("no Codex snapshots")
	}
	t.Logf("snapshots=%d distinct-sessions=%d fidelities=%v spawns=%d reports=%d", snapshots, len(distinct), counts, spawns, reports)
	keys := make([]string, 0, len(reasons))
	for reason := range reasons {
		keys = append(keys, reason)
	}
	sort.Strings(keys)
	for _, reason := range keys {
		t.Logf("%d: %s", reasons[reason], reason)
	}
}
