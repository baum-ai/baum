package cursortranscript

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/baumai/baum/sessions/sqliteread"
)

// Shape counts the source variants observed while decoding a store.
type Shape struct {
	Headers                    int            `json:"headers"`
	ComposerVersions           map[string]int `json:"composer_versions"`
	BubbleVersions             map[string]int `json:"bubble_versions"`
	BubbleTypes                map[string]int `json:"bubble_types"`
	ToolStatuses               map[string]int `json:"tool_statuses"`
	KeyFamilies                map[string]int `json:"key_families"`
	ComposersWithBubblesNoData int            `json:"composers_with_bubbles_without_data"`
	BubblesWithoutComposerData int            `json:"bubbles_without_composer_data"`
	JunkRows                   int            `json:"junk_rows"`
}

func (s *Store) Survey() (Shape, error) {
	shape := Shape{
		ComposerVersions: map[string]int{}, BubbleVersions: map[string]int{},
		BubbleTypes: map[string]int{}, ToolStatuses: map[string]int{}, KeyFamilies: map[string]int{},
	}
	composerIDs := map[string]bool{}
	bubblesByComposer := map[string]int{}
	if err := s.db.Scan(headerTable, func(row sqliteread.Row) error {
		id, ok := row.Text("composerId")
		if !ok || id == "" {
			shape.JunkRows++
			return nil
		}
		if raw, ok := rowBytes(row, "value"); ok && !isNull(raw) {
			if _, err := decodeHeaderValue(raw); err != nil {
				return fmt.Errorf("composer header %s: %w", id, err)
			}
		}
		shape.Headers++
		return nil
	}); err != nil {
		return shape, err
	}

	err := s.db.Scan(cursorTable, func(row sqliteread.Row) error {
		key, ok := row.Text("key")
		if !ok || key == "" {
			shape.JunkRows++
			shape.KeyFamilies["null"]++
			return nil
		}
		family := keyFamily(key)
		shape.KeyFamilies[family]++
		raw, ok := rowBytes(row, "value")
		if !ok || isNull(raw) {
			shape.JunkRows++
			return nil
		}
		switch family {
		case "composerData":
			data, err := DecodeComposerData(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			shape.ComposerVersions[strconv.Itoa(data.Version)]++
			composerIDs[strings.TrimPrefix(key, composerKeyPrefix)] = true
		case "bubbleId":
			bubble, err := DecodeBubble(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			if bubble == nil {
				shape.JunkRows++
				return nil
			}
			shape.BubbleVersions[strconv.Itoa(bubble.Version)]++
			shape.BubbleTypes[strconv.Itoa(bubble.Type)]++
			composerID, _, _ := strings.Cut(strings.TrimPrefix(key, bubbleKeyPrefix), ":")
			bubblesByComposer[composerID]++
			if bubble.Tool != nil {
				status := bubble.Tool.Status
				if bubble.Tool.Degenerate() {
					status = "degenerate"
				}
				if status == "" {
					status = "missing"
				}
				shape.ToolStatuses[status]++
			}
		case "codeBlockDiff":
			diff, err := DecodeCodeBlockDiff(raw)
			if err != nil || diff == nil {
				shape.JunkRows++
			}
		}
		return nil
	})
	if err != nil {
		return shape, err
	}
	for composerID, count := range bubblesByComposer {
		if !composerIDs[composerID] {
			shape.ComposersWithBubblesNoData++
			shape.BubblesWithoutComposerData += count
		}
	}
	return shape, nil
}

func keyFamily(key string) string {
	for _, prefix := range []string{
		"composerData:", "bubbleId:", "checkpointId:", "codeBlockDiff:",
		"messageRequestContext:", "ofsContent:", "agentKv:", "composer.content.",
		"codeBlockPartialInlineDiffFates:", "composerVirtualRowHeights:", "inlineDiff",
	} {
		if strings.HasPrefix(key, prefix) {
			return strings.TrimRight(prefix, ":.")
		}
	}
	return "other"
}
