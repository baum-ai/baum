package codexsession

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/lastpersonlabs/baum/sessions/codextranscript"
	"github.com/lastpersonlabs/baum/sessions/session"
	"github.com/lastpersonlabs/baum/sessions/traceprocessing"
)

const Vendor = "codex"

// Build maps one decoded root tree in memory. It returns a graph even when
// unsupported turns were found so the debug CLI can explain them; callers must
// refuse persistence unless the report is balanced and UnsupportedTurns is zero.
func Build(tree *codextranscript.Tree) (*session.Graph, *Report, error) {
	if tree == nil || tree.Root == nil {
		return nil, nil, fmt.Errorf("building Codex session: no root rollout")
	}
	if err := validateTree(tree); err != nil {
		return nil, nil, err
	}
	b := &builder{
		tree:       tree,
		graph:      &session.Graph{},
		report:     &Report{},
		byThread:   map[string]*threadState{},
		toolByCall: map[string]*toolEvidence{},
		toolByID:   map[string]*toolEvidence{},
		visited:    map[string]bool{},
	}
	if err := b.ingest(tree.Root, nil); err != nil {
		return nil, nil, err
	}
	for _, child := range tree.Children {
		if !b.visited[child.Meta.ID] {
			return nil, nil, fmt.Errorf("child rollout %s was not reached from its parent", child.Path)
		}
	}
	b.finalizeTools()
	for _, state := range b.order {
		state.finish()
		state.flush()
	}
	b.finishReport()
	return b.graph, b.report, nil
}

type builder struct {
	tree       *codextranscript.Tree
	graph      *session.Graph
	report     *Report
	byThread   map[string]*threadState
	toolByCall map[string]*toolEvidence
	toolByID   map[string]*toolEvidence
	visited    map[string]bool
	order      []*threadState
}

func (b *builder) ingest(file *codextranscript.File, spawn *session.SpawnLink) error {
	if b.visited[file.Meta.ID] {
		return fmt.Errorf("thread %s is reached more than once", file.Meta.ID)
	}
	b.visited[file.Meta.ID] = true
	state := newThreadState(b, file, spawn)
	b.byThread[file.Meta.ID] = state
	b.order = append(b.order, state)
	if file.TornFinalLine {
		b.report.Records.TornFinal++
	}

	boundary, err := b.ownedBoundary(file)
	if err != nil {
		return err
	}
	for i, line := range file.Lines {
		b.report.Records.Total++
		if i > 0 && line.Ordinal < boundary {
			b.report.discard("inherited history")
			continue
		}
		var next *codextranscript.Line
		if i+1 < len(file.Lines) {
			next = &file.Lines[i+1]
		}
		category, reason, err := state.process(line, next)
		if err != nil {
			return fmt.Errorf("%s line %d: %w", filepath.Base(file.Path), line.Ordinal, err)
		}
		b.account(category, reason)
	}
	for _, child := range b.childrenOf(file.Meta.ID) {
		link, err := state.resolveSpawn(child)
		if err != nil {
			return err
		}
		if err := b.ingest(child, link); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) childrenOf(parentThreadID string) []*codextranscript.File {
	var out []*codextranscript.File
	for _, child := range b.tree.Children {
		if child.Meta.ParentThreadID == parentThreadID {
			out = append(out, child)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Meta.ID < out[j].Meta.ID })
	return out
}

func (b *builder) ownedBoundary(file *codextranscript.File) (int, error) {
	if file == b.tree.Root {
		return 1, nil
	}
	if start := file.Meta.SubagentHistoryStartOrdinal; start != nil {
		if *start < 2 || *start > len(file.Lines)+1 {
			return 0, fmt.Errorf("child rollout %s has invalid subagent_history_start_ordinal %d", file.Path, *start)
		}
		return *start, nil
	}
	fork := b.tree.Rollout(file.Meta.ForkedFromID)
	if fork == nil {
		return 0, fmt.Errorf("child rollout %s names missing fork source %s", file.Path, file.Meta.ForkedFromID)
	}
	forkTurns := map[string]bool{}
	for _, line := range fork.Lines {
		if line.EventMsg != nil && line.EventMsg.TaskStarted != nil {
			forkTurns[line.EventMsg.TaskStarted.TurnID] = true
		}
	}
	for _, line := range file.Lines[1:] {
		if line.EventMsg != nil && line.EventMsg.TaskStarted != nil && !forkTurns[line.EventMsg.TaskStarted.TurnID] {
			return line.Ordinal, nil
		}
	}
	return 0, fmt.Errorf("child rollout %s has no provable own-history boundary", file.Path)
}

func validateTree(tree *codextranscript.Tree) error {
	byID := map[string]*codextranscript.File{tree.Root.Meta.ID: tree.Root}
	for _, child := range tree.Children {
		if child.Meta.SessionID != tree.Root.Meta.SessionID {
			return fmt.Errorf("child rollout %s reports session %s, want %s", child.Path, child.Meta.SessionID, tree.Root.Meta.SessionID)
		}
		if byID[child.Meta.ID] != nil {
			return fmt.Errorf("thread %s is listed more than once", child.Meta.ID)
		}
		byID[child.Meta.ID] = child
	}
	for _, child := range tree.Children {
		seen, entry, depth := map[string]bool{}, child, 0
		for entry != tree.Root {
			if seen[entry.Meta.ID] {
				return fmt.Errorf("child rollout %s is part of a parent cycle", child.Path)
			}
			seen[entry.Meta.ID], depth = true, depth+1
			spawn := entry.Meta.Source.ThreadSpawn
			if spawn == nil || spawn.ParentThreadID != entry.Meta.ParentThreadID {
				return fmt.Errorf("child rollout %s has disagreeing parent links", entry.Path)
			}
			parentID, childPath := entry.Meta.ParentThreadID, entry.Path
			entry = byID[parentID]
			if entry == nil {
				return fmt.Errorf("child rollout %s names missing parent %s", childPath, parentID)
			}
		}
		if spawn := child.Meta.Source.ThreadSpawn; spawn.Depth != 0 && spawn.Depth != depth {
			return fmt.Errorf("child rollout %s reports depth %d, want %d", child.Path, spawn.Depth, depth)
		}
	}
	return nil
}

type accountCategory uint8

const (
	accountMapped accountCategory = iota
	accountAttribute
	accountMerged
	accountDiscarded
)

func (b *builder) account(category accountCategory, reason string) {
	switch category {
	case accountMapped:
		b.report.Records.Mapped++
	case accountAttribute:
		b.report.Records.AttributeBearing++
	case accountMerged:
		b.report.Records.Merged++
	case accountDiscarded:
		b.report.discard(reason)
	}
}

type turnState struct {
	id          string
	unaccounted []*session.ModelInvocation
	lastInput   *session.UserInput
}

type batchState struct {
	row *session.ModelInvocation
}

type activityEvidence struct {
	line     codextranscript.Line
	activity codextranscript.SubAgentActivity
}

type pendingHumanInput struct {
	row  *session.UserInput
	line int
}

type threadState struct {
	b       *builder
	file    *codextranscript.File
	meta    *codextranscript.SessionMeta
	session *session.Session
	ids     traceprocessing.IDNamespace

	inputs      []*session.UserInput
	invocations []*session.ModelInvocation
	messages    []*session.AssistantMessage
	tools       []*session.ToolUse
	results     []*session.ToolResult
	touches     []*session.FileTouch
	attributes  []*session.SessionAttribute
	usage       []*session.TokenUsage
	vendorIDs   []session.VendorID

	openTurn        string
	lastClosedTurn  string
	turns           map[string]*turnState
	activeInput     *session.UserInput
	inputLine       int
	inputOrdinal    int
	inputTurn       map[string]string
	batch           *batchState
	batchNumber     int
	lastByInput     map[string]string
	latestContext   map[string]json.RawMessage
	serviceTier     string // service_tier of the latest thread_settings_applied; empty until one names it
	latestAttribute map[string]json.RawMessage
	attributeTime   map[string]time.Time
	pendingInter    *codextranscript.InterAgentMeta
	pendingHuman    []pendingHumanInput
	// hasUserMessageEvents is true when the rollout writes user_message
	// events, which then carry the human prompts instead of item_completed.
	hasUserMessageEvents bool
	// hasTokenUsageRecords is true when the rollout writes token_usage_record
	// lines, which then are the only token source; token_count events are
	// discarded as superseded.
	hasTokenUsageRecords bool
	// lastTotal is the previous token_count total of the thread, for repeat
	// detection in rollouts without token_usage_records.
	lastTotal       *codextranscript.Usage
	unsupported     map[string]bool
	unsupportedCall map[string]bool
	activities      []activityEvidence
	touchByToolPath map[string]*session.FileTouch

	times            traceprocessing.TimeBounds
	workingDir       string
	gitBranch        string
	finalTurnAborted bool
	committed        bool
}

func newThreadState(b *builder, file *codextranscript.File, spawn *session.SpawnLink) *threadState {
	meta := file.Meta
	ids := traceprocessing.NewIDNamespace(Vendor, meta.ID)
	row := &session.Session{ID: ids.Mint("session"), Outcome: session.OutcomeUnknown}
	row.LinkSpawn(spawn)
	state := &threadState{
		b: b, file: file, meta: meta, session: row, ids: ids,
		turns: map[string]*turnState{}, lastByInput: map[string]string{},
		inputTurn:     map[string]string{},
		latestContext: map[string]json.RawMessage{}, latestAttribute: map[string]json.RawMessage{},
		attributeTime: map[string]time.Time{}, unsupported: map[string]bool{},
		unsupportedCall: map[string]bool{}, touchByToolPath: map[string]*session.FileTouch{},
		workingDir: meta.CWD,
	}
	for _, line := range file.Lines {
		if line.EventMsg != nil && line.EventMsg.UserMessage != nil {
			state.hasUserMessageEvents = true
		}
		if line.TokenUsageRecord != nil {
			state.hasTokenUsageRecords = true
		}
	}
	if meta.Git != nil {
		state.gitBranch = meta.Git.Branch
	}
	state.vendorRow(row.ID, session.EntitySession, "thread_id", meta.ID, 1)
	if file == b.tree.Root && meta.SessionID != meta.ID {
		state.vendorRow(row.ID, session.EntitySession, "session_id", meta.SessionID, 1)
	}
	if file != b.tree.Root && meta.AgentPath != "" {
		state.vendorRow(row.ID, session.EntitySession, "agent_path", meta.AgentPath, 1)
	}
	return state
}

func (f *threadState) vendorRow(ourID string, ourType session.EntityType, kind, vendorID string, ordinal int) {
	if vendorID == "" {
		return
	}
	ref := filepath.Base(f.file.Path)
	if ordinal > 0 {
		ref = fmt.Sprintf("%s#%d", ref, ordinal)
	}
	f.vendorIDs = traceprocessing.AppendVendorID(f.vendorIDs, Vendor, f.session.ID, ourID, ourType, kind, vendorID, &ref)
}

func (f *threadState) finish() {
	f.session.StartedAt, f.session.EndedAt = f.times.Start, f.times.End
	f.session.WorkingDir = optional(f.workingDir)
	f.session.GitBranch = optional(f.gitBranch)
	switch {
	case f.committed:
		f.session.Outcome = session.OutcomeCommitted
	case f.finalTurnAborted:
		f.session.Outcome = session.OutcomeInterrupted
	default:
		f.session.Outcome = session.OutcomeUnknown
	}
}

func (f *threadState) flush() {
	traceprocessing.AppendSessionRows(f.b.graph, f.session, f.attributes, f.inputs, f.invocations, f.messages,
		f.tools, f.results, f.touches, f.usage, f.vendorIDs)
}

func (b *builder) finishReport() {
	g, r := b.graph, b.report
	rows := traceprocessing.TallyRows(g)
	r.Sessions, r.Rows = rows.Sessions, rows.ReportRows()
	r.Transcript = codextranscript.Survey(b.tree.Files())
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
