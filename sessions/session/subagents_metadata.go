package session

import "encoding/json"

func WithSubagentDetail(metadata json.RawMessage, detail SubagentDetail) json.RawMessage {
	bag := map[string]json.RawMessage{}
	if len(metadata) > 0 && json.Unmarshal(metadata, &bag) != nil {
		bag = map[string]json.RawMessage{}
	}
	if bag == nil {
		bag = map[string]json.RawMessage{}
	}
	bag[SubagentDetailKey], _ = json.Marshal(detail)
	raw, err := json.Marshal(bag)
	if err != nil {
		return metadata
	}
	return raw
}
