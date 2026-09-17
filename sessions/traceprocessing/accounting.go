package traceprocessing

import (
	"maps"
	"slices"
)

// LineAccounting accounts for classified JSONL lines or records.
type LineAccounting struct {
	Total            int            `json:"total"`
	Mapped           int            `json:"mapped"`
	AttributeBearing int            `json:"attribute_bearing"`
	Merged           int            `json:"merged"`
	Discarded        map[string]int `json:"discarded,omitempty"`
	TornFinal        int            `json:"torn_final_lines"`
}

func (a LineAccounting) Accounted() int {
	return a.Mapped + a.AttributeBearing + a.Merged + discardedTotal(a.Discarded)
}

func (a LineAccounting) Balanced() bool { return a.Total == a.Accounted() }

func (a LineAccounting) DiscardReasons() []string { return SortedDiscardReasons(a.Discarded) }

type ProvenanceAccounting struct {
	Total      int            `json:"total"`
	Mapped     int            `json:"mapped"`
	Provenance int            `json:"provenance"`
	Discarded  map[string]int `json:"discarded,omitempty"`
}

func (a ProvenanceAccounting) Accounted() int {
	return a.Mapped + a.Provenance + discardedTotal(a.Discarded)
}

func (a ProvenanceAccounting) Balanced() bool { return a.Total == a.Accounted() }

func (a ProvenanceAccounting) DiscardReasons() []string {
	return SortedDiscardReasons(a.Discarded)
}

func AddDiscard(discarded *map[string]int, reason string, count int) {
	if count == 0 {
		return
	}
	if *discarded == nil {
		*discarded = map[string]int{}
	}
	(*discarded)[reason] += count
}

func discardedTotal(discarded map[string]int) int {
	var total int
	for _, count := range discarded {
		total += count
	}
	return total
}

func SortedDiscardReasons(discarded map[string]int) []string {
	return slices.Sorted(maps.Keys(discarded))
}
