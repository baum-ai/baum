package entirecheckpoint

import (
	"github.com/baum-ai/baum/sessions/traceprocessing"
)

type Shape struct {
	CLIVersions           traceprocessing.Versions `json:"cli_versions"`
	CheckpointKeys        map[string]int           `json:"checkpoint_keys"`
	SessionKeys           map[string]int           `json:"session_keys"`
	UnknownCheckpointKeys map[string]int           `json:"unknown_checkpoint_keys,omitempty"`
	UnknownSessionKeys    map[string]int           `json:"unknown_session_keys,omitempty"`
	CompactRecordTypes    map[string]int           `json:"compact_record_types"`
	CompactBlockKinds     map[string]int           `json:"compact_block_kinds"`
	CompactAgents         map[string]int           `json:"compact_agents"`
	HashChecked           int                      `json:"hash_checked"`
	HashMismatches        int                      `json:"hash_mismatches"`
}

func Survey(checkpoints []*Checkpoint) Shape {
	shape := Shape{
		CLIVersions:    traceprocessing.NewVersions(),
		CheckpointKeys: map[string]int{}, SessionKeys: map[string]int{},
		UnknownCheckpointKeys: map[string]int{}, UnknownSessionKeys: map[string]int{},
		CompactRecordTypes: map[string]int{}, CompactBlockKinds: map[string]int{},
		CompactAgents: map[string]int{},
	}
	for _, checkpoint := range checkpoints {
		if checkpoint == nil {
			continue
		}
		shape.CLIVersions.Add(checkpoint.Metadata.CLIVersion)
		countStrings(shape.CheckpointKeys, checkpoint.Metadata.Keys)
		countStrings(shape.UnknownCheckpointKeys, checkpoint.Metadata.UnknownKeys)
		for _, snapshot := range checkpoint.Sessions {
			if snapshot == nil {
				continue
			}
			shape.CLIVersions.Add(snapshot.Metadata.CLIVersion)
			countStrings(shape.SessionKeys, snapshot.Metadata.Keys)
			countStrings(shape.UnknownSessionKeys, snapshot.Metadata.UnknownKeys)
			if snapshot.Hash.Checked {
				shape.HashChecked++
			}
			if snapshot.Hash.Mismatch {
				shape.HashMismatches++
			}
			for _, record := range snapshot.Compact {
				shape.CompactRecordTypes[string(record.Type)]++
				shape.CompactAgents[record.Agent]++
				for _, block := range record.Content {
					shape.CompactBlockKinds[string(block.Kind)]++
				}
			}
		}
	}
	shape.CLIVersions.Finalize()
	return shape
}

func countStrings(into map[string]int, values []string) {
	for _, value := range values {
		into[value]++
	}
}
