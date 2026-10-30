package entiresession

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/baum-ai/baum/sessions/traceprocessing"

	"github.com/baum-ai/baum/sessions/claudesession"
	"github.com/baum-ai/baum/sessions/claudetranscript"
	"github.com/baum-ai/baum/sessions/codexsession"
	"github.com/baum-ai/baum/sessions/codextranscript"
	"github.com/baum-ai/baum/sessions/cursorsession"
	"github.com/baum-ai/baum/sessions/cursortranscript"
	"github.com/baum-ai/baum/sessions/entirecheckpoint"
	"github.com/baum-ai/baum/sessions/session"
)

// delegateFunc maps one snapshot with its native mapper. tasks are the
// checkpoint's subagents, shared by every snapshot in it.
type delegateFunc func(*entirecheckpoint.SessionSnapshot, []*entirecheckpoint.Task, traceprocessing.MappingProfile) (*session.Graph, *DelegateReport, error)

var delegates = map[string]delegateFunc{
	"Claude Code": delegateClaude,
	"Codex":       delegateCodex,
	"Cursor":      delegateCursor,
}

func delegateCursor(snapshot *entirecheckpoint.SessionSnapshot, _ []*entirecheckpoint.Task, profile traceprocessing.MappingProfile) (*session.Graph, *DelegateReport, error) {
	records, err := cursortranscript.DecodePortable(bytes.NewReader(snapshot.Full))
	if err != nil {
		return nil, nil, fmt.Errorf("decode embedded Cursor transcript: %w", err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, snapshot.Metadata.CreatedAt)
	if err != nil {
		return nil, nil, fmt.Errorf("parse Cursor snapshot created_at: %w", err)
	}
	graph, inner, err := cursorsession.BuildPortableWith(
		snapshot.Metadata.SessionID, "full.jsonl", records, createdAt.UTC(), profile,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("map embedded Cursor transcript: %w", err)
	}
	if !inner.Balanced() {
		return nil, nil, fmt.Errorf("embedded Cursor mapper report is unbalanced")
	}
	// The portable transcript names no model; Entire's session metadata does.
	// Keep the raw string, since cost normalization strips Cursor's affixes.
	if model := snapshot.Metadata.Model; profile == traceprocessing.Hosted && model != "" {
		for i := range graph.ModelInvocations {
			if graph.ModelInvocations[i].Model == nil || *graph.ModelInvocations[i].Model == "" {
				graph.ModelInvocations[i].Model = optionalString(model)
			}
		}
	}
	return graph, &DelegateReport{
		SessionID: snapshot.Metadata.SessionID, Agent: snapshot.Metadata.Agent, Cursor: inner,
	}, nil
}

func delegateClaude(snapshot *entirecheckpoint.SessionSnapshot, tasks []*entirecheckpoint.Task, profile traceprocessing.MappingProfile) (*session.Graph, *DelegateReport, error) {
	lines, torn, err := claudetranscript.Decode(bytes.NewReader(snapshot.Full))
	if err != nil {
		return nil, nil, fmt.Errorf("decode embedded Claude transcript: %w", err)
	}
	if torn {
		return nil, nil, fmt.Errorf("embedded Claude transcript has a truncated final record")
	}
	for _, line := range lines {
		if line.SessionID != "" && line.SessionID != snapshot.Metadata.SessionID {
			return nil, nil, fmt.Errorf("embedded Claude transcript names session %s, metadata names %s", line.SessionID, snapshot.Metadata.SessionID)
		}
	}
	file := &claudetranscript.File{
		Path: "full.jsonl", SessionID: snapshot.Metadata.SessionID, Lines: lines,
	}
	tree, err := claudetranscript.SingleFileTree(file)
	if err != nil {
		return nil, nil, err
	}
	var claimed []*entirecheckpoint.Task
	var warnings []string
	if profile == traceprocessing.Hosted {
		claimed, warnings = claimClaudeTasks(tree, tasks)
	}
	graph, inner, err := claudesession.BuildWith(tree, claudesession.BuildOptions{Profile: profile, Survey: true})
	if err != nil {
		return nil, nil, fmt.Errorf("map embedded Claude transcript: %w", err)
	}
	if !inner.Lines.Balanced() {
		return nil, nil, fmt.Errorf("embedded Claude mapper report is unbalanced: read %d, accounted %d", inner.Lines.Total, inner.Lines.Accounted())
	}
	report := &DelegateReport{
		SessionID: snapshot.Metadata.SessionID, Agent: snapshot.Metadata.Agent, Claude: inner, Warnings: warnings,
	}
	nested := nestedAgents(graph)
	for _, task := range claimed {
		report.Tasks = append(report.Tasks, DelegatedTask{Directory: task.Directory, Nested: nested[task.AgentID()]})
	}
	return graph, report, nil
}

// claimClaudeTasks adds to tree every task transcript whose lines name this
// session. The task directory is the spawning tool call, as a native sidecar
// would say; claudesession stores it only if that call is in the transcript.
func claimClaudeTasks(tree *claudetranscript.Tree, tasks []*entirecheckpoint.Task) ([]*entirecheckpoint.Task, []string) {
	var claimed []*entirecheckpoint.Task
	var warnings []string
	for _, task := range tasks {
		if !task.TranscriptPresent || task.Problem != "" || codexRollout(task.Transcript) {
			continue
		}
		label := "tasks/" + task.Directory + "/" + task.TranscriptName
		file, err := claudetranscript.DecodeSubagent(bytes.NewReader(task.Transcript), label, task.AgentID())
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("subagent transcript %s is not stored: %v", label, err))
			continue
		}
		if file.SessionID != tree.Main.SessionID {
			continue
		}
		file.Meta = &claudetranscript.AgentMeta{
			AgentType: task.Meta.SubagentType, Description: task.Meta.TaskDescription, ToolUseID: task.Directory,
		}
		if err := tree.AddSubagent(file); err != nil {
			warnings = append(warnings, fmt.Sprintf("subagent transcript %s is not stored: %v", label, err))
			continue
		}
		claimed = append(claimed, task)
	}
	return claimed, warnings
}

// codexRollout reports whether a task transcript is a Codex child rollout,
// which opens with session_meta.
func codexRollout(raw []byte) bool {
	first, _, _ := bytes.Cut(raw, []byte("\n"))
	var line struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(first, &line) == nil && line.Type == "session_meta"
}

// nestedAgents is the set of Claude agent ids stored as nested sessions.
func nestedAgents(graph *session.Graph) map[string]bool {
	nested := map[string]bool{}
	for _, row := range graph.VendorIDs {
		if row.OurType == session.EntitySession && row.VendorIDKind == "agentId" {
			nested[row.VendorID] = true
		}
	}
	return nested
}

func delegateCodex(snapshot *entirecheckpoint.SessionSnapshot, _ []*entirecheckpoint.Task, profile traceprocessing.MappingProfile) (*session.Graph, *DelegateReport, error) {
	file, err := codextranscript.Read(bytes.NewReader(snapshot.Full), "full.jsonl")
	if err != nil {
		return nil, nil, fmt.Errorf("decode embedded Codex transcript: %w", err)
	}
	if file.TornFinalLine {
		return nil, nil, fmt.Errorf("embedded Codex transcript has a truncated final record")
	}
	if file.Meta.ID != snapshot.Metadata.SessionID && file.Meta.SessionID != snapshot.Metadata.SessionID {
		return nil, nil, fmt.Errorf("embedded Codex transcript names thread %s/session %s, metadata names %s", file.Meta.ID, file.Meta.SessionID, snapshot.Metadata.SessionID)
	}
	tree, err := codextranscript.SingleFileTree(file)
	if err != nil {
		return nil, nil, err
	}
	graph, inner, err := codexsession.BuildWith(tree, profile)
	if err != nil {
		return nil, nil, fmt.Errorf("map embedded Codex transcript: %w", err)
	}
	if !inner.Records.Balanced() || inner.UnsupportedTurns != 0 {
		return nil, nil, fmt.Errorf("embedded Codex mapper report is unsupported or unbalanced: read %d, accounted %d, unsupported turns %d", inner.Records.Total, inner.Records.Accounted(), inner.UnsupportedTurns)
	}
	// A checkpoint whose transcript has a record the mapper would leave out
	// falls back to its compact transcript, as one that failed to decode did.
	if inner.UnknownRecords != 0 {
		return nil, nil, fmt.Errorf("embedded Codex transcript has %d records of unknown type", inner.UnknownRecords)
	}
	return graph, &DelegateReport{SessionID: snapshot.Metadata.SessionID, Agent: snapshot.Metadata.Agent, Codex: inner}, nil
}
