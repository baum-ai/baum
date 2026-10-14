package entiresession

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/goldentest"
	"github.com/baum-ai/baum/sessions/claudesession"
	"github.com/baum-ai/baum/sessions/claudetranscript"
	"github.com/baum-ai/baum/sessions/cursorsession"
	"github.com/baum-ai/baum/sessions/cursortranscript"
	"github.com/baum-ai/baum/sessions/entirecheckpoint"
	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/traceprocessing"
)

func TestFixtureGoldens(t *testing.T) {
	for _, name := range []string{"two_claude", "codex", "skill_events"} {
		t.Run(name, func(t *testing.T) {
			checkpoint := fixtureCheckpoint(t, name)
			results, report, err := Build(checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			if !report.Balanced() {
				t.Fatalf("unbalanced report: %+v", report)
			}
			goldenResults := any(results)
			if name == "two_claude" {
				goldenResults = projectFixtureResults(results)
			}
			goldentest.Golden(t, filepath.Join("testdata", "golden", name+"_results.json"), goldenResults)
			goldentest.Golden(t, filepath.Join("testdata", "golden", name+"_report.json"), report)
		})
	}
}

func TestReportCarriesCheckpointShape(t *testing.T) {
	checkpoint := fixtureCheckpoint(t, "codex")
	_, report, err := Build(checkpoint)
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

func TestBuildIsolatesNilSnapshotFromTheSurvey(t *testing.T) {
	_, report, err := Build(&entirecheckpoint.Checkpoint{
		Sessions: []*entirecheckpoint.SessionSnapshot{nil},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.FailedSnapshots != 1 {
		t.Fatalf("failed snapshots = %d, want 1", report.FailedSnapshots)
	}
}

func TestDelegatedClaudeIDsConvergeWithDirectParse(t *testing.T) {
	checkpoint := fixtureCheckpoint(t, "two_claude")
	results, _, err := Build(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	direct := directClaudeGraph(t, checkpoint.Sessions[0])
	if got, want := compactSessionID(checkpoint.Sessions[0]), direct.Root().ID; got != want {
		t.Fatalf("compact fallback root id %s, native Claude root id %s", got, want)
	}
	want := graphIDs(direct)
	got := graphIDs(results[0].Graph)
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

func TestEnvelopeOnlyIsAdditiveToDirectGraph(t *testing.T) {
	checkpoint := fixtureCheckpoint(t, "two_claude")
	snapshot := checkpoint.Sessions[0]
	direct := directClaudeGraph(t, snapshot)
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
	envelope, err := Envelope(direct, snapshot, checkpoint.Metadata.CheckpointID, created)
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

func fixtureCheckpoint(t *testing.T, name string) *entirecheckpoint.Checkpoint {
	t.Helper()
	checkpoint, err := entirecheckpoint.ParseCheckpoint(os.DirFS(filepath.Join("..", "entirecheckpoint", "testdata", name)))
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

// The compact fallback must produce the same root id codexsession mints from
// the rollout, or an upload-path parse of the same session would land on a
// different row.
func TestCodexCompactSessionIDConvergesWithNativeRoot(t *testing.T) {
	checkpoint := fixtureCheckpoint(t, "codex")
	snapshot := checkpoint.Sessions[0]
	native, _, err := delegateCodex(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	compact, err := buildCompact(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := compact.Root().ID, native.Root().ID; got != want {
		t.Fatalf("compact fallback root id %s, native root id %s", got, want)
	}
}

func TestCodexCompactSessionIDPrefersRolloutThreadID(t *testing.T) {
	checkpoint := fixtureCheckpoint(t, "codex")
	snapshot := checkpoint.Sessions[0]
	fromRollout := compactSessionID(snapshot)
	snapshot.Metadata.SessionID = "not-the-thread-id"
	if got := compactSessionID(snapshot); got != fromRollout {
		t.Fatalf("metadata session_id drift changed the root id: %s vs %s", got, fromRollout)
	}
}

func TestCursorCompactSessionIDConvergesWithNativeRoot(t *testing.T) {
	checkpoint := syntheticCheckpoint(t, "Cursor", "", nil, false)
	snapshot := checkpoint.Sessions[0]
	native, _, err := cursorsession.Build(&cursortranscript.Tree{Composer: &cursortranscript.Composer{
		Data: &cursortranscript.ComposerData{ComposerID: snapshot.Metadata.SessionID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := native.Root().ID
	if got := compactSessionID(snapshot); got != want {
		t.Fatalf("compact fallback root id %s, native Cursor root id %s", got, want)
	}
}

func directClaudeGraph(t *testing.T, snapshot *entirecheckpoint.SessionSnapshot) *session.Graph {
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
	graph, report, err := claudesession.Build(tree)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Lines.Balanced() {
		t.Fatalf("direct report is unbalanced: %+v", report)
	}
	return graph
}

func graphIDs(graph *session.Graph) map[string][]string {
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

// fixtureResultProjection keeps the large Claude integration golden focused on
// the Entire wrapper contract. The Claude mapper owns a full graph golden;
// repeating its complete output here made one representative checkpoint
// produce a multi-megabyte fixture.
type fixtureResultProjection struct {
	SessionID     string                    `json:"session_id"`
	CheckpointID  string                    `json:"checkpoint_id"`
	CreatedAt     time.Time                 `json:"created_at"`
	Fidelity      FidelityRank              `json:"fidelity_rank"`
	Warnings      []string                  `json:"warnings,omitempty"`
	Root          fixtureRootProjection     `json:"root"`
	Rows          Rows                      `json:"rows"`
	EntityIDs     map[string][]string       `json:"entity_ids"`
	AttributeKeys []string                  `json:"attribute_keys,omitempty"`
	FileTouches   []fixtureTouchProjection  `json:"file_touches,omitempty"`
	VendorIDs     []fixtureVendorProjection `json:"vendor_ids,omitempty"`
}

type fixtureRootProjection struct {
	ID        string          `json:"id"`
	Outcome   session.Outcome `json:"outcome"`
	StartedAt *time.Time      `json:"started_at,omitempty"`
	EndedAt   *time.Time      `json:"ended_at,omitempty"`
	GitBranch *string         `json:"git_branch,omitempty"`
}

type fixtureTouchProjection struct {
	Path string            `json:"path"`
	Kind session.TouchKind `json:"kind"`
}

type fixtureVendorProjection struct {
	OurType session.EntityType `json:"our_type"`
	Vendor  string             `json:"vendor"`
	Kind    string             `json:"kind"`
}

func projectFixtureResults(results []Result) []fixtureResultProjection {
	projected := make([]fixtureResultProjection, 0, len(results))
	for _, result := range results {
		root := result.Graph.Root()
		attributes := make([]string, 0, len(result.Graph.Attributes))
		for _, attribute := range result.Graph.Attributes {
			attributes = append(attributes, attribute.Key)
		}
		sort.Strings(attributes)

		touches := make([]fixtureTouchProjection, 0, len(result.Graph.FileTouches))
		for _, touch := range result.Graph.FileTouches {
			touches = append(touches, fixtureTouchProjection{Path: touch.Path, Kind: touch.Kind})
		}
		sort.Slice(touches, func(i, j int) bool {
			if touches[i].Path == touches[j].Path {
				return touches[i].Kind < touches[j].Kind
			}
			return touches[i].Path < touches[j].Path
		})

		vendors := make([]fixtureVendorProjection, 0, len(result.Graph.VendorIDs))
		for _, vendor := range result.Graph.VendorIDs {
			vendors = append(vendors, fixtureVendorProjection{
				OurType: vendor.OurType, Vendor: vendor.Vendor, Kind: vendor.VendorIDKind,
			})
		}
		sort.Slice(vendors, func(i, j int) bool {
			if vendors[i].OurType != vendors[j].OurType {
				return vendors[i].OurType < vendors[j].OurType
			}
			if vendors[i].Vendor != vendors[j].Vendor {
				return vendors[i].Vendor < vendors[j].Vendor
			}
			return vendors[i].Kind < vendors[j].Kind
		})

		projected = append(projected, fixtureResultProjection{
			SessionID: result.SessionID, CheckpointID: result.CheckpointID,
			CreatedAt: result.CreatedAt, Fidelity: result.Fidelity, Warnings: result.Warnings,
			Root: fixtureRootProjection{
				ID: root.ID, Outcome: root.Outcome, StartedAt: root.StartedAt,
				EndedAt: root.EndedAt, GitBranch: root.GitBranch,
			},
			Rows: rowsFrom(traceprocessing.TallyRows(result.Graph)), EntityIDs: graphIDs(result.Graph),
			AttributeKeys: attributes, FileTouches: touches, VendorIDs: vendors,
		})
	}
	return projected
}
