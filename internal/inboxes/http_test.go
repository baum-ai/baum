package inboxes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type harness struct {
	t       *testing.T
	path    string
	handler http.Handler
	changed int
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, path: filepath.Join(t.TempDir(), "inboxes.json")}
	h.handler = (&API{Store: Open(h.path), Changed: func() { h.changed++ }}).Handler()
	return h
}

func (h *harness) do(method, body string) (int, map[string]any) {
	h.t.Helper()
	req := httptest.NewRequest(method, "/api/inboxes", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		h.t.Fatalf("%s %s: %v: %s", method, body, err, rec.Body)
	}
	return rec.Code, out
}

func (h *harness) put(body string) (int, map[string]any) { return h.do(http.MethodPut, body) }

func entry(name string) string {
	return `{"name":"` + name + `","color":"blue","what":"any","owners":[],"risk":"any","repositories":[],"no_bots":false,"badge":true}`
}

func list(v map[string]any) []map[string]any {
	var out []map[string]any
	for _, e := range v["inboxes"].([]any) {
		out = append(out, e.(map[string]any))
	}
	return out
}

func TestMissingFileReadsEmpty(t *testing.T) {
	h := newHarness(t)
	code, body := h.do(http.MethodGet, "")
	if code != 200 || body["available"] != true || body["revision"] != float64(0) || len(list(body)) != 0 {
		t.Fatalf("%d %v", code, body)
	}
	if _, err := os.Stat(h.path); err == nil {
		t.Fatal("a read created the file")
	}
}

func TestRoundTripOrderAndIDs(t *testing.T) {
	h := newHarness(t)
	code, body := h.put(`{"revision":0,"inboxes":[` + entry("Zeta") + `,` + entry("Alpha") + `]}`)
	if code != 200 || body["revision"] != float64(1) {
		t.Fatalf("%d %v", code, body)
	}
	first := list(body)
	if first[0]["name"] != "Zeta" || first[1]["name"] != "Alpha" || first[0]["id"] == "" || first[0]["id"] == first[1]["id"] {
		t.Fatalf("saved %v", first)
	}
	if h.changed != 1 {
		t.Fatalf("changed %d", h.changed)
	}

	// Reorder, rename one and add a third: ids stay with their entries.
	a, z := first[0]["id"].(string), first[1]["id"].(string)
	update := `{"revision":1,"inboxes":[{"id":"` + z + `","name":"Alpha 2","color":"red","what":"review","owners":["Baum-AI","baum-ai"],"risk":"4","repositories":["baum-ai/baum"],"no_bots":true,"badge":false},{"id":"` + a + `","name":" Zeta ","color":"grey","what":"mine","owners":[],"risk":"3","repositories":[],"no_bots":false,"badge":true},` + entry("New") + `]}`
	code, body = h.put(update)
	if code != 200 || body["revision"] != float64(2) {
		t.Fatalf("%d %v", code, body)
	}
	got := list(body)
	if got[0]["id"] != z || got[1]["id"] != a || got[1]["name"] != "Zeta" || got[2]["id"] == "" {
		t.Fatalf("ids or order lost: %v", got)
	}
	if owners := got[0]["owners"].([]any); len(owners) != 1 || owners[0] != "Baum-AI" {
		t.Fatalf("owners not deduplicated keeping case: %v", owners)
	}

	_, read := h.do(http.MethodGet, "")
	if read["revision"] != float64(2) || len(list(read)) != 3 || list(read)[0]["name"] != "Alpha 2" {
		t.Fatalf("read %v", read)
	}
	// A new store on the same file sees the same data.
	state, err := Open(h.path).Read()
	if err != nil || state.Revision != 2 || len(state.Inboxes) != 3 {
		t.Fatalf("%+v %v", state, err)
	}
}

func TestStaleRevisionAnswersCurrentList(t *testing.T) {
	h := newHarness(t)
	h.put(`{"revision":0,"inboxes":[` + entry("One") + `]}`)
	code, body := h.put(`{"revision":0,"inboxes":[]}`)
	if code != http.StatusConflict || body["code"] != "stale_revision" || body["revision"] != float64(1) || len(list(body)) != 1 {
		t.Fatalf("%d %v", code, body)
	}
	if h.changed != 1 {
		t.Fatalf("changed %d", h.changed)
	}
}

func TestValidation(t *testing.T) {
	h := newHarness(t)
	_, saved := h.put(`{"revision":0,"inboxes":[` + entry("One") + `]}`)
	id := list(saved)[0]["id"].(string)
	with := func(field, value string) string {
		return strings.Replace(entry("X"), field, value, 1)
	}
	many := make([]string, 21)
	for i := range many {
		many[i] = strings.Replace(entry("n"), `"n"`, `"n`+strings.Repeat("a", i)+`"`, 1)
	}
	owners := `"owners":[` + strings.TrimSuffix(strings.Repeat(`"a",`, 0), ",") + `]`
	var fifty []string
	for i := 0; i < 51; i++ {
		fifty = append(fifty, `"o`+strings.Repeat("a", i)+`"`)
	}
	owners = `"owners":[` + strings.Join(fifty, ",") + `]`
	repos := `"repositories":[` + strings.ReplaceAll(strings.Join(fifty, ","), `"o`, `"o/`) + `]`
	for name, tc := range map[string]struct{ body, code string }{
		"too many":        {`[` + strings.Join(many, ",") + `]`, "too_many_inboxes"},
		"empty name":      {`[` + entry("  ") + `]`, "invalid_inbox"},
		"long name":       {`[` + entry(strings.Repeat("é", 41)) + `]`, "invalid_inbox"},
		"duplicate name":  {`[` + entry("a") + `,` + entry("A") + `]`, "duplicate_name"},
		"color":           {`[` + with(`"blue"`, `"pink"`) + `]`, "invalid_inbox"},
		"what":            {`[` + with(`"any"`, `"all"`) + `]`, "invalid_inbox"},
		"risk":            {`[` + with(`"risk":"any"`, `"risk":"5"`) + `]`, "invalid_inbox"},
		"owner shape":     {`[` + with(`"owners":[]`, `"owners":["bad owner"]`) + `]`, "invalid_inbox"},
		"owner hyphen":    {`[` + with(`"owners":[]`, `"owners":["-a"]`) + `]`, "invalid_inbox"},
		"repo shape":      {`[` + with(`"repositories":[]`, `"repositories":["baum"]`) + `]`, "invalid_inbox"},
		"repo dots":       {`[` + with(`"repositories":[]`, `"repositories":["a/.."]`) + `]`, "invalid_inbox"},
		"too many owners": {`[` + with(`"owners":[]`, owners) + `]`, "invalid_inbox"},
		"too many repos":  {`[` + with(`"repositories":[]`, repos) + `]`, "invalid_inbox"},
		"unknown id":      {`[{"id":"nope","name":"B","color":"blue","what":"any","owners":[],"risk":"any","repositories":[],"no_bots":false,"badge":true}]`, "unknown_inbox"},
		"duplicate id":    {`[{"id":"` + id + `","name":"B","color":"blue","what":"any","owners":[],"risk":"any","repositories":[],"no_bots":false,"badge":true},{"id":"` + id + `","name":"C","color":"blue","what":"any","owners":[],"risk":"any","repositories":[],"no_bots":false,"badge":true}]`, "duplicate_id"},
	} {
		code, body := h.put(`{"revision":1,"inboxes":` + tc.body + `}`)
		if code != 400 || body["code"] != tc.code || body["message"] == "" {
			t.Errorf("%s: %d %v", name, code, body)
		}
	}
	for name, body := range map[string]string{"no revision": `{"inboxes":[]}`, "unknown field": `{"revision":1,"inboxes":[],"x":1}`, "not json": `x`, "two": `{"revision":1} {}`} {
		if code, got := h.put(body); code != 400 || got["code"] != "invalid_request" {
			t.Errorf("%s: %d %v", name, code, got)
		}
	}
	_, read := h.do(http.MethodGet, "")
	if read["revision"] != float64(1) || len(list(read)) != 1 {
		t.Fatalf("a refused save changed the list: %v", read)
	}
}

func TestOtherMethodsAreRefused(t *testing.T) {
	h := newHarness(t)
	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPatch} {
		if code, body := h.do(method, "{}"); code != 405 || body["code"] != "method_not_allowed" {
			t.Errorf("%s: %d %v", method, code, body)
		}
	}
}

func TestCorruptFileIsReportedAndKept(t *testing.T) {
	h := newHarness(t)
	const corrupt = `{"revision": 3, "inboxes": [`
	if err := os.WriteFile(h.path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, body := h.do(http.MethodGet, ""); code != 500 || body["code"] != "inboxes_unreadable" {
		t.Fatalf("get %d %v", code, body)
	}
	if code, body := h.put(`{"revision":0,"inboxes":[` + entry("A") + `]}`); code != 500 || body["code"] != "inboxes_unreadable" {
		t.Fatalf("put %d %v", code, body)
	}
	if data, _ := os.ReadFile(h.path); string(data) != corrupt {
		t.Fatalf("the corrupt file was touched: %q", data)
	}
	if h.changed != 0 {
		t.Fatal("changed after a refused save")
	}
}

func TestFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes are not meaningful on Windows")
	}
	h := newHarness(t)
	h.put(`{"revision":0,"inboxes":[` + entry("A") + `]}`)
	h.put(`{"revision":1,"inboxes":[` + entry("B") + `]}`)
	info, err := os.Stat(h.path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", info, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(h.path))
	if len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
}
