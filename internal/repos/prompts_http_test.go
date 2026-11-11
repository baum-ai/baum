package repos

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent/prompt"
)

// promptServer serves the global prompt routes beside the registry's.
func promptServer(r *Registry) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/settings/prompts", func(w http.ResponseWriter, q *http.Request) { r.ServePrompts(w, q, GlobalPrompts) })
	mux.HandleFunc("PUT /api/settings/prompts/{id}", func(w http.ResponseWriter, q *http.Request) {
		r.ServePromptPut(w, q, GlobalPrompts, q.PathValue("id"))
	})
	mux.Handle("/api/repositories/", r.Handler())
	return mux
}

type promptRequest func(method, path, body string) (int, []byte)

func promptDo(h http.Handler) promptRequest {
	return func(method, path, body string) (int, []byte) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w.Code, w.Body.Bytes()
	}
}

func promptList(t *testing.T, do promptRequest, path string) []PromptEntry {
	t.Helper()
	code, body := do("GET", path, "")
	var got struct{ Prompts []PromptEntry }
	if code != 200 || json.Unmarshal(body, &got) != nil {
		t.Fatalf("%d %s", code, body)
	}
	return got.Prompts
}

func promptByID(t *testing.T, list []PromptEntry, id prompt.ID) PromptEntry {
	t.Helper()
	for _, e := range list {
		if e.ID == string(id) {
			return e
		}
	}
	t.Fatalf("no entry %s", id)
	return PromptEntry{}
}

func promptMessage(t *testing.T, body []byte) string {
	t.Helper()
	var m struct{ Message string }
	if json.Unmarshal(body, &m) != nil || m.Message == "" {
		t.Fatalf("no message in %s", body)
	}
	return m.Message
}

func TestGlobalPromptRoutes(t *testing.T) {
	r := registry(t)
	do := promptDo(promptServer(r))
	list := promptList(t, do, "/api/settings/prompts")
	cat := prompt.Catalogue()
	if len(list) != len(cat) || len(list) < 11 {
		t.Fatalf("%d entries for %d prompts", len(list), len(cat))
	}
	for i, c := range cat {
		e := list[i]
		if e.ID != string(c.ID) || e.DefaultText != c.Default || e.DefaultVersion != c.DefaultVersion || e.Group != c.Group ||
			e.CustomText != nil || e.BasedOn != nil || e.Outdated || e.Source != "" || e.InheritedText != nil || len(e.SafetyRules) != len(c.SafetyRules) || e.MissingRules == nil || len(e.MissingRules) != 0 {
			t.Fatalf("entry %d: %+v", i, e)
		}
	}
	promptByID(t, list, prompt.IDReviewStagedDetails)
	// A raw list has arrays, never null.
	if _, raw := do("GET", "/api/settings/prompts", ""); strings.Contains(string(raw), `"missing_safety_rules":null`) || strings.Contains(string(raw), `"safety_rules":null`) {
		t.Fatal("null array")
	}

	// Save, then list.
	code, body := do("PUT", "/api/settings/prompts/fix", `{"text":"my fix prompt"}`)
	var saved PromptEntry
	fix, _ := prompt.Lookup(prompt.IDFix)
	if code != 200 || json.Unmarshal(body, &saved) != nil || saved.CustomText == nil || *saved.CustomText != "my fix prompt" ||
		saved.BasedOn == nil || *saved.BasedOn != fix.DefaultVersion || saved.Outdated {
		t.Fatalf("%d %s", code, body)
	}
	// It drops the fix rules.
	if len(saved.MissingRules) != len(fix.SafetyRules) || len(saved.MissingRules) == 0 {
		t.Fatalf("missing %v", saved.MissingRules)
	}
	got := promptByID(t, promptList(t, do, "/api/settings/prompts"), prompt.IDFix)
	if got.CustomText == nil || *got.CustomText != "my fix prompt" || *got.BasedOn != fix.DefaultVersion || got.Outdated {
		t.Fatalf("%+v", got)
	}
	// A text keeping the rules reports none missing.
	if code, body = do("PUT", "/api/settings/prompts/fix", mustJSON(map[string]any{"text": fix.Default + "\nextra"})); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if e := promptByID(t, promptList(t, do, "/api/settings/prompts"), prompt.IDFix); len(e.MissingRules) != 0 {
		t.Fatalf("%v", e.MissingRules)
	}

	// Outdated when the stored version differs.
	if _, e := r.db.Exec(`UPDATE prompt_overrides SET based_on='old' WHERE repository='' AND prompt_id='fix'`); e != nil {
		t.Fatal(e)
	}
	if e := promptByID(t, promptList(t, do, "/api/settings/prompts"), prompt.IDFix); !e.Outdated || *e.BasedOn != "old" {
		t.Fatalf("%+v", e)
	}

	// Reset by null and by "".
	for _, reset := range []string{`{"text":null}`, `{"text":""}`} {
		do("PUT", "/api/settings/prompts/fix", `{"text":"again"}`)
		code, body = do("PUT", "/api/settings/prompts/fix", reset)
		var e PromptEntry
		if code != 200 || json.Unmarshal(body, &e) != nil || e.CustomText != nil || e.BasedOn != nil || e.Outdated {
			t.Fatalf("%s: %d %s", reset, code, body)
		}
		if promptByID(t, promptList(t, do, "/api/settings/prompts"), prompt.IDFix).CustomText != nil {
			t.Fatalf("%s kept the text", reset)
		}
	}

	// Validation: readable message without the sentinel.
	code, body = do("PUT", "/api/settings/prompts/review-group", `{"text":"x\n\n## Submitting\ny"}`)
	if m := promptMessage(t, body); code != 400 || !strings.Contains(m, "Submitting") || strings.Contains(m, "invalid prompt") {
		t.Fatalf("%d %s", code, body)
	}
	if code, body = do("PUT", "/api/settings/prompts/fix", `{"text":"   "}`); code != 400 {
		t.Fatalf("blank %d %s", code, body)
	}
	if code, body = do("PUT", "/api/settings/prompts/nope", `{"text":"x"}`); code != 404 {
		t.Fatalf("unknown %d %s", code, body)
	}
	promptMessage(t, body)
	if code, body = do("PUT", "/api/settings/prompts/fix", `{"text":"`+strings.Repeat("a", 3*prompt.MaxOverrideBytes)+`"}`); code != 413 {
		t.Fatalf("large %d", code)
	}
	promptMessage(t, body)
	// Over the text limit but within the body bound: validation says so.
	if code, _ = do("PUT", "/api/settings/prompts/fix", `{"text":"`+strings.Repeat("a", prompt.MaxOverrideBytes+1)+`"}`); code != 400 {
		t.Fatalf("over limit %d", code)
	}
	for _, bad := range []string{`{"text":"x","extra":1}`, `{"text":"x"} {}`, `{}`, `[]`, `{"text":5}`, ``} {
		if code, body = do("PUT", "/api/settings/prompts/fix", bad); code != 400 {
			t.Fatalf("%q: %d %s", bad, code, body)
		}
		promptMessage(t, body)
	}
	if e := promptByID(t, promptList(t, do, "/api/settings/prompts"), prompt.IDFix); e.CustomText != nil {
		t.Fatalf("a refused request stored %+v", e)
	}
}

func TestRepositoryPromptRoutes(t *testing.T) {
	r := registry(t)
	do := promptDo(promptServer(r))
	const base = "/api/repositories/Owner/Repo/prompts"
	if code, body := do("GET", base, ""); code != 404 {
		t.Fatalf("get unpicked %d %s", code, body)
	}
	code, body := do("PUT", base+"/fix", `{"text":"x"}`)
	if code != 404 {
		t.Fatalf("put unpicked %d %s", code, body)
	}
	promptMessage(t, body)
	if got, _ := r.PromptOverrides(t.Context(), "owner/repo"); len(got) != 0 {
		t.Fatal("stored for an unpicked repository")
	}
	if _, e := r.Pick(t.Context(), "Owner/Repo", nil); e != nil {
		t.Fatal(e)
	}
	fix, _ := prompt.Lookup(prompt.IDFix)

	// Default.
	list := promptList(t, do, base)
	if len(list) != len(prompt.Catalogue()) {
		t.Fatalf("%d", len(list))
	}
	e := promptByID(t, list, prompt.IDFix)
	if e.Source != PromptSourceDefault || e.InheritedText == nil || *e.InheritedText != fix.Default || e.CustomText != nil || len(e.MissingRules) != 0 {
		t.Fatalf("%+v", e)
	}
	// Global.
	if code, body = do("PUT", "/api/settings/prompts/fix", mustJSON(map[string]any{"text": fix.Default + "\nglobal"})); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	e = promptByID(t, promptList(t, do, base), prompt.IDFix)
	if e.Source != PromptSourceGlobal || *e.InheritedText != fix.Default+"\nglobal" || e.CustomText != nil {
		t.Fatalf("%+v", e)
	}
	// Repository, dropping the rules; the global entry is unaffected.
	code, body = do("PUT", base+"/fix", `{"text":"repo fix"}`)
	var saved PromptEntry
	if code != 200 || json.Unmarshal(body, &saved) != nil || saved.Source != PromptSourceRepository || *saved.CustomText != "repo fix" ||
		*saved.BasedOn != fix.DefaultVersion || saved.Outdated || *saved.InheritedText != fix.Default+"\nglobal" || len(saved.MissingRules) != len(fix.SafetyRules) {
		t.Fatalf("%d %s", code, body)
	}
	if g := promptByID(t, promptList(t, do, "/api/settings/prompts"), prompt.IDFix); *g.CustomText != fix.Default+"\nglobal" {
		t.Fatalf("%+v", g)
	}
	// Outdated and reset.
	if _, e := r.db.Exec(`UPDATE prompt_overrides SET based_on='old' WHERE repository='owner/repo'`); e != nil {
		t.Fatal(e)
	}
	if e = promptByID(t, promptList(t, do, base), prompt.IDFix); !e.Outdated {
		t.Fatalf("%+v", e)
	}
	for _, reset := range []string{`{"text":null}`, `{"text":""}`} {
		do("PUT", base+"/fix", `{"text":"again"}`)
		code, body = do("PUT", base+"/fix", reset)
		var e PromptEntry
		if code != 200 || json.Unmarshal(body, &e) != nil || e.CustomText != nil || e.Source != PromptSourceGlobal {
			t.Fatalf("%s: %d %s", reset, code, body)
		}
	}
	// Errors.
	if code, _ = do("PUT", base+"/nope", `{"text":"x"}`); code != 404 {
		t.Fatalf("unknown %d", code)
	}
	if code, body = do("PUT", base+"/review-group", `{"text":"## Submitting"}`); code != 400 || strings.Contains(promptMessage(t, body), "invalid prompt") {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ = do("PUT", base+"/fix", `{"text":"x","a":1}`); code != 400 {
		t.Fatalf("strict %d", code)
	}
	if code, _ = do("PUT", base+"/fix", `{"text":"`+strings.Repeat("a", 3*prompt.MaxOverrideBytes)+`"}`); code != 413 {
		t.Fatalf("large %d", code)
	}
}
