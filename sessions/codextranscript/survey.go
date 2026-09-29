package codextranscript

import (
	"encoding/json"

	"github.com/lastpersonlabs/baum/sessions/traceprocessing"
)

type Shape struct {
	CLIVersions traceprocessing.Versions `json:"cli_versions"`
	Subtypes    map[string]int           `json:"subtypes,omitempty"`
	UnreadKeys  map[string]int           `json:"unread_keys,omitempty"`
}

func Survey(files []*File) Shape {
	shape := Shape{CLIVersions: traceprocessing.NewVersions(), Subtypes: map[string]int{}, UnreadKeys: map[string]int{}}
	for _, file := range files {
		shape.CLIVersions.Add(file.Meta.CLIVersion)
		for _, line := range file.Lines {
			if line.ResponseItem != nil {
				shape.Subtypes["response_item/"+line.ResponseItem.Type]++
			}
			if line.EventMsg != nil {
				shape.Subtypes["event_msg/"+line.EventMsg.Type]++
			}
			for _, key := range unreadEnvelopeKeys(line.Raw) {
				shape.UnreadKeys[key]++
			}
		}
	}
	shape.CLIVersions.Finalize()
	return shape
}

var knownEnvelopeKeys = map[string]struct{}{
	"timestamp": {},
	"ordinal":   {},
	"type":      {},
	"payload":   {},
}

func unreadEnvelopeKeys(raw json.RawMessage) []string {
	fields := map[string]json.RawMessage{}
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	var out []string
	for key := range fields {
		if _, known := knownEnvelopeKeys[key]; !known {
			out = append(out, key)
		}
	}
	return out
}
