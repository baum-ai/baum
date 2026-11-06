package cli

import (
	"bytes"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err = Run(args, strings.NewReader(""), &out, &errOut)
	return out.String(), errOut.String(), err
}

func TestHelpDescribesDesktopCommands(t *testing.T) {
	out, _, err := run(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"daemon", "desktop-setup", "hooks", "help"} {
		if !strings.Contains(out, "\n  "+name+" ") {
			t.Errorf("missing %s: %s", name, out)
		}
	}
	for _, text := range []string{"Hosted", "Entire", "login", "upload", "internal", "completion"} {
		if strings.Contains(out, text) {
			t.Errorf("help contains %q: %s", text, out)
		}
	}
	for _, args := range [][]string{nil, {"unknown"}} {
		_, stderr, err := run(t, args...)
		if err == nil || !strings.Contains(stderr, "Usage:") {
			t.Fatalf("%v: %v %s", args, err, stderr)
		}
	}
}

func TestRemovedCommandsAreUnavailable(t *testing.T) {
	for _, name := range []string{"login", "logout", "whoami", "upload", "setup", "completion", "generate", "bundle", "view", "validate", "refresh", "sessions", "decisions", "features"} {
		if _, _, err := run(t, name); err == nil {
			t.Errorf("removed %s still dispatches", name)
		}
	}
	for _, name := range []string{"generate", "bundle", "view", "validate", "refresh", "sessions", "decisions", "features"} {
		if _, _, err := run(t, "internal", name); err == nil {
			t.Errorf("removed internal %s still dispatches", name)
		}
	}
	for _, name := range []string{"remove", "list", "pick", "unpick", "repositories"} {
		if _, _, err := run(t, "internal", "repos", name); err == nil {
			t.Errorf("removed repos %s still dispatches", name)
		}
	}
	if _, _, err := run(t, "hooks", "bootstrap"); err == nil {
		t.Error("bootstrap still dispatches")
	}
}

func TestDesktopSupportCommandsDispatch(t *testing.T) {
	for _, args := range [][]string{{"internal", "doctor"}, {"internal", "repos", "add"}, {"internal", "reviews", "show"}, {"internal", "reviews", "set"}, {"internal", "github-credential"}, {"daemon", "ensure"}, {"daemon", "status"}, {"daemon", "stop"}, {"daemon", "run"}, {"daemon", "login-path"}, {"daemon", "print-env"}, {"desktop-setup"}} {
		if _, _, err := run(t, append(args, "--help")...); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
	out, _, err := run(t, "--version")
	if err != nil || !strings.HasPrefix(out, "baum version ") {
		t.Fatalf("%s %v", out, err)
	}
}
