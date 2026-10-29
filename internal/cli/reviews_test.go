package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/internal/repos"
)

func TestReviewsCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("BAUM_BACKEND", "")
	t.Setenv("BAUM_MODEL", "")
	if e := os.MkdirAll(filepath.Join(home, "baum"), 0o700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(home, "baum", "config.toml"), []byte("backend = \"claude\"\nmodel = \"file-model\"\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		e := Run(append([]string{"internal", "reviews"}, args...), nil, &out, &errOut)
		return out.String(), e
	}
	show := func() reviewSettings {
		t.Helper()
		raw, e := run("show")
		if e != nil {
			t.Fatal(e)
		}
		var s reviewSettings
		if e = json.Unmarshal([]byte(raw), &s); e != nil {
			t.Fatal(e)
		}
		return s
	}
	// Until the app chooses, the config file decides.
	if got := show(); got != (reviewSettings{Backend: "claude", Model: "file-model", Automatic: true, PullRequests: "both"}) {
		t.Fatalf("defaults %+v", got)
	}
	if _, e := run("set", "--backend=codex", "--model=gpt-5.5", "--automatic=false", "--pull-requests=requested"); e != nil {
		t.Fatal(e)
	}
	if got := show(); got != (reviewSettings{Backend: "codex", Model: "gpt-5.5", Automatic: false, PullRequests: "requested"}) {
		t.Fatalf("after set %+v", got)
	}
	if _, e := run("set", "--pull-requests=all"); e != nil {
		t.Fatal(e)
	}
	if got := show(); got.PullRequests != "all" {
		t.Fatalf("after choosing all %+v", got)
	}
	// An empty model is the backend's default, not the file's model.
	if _, e := run("set", "--model=", "--pull-requests=both"); e != nil {
		t.Fatal(e)
	}
	if got := show(); got.Model != "" || got.PullRequests != "both" || got.Backend != "codex" {
		t.Fatalf("after clearing the model %+v", got)
	}
	// Choosing cursor-agent acknowledges that its cost is unknown.
	if _, e := run("set", "--backend=cursor-agent", "--model=composer-2.5"); e != nil {
		t.Fatal(e)
	}
	r, e := repos.Open("")
	if e != nil {
		t.Fatal(e)
	}
	prefs, e := r.Preferences(context.Background())
	_ = r.Close()
	if e != nil || !prefs.CursorAgentAcknowledged {
		t.Fatalf("cursor-agent not acknowledged %+v %v", prefs, e)
	}
	if _, e := run("set", "--backend=codex"); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{{"set", "--backend=invalid"}, {"set", "--pull-requests=everyone"}} {
		if _, e := run(args...); e == nil {
			t.Errorf("%v accepted", args)
		}
	}
	// Fixes follow the reviews until a fix agent is chosen.
	if _, e := run("set", "--fix-backend=claude", "--fix-model=sonnet"); e != nil {
		t.Fatal(e)
	}
	if got := show(); got.FixBackend != "claude" || got.FixModel != "sonnet" {
		t.Fatalf("after choosing a fix agent %+v", got)
	}
	if _, e := run("set", "--fix-backend=cursor-agent"); e == nil {
		t.Error("cursor-agent accepted for fixes")
	}
	if _, e := run("set", "--fix-backend="); e != nil {
		t.Fatal(e)
	}
	if got := show(); got.FixBackend != "" || got.FixModel != "" {
		t.Fatalf("after following reviews again %+v", got)
	}
	if _, e := run("set", "--fix-model=sonnet"); e == nil {
		t.Error("a fix model accepted without a fix agent")
	}
	if got := show(); got.Backend != "codex" || got.PullRequests != "both" {
		t.Fatalf("a rejected change was saved %+v", got)
	}
}
