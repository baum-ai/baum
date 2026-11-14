package agentcontext

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/tiles"
)

// What an agent reads of a review's tiles holds each custom tile's
// definition without its HTML: the tile routes serve it so, and the reader
// passes it on as it is.
func TestTileReadsHoldNoTileSource(t *testing.T) {
	t.Cleanup(tileview.SetFixture(tileviewtest.AddCustomTiles))
	answer, err := tileview.Encode(tileview.Build(tileviewtest.Encode(t, tileviewtest.Base(t)), nil, tiles.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tiles" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(answer.Body.Data)
	})
	clone := func(context.Context, string) (string, error) { return "acme/widgets", nil }
	for name, reader := range map[string]*Reader{
		"tool":   NewReader(api, clone).within([]string{"/api/tiles"}, ""),
		"action": NewReader(api, clone).within([]string{"/api"}, "acme/widgets"),
	} {
		response, err := reader.Get(context.Background(), "/api/tiles?clone=c&pr=42")
		if err != nil || response.Status != 200 {
			t.Fatalf("%s: %v %d", name, err, response.Status)
		}
		body := string(response.Body)
		for _, id := range tileviewtest.CustomTileIDs {
			def := tileviewtest.CustomDefinition(id)
			if !strings.Contains(body, def.Digest) {
				t.Errorf("%s: %s's digest is missing", name, id)
			}
		}
		for _, source := range []string{"baum.tile(", "<script", `\u003cscript`, "outside.invalid/base", `"html":`} {
			if strings.Contains(body, source) {
				t.Errorf("%s: the answer holds tile source (%s)", name, source)
			}
		}
		if !strings.Contains(body, `"source":{"bytes":`) {
			t.Errorf("%s: no served source", name)
		}
	}
}
