package cursorsession

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lastpersonlabs/baum/sessions/cursortranscript"
	"github.com/lastpersonlabs/baum/sessions/session"
	"github.com/lastpersonlabs/baum/sessions/traceprocessing"
)

type PortableReport struct {
	Records              PortableAccounting `json:"records"`
	Blocks               PortableAccounting `json:"blocks"`
	EstimatedInvocations int                `json:"estimated_invocations"`
	Notes                []string           `json:"notes,omitempty"`
}

type PortableAccounting = traceprocessing.ProvenanceAccounting

func (r *PortableReport) Balanced() bool {
	return r != nil && r.Records.Balanced() && r.Blocks.Balanced()
}

// BuildPortable maps Entire's synthesized Cursor JSONL into the shared session
// model. It is the reduced-fidelity path; local upload uses Build instead.
func BuildPortable(sessionID, path string, records []cursortranscript.PortableRecord, validFrom time.Time) (*session.Graph, *PortableReport, error) {
	b, err := newPortableBuilder(sessionID, path, validFrom)
	if err != nil {
		return nil, nil, err
	}
	if len(records) == 0 {
		return nil, nil, fmt.Errorf("building Cursor portable session: transcript is empty")
	}
	for _, record := range records {
		if err := b.mapRecord(record); err != nil {
			return nil, nil, err
		}
	}
	return b.finish()
}

type portableBuilder struct {
	sessionID          string
	path               string
	rootID             string
	graph              *session.Graph
	report             *PortableReport
	active             *session.UserInput
	inputOrdinal       int
	batch              int
	previousInvocation string
	context            int64
}

func newPortableBuilder(sessionID, path string, validFrom time.Time) (*portableBuilder, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("building Cursor portable session: no session id")
	}
	if path == "" {
		path = "full.jsonl"
	}
	rootID := portableID("session", sessionID)
	b := &portableBuilder{
		sessionID: sessionID,
		path:      path,
		rootID:    rootID,
		graph:     &session.Graph{Sessions: []session.Session{{ID: rootID, Outcome: session.OutcomeUnknown}}},
		report:    &PortableReport{},
	}
	addPortableVendorID(b.graph, rootID, session.EntitySession, rootID, "composerId", sessionID, portableFileRef(path))
	b.addFallbackStoreAttribute(validFrom)
	return b, nil
}

func (b *portableBuilder) addFallbackStoreAttribute(validFrom time.Time) {
	if validFrom.IsZero() {
		return
	}
	attribute := session.SessionAttribute{
		ID:        portableID("session_attribute", b.sessionID, "cursor.fallback_store"),
		SessionID: b.rootID, Key: "cursor.fallback_store", Value: json.RawMessage(`"jsonl"`), ValidFrom: validFrom.UTC(),
	}
	b.graph.Attributes = append(b.graph.Attributes, attribute)
	addPortableVendorID(b.graph, attribute.ID, session.EntitySessionAttribute, b.rootID,
		"portable.field", attribute.Key, portableFileRef(b.path))
}

func (b *portableBuilder) mapRecord(record cursortranscript.PortableRecord) error {
	b.report.Records.Total++
	b.report.Blocks.Total += len(record.Blocks)
	switch record.Kind {
	case cursortranscript.PortableUser:
		return b.mapUser(record)
	case cursortranscript.PortableAssistant:
		return b.mapAssistant(record)
	case cursortranscript.PortableTurnEnd:
		b.mapTurnEnd(record)
		return nil
	default:
		return fmt.Errorf("the Cursor portable line %d has unknown kind %q", record.Ordinal, record.Kind)
	}
}

func (b *portableBuilder) mapUser(record cursortranscript.PortableRecord) error {
	b.inputOrdinal++
	b.batch = 0
	b.previousInvocation = ""

	text, wrapped := portableUserText(record.Blocks)
	if !wrapped {
		b.report.Notes = append(b.report.Notes, fmt.Sprintf("line %d user text has no <user_query> wrapper", record.Ordinal))
	}
	locator := portableLineLocator(b.sessionID, b.path, record.Ordinal)
	metadata, _ := json.Marshal(map[string]any{"fallback_store": "jsonl", "wrapped_user_query": wrapped})
	row := session.UserInput{
		ID: portableID("user_input", b.sessionID, locator), SessionID: b.rootID,
		Ordinal: b.inputOrdinal, Text: text, Kind: session.KindPrompt, Origin: session.OriginHuman, Metadata: metadata,
	}
	b.graph.UserInputs = append(b.graph.UserInputs, row)
	b.active = &b.graph.UserInputs[len(b.graph.UserInputs)-1]
	b.context += estimateTokens(text)
	addPortableVendorID(b.graph, row.ID, session.EntityUserInput, b.rootID, "line", locator, portableSourceRef(b.path, record.Ordinal))
	b.report.Records.Mapped++
	b.report.Blocks.Mapped += len(record.Blocks)
	return nil
}

func (b *portableBuilder) mapAssistant(record cursortranscript.PortableRecord) error {
	if b.active == nil {
		return fmt.Errorf("the Cursor portable line %d: assistant record appears before a user record", record.Ordinal)
	}
	invocation := b.addInvocation(record)
	for _, block := range record.Blocks {
		if err := mapPortableBlock(b.graph, b.active, invocation, record, block, b.sessionID, b.path); err != nil {
			return err
		}
	}
	b.report.Records.Mapped++
	b.report.Blocks.Mapped += len(record.Blocks)
	return nil
}

func (b *portableBuilder) addInvocation(record cursortranscript.PortableRecord) *session.ModelInvocation {
	b.batch++
	batchLocator := fmt.Sprintf("%s:%d:%d", b.sessionID, b.active.Ordinal, b.batch)
	invocation := session.ModelInvocation{
		ID:        portableID("model_invocation", b.sessionID, batchLocator),
		SessionID: b.rootID, UserInputID: b.active.ID, InputTokens: b.context,
		OutputTokens: portableOutputTokens(record.Blocks), TokenBasis: session.TokenBasisEstimated,
	}
	invocation.TotalTokens = invocation.InputTokens + invocation.OutputTokens
	b.context += invocation.OutputTokens
	b.report.EstimatedInvocations++
	if b.previousInvocation != "" {
		parent := b.previousInvocation
		invocation.ParentModelInvocationID = &parent
	}
	b.graph.ModelInvocations = append(b.graph.ModelInvocations, invocation)
	mapped := &b.graph.ModelInvocations[len(b.graph.ModelInvocations)-1]
	b.previousInvocation = mapped.ID
	addPortableVendorID(b.graph, mapped.ID, session.EntityModelInvocation, b.rootID, "batch", batchLocator,
		portableSourceRef(b.path, record.Ordinal))
	return mapped
}

func portableOutputTokens(blocks []cursortranscript.PortableBlock) int64 {
	var parts []string
	for _, block := range blocks {
		parts = append(parts, block.Text)
		if block.Tool != nil {
			parts = append(parts, block.Tool.Name, sourceText(block.Tool.Input))
		}
	}
	return estimateTokens(parts...)
}

func (b *portableBuilder) mapTurnEnd(record cursortranscript.PortableRecord) {
	if record.Status == "error" && b.active != nil && portableUserAbort(record.Error) {
		b.active.Interrupted = true
	}
	// The sentinel closes the current turn. A later assistant record
	// cannot be assigned until another user record starts one.
	b.active = nil
	b.report.Records.Provenance++
}

func (b *portableBuilder) finish() (*session.Graph, *PortableReport, error) {
	if !b.report.Balanced() {
		return nil, nil, fmt.Errorf("Cursor portable accounting is unbalanced")
	}
	if err := session.Validate(b.graph); err != nil {
		return nil, nil, fmt.Errorf("Cursor portable session graph is invalid: %w", err)
	}
	return b.graph, b.report, nil
}

func mapPortableBlock(
	graph *session.Graph,
	input *session.UserInput,
	invocation *session.ModelInvocation,
	record cursortranscript.PortableRecord,
	block cursortranscript.PortableBlock,
	sessionID, path string,
) error {
	identity := fmt.Sprintf("%s:%s:%d:%d", sessionID, path, record.Ordinal, block.Index)
	ref := portableSourceRef(path, record.Ordinal)
	switch block.Kind {
	case cursortranscript.PortableText:
		addPortableText(graph, input, invocation, sessionID, identity, ref, block.Text)
		return nil
	case cursortranscript.PortableToolBlock:
		return addPortableTool(graph, input, invocation, record, block, sessionID, identity, ref)
	default:
		return fmt.Errorf("the Cursor portable line %d block %d has unknown kind %q", record.Ordinal, block.Index, block.Kind)
	}
}

func addPortableText(
	graph *session.Graph,
	input *session.UserInput,
	invocation *session.ModelInvocation,
	sessionID, identity string,
	ref *string,
	text string,
) {
	row := session.AssistantMessage{
		ID: portableID("assistant_message", sessionID, identity), SessionID: input.SessionID,
		UserInputID: input.ID, ModelInvocationID: invocation.ID, Text: &text,
	}
	graph.AssistantMessages = append(graph.AssistantMessages, row)
	addPortableVendorID(graph, row.ID, session.EntityAssistantMessage, input.SessionID, "line.block", identity, ref)
}

func addPortableTool(
	graph *session.Graph,
	input *session.UserInput,
	invocation *session.ModelInvocation,
	record cursortranscript.PortableRecord,
	block cursortranscript.PortableBlock,
	sessionID, identity string,
	ref *string,
) error {
	if block.Tool == nil {
		return fmt.Errorf("the Cursor portable line %d block %d has no tool", record.Ordinal, block.Index)
	}
	resolved := resolvePortableTool(block.Tool.Name, block.Tool.Input)
	row := session.ToolUse{
		ID: portableID("tool_use", sessionID, identity), SessionID: input.SessionID,
		UserInputID: input.ID, ModelInvocationID: invocation.ID, Name: resolved.Name,
		Verb: resolved.Verb, Input: append(json.RawMessage(nil), resolved.Input...),
		Status: session.StatusNoResult, Metadata: resolved.Metadata,
	}
	graph.ToolUses = append(graph.ToolUses, row)
	use := &graph.ToolUses[len(graph.ToolUses)-1]
	addPortableVendorID(graph, use.ID, session.EntityToolUse, input.SessionID, "line.block", identity, ref)
	addPortableTouches(graph, use, &cursortranscript.PortableToolUse{Name: resolved.Name, Input: resolved.Input}, identity, ref)
	return nil
}

func portableUserText(blocks []cursortranscript.PortableBlock) (string, bool) {
	var text strings.Builder
	for _, block := range blocks {
		text.WriteString(block.Text)
	}
	raw := text.String()
	const open, close = "<user_query>", "</user_query>"
	start := strings.Index(raw, open)
	if start < 0 {
		return raw, false
	}
	start += len(open)
	end := strings.Index(raw[start:], close)
	if end < 0 {
		return raw, false
	}
	return raw[start : start+end], true
}

func portableVerb(name string) session.ToolVerb {
	return traceprocessing.GenericToolVerb(name)
}

func addPortableTouches(graph *session.Graph, use *session.ToolUse, tool *cursortranscript.PortableToolUse, identity string, ref *string) {
	kind, paths := portableTouchPaths(use.Verb, tool)
	if len(paths) == 0 {
		return
	}
	seen := map[string]bool{}
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		toolID := use.ID
		row := session.FileTouch{
			ID: session.MintID("file_touch", use.ID, path), ToolUseID: &toolID,
			UserInputID: use.UserInputID, SessionID: use.SessionID, Path: path, Kind: kind,
		}
		graph.FileTouches = append(graph.FileTouches, row)
		addPortableVendorID(graph, row.ID, session.EntityFileTouch, use.SessionID,
			"line.block.path", identity+":"+path, ref)
	}
}

func portableTouchPaths(verb session.ToolVerb, tool *cursortranscript.PortableToolUse) (session.TouchKind, []string) {
	kind, fileTool := portableTouchKind(verb)
	fields := map[string]json.RawMessage{}
	_ = json.Unmarshal(tool.Input, &fields)

	var paths []string
	if fileTool && tool.Name != "ListDir" {
		paths = append(paths, portablePathFields(fields)...)
	}
	if patchPaths := portablePatchPaths(tool.Name, tool.Input, fields); len(patchPaths) > 0 {
		return session.TouchEdit, append(paths, patchPaths...)
	}
	if !fileTool {
		return "", nil
	}
	return kind, paths
}

func portablePathFields(fields map[string]json.RawMessage) []string {
	var paths []string
	for _, key := range []string{"file_path", "notebook_path", "path", "targetFile", "relativeWorkspacePath"} {
		var value string
		if json.Unmarshal(fields[key], &value) == nil && value != "" {
			paths = append(paths, value)
		}
	}
	// ReadLints takes a list of paths.
	var list []string
	if json.Unmarshal(fields["paths"], &list) == nil {
		paths = append(paths, list...)
	}
	return paths
}

func portablePatchPaths(name string, input json.RawMessage, fields map[string]json.RawMessage) []string {
	if !strings.EqualFold(name, "applypatch") && name != "apply_patch" {
		return nil
	}
	var patch string
	if json.Unmarshal(input, &patch) != nil {
		for _, key := range []string{"input", "patch"} {
			if json.Unmarshal(fields[key], &patch) == nil && patch != "" {
				break
			}
		}
	}
	return traceprocessing.PatchHeaderPaths(patch)
}

func portableTouchKind(verb session.ToolVerb) (session.TouchKind, bool) {
	return traceprocessing.GenericTouchKind(verb)
}

func portableUserAbort(raw json.RawMessage) bool {
	text := strings.ToLower(string(raw))
	return strings.Contains(text, "user abort") || strings.Contains(text, "aborted by user") || strings.Contains(text, "cancelled by user")
}

func portableLineLocator(sessionID, path string, ordinal int) string {
	return fmt.Sprintf("%s:%s:%d", sessionID, path, ordinal)
}

func portableSourceRef(path string, ordinal int) *string {
	ref := fmt.Sprintf("%s#%d", path, ordinal)
	return &ref
}

func portableFileRef(path string) *string {
	ref := path
	return &ref
}

func portableID(kind, sessionID string, parts ...string) string {
	return traceprocessing.NewIDNamespace(Vendor, sessionID).Mint(kind, parts...)
}

func addPortableVendorID(graph *session.Graph, ourID string, ourType session.EntityType, owner, kind, vendorID string, sourceRef *string) {
	graph.VendorIDs = traceprocessing.AppendVendorID(graph.VendorIDs, Vendor, owner, ourID, ourType, kind, vendorID, sourceRef)
}
