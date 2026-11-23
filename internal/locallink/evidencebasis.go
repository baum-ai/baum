package locallink

import (
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/sessions/session"
)

// An evidence basis is not a general session graph. It retains only inputs
// sessionEvidenceWith reads, so that new paths/caps/thread names can be projected
// without decoding the source again. Optional bases share the digest budget;
// one larger than this ceiling uses the existing full-read fallback.
const maxEvidenceBasisBytes int64 = 4 << 20

func newEvidenceBasis(graph *session.Graph, limit int64) (*session.Graph, int64) {
	if graph == nil || graph.Root() == nil || limit <= 0 {
		return nil, 0
	}
	out := &session.Graph{}
	cost := estimateCost(reflect.ValueOf(out))
	add := func(value any) bool { cost += estimateCost(reflect.ValueOf(value)); return cost <= limit }
	for _, row := range graph.Sessions {
		if !add(row) {
			return nil, 0
		}
		out.Sessions = append(out.Sessions, row)
	}
	for _, row := range graph.Attributes {
		if !add(row) {
			return nil, 0
		}
		out.Attributes = append(out.Attributes, row)
	}
	root := graph.Root().ID
	needed := map[string]bool{}
	for _, row := range graph.UserInputs {
		text := ""
		if row.SessionID == root && row.Origin == session.OriginHuman && (row.Kind == session.KindPrompt || row.Kind == session.KindSlashCommand) && strings.TrimSpace(row.Text) != "" {
			text = basisText(row.Text)
			needed[row.ID] = true
		}
		row = session.UserInput{ID: row.ID, SessionID: row.SessionID, Ordinal: row.Ordinal, Text: text, TriggeredAt: row.TriggeredAt, Kind: row.Kind, Origin: row.Origin}
		if !add(row) {
			return nil, 0
		}
		out.UserInputs = append(out.UserInputs, row)
	}
	latest := map[string]*session.AssistantMessage{}
	for i := range graph.AssistantMessages {
		row := &graph.AssistantMessages[i]
		if !needed[row.UserInputID] || row.Text == nil || strings.TrimSpace(*row.Text) == "" {
			continue
		}
		if old := latest[row.UserInputID]; old == nil || newerMessage(row, old) {
			latest[row.UserInputID] = row
		}
	}
	for i := range graph.AssistantMessages {
		row := &graph.AssistantMessages[i]
		if latest[row.UserInputID] != row {
			continue
		}
		text := basisText(*row.Text)
		kept := session.AssistantMessage{ID: row.ID, UserInputID: row.UserInputID, Text: &text, Timestamp: row.Timestamp}
		if !add(kept) {
			return nil, 0
		}
		out.AssistantMessages = append(out.AssistantMessages, kept)
	}
	models := map[string]bool{}
	for _, row := range graph.ModelInvocations {
		if row.Model == nil || *row.Model == "" || models[*row.Model] {
			continue
		}
		models[*row.Model] = true
		kept := session.ModelInvocation{Model: row.Model}
		if !add(kept) {
			return nil, 0
		}
		out.ModelInvocations = append(out.ModelInvocations, kept)
	}
	for _, row := range graph.ToolUses {
		kept := session.ToolUse{ID: row.ID, SessionID: row.SessionID, UserInputID: row.UserInputID, Verb: row.Verb, Timestamp: row.Timestamp}
		if row.Verb == session.VerbExecute {
			kept.Input = row.Input
		}
		if !add(kept) {
			return nil, 0
		}
		out.ToolUses = append(out.ToolUses, kept)
	}
	statuses := map[string]bool{}
	for _, row := range graph.ToolResults {
		if statuses[row.ToolUseID] {
			continue
		}
		statuses[row.ToolUseID] = true
		kept := session.ToolResult{ToolUseID: row.ToolUseID, Status: row.Status}
		if !add(kept) {
			return nil, 0
		}
		out.ToolResults = append(out.ToolResults, kept)
	}
	for _, row := range graph.FileTouches {
		kept := session.FileTouch{ID: row.ID, SessionID: row.SessionID, UserInputID: row.UserInputID, Path: row.Path, Kind: row.Kind}
		if !add(kept) {
			return nil, 0
		}
		out.FileTouches = append(out.FileTouches, kept)
	}
	for _, row := range graph.VendorIDs {
		if row.OurID != root || row.VendorIDKind != "thread_id" {
			continue
		}
		kept := session.VendorID{OurID: row.OurID, VendorIDKind: row.VendorIDKind, VendorID: row.VendorID}
		if !add(kept) {
			return nil, 0
		}
		out.VendorIDs = append(out.VendorIDs, kept)
	}
	// Copy strings as well as slices/pointers: no substring or raw input may
	// keep the full source graph's large backing storage alive.
	return detach(out), cost
}

// Preserve both leadingChars' output and the nonblank test used to select
// prompts/replies, even when all visible characters precede nonblank text.
func basisText(text string) string {
	if utf8.RuneCountInString(text) <= bundle.PromptScanChars {
		return text
	}
	var out strings.Builder
	n := 0
	for _, r := range text {
		if n == bundle.PromptScanChars {
			break
		}
		out.WriteRune(r)
		n++
	}
	kept := out.String()
	if strings.TrimSpace(kept) == "" && strings.TrimSpace(text) != "" {
		kept += "x"
	}
	return kept
}
