package library_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/kit"
	"github.com/baum-ai/baum/tiles/library"
)

// TestRenderDirs checks each tile type directory in $BAUM_TILE_DIRS (comma
// separated, relative to the oss module) as the library's own types are
// checked, and writes what the app's opt-in pictures of tile types draw
// (`just local_app::tile-type-screens`) into $BAUM_TILE_OUT: per type, its
// frame document (<name>.html) and <name>.json with its title, sizes and
// samples, the edge samples the schema yields among them, each with the
// citation payloads a glance and a detail receive. Without them it is
// skipped.
func TestRenderDirs(t *testing.T) {
	dirs, out := os.Getenv("BAUM_TILE_DIRS"), os.Getenv("BAUM_TILE_OUT")
	if dirs == "" || out == "" {
		t.Skip("set BAUM_TILE_DIRS and BAUM_TILE_OUT to check and draw tile type directories")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, dir := range strings.Split(dirs, ",") {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join("..", "..", dir)
		}
		entry, err := library.Load(os.DirFS(dir))
		if err != nil {
			t.Errorf("%s: %v", dir, err)
			continue
		}
		name := entry.Definition.Name
		problems := library.Check(entry)
		for _, problem := range problems {
			t.Errorf("%s: %s", name, problem)
		}
		if len(problems) == 0 {
			t.Logf("%s: every check passes (%d samples, digest %s)", name, len(entry.Samples), entry.Definition.Digest)
		}
		document, err := kit.Compose(entry.Definition.Kit, entry.Definition.Source.HTML)
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(out, name+".html"), document, 0o644); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.MarshalIndent(rendered(t, entry), "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, name+".json"), encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	encoded, _ := json.Marshal(names)
	if err := os.WriteFile(filepath.Join(out, "index.json"), encoded, 0o644); err != nil {
		t.Fatal(err)
	}
}

type renderSample struct {
	Name   string          `json:"name"`
	Edge   bool            `json:"edge,omitempty"`
	Data   json.RawMessage `json:"data"`
	Glance map[string]any  `json:"glance"`
	Detail map[string]any  `json:"detail"`
}

func rendered(t *testing.T, entry library.Entry) map[string]any {
	t.Helper()
	pool := library.Sample{Hunks: map[string]library.Hunk{}, Items: map[string]library.Item{}}
	var citations []json.RawMessage
	var samples []renderSample
	for _, s := range entry.Samples {
		glance, _ := s.Refs("glance")
		detail, _ := s.Refs("detail")
		samples = append(samples, renderSample{Name: s.Name, Data: s.Data, Glance: glance, Detail: detail})
		ids := make([]string, 0, len(s.Hunks))
		for id, h := range s.Hunks {
			pool.Hunks[id] = h
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			raw, _ := json.Marshal(map[string]any{"type": "hunk", "id": id})
			citations = append(citations, raw)
		}
		for id, item := range s.Items {
			pool.Items[id] = item
			kind := "risk"
			if strings.HasPrefix(id, "ev_") {
				kind = "evidence"
			}
			raw, _ := json.Marshal(map[string]any{"type": kind, "id": id})
			citations = append(citations, raw)
		}
	}
	edges, warnings, err := tiles.EdgeSamples(entry.Definition.DataSchema, citations)
	if err != nil {
		t.Errorf("%s: %v", entry.Definition.Name, err)
	}
	for _, w := range warnings {
		t.Logf("%s: %s", entry.Definition.Name, w)
	}
	for _, edge := range edges {
		s := pool
		s.Name, s.Data = edge.Name, edge.Data
		glance, _ := s.Refs("glance")
		detail, _ := s.Refs("detail")
		samples = append(samples, renderSample{Name: edge.Name, Edge: true, Data: edge.Data, Glance: glance, Detail: detail})
	}
	return map[string]any{
		"name": entry.Definition.Name, "title": entry.Definition.Title, "sizes": entry.Definition.Sizes,
		"digest": entry.Definition.Digest, "samples": samples,
	}
}
