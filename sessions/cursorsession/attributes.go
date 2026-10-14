package cursorsession

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/baumai/baum/sessions/session"
)

func (f *composerState) addAttributes() error {
	start := epochMillis(f.data.CreatedAt)
	var end *time.Time
	var modelConfig, workspaceIdentifier json.RawMessage
	if f.data.LastUpdatedAt != nil {
		end = epochMillis(*f.data.LastUpdatedAt)
	}
	if f.data.ModelConfig != nil {
		modelConfig = f.data.ModelConfig.Raw
	}
	if f.data.WorkspaceIdentifier != nil {
		workspaceIdentifier = f.data.WorkspaceIdentifier.Raw
	}
	config := []struct {
		key   string
		value any
		set   bool
	}{
		{"unifiedMode", f.data.UnifiedMode, f.data.UnifiedMode != ""},
		{"modelConfig", modelConfig, f.data.ModelConfig != nil},
		{"agentBackend", f.data.AgentBackend, f.data.AgentBackend != ""},
		{"workspaceIdentifier", workspaceIdentifier, f.data.WorkspaceIdentifier != nil},
	}
	for _, attribute := range config {
		if attribute.set {
			if start == nil {
				f.b.report.note("composer %s attribute %s omitted because createdAt is absent", f.data.ComposerID, attribute.key)
				continue
			}
			if err := f.addAttribute(attribute.key, attribute.value, *start); err != nil {
				return err
			}
		}
	}
	final := []struct {
		key   string
		value any
		set   bool
	}{
		{"contextTokensUsed", f.data.ContextTokensUsed, f.data.ContextTokensUsed != nil},
		{"contextTokenLimit", f.data.ContextTokenLimit, f.data.ContextTokenLimit != nil},
		{"promptTokenBreakdown", nonNullRaw(f.data.PromptTokenBreakdown), len(nonNullRaw(f.data.PromptTokenBreakdown)) > 0},
	}
	for _, attribute := range final {
		if attribute.set {
			if end == nil {
				f.b.report.note("composer %s attribute %s omitted because lastUpdatedAt is absent", f.data.ComposerID, attribute.key)
				continue
			}
			if err := f.addAttribute(attribute.key, attribute.value, *end); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *composerState) addAttribute(key string, value any, validFrom time.Time) error {
	var raw json.RawMessage
	switch value := value.(type) {
	case json.RawMessage:
		raw = append(raw, value...)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encoding attribute %s: %w", key, err)
		}
		raw = encoded
	}
	row := &session.SessionAttribute{
		ID: f.ids.Mint("session_attribute", key), SessionID: f.session.ID,
		Key: key, Value: raw, ValidFrom: validFrom,
	}
	f.attributes = append(f.attributes, row)
	f.vendorRow(row.ID, session.EntitySessionAttribute, "composerData.field", key, composerKey(f.data.ComposerID))
	return nil
}
