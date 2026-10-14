// Package cursorsession maps Cursor's typed SQLite records into the shared
// session model.
package cursorsession

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/baumai/baum/sessions/cursortranscript"
	"github.com/baumai/baum/sessions/session"
	"github.com/baumai/baum/sessions/sourcepath"
	"github.com/baumai/baum/sessions/traceprocessing"
)

const Vendor = "cursor"

// Build maps one materialized Cursor composer tree entirely in memory.
func Build(tree *cursortranscript.Tree) (*session.Graph, *Report, error) {
	if tree == nil || tree.Composer == nil || tree.Composer.Data == nil {
		return nil, nil, fmt.Errorf("building Cursor session: no composer data")
	}
	b := &builder{graph: &session.Graph{}, report: &Report{}, visited: map[string]bool{}}
	if err := b.ingest(tree, nil); err != nil {
		return nil, nil, err
	}
	b.finishReport()
	if !b.report.Bubbles.Balanced() {
		return nil, nil, fmt.Errorf("the Cursor bubble accounting is unbalanced: %d read, %d accounted", b.report.Bubbles.Total, b.report.Bubbles.Accounted())
	}
	if err := session.Validate(b.graph); err != nil {
		return nil, nil, fmt.Errorf("the Cursor session graph is invalid: %w", err)
	}
	return b.graph, b.report, nil
}

type builder struct {
	graph   *session.Graph
	report  *Report
	visited map[string]bool
}

func (b *builder) ingest(tree *cursortranscript.Tree, spawn *session.SpawnLink) error {
	composerID := tree.ComposerID()
	if b.visited[composerID] {
		return fmt.Errorf("composer %s is reached more than once", composerID)
	}
	b.visited[composerID] = true

	state, err := newComposerState(b, tree.Composer, spawn)
	if err != nil {
		return fmt.Errorf("composer %s: %w", composerID, err)
	}
	if err := state.process(); err != nil {
		return fmt.Errorf("composer %s: %w", composerID, err)
	}
	links := state.resolveChildren(tree)
	state.flush()

	for _, child := range tree.Children {
		link := links[child.ComposerID()]
		if link == nil {
			// resolveChildren already said why. The child's whole subtree stays
			// out of the graph, and its bubbles are accounted rather than lost.
			bubbles := treeBubbleCount(child)
			b.report.Bubbles.Total += bubbles
			b.report.discardN("unresolved subagent spawn", bubbles)
			continue
		}
		if err := b.ingest(child, link); err != nil {
			return err
		}
	}
	return nil
}

func treeBubbleCount(tree *cursortranscript.Tree) int {
	total := 0
	for _, composer := range tree.Composers() {
		total += len(composer.Bubbles) + len(composer.Unlisted)
	}
	return total
}

type ownedInput struct {
	row     *session.UserInput
	bubble  *cursortranscript.Bubble
	bubbles []*cursortranscript.Bubble
}

type toolEvidence struct {
	bubble *cursortranscript.Bubble
	row    *session.ToolUse
}

type composerState struct {
	b        *builder
	composer *cursortranscript.Composer
	data     *cursortranscript.ComposerData
	session  *session.Session
	ids      traceprocessing.IDNamespace

	inputs      []*session.UserInput
	groups      []*ownedInput
	invocations []*session.ModelInvocation
	messages    []*session.AssistantMessage
	tools       []*session.ToolUse
	results     []*session.ToolResult
	touches     []*session.FileTouch
	attributes  []*session.SessionAttribute
	usage       []*session.TokenUsage
	vendorIDs   []session.VendorID

	toolByBubble  map[string]*toolEvidence
	ownerByBubble map[string]*ownedInput
	workingDir    string
	endedAt       *time.Time
	estimator     *tokenEstimator
}

func newComposerState(b *builder, composer *cursortranscript.Composer, spawn *session.SpawnLink) (*composerState, error) {
	data := composer.Data
	ids := traceprocessing.NewIDNamespace(Vendor, data.ComposerID)
	row := &session.Session{
		ID:      ids.Mint("session"),
		Outcome: session.OutcomeUnknown,
	}
	row.LinkSpawn(spawn)
	state := &composerState{
		b: b, composer: composer, data: data, session: row, ids: ids,
		toolByBubble:  map[string]*toolEvidence{},
		ownerByBubble: map[string]*ownedInput{},
	}
	state.vendorRow(row.ID, session.EntitySession, "composerId", data.ComposerID, composerKey(data.ComposerID))
	if composer.Header != nil && composer.Header.WorkspaceID != "" {
		state.vendorRow(row.ID, session.EntitySession, "workspaceId", composer.Header.WorkspaceID, "composerHeaders:"+data.ComposerID)
	}
	if err := state.applySessionFields(); err != nil {
		return nil, err
	}
	if err := state.addAttributes(); err != nil {
		return nil, err
	}
	return state, nil
}

func (f *composerState) vendorRow(ourID string, ourType session.EntityType, kind, vendorID, sourceRef string) {
	if vendorID == "" {
		return
	}
	ref := sourceRef
	f.vendorIDs = traceprocessing.AppendVendorID(f.vendorIDs, Vendor, f.session.ID, ourID, ourType, kind, vendorID, &ref)
}

func (f *composerState) applySessionFields() error {
	if at := epochMillis(f.data.CreatedAt); at != nil {
		f.session.StartedAt = at
	}
	if f.data.LastUpdatedAt != nil {
		f.endedAt = epochMillis(*f.data.LastUpdatedAt)
	}
	if f.endedAt == nil && len(f.composer.Bubbles) > 0 {
		at, err := bubbleTime(f.composer.Bubbles[len(f.composer.Bubbles)-1])
		if err != nil {
			return err
		}
		f.endedAt = at
	}
	f.session.EndedAt = f.endedAt
	f.session.Title = optional(f.data.Name)
	f.session.GitBranch = optional(f.data.CreatedOnBranch)
	identifier := f.data.WorkspaceIdentifier
	if f.composer.Header != nil && f.composer.Header.Value.WorkspaceIdentifier != nil {
		identifier = f.composer.Header.Value.WorkspaceIdentifier
	}
	if identifier != nil {
		f.workingDir = identifier.URI.FilePath()
		f.session.WorkingDir = optional(f.workingDir)
	}
	switch {
	case f.data.CommittedToBranch != "":
		f.session.Outcome = session.OutcomeCommitted
	case f.data.Status == "aborted":
		f.session.Outcome = session.OutcomeInterrupted
	default:
		f.session.Outcome = session.OutcomeUnknown
	}
	return nil
}

func (f *composerState) process() error {
	var active *ownedInput
	for _, bubble := range f.composer.Bubbles {
		f.b.report.Bubbles.Total++
		switch bubble.Type {
		case cursortranscript.BubbleTypeUser:
			row, err := f.addUserInput(bubble, len(f.inputs)+1)
			if err != nil {
				return err
			}
			active = &ownedInput{row: row, bubble: bubble}
			f.groups = append(f.groups, active)
			f.b.report.mapped()
			if bubble.Tool != nil {
				f.b.report.note("composer %s user bubble %s carries a tool record that is not mapped as a model consequence", f.data.ComposerID, bubble.BubbleID)
			}
		case cursortranscript.BubbleTypeAssistant:
			if active == nil {
				// No input owns this bubble, so it maps to nothing — but a real
				// token count is still usage the session paid for (§4.1).
				if bubble.TokenCount.HasUsage() {
					f.addTokenUsage(bubble, nil)
					f.b.report.mapped()
				} else {
					f.b.report.discard("unsupported assistant before first input")
				}
				continue
			}
			f.ownerByBubble[bubble.BubbleID] = active
			if f.accountAssistant(bubble) {
				active.bubbles = append(active.bubbles, bubble)
			}
		}
	}
	for range f.composer.Unlisted {
		f.b.report.Bubbles.Total++
		f.b.report.provenance()
	}
	f.estimator = newTokenEstimator(f.data, f.composer.Bubbles)
	for _, group := range f.groups {
		if err := f.processBatches(group); err != nil {
			return err
		}
	}
	f.enrichDiffs()
	if f.data.Status == "aborted" && len(f.inputs) > 0 {
		f.inputs[len(f.inputs)-1].Interrupted = true
	}
	return nil
}

// accountAssistant files one owned assistant bubble and reports whether it
// carries anything the mapping stores or assigns: text, a well-formed tool
// record, thinking, or token usage. A bubble carrying none of those maps to
// nothing (§4.2) and must stay out of the model batches, or it would open a
// model invocation that never happened.
func (f *composerState) accountAssistant(bubble *cursortranscript.Bubble) bool {
	switch {
	case bubble.Text != "" || (bubble.Tool != nil && !bubble.Tool.Degenerate()) || bubble.TokenCount.HasUsage():
		f.b.report.mapped()
		return true
	case len(nonNullRaw(bubble.Thinking)) > 0:
		f.b.report.provenance()
		return true
	case bubble.Tool != nil:
		f.b.report.discard("degenerate tool record")
		f.b.report.note("composer %s bubble %s has a degenerate tool record", f.data.ComposerID, bubble.BubbleID)
		return false
	default:
		f.b.report.discard("context/empty assistant bubble")
		return false
	}
}

func (f *composerState) flush() {
	traceprocessing.AppendSessionRows(f.b.graph, f.session, f.attributes, f.inputs, f.invocations, f.messages,
		f.tools, f.results, f.touches, f.usage, f.vendorIDs)
}

func (b *builder) finishReport() {
	rows := traceprocessing.TallyRows(b.graph)
	b.report.Sessions, b.report.Rows = rows.Sessions, rows.ReportRows()
	sort.Strings(b.report.Notes)
}

func epochMillis(ms int64) *time.Time {
	if ms == 0 {
		return nil
	}
	at := time.UnixMilli(ms).UTC()
	return &at
}

func bubbleTime(bubble *cursortranscript.Bubble) (*time.Time, error) {
	if bubble.CreatedAt == "" {
		return nil, nil
	}
	at, err := time.Parse(time.RFC3339Nano, bubble.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("bubble %s has invalid createdAt %q: %w", bubble.BubbleID, bubble.CreatedAt, err)
	}
	at = at.UTC()
	return &at, nil
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func composerKey(composerID string) string { return "composerData:" + composerID }

func nonNullRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}

func resolvedPath(workingDir, path string) string {
	target := sourcepath.Parse(path)
	if target.String() == "" || target.IsAbsolute() || !target.IsRepositoryRelative() || workingDir == "" {
		return target.String()
	}
	return sourcepath.Join(workingDir, target.String())
}
