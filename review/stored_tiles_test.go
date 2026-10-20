package review

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	bundlepkg "github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/review/tilecatalogue"
	reviewschema "github.com/baum-ai/baum/schema"
)

// The tile shapes below are synthetic, shaped like the tiles releases before
// the tile kinds were finalised stored: retired kinds the viewer still
// renders, and tiles neither the current schema nor a retired kind accepts.

func storedTileAnchor() []any {
	return []any{map[string]any{"type": "hunk", "id": "h_141757b19901"}}
}

// retiredWorkflowTile is a tile of a kind the schema retired and the viewer
// still renders.
func retiredWorkflowTile() map[string]any {
	return map[string]any{"id": "tile_retry_flow", "kind": "workflow-state-machine", "anchors": storedTileAnchor(), "code": []any{}, "data": map[string]any{
		"primary_path":   []any{map[string]any{"id": "queued", "label": "Queued", "kind": "start"}, map[string]any{"id": "done", "label": "Done", "kind": "terminal"}},
		"failure_path":   []any{map[string]any{"id": "dead", "label": "Dead letter", "kind": "failure", "change": "add"}},
		"new_transition": "A job that fails five times moves to the dead letter queue.",
		"retry":          "Fixed delay between attempts.",
		"terminal_state": "Done",
	}}
}

func retiredRefactorTile() map[string]any {
	return map[string]any{"id": "tile_store_split", "kind": "refactor", "anchors": storedTileAnchor(), "code": []any{}, "data": map[string]any{"was": "One store for every queue.", "now": "A store per queue."}}
}

// unrenderableTiles are tiles no current or retired definition accepts: a
// retired kind missing a field its definition requires, a kind that was never
// in the catalogue, and a current kind with an older field shape.
func unrenderableTiles() []any {
	oldWorkflow := retiredWorkflowTile()
	oldWorkflow["id"] = "tile_old_flow"
	delete(oldWorkflow["data"].(map[string]any), "retry")
	return []any{
		oldWorkflow,
		map[string]any{"id": "tile_preview", "kind": "ui-preview", "anchors": storedTileAnchor(), "code": []any{}, "data": map[string]any{"screenshot": "before.png"}},
		map[string]any{"id": "tile_old_fix", "kind": "bug-fix", "anchors": storedTileAnchor(), "code": []any{}, "data": map[string]any{"summary": "Ack checks the job state."}},
	}
}

// storedTilesDocument is the example document with tiles of retired kinds in
// its first section and that section's first risk, and, when unrenderable,
// retired fields in its architecture tile and tiles no definition accepts
// mixed in among them.
func storedTilesDocument(t *testing.T, unrenderable bool) map[string]any {
	t.Helper()
	doc := storedExample(t)
	section := firstSection(doc)
	tiles := append(section["tiles"].([]any), retiredWorkflowTile())
	risk := section["risks"].([]any)[0].(map[string]any)
	riskTiles := []any{retiredRefactorTile()}
	if unrenderable {
		// The architecture tile carries the fields the schema removed from
		// its kind, which the viewer ignores.
		node := map[string]any{"id": "outside", "label": "Outside world", "kind": "external"}
		data := tiles[0].(map[string]any)["data"].(map[string]any)
		data["context"], data["deployment"] = []any{node}, []any{node}
		bad := unrenderableTiles()
		tiles = slices.Insert(tiles, 1, bad[0])
		tiles = append(tiles, bad[1])
		riskTiles = append([]any{bad[2]}, riskTiles...)
	}
	section["tiles"], risk["tiles"] = tiles, riskTiles
	return doc
}

func storedTileIDs(tiles any) []string {
	var ids []string
	for _, tile := range tiles.([]any) {
		ids = append(ids, tile.(map[string]any)["id"].(string))
	}
	return ids
}

func encodeJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestStoredTilesKeepRenderableKindsAndDropTheRest(t *testing.T) {
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	stored := encodeJSON(t, storedTilesDocument(t, true))
	if reviewschema.Validate(stored) == nil {
		t.Fatal("the current schema accepts the stored document as it is")
	}
	if validator.full.Validate(decodedJSON(t, stored)) == nil {
		t.Fatal("stored-document validation accepts tiles no definition accepts")
	}
	if findings := validator.ValidateStored(stored, nil); findings != nil {
		t.Fatalf("stored document rejected: %q", findings)
	}
	value, normalized, err := decodeStored(stored, "stored review")
	if err != nil {
		t.Fatal(err)
	}
	section := firstSection(value)
	if got, want := storedTileIDs(section["tiles"]), []string{"tile_retry_architecture", "tile_retry_flow"}; !slices.Equal(got, want) {
		t.Fatalf("section tiles = %v, want %v", got, want)
	}
	if got, want := storedTileIDs(section["risks"].([]any)[0].(map[string]any)["tiles"]), []string{"tile_store_split"}; !slices.Equal(got, want) {
		t.Fatalf("risk tiles = %v, want %v", got, want)
	}
	// Only the retired fields and the unrenderable tiles are removed: the
	// result is the document that never had them.
	if want := decodedJSON(t, encodeJSON(t, storedTilesDocument(t, false))); !reflect.DeepEqual(decodedJSON(t, normalized), want) {
		t.Fatalf("normalization changed more than the unrenderable tiles:\n%s", normalized)
	}
	// Idempotent: the normalized document is a valid one, which no rule
	// rewrites again.
	if _, again, err := decodeStored(normalized, "stored review"); err != nil || !bytes.Equal(again, normalized) {
		t.Fatalf("normalizing again changed the document (%v):\n%s", err, again)
	}
	if findings := validator.ValidateStored(normalized, nil); findings != nil {
		t.Fatalf("normalized document rejected: %q", findings)
	}
}

// A stored document whose tiles are all current or retired kinds the viewer
// renders passes stored validation and is left byte for byte unchanged.
func TestStoredTilesLeaveARenderableDocumentUnchanged(t *testing.T) {
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]map[string]any{"current kinds": storedExample(t), "retired kinds": storedTilesDocument(t, false)} {
		// Formatting the normalizer would not reproduce shows it returns
		// the input itself.
		var indented bytes.Buffer
		if err := json.Indent(&indented, encodeJSON(t, doc), "", "  "); err != nil {
			t.Fatal(err)
		}
		if _, got, err := decodeStored(indented.Bytes(), "stored review"); err != nil || !bytes.Equal(got, indented.Bytes()) {
			t.Errorf("%s: a document no rule applies to was re-encoded (%v)", name, err)
		}
		if findings := validator.ValidateStored(indented.Bytes(), nil); findings != nil {
			t.Errorf("%s: rejected: %q", name, findings)
		}
	}
}

// A refresh of a stored document with tiles no definition accepts succeeds,
// stores the document without them or the retired fields of a current kind,
// and keeps the retired kinds.
func TestRefreshStoredDocumentWithUnrenderableTiles(t *testing.T) {
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	doc := storedTilesDocument(t, true)
	header := doc["review"].(map[string]any)
	bundle := &bundlepkg.Bundle{Repository: "example-org/taskline", PullRequest: bundlepkg.PullRequest{Number: 212, State: "open", HeadSHA: header["head"].(string), BaseSHA: header["base"].(string), MergeBaseSHA: header["merge_base"].(string)}, Sources: bundlepkg.Sources{"diff": true}}
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	refreshed, err := validator.Refresh(encodeJSON(t, doc), bundle, now)
	if err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	expected, err := validator.Refresh(encodeJSON(t, storedTilesDocument(t, false)), bundle, now)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(refreshed, expected) {
		t.Fatalf("refresh differs from the refresh of the document without unrenderable tiles:\n%s\n%s", refreshed, expected)
	}
	if !bytes.Contains(refreshed, []byte(`"tile_retry_flow"`)) || !bytes.Contains(refreshed, []byte(`"tile_store_split"`)) {
		t.Fatal("refresh dropped a tile of a retired kind")
	}
	if findings := validator.ValidateStored(refreshed, nil); findings != nil {
		t.Fatalf("refreshed document rejected: %q", findings)
	}
	if _, err := validator.Refresh(refreshed, bundle, now); err != nil {
		t.Fatalf("second refresh failed: %v", err)
	}
}

// The previous version a generation carries has the tiles a refresh would
// store: retired fields removed, and unrenderable tiles dropped from the
// section and its risks alike.
func TestPreviousVersionOfDropsUnrenderableTiles(t *testing.T) {
	previous, err := PreviousVersionOf(encodeJSON(t, storedTilesDocument(t, true)), 2)
	if err != nil {
		t.Fatal(err)
	}
	var content struct {
		Tiles []any `json:"tiles"`
		Risks []struct {
			Tiles []any `json:"tiles"`
		} `json:"risks"`
	}
	if err := json.Unmarshal(previous.Sections[0].Content, &content); err != nil {
		t.Fatal(err)
	}
	want := decodedJSON(t, encodeJSON(t, firstSection(storedTilesDocument(t, false)))).(map[string]any)
	if !reflect.DeepEqual(content.Tiles, want["tiles"]) {
		t.Fatalf("previous section tiles = %v, want %v", content.Tiles, want["tiles"])
	}
	if !reflect.DeepEqual(content.Risks[0].Tiles, want["risks"].([]any)[0].(map[string]any)["tiles"]) {
		t.Fatalf("previous risk tiles = %v", content.Risks[0].Tiles)
	}
}

// A tile is kept only when it also passes the document's own tile rules: one
// whose references removing a retired field breaks, one with retired fields
// and another fault, and a second tile with an owner's tile ID are dropped,
// and the result passes stored validation.
func TestStoredTilesDropTilesTheDocumentRulesReject(t *testing.T) {
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	doc := storedExample(t)
	section := firstSection(doc)
	architecture := section["tiles"].([]any)[0].(map[string]any)
	node := map[string]any{"id": "outside", "label": "Outside world", "kind": "external"}
	clone := func(id string) map[string]any {
		tile := decodedJSON(t, encodeJSON(t, architecture)).(map[string]any)
		tile["id"] = id
		data := tile["data"].(map[string]any)
		data["context"], data["deployment"] = []any{node}, []any{node}
		return tile
	}
	// The worker reaches the outside node, which only the retired context
	// field defined.
	dangling := clone("tile_retry_outside")
	sequence := dangling["data"].(map[string]any)["sequences"].([]any)[0].(map[string]any)
	sequence["steps"] = append(sequence["steps"].([]any), map[string]any{"from": "worker", "message": "Report", "to": "outside"})
	faulty := clone("tile_retry_faulty")
	delete(faulty["data"].(map[string]any), "product")
	duplicate := retiredWorkflowTile()
	duplicate["id"] = "tile_retry_architecture"
	section["tiles"] = append(section["tiles"].([]any), dangling, faulty, duplicate)
	stored := encodeJSON(t, doc)
	if findings := validator.ValidateStored(stored, nil); findings != nil {
		t.Fatalf("stored document rejected: %q", findings)
	}
	value, normalized, err := decodeStored(stored, "stored review")
	if err != nil {
		t.Fatal(err)
	}
	if want := decodedJSON(t, encodeJSON(t, storedExample(t))); !reflect.DeepEqual(decodedJSON(t, normalized), want) {
		t.Fatalf("tiles = %v, want only the example's", storedTileIDs(firstSection(value)["tiles"]))
	}
	if _, again, err := decodeStored(normalized, "stored review"); err != nil || !bytes.Equal(again, normalized) {
		t.Fatalf("normalizing again changed the document (%v)", err)
	}
}

// A kind brought back into the schema must leave the retired definitions:
// the stored schema refuses a definition both files name.
func TestRetiredTileDefinitionsMayNotRedefineTheSchema(t *testing.T) {
	if _, err := withRetiredTiles(reviewSchema, reviewschema.LegacyTiles); err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(reviewschema.LegacyTiles, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["$defs"].(map[string]any)["tileData_bug-fix"] = map[string]any{"type": "object"}
	_, err := withRetiredTiles(reviewSchema, encodeJSON(t, legacy))
	if err == nil || !strings.Contains(err.Error(), "tileData_bug-fix") {
		t.Fatalf("error = %v, want one naming the redefined tileData_bug-fix", err)
	}
}

// Stored validation accepts the retired kinds; generation never offers them
// and the authored contract still rejects them.
func TestGenerationRejectsRetiredTileKinds(t *testing.T) {
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"workflow-state-machine", "refactor"} {
		if slices.Contains(tilecatalogue.Kinds(), kind) {
			t.Errorf("the catalogue offers the retired kind %s", kind)
		}
		if _, err := validator.ModelSchema([]string{kind}); err == nil {
			t.Errorf("the model schema offers the retired kind %s", kind)
		}
		if strings.Contains(string(validator.Schema), `"const":"`+kind+`"`) || strings.Contains(string(validator.Schema), "legacyTile") {
			t.Errorf("the model-facing schema mentions the retired kind %s", kind)
		}
	}
	doc, err := decodeObject(authoredFixture(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	firstSection(doc)["tiles"] = []any{retiredWorkflowTile()}
	if findings := validator.Validate(encodeJSON(t, doc), nil); findings == "" {
		t.Fatal("the authored contract accepts a tile of a retired kind")
	}
}
