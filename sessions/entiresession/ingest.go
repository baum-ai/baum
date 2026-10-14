// Package entiresession maps Entire checkpoint snapshots into shared session
// graphs, delegating native transcripts where a mapper exists.
package entiresession

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/baum-ai/baum/sessions/entirecheckpoint"
	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/traceprocessing"
)

type FidelityRank int

const (
	FidelityDelegated    FidelityRank = 2
	FidelityCompact      FidelityRank = 3
	FidelityMetadataOnly FidelityRank = 4
)

type Result struct {
	Graph     *session.Graph `json:"graph"`
	SessionID string         `json:"session_id"`
	// Vendor is the wrapped agent's canonical slug, never "entire" unless the
	// checkpoint named no agent at all. The ingest layer stores it as the
	// session's vendor, so how Entire spells an agent stays in this package.
	Vendor           string    `json:"vendor,omitempty"`
	CheckpointID     string    `json:"checkpoint_id"`
	CreatedAt        time.Time `json:"created_at"`
	CheckpointsCount int       `json:"checkpoints_count,omitempty"`
	// SourceBlobHash is the checkpoint's recorded hash of the native
	// transcript, empty when the snapshot carries none.
	SourceBlobHash string       `json:"source_blob_hash,omitempty"`
	Fidelity       FidelityRank `json:"fidelity_rank"`
	Warnings       []string     `json:"warnings,omitempty"`
	Error          string       `json:"error,omitempty"`
	Err            error        `json:"-"`
	// Envelope is the Entire-owned metadata layer, already applied to Graph. A
	// lower-fidelity arrival cannot replace a tree an upload owns, and this is
	// what it contributes instead.
	// Not serialized: it is a view of rows the graph above already
	// carries.
	Envelope *EnvelopeData `json:"-"`
}

// vendorSlugs maps Entire's agent spelling to the canonical vendor slug the
// wrapped agent's own mapper uses.
var vendorSlugs = map[string]string{
	"Claude Code": "claude_code",
	"Codex":       "codex",
	"Cursor":      "cursor",
}

// vendorSlug answers what ran. An agent Entire names but we have no mapper for
// still reports itself rather than collapsing to "entire": the checkpoint is
// the recorder, not the agent, and a coauthor trailer names the agent.
func vendorSlug(agent string) string {
	if slug, known := vendorSlugs[agent]; known {
		return slug
	}
	normalized := strings.ToLower(strings.TrimSpace(agent))
	normalized = strings.ReplaceAll(normalized, " ", "_")
	if normalized == "" {
		return "entire"
	}
	return normalized
}

// Build maps every wrapped session in one checkpoint. Snapshot selection is a
// caller concern: Result exposes the stable identity and created_at needed to
// choose the latest snapshot before persistence.
func Build(checkpoint *entirecheckpoint.Checkpoint) ([]Result, *Report, error) {
	if checkpoint == nil {
		return nil, nil, fmt.Errorf("building Entire sessions: nil checkpoint")
	}
	report := &Report{}
	accountCheckpointMetadata(report, checkpoint.Metadata)
	results := make([]Result, 0, len(checkpoint.Sessions))
	for _, snapshot := range checkpoint.Sessions {
		results = append(results, mapSnapshot(report, checkpoint.Metadata.CheckpointID, snapshot, checkpoint.Sessions))
	}
	report.Sessions = len(results)
	report.Shape = entirecheckpoint.Survey([]*entirecheckpoint.Checkpoint{checkpoint})
	if report.Envelope.FilesUnrepresentable > 0 {
		report.Findings = append(report.Findings, "metadata files_touched cannot be emitted without inventing a user_input_id required by the current session schema")
	}
	if report.Envelope.FilesShared > 0 {
		report.Findings = append(report.Findings, "metadata files_touched identical across sessions is the checkpoint's diff and is attributed to no session")
	}
	return results, report, nil
}

func mapSnapshot(report *Report, checkpointID string, snapshot *entirecheckpoint.SessionSnapshot, siblings []*entirecheckpoint.SessionSnapshot) Result {
	if snapshot == nil {
		return failSnapshot(report, nil, checkpointID, "nil_snapshot", fmt.Errorf("nil session snapshot"))
	}
	if err := prepareSnapshotMetadata(snapshot, checkpointID); err != nil {
		return failSnapshot(report, snapshot, checkpointID, "metadata", err)
	}
	smeta := &snapshot.Metadata
	createdAt, err := time.Parse(time.RFC3339Nano, smeta.CreatedAt)
	if err != nil {
		return failSnapshot(report, snapshot, checkpointID, "metadata", fmt.Errorf("invalid created_at %q: %w", smeta.CreatedAt, err))
	}
	createdAt = createdAt.UTC()

	graph, delegated, fidelity, sourceWarnings, err := buildSource(snapshot)
	if err != nil {
		return failSnapshot(report, snapshot, checkpointID, "source", err)
	}
	envelope, err := Envelope(graph, snapshot, checkpointID, createdAt)
	if err != nil {
		return failSnapshot(report, snapshot, checkpointID, "envelope", err)
	}
	if sharedFilesTouched(snapshot, siblings) {
		envelope.MetadataFilesShared = true
		envelope.Report.FilesShared = len(envelope.MetadataFiles)
	}
	envelope.Apply(graph)

	warnings := append([]string(nil), sourceWarnings...)
	if snapshot.Hash.Mismatch {
		warnings = append(warnings, fmt.Sprintf("full.jsonl hash %s does not match recorded content hash %s", snapshot.Hash.Actual, snapshot.Hash.Recorded))
	}
	if err := session.Validate(graph); err != nil {
		return failSnapshot(report, snapshot, checkpointID, "validation", err)
	}

	accountMappedSnapshot(report, snapshot, delegated, fidelity, envelope, sourceWarnings)
	report.addRows(traceprocessing.TallyRows(graph))
	return Result{
		Graph: graph, SessionID: smeta.SessionID, Vendor: vendorSlug(smeta.Agent),
		CheckpointID: checkpointID, CreatedAt: createdAt, CheckpointsCount: smeta.CheckpointsCount,
		SourceBlobHash: snapshot.ContentHash, Fidelity: fidelity, Warnings: warnings, Envelope: envelope,
	}
}

func prepareSnapshotMetadata(snapshot *entirecheckpoint.SessionSnapshot, checkpointID string) error {
	smeta := &snapshot.Metadata
	if smeta.CheckpointID != "" && smeta.CheckpointID != checkpointID {
		return fmt.Errorf("checkpoint metadata id %s disagrees with session metadata id %s", checkpointID, smeta.CheckpointID)
	}
	if smeta.CheckpointID == "" {
		smeta.CheckpointID = checkpointID
	}
	if smeta.SessionID == "" {
		return fmt.Errorf("session %d has no session_id", snapshot.Index)
	}
	return nil
}

func accountMappedSnapshot(
	report *Report,
	snapshot *entirecheckpoint.SessionSnapshot,
	delegated *DelegateReport,
	fidelity FidelityRank,
	envelope *EnvelopeData,
	sourceWarnings []string,
) {
	if delegated != nil {
		report.Delegated = append(report.Delegated, *delegated)
		accountDelegatedCompact(report, snapshot)
	} else if fidelity == FidelityCompact {
		accountMappedCompact(report, snapshot)
	}
	mergeEnvelopeReport(&report.Envelope, envelope.Report)
	accountSessionMetadata(report, snapshot.Metadata)
	for _, warning := range sourceWarnings {
		report.Findings = append(report.Findings, fmt.Sprintf("session %s: %s", snapshot.Metadata.SessionID, warning))
	}
	if snapshot.Hash.Checked {
		report.Envelope.HashChecked++
	}
	if snapshot.Hash.Mismatch {
		report.Envelope.HashMismatches++
	}
}

func failSnapshot(report *Report, snapshot *entirecheckpoint.SessionSnapshot, checkpointID, reason string, cause error) Result {
	sessionID := ""
	result := Result{CheckpointID: checkpointID}
	if snapshot != nil {
		sessionID = snapshot.Metadata.SessionID
		result.SessionID = sessionID
		if createdAt, err := time.Parse(time.RFC3339Nano, snapshot.Metadata.CreatedAt); err == nil {
			result.CreatedAt = createdAt.UTC()
		}
		accountFailedSnapshot(report, snapshot, "failed/"+reason)
	}
	err := fmt.Errorf("checkpoint %s session %s: %w", checkpointID, sessionID, cause)
	report.FailedSnapshots++
	report.Findings = append(report.Findings, err.Error())
	result.Error, result.Err = err.Error(), err
	return result
}

func accountFailedSnapshot(report *Report, snapshot *entirecheckpoint.SessionSnapshot, reason string) {
	for range snapshot.Metadata.Keys {
		report.Metadata.Total++
		report.Metadata.discard(reason, 1)
	}
	for _, record := range snapshot.Compact {
		report.Compact.Lines.Total++
		report.Compact.Lines.discard(reason, 1)
		for range record.Content {
			report.Compact.Blocks.Total++
			report.Compact.Blocks.discard(reason, 1)
		}
	}
}

func buildSource(snapshot *entirecheckpoint.SessionSnapshot) (*session.Graph, *DelegateReport, FidelityRank, []string, error) {
	if snapshot.FullPresent {
		if delegate := delegates[snapshot.Metadata.Agent]; delegate != nil {
			graph, inner, err := delegate(snapshot)
			if err == nil {
				return graph, inner, FidelityDelegated, nil, nil
			}
			if !snapshot.CompactPresent {
				return nil, nil, 0, nil, err
			}
			graph, compactErr := buildCompact(snapshot)
			if compactErr != nil {
				return nil, nil, 0, nil, fmt.Errorf("native delegate failed (%v), then compact fallback failed: %w", err, compactErr)
			}
			warning := fmt.Sprintf("native %s delegate failed; used compact fallback: %v", snapshot.Metadata.Agent, err)
			return graph, nil, FidelityCompact, []string{warning}, nil
		}
	}
	if snapshot.CompactPresent {
		graph, err := buildCompact(snapshot)
		return graph, nil, FidelityCompact, nil, err
	}
	graph, err := buildMetadataOnly(snapshot)
	return graph, nil, FidelityMetadataOnly, nil, err
}

func buildMetadataOnly(snapshot *entirecheckpoint.SessionSnapshot) (*session.Graph, error) {
	rootID := compactSessionID(snapshot)
	graph := &session.Graph{Sessions: []session.Session{{ID: rootID, Outcome: session.OutcomeUnknown}}}
	if !snapshot.PromptPresent {
		return graph, nil
	}
	segments := strings.Split(string(snapshot.Prompt), "\n---\n")
	for index, text := range segments {
		if text == "" {
			continue
		}
		ordinal := index + 1
		locator := fmt.Sprintf("%s:prompt.txt:%d", snapshot.Metadata.SessionID, ordinal)
		row := session.UserInput{
			ID: session.MintID("user_input", rootID, locator), SessionID: rootID,
			Ordinal: ordinal, Text: text, Kind: session.KindPrompt, Origin: session.OriginHuman,
			Metadata: json.RawMessage(`{"agent":"entire"}`),
		}
		graph.UserInputs = append(graph.UserInputs, row)
		addVendorID(graph, row.ID, session.EntityUserInput, rootID, "prompt.line", locator, sourceRef(snapshot, "prompt.txt", ordinal))
	}
	return graph, nil
}

func compactSessionID(snapshot *entirecheckpoint.SessionSnapshot) string {
	vendorSessionID := snapshot.Metadata.SessionID
	switch snapshot.Metadata.Agent {
	case "Claude Code":
		return session.MintID("session", "claude_code", vendorSessionID, "")
	case "Codex":
		// codexsession's root namespace is (vendor, thread id), with no empty
		// agent component. The rollout's thread id and the metadata session_id
		// are usually equal but the delegate accepts either, so prefer the
		// rollout's own answer: a compact-fallback graph must still collide
		// with a native parse of the same session.
		if threadID := codexThreadID(snapshot.Full); threadID != "" {
			vendorSessionID = threadID
		}
		return session.MintID("session", "codex", vendorSessionID)
	case "Cursor":
		return session.MintID("session", "cursor", vendorSessionID)
	default:
		return session.MintID("session", "entire", vendorSessionID, "")
	}
}

// codexThreadID reads the thread id from a rollout's leading session_meta
// record. It is tolerant — the compact fallback exists because the native
// bytes may be unparseable, so any shape surprise means "no answer", never an
// error.
func codexThreadID(full []byte) string {
	for len(full) > 0 {
		line := full
		if newline := bytes.IndexByte(full, '\n'); newline >= 0 {
			line, full = full[:newline], full[newline+1:]
		} else {
			full = nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var record struct {
			Type    string `json:"type"`
			Payload struct {
				ID string `json:"id"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &record) != nil || record.Type != "session_meta" {
			return ""
		}
		return record.Payload.ID
	}
	return ""
}

func accountMappedCompact(report *Report, snapshot *entirecheckpoint.SessionSnapshot) {
	for _, record := range snapshot.Compact {
		report.Compact.Lines.Total++
		report.Compact.Lines.Mapped++
		for range record.Content {
			report.Compact.Blocks.Total++
			report.Compact.Blocks.Mapped++
		}
	}
}

func accountDelegatedCompact(report *Report, snapshot *entirecheckpoint.SessionSnapshot) {
	for _, record := range snapshot.Compact {
		report.Compact.Lines.Total++
		report.Compact.Lines.discard("native delegate selected", 1)
		for range record.Content {
			report.Compact.Blocks.Total++
			report.Compact.Blocks.discard("native delegate selected", 1)
		}
	}
}

func accountCheckpointMetadata(report *Report, metadata entirecheckpoint.CheckpointMetadata) {
	unknown := make(map[string]bool, len(metadata.UnknownKeys))
	for _, key := range metadata.UnknownKeys {
		unknown[key] = true
	}
	for _, key := range metadata.Keys {
		report.Metadata.Total++
		if unknown[key] {
			report.Metadata.discard("unknown checkpoint metadata key: "+key, 1)
			continue
		}
		switch key {
		case "checkpoint_id", "sessions":
			report.Metadata.Evidence++
		default:
			report.Metadata.discard("checkpoint metadata is not session-attributable: "+key, 1)
		}
	}
}

func accountSessionMetadata(report *Report, metadata entirecheckpoint.SessionMetadata) {
	unknown := make(map[string]bool, len(metadata.UnknownKeys))
	for _, key := range metadata.UnknownKeys {
		unknown[key] = true
	}
	for _, key := range metadata.Keys {
		report.Metadata.Total++
		if unknown[key] {
			report.Metadata.discard("unknown session metadata key: "+key, 1)
			continue
		}
		switch key {
		case "checkpoint_transcript_start", "transcript_lines_at_start", "compact_transcript_start", "transcript_identifier_at_start":
			report.Metadata.Evidence++
		default:
			report.Metadata.Mapped++
		}
	}
}

func mergeEnvelopeReport(into *EnvelopeReport, from EnvelopeReport) {
	into.AttributesAdded += from.AttributesAdded
	into.VendorIDsAdded += from.VendorIDsAdded
	into.AggregateUsageAdded += from.AggregateUsageAdded
	into.AggregateUsageAttributed += from.AggregateUsageAttributed
	into.FilesConsidered += from.FilesConsidered
	into.FilesAlreadyCovered += from.FilesAlreadyCovered
	into.FilesUnrepresentable += from.FilesUnrepresentable
	into.FilesShared += from.FilesShared
}
