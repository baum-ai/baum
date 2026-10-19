package review

import (
	"encoding/json"

	"github.com/baum-ai/baum/bundle"
)

// PreviousVersionOf carries a stored document into the next bundle without
// derived fields, after the stored-document rules (normalizeStored), so the
// next version keeps identifiers the current schema accepts.
func PreviousVersionOf(document []byte, version int) (*bundle.PreviousVersion, error) {
	_, document, err := decodeStored(document, "stored review")
	if err != nil {
		return nil, err
	}
	var stored struct {
		Sections []json.RawMessage `json:"sections"`
		Index    struct {
			Hunks []struct {
				ID       string `json:"id"`
				NewRange []int  `json:"new_range"`
			} `json:"hunks"`
		} `json:"index"`
	}
	if err := json.Unmarshal(document, &stored); err != nil {
		return nil, err
	}
	previous := &bundle.PreviousVersion{Version: version, HunkRanges: map[string][2]int{}}
	for _, hunk := range stored.Index.Hunks {
		if len(hunk.NewRange) == 2 {
			previous.HunkRanges[hunk.ID] = [2]int{hunk.NewRange[0], hunk.NewRange[1]}
		}
	}
	for position, content := range stored.Sections {
		var section struct {
			ID, Title, Kind string
			Hunks           []string
		}
		if err := json.Unmarshal(content, &section); err != nil {
			return nil, err
		}
		var authored map[string]json.RawMessage
		if err := json.Unmarshal(content, &authored); err != nil {
			return nil, err
		}
		delete(authored, "derived")
		if title := SectionTitle(section.Title, position); title != section.Title {
			section.Title = title
			authored["title"], _ = json.Marshal(title)
		}
		content, _ = json.Marshal(authored)
		previous.Sections = append(previous.Sections, bundle.PreviousSection{ID: section.ID, Title: section.Title, Kind: section.Kind, HunkContentHashes: section.Hunks, Content: content})
	}
	return previous, nil
}
