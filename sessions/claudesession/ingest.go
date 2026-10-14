package claudesession

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/baumai/baum/sessions/claudetranscript"
	"github.com/baumai/baum/sessions/session"
	"github.com/baumai/baum/sessions/traceprocessing"
)

const Vendor = "claude_code"

// Build maps a transcript tree into one session tree. Its report includes the
// transcript's shape (versions, content shapes, keys the decoder left unread).
func Build(tree *claudetranscript.Tree) (*session.Graph, *Report, error) {
	return BuildWith(tree, BuildOptions{Survey: true})
}

// BuildOptions choose what Build reports beyond the graph and its accounting.
type BuildOptions struct {
	// Survey fills Report.Transcript, which re-reads every line to describe
	// the transcript's shape: a diagnostic, and a large part of the cost of
	// mapping a long session.
	Survey bool
}

// BuildWith is Build with options.
func BuildWith(tree *claudetranscript.Tree, opts BuildOptions) (*session.Graph, *Report, error) {
	if tree == nil || tree.Main == nil {
		return nil, nil, fmt.Errorf("building session: no transcript")
	}

	b := &builder{
		tree:    tree,
		graph:   &session.Graph{},
		report:  &Report{},
		visited: map[string]bool{},
	}
	if err := b.ingestFile(tree.Main, nil); err != nil {
		return nil, nil, err
	}

	// A nested session needs the tool call that created it, so a subagent
	// transcript nothing spawned cannot be stored.
	for _, file := range tree.Subagents {
		if !b.visited[file.AgentID] {
			b.report.note("subagent %s was not reached from any tool call and is not stored", file.AgentID)
			b.report.Lines.Total += len(file.Lines)
			b.report.discardN("unreachable subagent transcript", len(file.Lines))
		}
	}

	attributeNotifications(b.graph)

	rows := traceprocessing.TallyRows(b.graph)
	b.report.Sessions, b.report.Rows = rows.Sessions, rowsFrom(rows)
	if opts.Survey {
		b.report.Transcript = claudetranscript.Survey(tree.Files())
	}
	return b.graph, b.report, nil
}

// builder holds what is true for a whole tree.
type builder struct {
	tree    *claudetranscript.Tree
	graph   *session.Graph
	report  *Report
	visited map[string]bool
}

// ingestFile maps one transcript into one session, then descends into the
// transcripts its tool calls spawned.
func (b *builder) ingestFile(file *claudetranscript.File, spawn *session.SpawnLink) error {
	if file.AgentID != "" {
		if b.visited[file.AgentID] {
			return fmt.Errorf("subagent %s is spawned more than once", file.AgentID)
		}
		b.visited[file.AgentID] = true
	}

	state := newFileState(b, file, spawn)
	if file.TornFinalLine {
		b.report.Lines.TornFinal++
	}

	for _, line := range file.Lines {
		b.report.Lines.Total++
		if err := state.line(line); err != nil {
			return fmt.Errorf("%s line %d: %w", filepath.Base(file.Path), line.Ordinal, err)
		}
	}

	state.finish()
	state.flush()

	for _, spawned := range state.spawns {
		child := b.tree.SubagentForToolUse(spawned.vendorToolUseID)
		if child == nil {
			continue
		}
		if err := b.ingestFile(child, &session.SpawnLink{
			ParentSessionID: state.sessionID,
			ToolUseID:       spawned.ourToolUseID,
		}); err != nil {
			return err
		}
	}
	return nil
}

type spawnedTranscript struct {
	vendorToolUseID string
	ourToolUseID    string
}

type fileState struct {
	b       *builder
	file    *claudetranscript.File
	session *session.Session
	// ids is scoped by the vendor, top-level session, and agent.
	ids       traceprocessing.IDNamespace
	sessionID string

	owners         map[string]string
	promptOrdinal  map[string]int
	promptOrder    []string
	currentPrompt  string
	lastAttachable *session.UserInput

	inputs      []*session.UserInput
	invocations []*session.ModelInvocation
	messages    []*session.AssistantMessage
	toolUses    []*session.ToolUse
	results     []*session.ToolResult
	touches     []*session.FileTouch
	vendorIDs   []session.VendorID

	byMessageID         map[string]*session.ModelInvocation
	lastByInput         map[string]string
	byToolUseID         map[string]*session.ToolUse
	resultByTool        map[string]*session.ToolResult
	touchByTool         map[string][]*session.FileTouch
	toolUseVendor       map[string]string
	placeholders        map[string]bool
	mergedNotifications map[string]bool
	queuedByIdentity    map[string]*queuedInput
	queuedByText        map[string][]*queuedInput

	spawns   []spawnedTranscript
	pending  []pendingAttribute
	seenNote map[string]bool

	ordinal          int
	times            traceprocessing.TimeBounds
	firstAnyTime     *time.Time
	lastEnvelopeTime *time.Time
	workingDir       string
	gitBranch        string
	latestCWD        string
	latestGitBranch  string
	title            string
	committed        bool
	lastToolStatus   session.ToolStatus
}

func newFileState(b *builder, file *claudetranscript.File, spawn *session.SpawnLink) *fileState {
	agentID := file.AgentID
	ids := traceprocessing.NewIDNamespace(Vendor, file.SessionID, agentID)

	row := &session.Session{
		ID:      ids.Mint("session"),
		Outcome: session.OutcomeUnknown,
	}
	row.LinkSpawn(spawn)

	state := &fileState{
		b:             b,
		file:          file,
		session:       row,
		ids:           ids,
		sessionID:     row.ID,
		owners:        map[string]string{},
		promptOrdinal: map[string]int{},
		byMessageID:   map[string]*session.ModelInvocation{},
		lastByInput:   map[string]string{},
		byToolUseID:   map[string]*session.ToolUse{},
		resultByTool:  map[string]*session.ToolResult{},
		touchByTool:   map[string][]*session.FileTouch{},
		toolUseVendor: map[string]string{},

		placeholders:        map[string]bool{},
		mergedNotifications: map[string]bool{},
		queuedByIdentity:    map[string]*queuedInput{},
		queuedByText:        map[string][]*queuedInput{},
		seenNote:            map[string]bool{},
	}

	kind, vendorID := "sessionId", file.SessionID
	if agentID != "" {
		kind, vendorID = "agentId", agentID
	}
	state.vendorRow(row.ID, session.EntitySession, kind, vendorID, 0)
	return state
}

// vendorRow records where one of our rows came from.
func (f *fileState) vendorRow(ourID string, ourType session.EntityType, kind, vendorID string, ordinal int) {
	if vendorID == "" {
		return
	}
	ref := filepath.Base(f.file.Path)
	if ordinal > 0 {
		ref = fmt.Sprintf("%s#%d", ref, ordinal)
	}
	f.vendorIDs = traceprocessing.AppendVendorID(f.vendorIDs, Vendor, f.sessionID, ourID, ourType, kind, vendorID, &ref)
}

// blockVendorRow records where a row that came from one block of a line came from.
func (f *fileState) blockVendorRow(
	ourID string,
	ourType session.EntityType,
	kind, lineID string,
	index, ordinal int,
) {
	if lineID == "" {
		return
	}
	f.vendorRow(ourID, ourType, kind+".block", fmt.Sprintf("%s:%d", lineID, index), ordinal)
}

func (f *fileState) line(line claudetranscript.Line) error {
	f.observe(line)

	switch line.Class {
	case claudetranscript.ClassUser:
		return f.userLine(line)
	case claudetranscript.ClassAssistant:
		return f.assistantLine(line)
	case claudetranscript.ClassControl:
		return f.controlLine(line)
	case claudetranscript.ClassSystem:
		subtype := line.Subtype
		if subtype == "" {
			subtype = "(none)"
		}
		f.b.report.discard("system/" + subtype)
	case claudetranscript.ClassAttachment:
		f.attachmentLine(line)
	case claudetranscript.ClassProgress:
		f.b.report.discard("progress")
	default:
		f.b.report.discard("unrecognised type/" + line.Type)
	}
	return nil
}

// observe collects the ambient facts every envelope line repeats: when the session ran, where, and on what branch.
func (f *fileState) observe(line claudetranscript.Line) {
	if line.Timestamp != nil && f.firstAnyTime == nil {
		f.firstAnyTime = line.Timestamp
	}
	if !line.Envelope() {
		return
	}
	f.lastEnvelopeTime = line.Timestamp
	f.times.Observe(line.Timestamp)
	if line.CWD != "" && line.CWD != f.latestCWD {
		f.latestCWD = line.CWD
		f.attributeValue(line, "cwd", jsonString(line.CWD))
	}
	if line.GitBranch != "" && line.GitBranch != f.latestGitBranch {
		f.latestGitBranch = line.GitBranch
		f.attributeValue(line, "gitBranch", jsonString(line.GitBranch))
	}
	if f.workingDir == "" {
		f.workingDir = line.CWD
	}
	if f.gitBranch == "" {
		f.gitBranch = line.GitBranch
	}
}

func (f *fileState) finish() {
	f.session.StartedAt = f.times.Start
	f.session.EndedAt = f.times.End
	f.session.WorkingDir = optional(f.workingDir)
	f.session.GitBranch = optional(f.gitBranch)
	f.session.Title = optional(f.title)
	f.session.Outcome = f.outcome()
	f.resolveAttributes()
}

func (f *fileState) outcome() session.Outcome {
	if f.committed {
		return session.OutcomeCommitted
	}
	if f.lastToolStatus == session.StatusInterrupted {
		return session.OutcomeInterrupted
	}
	if len(f.inputs) > 0 && f.inputs[len(f.inputs)-1].Interrupted {
		return session.OutcomeInterrupted
	}
	return session.OutcomeUnknown
}

// flush moves this transcript's rows into the graph.
func (f *fileState) flush() {
	for _, promptID := range f.promptOrder {
		f.vendorRow(f.owners[promptID], session.EntityUserInput, "promptId", promptID, f.promptOrdinal[promptID])
	}

	traceprocessing.AppendSessionRows(f.b.graph, f.session, nil, f.inputs, f.invocations, f.messages,
		f.toolUses, f.results, f.touches, nil, f.vendorIDs)
}

// ownerInput is the ask a model reply belongs to.
func (f *fileState) ownerInput() (string, error) {
	if id, owned := f.owners[f.currentPrompt]; owned {
		return id, nil
	}
	if f.lastAttachable != nil {
		return f.lastAttachable.ID, nil
	}
	if len(f.inputs) > 0 {
		return f.inputs[len(f.inputs)-1].ID, nil
	}
	return "", fmt.Errorf("a model reply appears before any ask in this transcript")
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func jsonObjectOr(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || raw[0] != '{' {
		return json.RawMessage(`{}`)
	}
	return raw
}
