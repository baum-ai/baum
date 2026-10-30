package entiresession

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/baum-ai/baum/sessions/claudesession"
	"github.com/baum-ai/baum/sessions/claudetranscript"
	"github.com/baum-ai/baum/sessions/cursorsession"
	"github.com/baum-ai/baum/sessions/cursortranscript"
	"github.com/baum-ai/baum/sessions/entirecheckpoint"
	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/traceprocessing"
)

func TestHostedReportCarriesCheckpointShape(t *testing.T) {
	checkpoint := hostedFixtureCheckpoint(t, "codex")
	_, report, err := hostedBuild(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report.Shape, entirecheckpoint.Survey([]*entirecheckpoint.Checkpoint{checkpoint})) {
		t.Fatalf("report shape = %+v", report.Shape)
	}
	if report.Shape.CLIVersions.Max == "unknown" || len(report.Shape.SessionKeys) == 0 || report.Shape.CompactRecordTypes["assistant"] == 0 {
		t.Fatalf("report shape is not populated: %+v", report.Shape)
	}
	report.Shape.HashMismatches++
	if !report.Balanced() {
		t.Fatal("the format-drift survey took part in the balancing account")
	}
}

func TestHostedBuildIsolatesNilSnapshotFromTheSurvey(t *testing.T) {
	_, report, err := hostedBuild(&entirecheckpoint.Checkpoint{
		Sessions: []*entirecheckpoint.SessionSnapshot{nil},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.FailedSnapshots != 1 {
		t.Fatalf("failed snapshots = %d, want 1", report.FailedSnapshots)
	}
}

func TestHostedDelegatedClaudeIDsConvergeWithDirectParse(t *testing.T) {
	checkpoint := hostedFixtureCheckpoint(t, "two_claude")
	results, _, err := hostedBuild(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	direct := hostedDirectClaudeGraph(t, checkpoint.Sessions[0])
	if got, want := compactSessionID(checkpoint.Sessions[0]), direct.Root().ID; got != want {
		t.Fatalf("compact fallback root id %s, native Claude root id %s", got, want)
	}
	want := hostedGraphIDs(direct)
	got := hostedGraphIDs(results[0].Graph)
	for entity, ids := range want {
		gotSet := map[string]bool{}
		for _, id := range got[entity] {
			gotSet[id] = true
		}
		for _, id := range ids {
			if !gotSet[id] {
				t.Fatalf("%s id %s from direct parse is absent after Entire delegation", entity, id)
			}
		}
	}
}

func TestHostedEnvelopeOnlyIsAdditiveToDirectGraph(t *testing.T) {
	checkpoint := hostedFixtureCheckpoint(t, "two_claude")
	snapshot := checkpoint.Sessions[0]
	direct := hostedDirectClaudeGraph(t, snapshot)
	beforeInputs := append([]session.UserInput(nil), direct.UserInputs...)
	beforeInvocations := append([]session.ModelInvocation(nil), direct.ModelInvocations...)
	beforeMessages := append([]session.AssistantMessage(nil), direct.AssistantMessages...)
	beforeUses := append([]session.ToolUse(nil), direct.ToolUses...)
	beforeResults := append([]session.ToolResult(nil), direct.ToolResults...)
	beforeTouches := append([]session.FileTouch(nil), direct.FileTouches...)
	beforeAttributes := len(direct.Attributes)
	beforeVendorIDs := len(direct.VendorIDs)
	created, err := time.Parse(time.RFC3339Nano, snapshot.Metadata.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := hostedEnvelope(direct, snapshot, checkpoint.Metadata.CheckpointID, created)
	if err != nil {
		t.Fatal(err)
	}
	envelope.Apply(direct)
	if !reflect.DeepEqual(beforeInputs, direct.UserInputs) ||
		!reflect.DeepEqual(beforeInvocations, direct.ModelInvocations) ||
		!reflect.DeepEqual(beforeMessages, direct.AssistantMessages) ||
		!reflect.DeepEqual(beforeUses, direct.ToolUses) ||
		!reflect.DeepEqual(beforeResults, direct.ToolResults) ||
		!reflect.DeepEqual(beforeTouches, direct.FileTouches[:len(beforeTouches)]) {
		t.Fatal("Envelope changed transcript-derived rows")
	}
	if len(direct.Attributes) <= beforeAttributes || len(direct.VendorIDs) <= beforeVendorIDs {
		t.Fatal("Envelope did not append its additive rows")
	}
	rows := rowsFrom(traceprocessing.TallyRows(direct))
	envelope.Apply(direct)
	if got := rowsFrom(traceprocessing.TallyRows(direct)); got != rows {
		t.Fatalf("second Envelope application changed rows: before=%+v after=%+v", rows, got)
	}
}

func hostedFixtureCheckpoint(t *testing.T, name string) *entirecheckpoint.Checkpoint {
	t.Helper()
	root := filepath.Join("..", "entirecheckpoint", "testdata", name)
	checkpoint, err := entirecheckpoint.ParseHostedCheckpoint(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

// The compact fallback must produce the same root id codexsession mints from
// the rollout, or an upload-path parse of the same session would land on a
// different row.
func TestHostedCodexCompactSessionIDConvergesWithNativeRoot(t *testing.T) {
	checkpoint := hostedFixtureCheckpoint(t, "codex")
	snapshot := checkpoint.Sessions[0]
	native, _, err := delegateCodex(snapshot, nil, traceprocessing.Hosted)
	if err != nil {
		t.Fatal(err)
	}
	compact, err := hostedBuildCompact(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := compact.Root().ID, native.Root().ID; got != want {
		t.Fatalf("compact fallback root id %s, native root id %s", got, want)
	}
}

func TestHostedCodexCompactSessionIDPrefersRolloutThreadID(t *testing.T) {
	checkpoint := hostedFixtureCheckpoint(t, "codex")
	snapshot := checkpoint.Sessions[0]
	fromRollout := compactSessionID(snapshot)
	snapshot.Metadata.SessionID = "not-the-thread-id"
	if got := compactSessionID(snapshot); got != fromRollout {
		t.Fatalf("metadata session_id drift changed the root id: %s vs %s", got, fromRollout)
	}
}

func TestHostedCursorCompactSessionIDConvergesWithNativeRoot(t *testing.T) {
	checkpoint := hostedSyntheticCheckpoint(t, "Cursor", "", nil, false)
	snapshot := checkpoint.Sessions[0]
	native, _, err := cursorsession.BuildWith(&cursortranscript.Tree{Composer: &cursortranscript.Composer{
		Data: &cursortranscript.ComposerData{ComposerID: snapshot.Metadata.SessionID},
	}}, traceprocessing.Hosted)
	if err != nil {
		t.Fatal(err)
	}
	want := native.Root().ID
	if got := compactSessionID(snapshot); got != want {
		t.Fatalf("compact fallback root id %s, native Cursor root id %s", got, want)
	}
}

func hostedDirectClaudeGraph(t *testing.T, snapshot *entirecheckpoint.SessionSnapshot) *session.Graph {
	t.Helper()
	lines, torn, err := claudetranscript.Decode(bytes.NewReader(snapshot.Full))
	if err != nil || torn {
		t.Fatalf("decode direct: torn=%v err=%v", torn, err)
	}
	tree, err := claudetranscript.SingleFileTree(&claudetranscript.File{
		Path: "full.jsonl", SessionID: snapshot.Metadata.SessionID, Lines: lines,
	})
	if err != nil {
		t.Fatal(err)
	}
	graph, report, err := claudesession.BuildWith(tree, claudesession.BuildOptions{Profile: traceprocessing.Hosted, Survey: true})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Lines.Balanced() {
		t.Fatalf("direct report is unbalanced: %+v", report)
	}
	return graph
}

func hostedGraphIDs(graph *session.Graph) map[string][]string {
	ids := map[string][]string{}
	for _, row := range graph.Sessions {
		ids["sessions"] = append(ids["sessions"], row.ID)
	}
	for _, row := range graph.Attributes {
		ids["attributes"] = append(ids["attributes"], row.ID)
	}
	for _, row := range graph.UserInputs {
		ids["inputs"] = append(ids["inputs"], row.ID)
	}
	for _, row := range graph.ModelInvocations {
		ids["invocations"] = append(ids["invocations"], row.ID)
	}
	for _, row := range graph.AssistantMessages {
		ids["messages"] = append(ids["messages"], row.ID)
	}
	for _, row := range graph.ToolUses {
		ids["tools"] = append(ids["tools"], row.ID)
	}
	for _, row := range graph.ToolResults {
		ids["results"] = append(ids["results"], row.ID)
	}
	for _, row := range graph.FileTouches {
		ids["touches"] = append(ids["touches"], row.ID)
	}
	for _, row := range graph.TokenUsage {
		ids["usage"] = append(ids["usage"], row.ID)
	}
	for key := range ids {
		sort.Strings(ids[key])
	}
	return ids
}
