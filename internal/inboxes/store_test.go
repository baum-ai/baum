package inboxes

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/internal/appdb"
)

func stored(t *testing.T, path string) string {
	t.Helper()
	data, err := appdb.Read(path, "state", "inboxes")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func seed(t *testing.T, h *harness, doc string) {
	t.Helper()
	if err := appdb.Write(h.path, "state", "inboxes", []byte(doc)); err != nil {
		t.Fatal(err)
	}
}

func storedItems(t *testing.T, path string) []string {
	t.Helper()
	var doc struct {
		Inboxes []json.RawMessage `json:"inboxes"`
	}
	if err := json.Unmarshal([]byte(stored(t, path)), &doc); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, raw := range doc.Inboxes {
		out = append(out, string(raw))
	}
	return out
}

func TestFreshReadCreatesDefault(t *testing.T) {
	h := newFresh(t)
	code, body := h.do(http.MethodGet, "")
	if code != 200 || body["available"] != true || body["revision"] != float64(1) || body["initialized"] != true {
		t.Fatalf("%d %v", code, body)
	}
	got := list(body)
	if len(got) != 1 || got[0]["name"] != "Default" || got[0]["query"] != "" || got[0]["what"] != "any" ||
		got[0]["risk"] != "any" || got[0]["badge"] != true || got[0]["no_bots"] != false ||
		len(got[0]["owners"].([]any)) != 0 || len(got[0]["repositories"].([]any)) != 0 || !idPattern.MatchString(got[0]["id"].(string)) {
		t.Fatalf("default: %v", got)
	}
	_, again := h.do(http.MethodGet, "")
	state, err := Open(h.path).Read()
	if err != nil || state.Revision != 1 || len(state.Inboxes) != 1 || state.Inboxes[0].ID != got[0]["id"] || list(again)[0]["id"] != got[0]["id"] || again["revision"] != float64(1) {
		t.Fatalf("%+v %v %v", state, err, again)
	}
}

func TestConcurrentFirstReadsCreateOneDefault(t *testing.T) {
	h := newFresh(t)
	const n = 8
	states := make([]State, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			states[i], errs[i] = Open(h.path).Read()
		}()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil || states[i].Revision != 1 || len(states[i].Inboxes) != 1 || states[i].Inboxes[0].ID != states[0].Inboxes[0].ID {
			t.Fatalf("%d: %+v %v", i, states[i], errs[i])
		}
	}
	if len(storedItems(t, h.path)) != 1 {
		t.Fatal("more than one default stored")
	}
}

func TestUpgradeKeepsExistingInboxesAndAppendsDefault(t *testing.T) {
	h := newFresh(t)
	const a = `{"id":"0123456789abcdef0123456789abcdef","name":"Zeta","color":"red","what":"review","owners":["x"],"risk":"4","repositories":["x/y"],"query":"q","no_bots":true,"badge":false}`
	const b = `{"id":"fedcba9876543210fedcba9876543210","name":"Alpha","color":"grey","what":"mine","owners":[],"risk":"3","repositories":[],"no_bots":false,"badge":true,"extra":1}`
	seed(t, h, `{"revision":5,"inboxes":[`+a+`,`+b+`]}`)
	_, body := h.do(http.MethodGet, "")
	got := list(body)
	if body["revision"] != float64(6) || len(got) != 3 || got[2]["name"] != "Default" {
		t.Fatalf("%v", body)
	}
	items := storedItems(t, h.path)
	if len(items) != 3 || items[0] != a || items[1] != b {
		t.Fatalf("existing inboxes changed: %s", stored(t, h.path))
	}
}

func TestUpgradeAtTheLimitAddsNothing(t *testing.T) {
	h := newFresh(t)
	var items []string
	for i := range MaxInboxes {
		items = append(items, `{"id":"`+strings.Repeat("a", 31)+string(rune('a'+i%6))+`","name":"n`+strings.Repeat("x", i)+`","color":"blue","what":"any","owners":[],"risk":"any","repositories":[],"query":"","no_bots":false,"badge":true}`)
	}
	seed(t, h, `{"revision":2,"inboxes":[`+strings.Join(items, ",")+`]}`)
	_, body := h.do(http.MethodGet, "")
	got := list(body)
	if body["revision"] != float64(3) || body["initialized"] != true || len(got) != MaxInboxes || got[0]["name"] != "n" {
		t.Fatalf("%v", body)
	}
	for _, g := range got {
		if g["name"] == "Default" {
			t.Fatal("added a default beyond the limit")
		}
	}
	for i, raw := range storedItems(t, h.path) {
		if raw != items[i] {
			t.Fatalf("inbox %d changed: %s", i, raw)
		}
	}
	if _, again := h.do(http.MethodGet, ""); again["revision"] != float64(3) {
		t.Fatalf("written again: %v", again)
	}
}

func TestDefaultNameAvoidsCollisions(t *testing.T) {
	h := newFresh(t)
	seed(t, h, `{"revision":1,"inboxes":[`+strings.Replace(entry("default"), `{"name"`, `{"id":"0123456789abcdef0123456789abcdef","name"`, 1)+`]}`)
	_, body := h.do(http.MethodGet, "")
	got := list(body)
	if len(got) != 2 || got[0]["name"] != "default" || got[1]["name"] != "Default 2" {
		t.Fatalf("%v", got)
	}
}

func TestDeletedDefaultIsNotRecreated(t *testing.T) {
	h := newFresh(t)
	_, body := h.do(http.MethodGet, "")
	def := list(body)[0]["id"].(string)
	code, saved := h.put(`{"revision":1,"inboxes":[` + entry("Other") + `]}`)
	if code != 200 || saved["revision"] != float64(2) || saved["initialized"] != true {
		t.Fatalf("%d %v", code, saved)
	}
	for range 2 {
		state, err := Open(h.path).Read()
		if err != nil || state.Revision != 2 || len(state.Inboxes) != 1 || state.Inboxes[0].Name != "Other" || state.Inboxes[0].ID == def {
			t.Fatalf("%+v %v", state, err)
		}
	}
}

func TestStaleRevisionZeroCarriesInitializedList(t *testing.T) {
	h := newFresh(t)
	code, body := h.put(`{"revision":0,"inboxes":[` + entry("A") + `]}`)
	if code != http.StatusConflict || body["code"] != "stale_revision" || body["revision"] != float64(1) || body["initialized"] != true ||
		len(list(body)) != 1 || list(body)[0]["name"] != "Default" {
		t.Fatalf("%d %v", code, body)
	}
	if state, err := Open(h.path).Read(); err != nil || state.Revision != 1 || state.Inboxes[0].ID != list(body)[0]["id"] {
		t.Fatalf("%+v %v", state, err)
	}
}

func TestLastInboxCannotBeDeleted(t *testing.T) {
	h := newFresh(t)
	h.do(http.MethodGet, "")
	before := stored(t, h.path)
	code, body := h.put(`{"revision":1,"inboxes":[]}`)
	if code != 400 || body["code"] != "last_inbox" || body["message"] == "" {
		t.Fatalf("%d %v", code, body)
	}
	if stored(t, h.path) != before || h.changed != 0 {
		t.Fatal("a refused save changed the document")
	}
}

func TestCorruptDocumentIsNeverInitialized(t *testing.T) {
	h := newFresh(t)
	for _, corrupt := range []string{`{"revision": 3, "inboxes": [`, `{"revision":-1,"inboxes":[]}`, `{"revision":1,"inboxes":"x"}`} {
		seed(t, h, corrupt)
		if code, body := h.do(http.MethodGet, ""); code != 500 || body["code"] != CodeUnreadable {
			t.Fatalf("get %d %v", code, body)
		}
		if code, body := h.put(`{"revision":0,"inboxes":[` + entry("A") + `]}`); code != 500 || body["code"] != CodeUnreadable {
			t.Fatalf("put %d %v", code, body)
		}
		if got := stored(t, h.path); got != corrupt {
			t.Fatalf("document touched: %q", got)
		}
	}
}
