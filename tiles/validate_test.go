package tiles

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// golden reads a tile document the converter writes for a fixture
// (testdata/<name>.tiles.json).
func golden(t *testing.T, name string) *Document {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name + ".tiles.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return &doc
}

func validDocument(t *testing.T) *Document {
	t.Helper()
	return golden(t, "base")
}

func validate(t *testing.T, doc *Document) (*Document, error) {
	t.Helper()
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return defaultValidator.Validate(encoded)
}

func addTile(doc *Document, tile Tile) {
	doc.Tiles = append(doc.Tiles, tile)
	doc.Layout.Entries = append(doc.Layout.Entries, Entry{Tile: tile.ID, Size: SizeThird})
}

func TestValidateAcceptsConvertedDocument(t *testing.T) {
	doc, err := validate(t, validDocument(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, tile := range doc.Tiles {
		if tile.State != StateReady {
			t.Errorf("tile %s is %s: %v", tile.ID, tile.State, tile.Problems)
		}
	}
}

func TestValidateRejectsDocuments(t *testing.T) {
	for _, tc := range []struct {
		name   string
		want   string
		mutate func(doc *Document)
	}{
		{"another schema", "", func(doc *Document) { doc.Schema = "baum.tiles/2" }},
		{"no origin kind", "", func(doc *Document) { doc.Origin = Origin{} }},
		{"a substrate risk without its change", "", func(doc *Document) { doc.Risks[0].Change = "" }},
		{"a malformed review header", "", func(doc *Document) { doc.Review = json.RawMessage(`{"repository":"acme/widgets"}`) }},
		{"a tile ID with capitals", "", func(doc *Document) {
			doc.Tiles[0].ID = "Overview"
			doc.Layout.Entries[0].Tile = "Overview"
		}},
		{"a tile without a title", "", func(doc *Document) { doc.Tiles[0].Title = "" }},
		{"a headline on two lines", "", func(doc *Document) { doc.Tiles[0].Headline = "One\nTwo" }},
		{"a custom tile without its definition", "", func(doc *Document) {
			addTile(doc, Tile{ID: "custom_chart", Type: TypeCustom, Title: "Chart", Data: json.RawMessage(`{}`), State: StateReady})
		}},
		{"a built-in tile with a definition", "", func(doc *Document) { doc.Tiles[0].Definition = customDefinition() }},
		{"an unknown tile state", "", func(doc *Document) { doc.Tiles[0].State = "hidden" }},
		{"a repeated change", "change sec_retry_policy is not unique", func(doc *Document) {
			doc.Changes = append(doc.Changes, doc.Changes[0])
		}},
		{"a repeated risk", "risk rsk_retry_storm is not unique", func(doc *Document) {
			doc.Risks = append(doc.Risks, doc.Risks[0])
		}},
		{"a repeated evidence item", "evidence ev_retry_test is not unique", func(doc *Document) {
			doc.Evidence = append(doc.Evidence, doc.Evidence[0])
		}},
		{"a repeated tile", "tile evidence is not unique", func(doc *Document) {
			doc.Tiles = append(doc.Tiles, *tileOf(doc, TypeEvidence))
		}},
		{"a repeated group", "group changes is not unique", func(doc *Document) {
			doc.Layout.Groups = append(doc.Layout.Groups, doc.Layout.Groups[0])
		}},
		{"a risk of an unknown change", "names change sec_gone", func(doc *Document) { doc.Risks[0].Change = "sec_gone" }},
		{"evidence of an unknown change", "names change sec_gone", func(doc *Document) { doc.Evidence[0].Change = "sec_gone" }},
		{"a current hunk the index lacks", "which the index does not list", func(doc *Document) {
			doc.Changes[0].Hunks = append(doc.Changes[0].Hunks, "h_00000000ffff")
		}},
		{"an entry for an unknown tile", "places tile ghost", func(doc *Document) {
			doc.Layout.Entries = append(doc.Layout.Entries, Entry{Tile: "ghost", Size: SizeThird})
		}},
		{"an unplaced tile", "does not place tile evidence", func(doc *Document) {
			doc.Layout.Entries = removeEntry(doc.Layout.Entries, TypeEvidence)
		}},
		{"a tile placed twice", "places tile evidence more than once", func(doc *Document) {
			doc.Layout.Entries = append(doc.Layout.Entries, Entry{Tile: TypeEvidence, Size: SizeThird})
		}},
		{"an unknown group", "names group extra", func(doc *Document) { doc.Layout.Entries[0].Group = "extra" }},
		{"a split group", "group changes are not contiguous", func(doc *Document) {
			doc.Layout.Entries[len(doc.Layout.Entries)-1].Group = GroupChanges
		}},
		{"landing past the entries", "landing 11 is past the 10 entries", func(doc *Document) { doc.Layout.Landing = 11 }},
		{"two tiles of a single type", "more than one evidence tile", func(doc *Document) {
			addTile(doc, Tile{ID: "evidence_more", Type: TypeEvidence, Title: "More evidence", Data: json.RawMessage(`{}`), State: StateReady})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := validDocument(t)
			tc.mutate(doc)
			_, err := validate(t, doc)
			var invalid *DocumentError
			if !errors.As(err, &invalid) {
				t.Fatalf("err = %v, want a document error", err)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

func removeEntry(entries []Entry, tile string) []Entry {
	var kept []Entry
	for _, entry := range entries {
		if entry.Tile != tile {
			kept = append(kept, entry)
		}
	}
	return kept
}

func customDefinition() json.RawMessage {
	return json.RawMessage(`{"name":"chart","title":"Chart","use_when":"Numbers over time.","fill":"Fill the series.","data_schema":{"type":"object"},"source":{"format":"html","html":"<div></div>"},"digest":"sha256:` + strings.Repeat("0", 64) + `"}`)
}

// A tile-level failure marks only that tile unavailable, with its problems.
func TestValidateMarksTilesUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tile   string
		want   string
		mutate func(doc *Document)
	}{
		{"data of another shape", TypeEvidence, "data does not have the evidence tile's shape", func(doc *Document) {
			tileOf(doc, TypeEvidence).Data = json.RawMessage(`{"items":[]}`)
		}},
		{"a change tile without its blocks", "sec_retry_policy", "data does not have the change tile's shape", func(doc *Document) {
			setData(doc, "sec_retry_policy", map[string]any{"change": "sec_retry_policy", "attention": nil})
		}},
		{"an unknown type", "sparkline", `tile type "sparkline" is unknown`, func(doc *Document) {
			addTile(doc, Tile{ID: "sparkline", Type: "sparkline", Title: "Trend", Data: json.RawMessage(`{}`), State: StateReady})
		}},
		{"a custom tile", "custom_chart", unsupportedCustom, func(doc *Document) {
			addTile(doc, Tile{ID: "custom_chart", Type: TypeCustom, Title: "Chart", Data: json.RawMessage(`{}`), State: StateReady, Definition: customDefinition()})
		}},
		{"a single tile under another ID", "proof", `a evidence tile's ID must be "evidence"`, func(doc *Document) {
			tile := tileOf(doc, TypeEvidence)
			tile.ID = "proof"
			for i := range doc.Layout.Entries {
				if doc.Layout.Entries[i].Tile == TypeEvidence {
					doc.Layout.Entries[i].Tile = "proof"
				}
			}
		}},
		{"a change tile under another ID", "sec_retry_policy", "a change tile's ID must be its change's", func(doc *Document) {
			var data ChangeData
			_ = json.Unmarshal(tileOf(doc, "sec_retry_policy").Data, &data)
			data.Change = "sec_config_flag"
			setData(doc, "sec_retry_policy", data)
		}},
		{"a change tile of an unknown change", "sec_gone", "names change sec_gone", func(doc *Document) {
			addTile(doc, Tile{ID: "sec_gone", Type: TypeChange, Title: "Gone", Data: json.RawMessage(`{"change":"sec_gone","attention":null,"blocks":[]}`), State: StateReady})
		}},
		{"a change tile of the current mechanical change", "sec_generated_client", "everything_else shows it", func(doc *Document) {
			addTile(doc, Tile{ID: "sec_generated_client", Type: TypeChange, Title: "Client", Data: json.RawMessage(`{"change":"sec_generated_client","attention":null,"blocks":[]}`), State: StateReady})
		}},
		{"everything_else showing a logical change", TypeEverythingElse, "not the current mechanical change", func(doc *Document) {
			setData(doc, TypeEverythingElse, map[string]any{"change": "sec_config_flag", "unassigned": []string{}, "attention": nil, "blocks": []any{}})
		}},
		{"an unknown ranked risk", TypeRisks, "ranked risk rsk_gone is not in the document", func(doc *Document) {
			setData(doc, TypeRisks, RisksData{Ranked: []string{"rsk_gone"}})
		}},
		{"a mitigated ranked risk", TypeRisks, "ranked risk rsk_old_backoff is not active", func(doc *Document) {
			setData(doc, TypeRisks, RisksData{Ranked: []string{"rsk_old_backoff"}})
		}},
		{"a ranked risk of an obsolete change", TypeRisks, "belongs to an obsolete change", func(doc *Document) {
			doc.Changes[1].Derived = json.RawMessage(`{"state":"stale","risk_tier":"low","additions":0,"deletions":0,"role_loc":{},"reviewers":[],"evidence_summary":{},"open_findings":0}`)
			setData(doc, TypeRisks, RisksData{Ranked: []string{"rsk_flag_default"}})
		}},
		{"an intent line of an unknown change", TypeIntent, "an intent line origin names change sec_gone", func(doc *Document) {
			setData(doc, TypeIntent, IntentData{Implements: json.RawMessage(`[]`), Lines: []IntentLine{{Text: "Why.", Anchors: json.RawMessage(`[]`), Origin: &BlockOrigin{Change: "sec_gone"}}}})
		}},
		{"a decision of an unknown change", TypeDecisions, "a decision names change sec_gone", func(doc *Document) {
			setData(doc, TypeDecisions, DecisionsData{Items: []json.RawMessage{json.RawMessage(`{"id":"dec_x","by":"agent","text":"T.","anchors":[{"type":"file","path":"a.go"}],"change":"sec_gone"}`)}})
		}},
		{"media of an unknown change", TypeMedia, "a media item origin names change sec_gone", func(doc *Document) {
			var data MediaData
			_ = json.Unmarshal(tileOf(doc, TypeMedia).Data, &data)
			data.Items[0].Origin.Change = "sec_gone"
			setData(doc, TypeMedia, data)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := validDocument(t)
			tc.mutate(doc)
			validated, err := validate(t, doc)
			if err != nil {
				t.Fatalf("a tile-level failure rejected the document: %v", err)
			}
			for _, tile := range validated.Tiles {
				if tile.ID != tc.tile {
					if tile.State == StateUnavailable {
						t.Errorf("tile %s is unavailable too: %v", tile.ID, tile.Problems)
					}
					continue
				}
				if tile.State != StateUnavailable {
					t.Fatalf("tile %s is %s, want unavailable", tile.ID, tile.State)
				}
				if !strings.Contains(strings.Join(tile.Problems, "; "), tc.want) {
					t.Errorf("problems = %v, want one saying %q", tile.Problems, tc.want)
				}
			}
		})
	}
}

// A pending tile may have no data yet; once it has data, the data is checked.
func TestValidatePendingTiles(t *testing.T) {
	doc := golden(t, "draft-context-missing")
	validated, err := validate(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	if tile := tileOf(validated, TypeContext); tile.State != StatePending {
		t.Errorf("context is %s: %v", tile.State, tile.Problems)
	}
	tileOf(doc, TypeContext).Data = json.RawMessage(`{"area":{}}`)
	validated, err = validate(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	if tile := tileOf(validated, TypeContext); tile.State != StateUnavailable {
		t.Errorf("context with malformed data is %s", tile.State)
	}
}

// CheckTiles is the serving path: it checks tiles of a document the host
// built itself, without the document schema.
func TestCheckTiles(t *testing.T) {
	doc := validDocument(t)
	tileOf(doc, TypeEvidence).Data = json.RawMessage(`{"items":[]}`)
	if err := defaultValidator.CheckTiles(doc); err != nil {
		t.Fatal(err)
	}
	if tile := tileOf(doc, TypeEvidence); tile.State != StateUnavailable || len(tile.Problems) == 0 {
		t.Errorf("evidence = %s %v", tile.State, tile.Problems)
	}
	if tile := tileOf(doc, TypeOverview); tile.State != StateReady {
		t.Errorf("overview = %s", tile.State)
	}
	problems, err := defaultValidator.TileProblems(doc, tileOf(doc, TypeOverview))
	if err != nil || len(problems) != 0 {
		t.Errorf("problems = %v, err %v", problems, err)
	}
}

func tileOf(doc *Document, id string) *Tile {
	for i := range doc.Tiles {
		if doc.Tiles[i].ID == id {
			return &doc.Tiles[i]
		}
	}
	panic("no tile " + id)
}

func setData(doc *Document, id string, data any) {
	encoded, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	tileOf(doc, id).Data = encoded
}
