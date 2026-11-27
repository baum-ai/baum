package uiserver

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/config"
)

// cloneReadsFixture is a registered clone with main (a.txt, then a second
// commit editing it) and a feature branch adding f.txt.
func cloneReadsFixture(t *testing.T) (handler *LocalHandler, id string, first, second, feature string) {
	t.Helper()
	registry, clone := factsRegistry(t)
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = clone.Path
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(string(out), err)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(clone.Path, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("checkout", "-q", "-b", "main")
	write("a.txt", "one\ntwo\n")
	run("add", "-A")
	run("commit", "-q", "-m", "first")
	first = run("rev-parse", "HEAD")
	run("checkout", "-q", "-b", "feature")
	write("f.txt", "feature\n")
	run("add", "-A")
	run("commit", "-q", "-m", "feature work")
	feature = run("rev-parse", "HEAD")
	run("checkout", "-q", "main")
	write("a.txt", "one\nTWO\n")
	run("add", "-A")
	run("commit", "-q", "-m", "second")
	second = run("rev-parse", "HEAD")
	handler = LocalAPI{Registry: registry, Config: config.Default()}.Handler()
	t.Cleanup(handler.Close)
	return handler, clone.ID, first, second, feature
}

func TestCloneReadsAnswerTheirShapes(t *testing.T) {
	handler, id, first, second, feature := cloneReadsFixture(t)
	get := func(query string) (int, map[string]any) {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "http://local/api/clone/"+query, nil))
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v: %s", query, err, w.Body)
		}
		return w.Code, body
	}

	status, log := get("log?clone=" + id + "&rev=" + second[:10] + "&path=a.txt&limit=1&since=2000-01-01")
	commits, _ := log["commits"].([]any)
	if status != 200 || log["rev"] != second || log["truncated"] != true || len(commits) != 1 || log["paths"].([]any)[0] != "a.txt" {
		t.Fatalf("log = %d %v", status, log)
	}
	commit := commits[0].(map[string]any)
	if commit["sha"] != second || commit["subject"] != "second" || commit["author"] != "Test" || commit["parents"].([]any)[0] != first || commit["date"] == "" {
		t.Fatalf("commit = %v", commit)
	}
	_, whole := get("log?clone=" + id + "&rev=" + second)
	if len(whole["commits"].([]any)) != 2 || whole["truncated"] != false || len(whole["paths"].([]any)) != 0 {
		t.Fatalf("whole = %v", whole)
	}

	status, blame := get("blame?clone=" + id + "&rev=" + second + "&path=a.txt&start=1&end=2")
	lines, _ := blame["lines"].([]any)
	if status != 200 || len(lines) != 2 || lines[0].(map[string]any)["sha"] != first || lines[1].(map[string]any)["sha"] != second ||
		lines[1].(map[string]any)["text"] != "TWO" || lines[1].(map[string]any)["line"] != float64(2) || blame["start"] != float64(1) || blame["end"] != float64(2) {
		t.Fatalf("blame = %d %v", status, blame)
	}

	status, file := get("file?clone=" + id + "&rev=" + feature + "&path=f.txt")
	if status != 200 || file["content"] != "feature\n" || file["truncated"] != false || file["path"] != "f.txt" || file["rev"] != feature {
		t.Fatalf("file = %d %v", status, file)
	}
	status, file = get("file?clone=" + id + "&rev=" + second + "&path=f.txt")
	if status != 200 || file["content"] != nil || file["code"] != "not_a_file" {
		t.Fatalf("missing file = %d %v", status, file)
	}

	status, changed := get("changed?clone=" + id + "&base=" + second + "&head=" + feature)
	files, _ := changed["files"].([]any)
	if status != 200 || changed["merge_base"] != first || changed["base"] != second || changed["head"] != feature || len(files) != 1 || files[0] != "f.txt" || changed["truncated"] != false {
		t.Fatalf("changed = %d %v", status, changed)
	}
}

func TestCloneReadsReportWhatIsWrong(t *testing.T) {
	handler, id, _, second, _ := cloneReadsFixture(t)
	absent := strings.Repeat("a", 40)
	for _, tc := range []struct {
		query  string
		status int
		code   string
	}{
		{"log?clone=nope&rev=" + second, 404, "clone_unknown"},
		{"log?rev=" + second, 400, "invalid"},
		{"log?clone=" + id, 400, "invalid"},
		{"log?clone=" + id + "&rev=--all", 400, "invalid"},
		{"log?clone=" + id + "&rev=" + second + "&limit=x", 400, "invalid"},
		{"log?clone=" + id + "&rev=" + second + "&since=yesterday", 400, "invalid"},
		{"log?clone=" + id + "&rev=" + second + "&path=../x", 400, "invalid"},
		{"log?clone=" + id + "&rev=" + absent, 422, "commit_missing"},
		{"blame?clone=" + id + "&rev=" + second + "&path=a.txt&start=0&end=2", 400, "invalid"},
		{"blame?clone=" + id + "&rev=" + second + "&path=a.txt&start=1&end=5000", 400, "invalid"},
		{"blame?clone=" + id + "&rev=" + second + "&path=a.txt&start=1", 400, "invalid"},
		{"blame?clone=" + id + "&rev=" + absent + "&path=a.txt&start=1&end=2", 422, "commit_missing"},
		{"file?clone=" + id + "&rev=" + second, 400, "invalid"},
		{"file?clone=nope&rev=" + second + "&path=a.txt", 404, "clone_unknown"},
		{"changed?clone=" + id + "&base=" + second, 400, "invalid"},
		{"changed?clone=" + id + "&base=" + second + "&head=" + absent, 422, "commit_missing"},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "http://local/api/clone/"+tc.query, nil))
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != tc.status || body["code"] != tc.code || body["message"] == "" {
			t.Errorf("%s = %d %s", tc.query, w.Code, w.Body)
		}
	}
}
