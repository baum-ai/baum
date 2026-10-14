package entiresession

import (
	"bytes"
	"fmt"
	"time"

	"github.com/baumai/baum/sessions/claudesession"
	"github.com/baumai/baum/sessions/claudetranscript"
	"github.com/baumai/baum/sessions/codexsession"
	"github.com/baumai/baum/sessions/codextranscript"
	"github.com/baumai/baum/sessions/cursorsession"
	"github.com/baumai/baum/sessions/cursortranscript"
	"github.com/baumai/baum/sessions/entirecheckpoint"
	"github.com/baumai/baum/sessions/session"
)

type delegateFunc func(*entirecheckpoint.SessionSnapshot) (*session.Graph, *DelegateReport, error)

var delegates = map[string]delegateFunc{
	"Claude Code": delegateClaude,
	"Codex":       delegateCodex,
	"Cursor":      delegateCursor,
}

func delegateCursor(snapshot *entirecheckpoint.SessionSnapshot) (*session.Graph, *DelegateReport, error) {
	records, err := cursortranscript.DecodePortable(bytes.NewReader(snapshot.Full))
	if err != nil {
		return nil, nil, fmt.Errorf("decode embedded Cursor transcript: %w", err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, snapshot.Metadata.CreatedAt)
	if err != nil {
		return nil, nil, fmt.Errorf("parse Cursor snapshot created_at: %w", err)
	}
	graph, inner, err := cursorsession.BuildPortable(
		snapshot.Metadata.SessionID, "full.jsonl", records, createdAt.UTC(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("map embedded Cursor transcript: %w", err)
	}
	if !inner.Balanced() {
		return nil, nil, fmt.Errorf("embedded Cursor mapper report is unbalanced")
	}
	return graph, &DelegateReport{
		SessionID: snapshot.Metadata.SessionID, Agent: snapshot.Metadata.Agent, Cursor: inner,
	}, nil
}

func delegateClaude(snapshot *entirecheckpoint.SessionSnapshot) (*session.Graph, *DelegateReport, error) {
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
	graph, inner, err := claudesession.Build(tree)
	if err != nil {
		return nil, nil, fmt.Errorf("map embedded Claude transcript: %w", err)
	}
	if !inner.Lines.Balanced() {
		return nil, nil, fmt.Errorf("embedded Claude mapper report is unbalanced: read %d, accounted %d", inner.Lines.Total, inner.Lines.Accounted())
	}
	return graph, &DelegateReport{SessionID: snapshot.Metadata.SessionID, Agent: snapshot.Metadata.Agent, Claude: inner}, nil
}

func delegateCodex(snapshot *entirecheckpoint.SessionSnapshot) (*session.Graph, *DelegateReport, error) {
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
	graph, inner, err := codexsession.Build(tree)
	if err != nil {
		return nil, nil, fmt.Errorf("map embedded Codex transcript: %w", err)
	}
	if !inner.Records.Balanced() || inner.UnsupportedTurns != 0 {
		return nil, nil, fmt.Errorf("embedded Codex mapper report is unsupported or unbalanced: read %d, accounted %d, unsupported turns %d", inner.Records.Total, inner.Records.Accounted(), inner.UnsupportedTurns)
	}
	return graph, &DelegateReport{SessionID: snapshot.Metadata.SessionID, Agent: snapshot.Metadata.Agent, Codex: inner}, nil
}
