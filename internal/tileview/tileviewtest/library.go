package tileviewtest

import (
	"encoding/json"
	"testing"

	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
	"github.com/baum-ai/baum/tiles/view"
)

// LibraryComposition returns a review view composition of the review (as
// JSON) as an older generation with tile reviews on stored it when its view
// call placed nothing: two library tiles, filled from the library's synthetic
// samples (schema_change's postgres-migration, which has a destructive
// change, and api_change's first sample), and two authored tiles, a table
// and a code excerpt, all placed by the host on the section whose hunk they
// cite. Every citation names the first hunk of the review's first change,
// without lines.
func LibraryComposition(t testing.TB, document []byte) []byte {
	t.Helper()
	normalized, err := review.NormalizeStored(document)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// The first hunk of a change, so the change lists the tiles that cite it.
	var hunk string
	for _, tile := range doc.Tiles {
		if tile.Type == tiles.TypeChange && len(tile.Covers) > 0 {
			hunk = tile.Covers[0]
			break
		}
	}
	if hunk == "" {
		t.Fatal("the review has no change with a hunk")
	}
	content := &view.Composition{Schema: view.CompositionName, Generator: &tiles.Generator{PromptVersion: "fixture"},
		Overview: view.Overview{Screenshots: []string{}, Intent: []view.Intent{}}, Sections: []view.Placement{}}
	for _, pick := range []struct{ name, sample string }{{"schema_change", "postgres-migration"}, {"api_change", ""}} {
		entry, ok := library.Get(pick.name)
		if !ok {
			t.Fatalf("no %s type", pick.name)
		}
		sample := entry.Samples[0]
		for _, s := range entry.Samples {
			if s.Name == pick.sample {
				sample = s
			}
		}
		content.Library = append(content.Library, tiles.LibraryTile{Definition: entry.Encoded, Data: citing(sample.Data, hunk)})
	}
	source := `[{"type":"hunk","id":` + mustQuote(hunk) + `}]`
	content.Authored = []tiles.AuthoredTile{
		{ID: "table_retry_states", Title: "How a failed sync is retried", Data: json.RawMessage(`{"columns":["Failure","Before","Now"],"rows":[` +
			`["GitHub answers 502","gives up and shows an error","retries after 2 s, 4 s and 8 s"],` +
			`["The token expired","gives up and shows an error","asks to sign in again"],` +
			`["The clone is locked","waits forever","waits 30 s, then reports the lock"],` +
			`["Rate limited","retries at once","waits for the reset time GitHub sends"]],"source":` + source + `}`)},
		{ID: "code_sync_backoff", Title: "The backoff that decides when a sync retries", Data: json.RawMessage(`{"language":"go","content":` +
			mustQuote("func backoff(attempt int, reset time.Time) time.Duration {\n\tif !reset.IsZero() {\n\t\treturn time.Until(reset)\n\t}\n\t// …\n\treturn time.Duration(1<<attempt) * time.Second\n}") +
			`,"caption":"A rate limit's reset time wins over the doubling delay.","source":` + source + `}`)},
	}
	if library, authored, report := tiles.NormalizeExtras(doc, content.Library, content.Authored); len(library) != 2 || len(authored) != 2 {
		t.Fatalf("the library composition does not apply: %v", report.Messages())
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// citing rewrites every hunk citation of sample data to name hunk, without
// lines, and drops its related items.
func citing(data json.RawMessage, hunk string) json.RawMessage {
	var value map[string]any
	_ = json.Unmarshal(data, &value)
	delete(value, library.Related) // the sample's items are not the review's
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case []any:
			for _, item := range v {
				walk(item)
			}
		case map[string]any:
			if v["type"] == "hunk" {
				v["id"] = hunk
				delete(v, "lines")
				return
			}
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(value)
	encoded, _ := json.Marshal(value)
	return encoded
}
