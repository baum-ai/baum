package replay_test

import (
	"slices"
	"testing"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/replay"
)

// hunk is a synthetic hunk of path, whose role is the one the bundle derives
// from the path, with the patch's changed lines given.
func hunk(path string, lines ...string) bundle.Hunk {
	patch := "@@ -1,3 +1,3 @@\n context"
	for _, l := range lines {
		patch += "\n" + l
	}
	return bundle.Hunk{ID: "h_" + path, Path: path, Role: bundle.RoleForPath(path), Patch: patch}
}

func TestSchemaDetector(t *testing.T) {
	for _, tc := range []struct {
		name string
		diff []bundle.Hunk
		want bool
	}{
		{"nothing", nil, false},
		{"plain code", []bundle.Hunk{hunk("api/server.go", "+x := 1", "-y := 2")}, false},
		{"create table in a query string", []bundle.Hunk{hunk("store/store.go", "+const q = `CREATE TABLE versions (id integer)`")}, true},
		{"alter table removed", []bundle.Hunk{hunk("store/store.go", "-\tdb.Exec(\"alter table runs drop column note\")")}, true},
		{"ddl in a context line only", []bundle.Hunk{{Path: "a.go", Role: "code", Patch: "@@ -1 +1 @@\n CREATE TABLE a (b int)\n+x"}}, false},
		{"a migration path", []bundle.Hunk{hunk("platform/migrations/0042_add.up.txt", "+anything")}, true},
		{"a sql file", []bundle.Hunk{hunk("db/queries/runs.sql", "+select 1")}, true},
		{"sql in a test", []bundle.Hunk{hunk("db/testdata/schema.sql", "+create table t (a int)"), hunk("store/store_test.go", "+db.Exec(`create table t (a int)`)")}, false},
		{"ddl in docs", []bundle.Hunk{hunk("docs/design.md", "+create table runs (id int)")}, false},
		{"generated sqlc output", []bundle.Hunk{hunk("internal/db/sqlc/models.go", "+// CREATE TABLE runs")}, false},
		{"prose that is not ddl", []bundle.Hunk{hunk("a.go", "+// we create a table of values and drop it")}, false},
		{"add column", []bundle.Hunk{hunk("store/store.go", "+ALTER TABLE runs ADD COLUMN cost real")}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := replay.SchemaChange(tc.diff); got != tc.want {
				t.Errorf("SchemaChange = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAPIDetector(t *testing.T) {
	for _, tc := range []struct {
		name string
		diff []bundle.Hunk
		want bool
	}{
		{"nothing", nil, false},
		{"plain code", []bundle.Hunk{hunk("api/server.go", "+x := 1")}, false},
		{"mux handle", []bundle.Hunk{hunk("internal/server/routes.go", `+	mux.HandleFunc("GET /api/runs", s.listRuns)`)}, true},
		{"router verb", []bundle.Hunk{hunk("internal/server/routes.go", `-	r.Post("/runs/{id}/cancel", cancel)`)}, true},
		{"python decorator", []bundle.Hunk{hunk("app/routes.py", `+@router.get("/items")`)}, true},
		{"express route", []bundle.Hunk{hunk("server/index.js", "+app.get('/health', handler)")}, true},
		{"a verb call that is not a route", []bundle.Hunk{hunk("client/client.go", `+	resp, err := c.Get(url)`)}, false},
		{"tool definition", []bundle.Hunk{hunk("agent/tools.go", `+	server.AddTool(mcp.NewTool("search"), search)`)}, true},
		{"input schema beside a tool name", []bundle.Hunk{hunk("agent/tools.ts", `+  name: "search",`, `+  input_schema: { type: "object" },`)}, true},
		{"input schema in a call to a model", []bundle.Hunk{hunk("agent/client.ts", `+  input_schema: { type: "object" },`)}, false},
		{"published schema file", []bundle.Hunk{hunk("oss/schema/review-1.json", "+  \"x\": 1")}, true},
		{"schema suffix", []bundle.Hunk{hunk("api/run.schema.json", "+{}")}, true},
		{"openapi", []bundle.Hunk{hunk("openapi.yaml", "+paths: {}")}, true},
		{"a json file elsewhere", []bundle.Hunk{hunk("config/settings.json", "+{}")}, false},
		{"route in a test", []bundle.Hunk{hunk("internal/server/routes_test.go", `+	mux.HandleFunc("GET /x", h)`)}, false},
		{"route in docs", []bundle.Hunk{hunk("docs/api.md", `+mux.HandleFunc("GET /x", h)`)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := replay.APIChange(tc.diff); got != tc.want {
				t.Errorf("APIChange = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDetectorsAvoidTheObviousFalsePositives(t *testing.T) {
	for _, tc := range []struct {
		name         string
		diff         []bundle.Hunk
		schema, apis bool
	}{
		{"a comment that mentions ddl", []bundle.Hunk{hunk("store/store.go", "+// create table runs (id int) happens at startup")}, false, false},
		{"a ui string", []bundle.Hunk{hunk("ui/dialog.tsx", `+  <Button>Drop table?</Button>`, `+  label="Create table of values"`)}, false, false},
		{"statement in a string", []bundle.Hunk{hunk("store/store.go", "+\tdb.Exec(`DROP TABLE IF EXISTS runs;`)")}, true, false},
		{"a client call with a path", []bundle.Hunk{hunk("client/api.go", `+	resp, err := client.Get("/v1/runs")`, `+	http.Post("/x", "", nil)`)}, false, false},
		{"a route on a router", []bundle.Hunk{hunk("server/api.go", `+	router.Get("/v1/runs", list)`)}, false, true},
		{"rails routes", []bundle.Hunk{hunk("config/routes.rb", "+  resources :runs")}, false, true},
		{"a decorator", []bundle.Hunk{hunk("app/main.py", `+@app.post("/runs")`)}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := replay.SchemaChange(tc.diff); got != tc.schema {
				t.Errorf("SchemaChange = %v, want %v", got, tc.schema)
			}
			if got := replay.APIChange(tc.diff); got != tc.apis {
				t.Errorf("APIChange = %v, want %v", got, tc.apis)
			}
		})
	}
}

func TestDetectionsSayWhatTheyRestOn(t *testing.T) {
	found := replay.Detect([]bundle.Hunk{hunk("db/migrations/1.sql", "+x"), hunk("server/api.go", `+	mux.HandleFunc("GET /x", h)`), hunk("config/routes.rb", "+get 'x'")})
	if !found.Schema || !found.API || found.Blind || !slices.Equal(found.Evidence, []string{replay.EvidenceAPIRoute, replay.EvidenceAPIRoutesFile, replay.EvidenceSchemaPath}) {
		t.Errorf("%+v", found)
	}
	// A case with no patch text is invisible to the text rules.
	blind := replay.Detect([]bundle.Hunk{{Path: "server/api.go", Role: "code"}, {Path: "db/migrations/1.sql", Role: "migration"}})
	if !blind.Blind || !blind.Schema || blind.API {
		t.Errorf("%+v", blind)
	}
	if replay.Detect(nil).Blind {
		t.Error("an empty diff is blind")
	}
}

func TestDetectionsMatchTheFilters(t *testing.T) {
	both := replay.Detections{Schema: true, API: true}
	schema, api, none := replay.Detections{Schema: true}, replay.Detections{API: true}, replay.Detections{}
	for _, tc := range []struct {
		filter replay.Detector
		found  replay.Detections
		want   bool
	}{
		{replay.DetectSchema, schema, true}, {replay.DetectSchema, api, false},
		{replay.DetectAPI, api, true}, {replay.DetectAPI, both, true}, {replay.DetectAPI, none, false},
		{replay.DetectAny, schema, true}, {replay.DetectAny, none, false},
		{replay.DetectNone, none, true}, {replay.DetectNone, both, false},
		{"", none, true},
	} {
		if got := tc.found.Matches(tc.filter); got != tc.want {
			t.Errorf("%+v matches %q = %v, want %v", tc.found, tc.filter, got, tc.want)
		}
	}
}
