package entiresession

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/baumai/baum/sessions/entirecheckpoint"
	"github.com/baumai/baum/sessions/session"
	"github.com/baumai/baum/sessions/traceprocessing"
)

// EnvelopeData is the standalone Entire-owned enrichment for a graph. The
// future ranked ingest can apply these rows to a richer graph without rebuilding
// or replacing its transcript-derived tree.
type EnvelopeData struct {
	StartedAtFallback *time.Time                 `json:"started_at_fallback,omitempty"`
	EndedAtFallback   *time.Time                 `json:"ended_at_fallback,omitempty"`
	GitBranchFill     *string                    `json:"git_branch_fill,omitempty"`
	Outcome           session.Outcome            `json:"outcome"`
	OutcomeEvidence   string                     `json:"outcome_evidence"`
	Attributes        []session.SessionAttribute `json:"session_attributes,omitempty"`
	// MetadataFiles are the session's files_touched entries, distinct and in
	// metadata order. They are repository-relative, but they are the working
	// tree's changes over the checkpoint window rather than this session's own
	// edits: Entire records what changed while the session was active, and in a
	// multi-session checkpoint it often hands every session the same list. No
	// FileTouch row can carry them (see FilesUnrepresentable below); the ingest
	// layer decides whether they become session-scoped evidence.
	MetadataFiles []string `json:"metadata_files,omitempty"`
	// MetadataFilesShared is true when another session in the same checkpoint
	// reports the identical non-empty files_touched list. The list is then the
	// checkpoint's diff and names nobody's work in particular.
	MetadataFilesShared bool                 `json:"metadata_files_shared,omitempty"`
	FileTouches         []session.FileTouch  `json:"file_touches,omitempty"`
	TokenUsage          []session.TokenUsage `json:"token_usage,omitempty"`
	VendorIDs           []session.VendorID   `json:"vendor_ids,omitempty"`
	Report              EnvelopeReport       `json:"report"`
}

// Envelope computes the metadata layer without mutating graph. File evidence
// is compared with graph here so enrichment remains path-boundary additive.
func Envelope(graph *session.Graph, snapshot *entirecheckpoint.SessionSnapshot, checkpointID string, createdAt time.Time) (*EnvelopeData, error) {
	if graph == nil || graph.Root() == nil {
		return nil, fmt.Errorf("graph has no root session")
	}
	if snapshot == nil {
		return nil, fmt.Errorf("nil session snapshot")
	}
	root := graph.Root()
	data := &EnvelopeData{
		Outcome:         session.OutcomeCommitted,
		OutcomeEvidence: "entire checkpoint " + checkpointID,
	}
	if root.StartedAt == nil {
		data.StartedAtFallback = cloneTime(&createdAt)
	}
	if root.EndedAt == nil {
		data.EndedAtFallback = cloneTime(&createdAt)
	}
	if root.GitBranch == nil && snapshot.Metadata.Branch != "" {
		branch := snapshot.Metadata.Branch
		data.GitBranchFill = &branch
	}

	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(snapshot.Metadata.Raw, &fields); err != nil {
		return nil, fmt.Errorf("decode retained session metadata: %w", err)
	}
	attributeTime := createdAt.UTC().Truncate(time.Microsecond)
	for _, key := range envelopeAttributeKeys {
		if key == "token_usage" {
			continue
		}
		if value, present := fields[key]; present {
			data.Attributes = append(data.Attributes, envelopeAttribute(root.ID, key, value, attributeTime))
		}
	}

	if usage := snapshot.Metadata.TokenUsage; usage != nil {
		value := fields["token_usage"]
		if graphHasInvocationUsage(graph) {
			data.Attributes = append(data.Attributes, envelopeAttribute(root.ID, "token_usage", value, attributeTime))
			data.Report.AggregateUsageAttributed++
		} else {
			input, output := usage.InputTokens, usage.OutputTokens
			cacheRead, cacheWrite := usage.CacheReadTokens, usage.CacheCreationTokens
			total := input + output + cacheRead + cacheWrite
			data.TokenUsage = append(data.TokenUsage, session.TokenUsage{
				ID:        session.MintID("token_usage", root.ID, "entire", "aggregate"),
				SessionID: root.ID, Scope: session.UsageScopeSession,
				Model:       optionalString(snapshot.Metadata.Model),
				InputTokens: &input, OutputTokens: &output,
				CacheReadTokens: &cacheRead, CacheWriteTokens: &cacheWrite, TotalTokens: &total, APICallCount: &usage.APICallCount,
			})
			data.Report.AggregateUsageAdded++
			data.Attributes = append(data.Attributes, envelopeAttribute(root.ID, "token_usage", value, attributeTime))
		}
	}

	metadataRef := fmt.Sprintf("%s/%s/metadata.json", checkpointID, snapshot.Directory)
	data.VendorIDs = traceprocessing.AppendVendorID(data.VendorIDs, "entire", root.ID, root.ID, session.EntitySession, "session_id", snapshot.Metadata.SessionID, &metadataRef)
	data.VendorIDs = traceprocessing.AppendVendorID(data.VendorIDs, "entire", root.ID, root.ID, session.EntitySession, "checkpoint_id", checkpointID, &metadataRef)
	data.VendorIDs = traceprocessing.AppendVendorID(data.VendorIDs, "entire", root.ID, root.ID, session.EntitySession, "turn_id", snapshot.Metadata.TurnID, &metadataRef)

	seenPaths := make([]string, 0, len(graph.FileTouches))
	for _, touch := range graph.FileTouches {
		seenPaths = append(seenPaths, touch.Path)
	}
	metadataSeen := map[string]bool{}
	for _, candidate := range snapshot.Metadata.FilesTouched {
		if candidate == "" || metadataSeen[candidate] {
			continue
		}
		metadataSeen[candidate] = true
		data.MetadataFiles = append(data.MetadataFiles, candidate)
		data.Report.FilesConsidered++
		if coveredPath(seenPaths, candidate) {
			data.Report.FilesAlreadyCovered++
			continue
		}
		// FileTouch.ToolUseID is nullable, but UserInputID is still mandatory
		// and validated. Session metadata does not say which input owns a path,
		// so no correct row can be formed without inventing that relationship.
		data.Report.FilesUnrepresentable++
	}

	data.Report.AttributesAdded = len(data.Attributes)
	data.Report.VendorIDsAdded = len(data.VendorIDs)
	return data, nil
}

var envelopeAttributeKeys = []string{
	"agent", "model", "strategy", "cli_version", "checkpoints_count", "save_step_count",
	"session_metrics", "initial_attribution", "prompt_attributions", "skill_events_version",
	"skill_events", "kind", "review_skills", "review_prompt", "investigate_run_id",
	"investigate_topic", "summary", "is_task", "tool_use_id", "commit_sha", "imported",
}

func envelopeAttribute(sessionID, key string, value json.RawMessage, at time.Time) session.SessionAttribute {
	if len(value) == 0 {
		value = json.RawMessage("null")
	}
	return session.SessionAttribute{
		ID:        session.MintID("session_attribute", sessionID, "entire", key, at.Format(time.RFC3339Nano)),
		SessionID: sessionID, Key: key, Value: append(json.RawMessage(nil), value...), ValidFrom: at,
	}
}

func graphHasInvocationUsage(graph *session.Graph) bool {
	for _, invocation := range graph.ModelInvocations {
		if invocation.InputTokens != 0 || invocation.OutputTokens != 0 || invocation.CacheReadTokens != 0 || invocation.CacheWriteTokens != 0 || invocation.TotalTokens != 0 {
			return true
		}
	}
	return false
}

func coveredPath(seen []string, candidate string) bool {
	for _, existing := range seen {
		if existing == candidate || strings.HasSuffix(existing, "/"+candidate) {
			return true
		}
	}
	return false
}

// sharedFilesTouched reports whether another session snapshot in the same
// checkpoint lists exactly the files this one does. Entire derives a session's
// files_touched from the working tree, not from the session's tool calls, and
// an identical non-empty list on two sessions is the checkpoint's diff handed
// to both: evidence for neither.
func sharedFilesTouched(own *entirecheckpoint.SessionSnapshot, siblings []*entirecheckpoint.SessionSnapshot) bool {
	ownSet := fileSet(own.Metadata.FilesTouched)
	if len(ownSet) == 0 {
		return false
	}
	for _, sibling := range siblings {
		if sibling == nil || sibling == own {
			continue
		}
		if maps.Equal(ownSet, fileSet(sibling.Metadata.FilesTouched)) {
			return true
		}
	}
	return false
}

func fileSet(paths []string) map[string]struct{} {
	set := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if path != "" {
			set[path] = struct{}{}
		}
	}
	return set
}

// Apply fills only missing session fields, sets checkpoint-proven committed
// outcome, and appends rows not already present. It never removes transcript
// rows and is idempotent on one in-memory graph.
func (e *EnvelopeData) Apply(graph *session.Graph) {
	if e == nil || graph == nil || graph.Root() == nil {
		return
	}
	root := graph.Root()
	if root.StartedAt == nil {
		root.StartedAt = cloneTime(e.StartedAtFallback)
	}
	if root.EndedAt == nil {
		root.EndedAt = cloneTime(e.EndedAtFallback)
	}
	if root.GitBranch == nil && e.GitBranchFill != nil {
		branch := *e.GitBranchFill
		root.GitBranch = &branch
	}
	root.Outcome = e.Outcome
	graph.Attributes = appendUnique(graph.Attributes, e.Attributes, func(row session.SessionAttribute) string { return row.ID })
	graph.FileTouches = appendUnique(graph.FileTouches, e.FileTouches, func(row session.FileTouch) string { return row.ID })
	graph.TokenUsage = appendUnique(graph.TokenUsage, e.TokenUsage, func(row session.TokenUsage) string { return row.ID })
	for _, row := range e.VendorIDs {
		duplicate := false
		for _, existing := range graph.VendorIDs {
			if existing.OurID == row.OurID && existing.OurType == row.OurType && existing.Vendor == row.Vendor && existing.VendorIDKind == row.VendorIDKind && existing.VendorID == row.VendorID {
				duplicate = true
				break
			}
		}
		if !duplicate {
			graph.VendorIDs = append(graph.VendorIDs, row)
		}
	}
}

func appendUnique[T any](existing []T, additions []T, id func(T) string) []T {
	seen := make(map[string]bool, len(existing)+len(additions))
	for _, row := range existing {
		seen[id(row)] = true
	}
	for _, row := range additions {
		if key := id(row); !seen[key] {
			existing = append(existing, row)
			seen[key] = true
		}
	}
	return existing
}

func addVendorID(graph *session.Graph, ourID string, ourType session.EntityType, sessionID, kind, vendorID string, ref *string) {
	graph.VendorIDs = traceprocessing.AppendVendorID(graph.VendorIDs, "entire", sessionID, ourID, ourType, kind, vendorID, ref)
}

func sourceRef(snapshot *entirecheckpoint.SessionSnapshot, file string, ordinal int) *string {
	ref := fmt.Sprintf("%s/%s/%s", snapshot.Metadata.CheckpointID, snapshot.Directory, file)
	if ordinal > 0 {
		ref = fmt.Sprintf("%s:%d", ref, ordinal)
	}
	return &ref
}
