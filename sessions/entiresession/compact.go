package entiresession

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/baum-ai/baum/sessions/entirecheckpoint"
	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/traceprocessing"
)

func buildCompact(snapshot *entirecheckpoint.SessionSnapshot) (*session.Graph, error) {
	return buildCompactWith(snapshot, traceprocessing.Local)
}

func buildCompactWith(snapshot *entirecheckpoint.SessionSnapshot, profile traceprocessing.MappingProfile) (*session.Graph, error) {
	rootID := compactSessionID(snapshot)
	state := &compactState{
		profile:            profile,
		snapshot:           snapshot,
		graph:              &session.Graph{Sessions: []session.Session{{ID: rootID, Outcome: session.OutcomeUnknown}}},
		rootID:             rootID,
		invocationBySource: map[string]*session.ModelInvocation{},
		lastInvocation:     map[string]string{},
		recordIDCount:      map[string]int{},
		userIDCount:        map[string]int{},
		toolIDCount:        map[string]int{},
	}
	state.countSourceIDs()
	for _, record := range snapshot.Compact {
		if err := state.record(record); err != nil {
			return nil, fmt.Errorf("compact line %d: %w", record.Ordinal, err)
		}
	}
	state.finishSessionTimes()
	return state.graph, nil
}

// compactState walks one compact transcript into a session graph. Vendor ids
// are counted first so colliding ids can be disambiguated with the line/block
// ordinal before any row is minted.
type compactState struct {
	profile            traceprocessing.MappingProfile
	snapshot           *entirecheckpoint.SessionSnapshot
	graph              *session.Graph
	rootID             string
	activeInput        *session.UserInput
	inputOrdinal       int
	invocationBySource map[string]*session.ModelInvocation
	lastInvocation     map[string]string
	recordIDCount      map[string]int
	userIDCount        map[string]int
	toolIDCount        map[string]int
}

type mintedIdentity struct {
	value string
	kind  string
}

func (s *compactState) countSourceIDs() {
	for _, record := range s.snapshot.Compact {
		if record.ID != "" {
			s.recordIDCount[record.ID]++
		}
		for _, block := range record.Content {
			if record.Type == entirecheckpoint.CompactUser && block.ID != "" {
				s.userIDCount[block.ID]++
			}
			if block.ToolUse != nil && block.ToolUse.ID != "" {
				s.toolIDCount[block.ToolUse.ID]++
			}
		}
	}
}

func (s *compactState) record(record entirecheckpoint.CompactRecord) error {
	switch record.Type {
	case entirecheckpoint.CompactUser:
		return s.user(record)
	case entirecheckpoint.CompactAssistant:
		return s.assistant(record)
	default:
		return fmt.Errorf("unknown compact record type %q", record.Type)
	}
}

func (s *compactState) user(record entirecheckpoint.CompactRecord) error {
	s.inputOrdinal++
	var text strings.Builder
	image := false
	vendorID := ""
	for _, block := range record.Content {
		switch block.Kind {
		case entirecheckpoint.CompactUserText:
			text.WriteString(block.Text)
			if vendorID == "" {
				vendorID = block.ID
			}
		case entirecheckpoint.CompactImage:
			image = true
		default:
			return fmt.Errorf("user line has %s block", block.Kind)
		}
	}

	id := s.userIdentity(record, vendorID)
	metadata, _ := json.Marshal(map[string]any{"agent": record.Agent, "image": image})
	row := session.UserInput{
		ID: session.MintID("user_input", s.rootID, id.value), SessionID: s.rootID,
		Ordinal: s.inputOrdinal, Text: text.String(), TriggeredAt: cloneTime(record.Timestamp),
		Kind: session.KindPrompt, Origin: session.OriginHuman, Metadata: metadata,
	}
	s.graph.UserInputs = append(s.graph.UserInputs, row)
	s.activeInput = &s.graph.UserInputs[len(s.graph.UserInputs)-1]
	addVendorID(s.graph, row.ID, session.EntityUserInput, s.rootID, id.kind, id.value,
		sourceRef(s.snapshot, "transcript.jsonl", record.Ordinal))
	return nil
}

func (s *compactState) assistant(record entirecheckpoint.CompactRecord) error {
	if s.activeInput == nil {
		return fmt.Errorf("assistant record appears before any user input")
	}
	invocation, err := s.ensureInvocation(record)
	if err != nil {
		return err
	}
	for _, block := range record.Content {
		switch block.Kind {
		case entirecheckpoint.CompactAssistantText:
			s.message(record, block, invocation)
		case entirecheckpoint.CompactToolUse:
			if err := s.tool(record, block, invocation); err != nil {
				return err
			}
		default:
			return fmt.Errorf("assistant line has %s block", block.Kind)
		}
	}
	return nil
}

func (s *compactState) ensureInvocation(record entirecheckpoint.CompactRecord) (*session.ModelInvocation, error) {
	locator := compactLineLocator(s.snapshot, record.Ordinal)
	sourceID := record.ID
	if sourceID == "" {
		sourceID = locator
	}
	key := s.activeInput.ID + "\x00" + sourceID
	if invocation := s.invocationBySource[key]; invocation != nil {
		if invocation.InputTokens != record.InputTokens || invocation.OutputTokens != record.OutputTokens {
			return nil, fmt.Errorf("assistant id %s reports conflicting token usage", sourceID)
		}
		return invocation, nil
	}

	id := s.invocationIdentity(record, locator)
	row := session.ModelInvocation{
		ID:        session.MintID("model_invocation", s.rootID, id.value),
		SessionID: s.rootID, UserInputID: s.activeInput.ID,
		Model: optionalString(s.snapshot.Metadata.Model), InputTokens: record.InputTokens,
		OutputTokens: record.OutputTokens, TotalTokens: record.InputTokens + record.OutputTokens,
		StartedAt: cloneTime(record.Timestamp),
	}
	if parent := s.lastInvocation[s.activeInput.ID]; parent != "" {
		row.ParentModelInvocationID = &parent
	}
	s.graph.ModelInvocations = append(s.graph.ModelInvocations, row)
	invocation := &s.graph.ModelInvocations[len(s.graph.ModelInvocations)-1]
	s.invocationBySource[key] = invocation
	s.lastInvocation[s.activeInput.ID] = invocation.ID
	addVendorID(s.graph, invocation.ID, session.EntityModelInvocation, s.rootID, id.kind, id.value,
		sourceRef(s.snapshot, "transcript.jsonl", record.Ordinal))
	return invocation, nil
}

func (s *compactState) message(record entirecheckpoint.CompactRecord, block entirecheckpoint.CompactBlock, invocation *session.ModelInvocation) {
	identity := s.blockIdentity(record, block)
	text := block.Text
	row := session.AssistantMessage{
		ID: session.MintID("assistant_message", s.rootID, identity), SessionID: s.rootID,
		UserInputID: s.activeInput.ID, ModelInvocationID: invocation.ID,
		Text: &text, Timestamp: cloneTime(record.Timestamp),
	}
	s.graph.AssistantMessages = append(s.graph.AssistantMessages, row)
	addVendorID(s.graph, row.ID, session.EntityAssistantMessage, s.rootID,
		s.messageVendorKind(record), identity, sourceRef(s.snapshot, "transcript.jsonl", record.Ordinal))
}

func (s *compactState) tool(record entirecheckpoint.CompactRecord, block entirecheckpoint.CompactBlock, invocation *session.ModelInvocation) error {
	tool := block.ToolUse
	id := s.toolIdentity(record, block)
	status, err := toolStatus(tool)
	if err != nil {
		return err
	}
	metadata := json.RawMessage(`{}`)
	if !tool.InputPresent {
		metadata = json.RawMessage(`{"input_absent":true}`)
	}
	if s.profile == traceprocessing.Hosted && compactSpawnTools[s.snapshot.Metadata.Agent][tool.Name] {
		// A compact transcript holds no subagent transcript, and cannot tell a
		// launch stub from a report.
		metadata = session.WithSubagentDetail(metadata, session.SubagentNone)
	}
	row := session.ToolUse{
		ID: session.MintID("tool_use", s.rootID, id.value), SessionID: s.rootID,
		UserInputID: s.activeInput.ID, ModelInvocationID: invocation.ID,
		Name: tool.Name, Verb: compactVerbWith(tool.Name, s.profile), Input: compactInput(tool.Name, tool.Input, s.profile),
		Status: status, Metadata: metadata, Timestamp: cloneTime(record.Timestamp),
	}
	s.graph.ToolUses = append(s.graph.ToolUses, row)
	use := &s.graph.ToolUses[len(s.graph.ToolUses)-1]
	ref := sourceRef(s.snapshot, "transcript.jsonl", record.Ordinal)
	addVendorID(s.graph, use.ID, session.EntityToolUse, s.rootID, id.kind, id.value, ref)

	if tool.Result != nil {
		output := tool.Result.Output
		result := session.ToolResult{
			ID: session.MintID("tool_result", s.rootID, id.value), ToolUseID: use.ID,
			SessionID: s.rootID, UserInputID: s.activeInput.ID, Status: status,
			OutputText: &output, Output: append(json.RawMessage(nil), tool.Result.Raw...),
			Timestamp: cloneTime(record.Timestamp),
		}
		s.graph.ToolResults = append(s.graph.ToolResults, result)
		addVendorID(s.graph, result.ID, session.EntityToolResult, s.rootID, id.kind, id.value, ref)
	}
	s.addTouches(block, use, id.value, ref)
	return nil
}

// compactSpawnTools are the calls that start a subagent, by Entire's agent name.
var compactSpawnTools = map[string]map[string]bool{
	"Claude Code": {"Agent": true, "Task": true},
	"Codex":       {"spawn_agent": true},
}

func toolStatus(tool *entirecheckpoint.CompactToolUseBlock) (session.ToolStatus, error) {
	if tool.Result == nil {
		return session.StatusNoResult, nil
	}
	switch tool.Result.Status {
	case "success":
		return session.StatusOK, nil
	case "error":
		return session.StatusError, nil
	default:
		return "", fmt.Errorf("tool %s has unknown result status %q", tool.Name, tool.Result.Status)
	}
}

func (s *compactState) addTouches(block entirecheckpoint.CompactBlock, use *session.ToolUse, vendorIdentity string, ref *string) {
	touches := structuredToolTouches(block.ToolUse, use.Verb, s.profile)
	seen := map[string]bool{}
	for _, touch := range touches {
		path := touch.Path
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		toolID := use.ID
		row := session.FileTouch{
			ID: session.MintID("file_touch", use.ID, path), ToolUseID: &toolID,
			UserInputID: use.UserInputID, SessionID: s.rootID, Path: path, Kind: touch.Kind,
		}
		s.graph.FileTouches = append(s.graph.FileTouches, row)
		addVendorID(s.graph, row.ID, session.EntityFileTouch, s.rootID, "tool_use.id.path", vendorIdentity+":"+path, ref)
	}
}

func (s *compactState) finishSessionTimes() {
	root := s.graph.Root()
	var times traceprocessing.TimeBounds
	for _, input := range s.graph.UserInputs {
		times.Observe(input.TriggeredAt)
	}
	for _, invocation := range s.graph.ModelInvocations {
		times.Observe(invocation.StartedAt)
	}
	for _, message := range s.graph.AssistantMessages {
		times.Observe(message.Timestamp)
	}
	for _, use := range s.graph.ToolUses {
		times.Observe(use.Timestamp)
	}
	if root != nil {
		root.StartedAt, root.EndedAt = times.Start, times.End
	}
}

func compactVerb(name string) session.ToolVerb { return compactVerbWith(name, traceprocessing.Local) }
func compactVerbWith(name string, profile traceprocessing.MappingProfile) session.ToolVerb {
	return traceprocessing.GenericToolVerbWith(name, profile)
}

type structuredTouch struct {
	Path string
	Kind session.TouchKind
}

func structuredToolTouches(tool *entirecheckpoint.CompactToolUseBlock, verb session.ToolVerb, profile traceprocessing.MappingProfile) []structuredTouch {
	if tool == nil {
		return nil
	}
	fields := map[string]json.RawMessage{}
	_ = json.Unmarshal(tool.Input, &fields)
	var touches []structuredTouch
	if kind, ok := touchKindForVerb(verb); ok && tool.Name != "ListDir" {
		for _, key := range []string{"file_path", "notebook_path", "path"} {
			var value string
			if json.Unmarshal(fields[key], &value) == nil && value != "" {
				touches = append(touches, structuredTouch{Path: value, Kind: kind})
			}
		}
	}
	if isApplyPatch(tool.Name) {
		patch := compactPatchText(tool.Input, fields)
		for _, path := range traceprocessing.PatchHeaderPathsWith(patch, profile) {
			touches = append(touches, structuredTouch{Path: path, Kind: session.TouchEdit})
		}
	}
	if tool.Result != nil && tool.Result.File != nil && tool.Result.File.FilePath != "" {
		if kind, ok := touchKindForVerb(verb); ok {
			touches = append(touches, structuredTouch{Path: tool.Result.File.FilePath, Kind: kind})
		} else if tool.Name == "ExitPlanMode" {
			touches = append(touches, structuredTouch{Path: tool.Result.File.FilePath, Kind: session.TouchEdit})
		}
	}
	return touches
}

func touchKindForVerb(verb session.ToolVerb) (session.TouchKind, bool) {
	return traceprocessing.GenericTouchKind(verb)
}

func isApplyPatch(name string) bool {
	switch strings.ToLower(name) {
	case "apply_patch", "applypatch":
		return true
	default:
		return false
	}
}

func compactPatchText(input json.RawMessage, fields map[string]json.RawMessage) string {
	var patch string
	if json.Unmarshal(input, &patch) == nil && patch != "" {
		return patch
	}
	for _, key := range []string{"input", "patch"} {
		if json.Unmarshal(fields[key], &patch) == nil && patch != "" {
			return patch
		}
	}
	return ""
}

func compactLineLocator(snapshot *entirecheckpoint.SessionSnapshot, ordinal int) string {
	return fmt.Sprintf("%s:transcript.jsonl:%d", snapshot.Metadata.SessionID, ordinal)
}

func (s *compactState) userIdentity(record entirecheckpoint.CompactRecord, vendorID string) mintedIdentity {
	if vendorID == "" {
		return mintedIdentity{value: compactLineLocator(s.snapshot, record.Ordinal), kind: "line"}
	}
	if s.userIDCount[vendorID] > 1 {
		return mintedIdentity{value: fmt.Sprintf("%s:%d", vendorID, record.Ordinal), kind: "user.id.line"}
	}
	return mintedIdentity{value: vendorID, kind: "user.id"}
}

func (s *compactState) invocationIdentity(record entirecheckpoint.CompactRecord, locator string) mintedIdentity {
	if record.ID == "" {
		return mintedIdentity{value: locator, kind: "line"}
	}
	if s.recordIDCount[record.ID] > 1 {
		return mintedIdentity{value: fmt.Sprintf("%s:%d", record.ID, record.Ordinal), kind: "message.id.line"}
	}
	return mintedIdentity{value: record.ID, kind: "message.id"}
}

func (s *compactState) messageVendorKind(record entirecheckpoint.CompactRecord) string {
	if record.ID == "" {
		return "line.block"
	}
	if s.recordIDCount[record.ID] > 1 {
		return "message.id.line.block"
	}
	return "message.id.block"
}

func (s *compactState) toolIdentity(record entirecheckpoint.CompactRecord, block entirecheckpoint.CompactBlock) mintedIdentity {
	id := block.ToolUse.ID
	if id == "" {
		return mintedIdentity{value: s.blockIdentity(record, block), kind: "line.block"}
	}
	if s.toolIDCount[id] > 1 {
		return mintedIdentity{
			value: fmt.Sprintf("%s:%d:%d", id, record.Ordinal, block.Index),
			kind:  "tool_use.id.line.block",
		}
	}
	return mintedIdentity{value: id, kind: "tool_use.id"}
}

func (s *compactState) blockIdentity(record entirecheckpoint.CompactRecord, block entirecheckpoint.CompactBlock) string {
	base := record.ID
	if base == "" {
		base = compactLineLocator(s.snapshot, record.Ordinal)
	} else if s.recordIDCount[base] > 1 {
		base = fmt.Sprintf("%s:%d", base, record.Ordinal)
	}
	return fmt.Sprintf("%s:%d", base, block.Index)
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func compactInput(name string, raw json.RawMessage, profile traceprocessing.MappingProfile) json.RawMessage {
	input := append(json.RawMessage(nil), raw...)
	if profile == traceprocessing.Hosted {
		return traceprocessing.NormalizeCodexExecInput(name, input)
	}
	return input
}
