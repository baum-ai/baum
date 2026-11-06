package agentmatrix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/baum-ai/baum/hosted/agenthooks"
	"github.com/baum-ai/baum/internal/agentplugins"
)

// checkHookEvents looks in every file of the npm install for each hook event
// name baum registers for the agent. Agents ignore a hook for an event they do
// not know, so a name missing from a release means that hook never runs there.
func (m *matrix) checkHookEvents() {
	user, ok := agenthooks.LookupUserWide(m.agent)
	if !ok {
		m.record("events", "fail", "baum has no user-wide hooks for %s", m.agent)
		return
	}
	var names []string
	seen := map[string]bool{}
	for _, p := range user.Placements {
		if !seen[p.HookEvent] {
			seen[p.HookEvent] = true
			names = append(names, p.HookEvent)
		}
	}
	found := map[string]bool{}
	files := 0
	_ = filepath.WalkDir(m.prefix, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() == 0 || info.Size() > 1<<30 {
			return nil
		}
		files++
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, n := range names {
			if !found[n] && bytes.Contains(data, []byte(n)) {
				found[n] = true
			}
		}
		return nil
	})
	var missing []string
	for _, n := range names {
		if !found[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		m.record("events", "fail", "hook events baum registers that appear nowhere in the %d installed files: %v", files, missing)
		return
	}
	m.record("events", "pass", "all %d hook events baum registers appear in the installed package: %s", len(names), strings.Join(names, ", "))
}

// native runs the agent's own plugin command, as baum's installer does.
func (m *matrix) native(h home, args ...string) (string, error) {
	out, _, err := h.run(h.dir, 90*time.Second, "", m.bin, args...)
	return out, err
}

// installLocal registers this checkout's marketplace (oss/ is the public
// repository's root) and installs baum@baum from it with the agent's own
// commands, offline.
func (m *matrix) installLocal(h home) error {
	if out, err := m.native(h, "plugin", "marketplace", "add", m.repo); err != nil {
		return fmt.Errorf("marketplace add: %v: %s", err, oneLine(out))
	}
	args := []string{"plugin", "add", agentplugins.Plugin, "--json"}
	if m.agent == "claude-code" {
		args = []string{"plugin", "install", agentplugins.Plugin, "--scope", "user"}
	}
	if out, err := m.native(h, args...); err != nil {
		return fmt.Errorf("plugin install: %v: %s", err, oneLine(out))
	}
	return nil
}

// userConfig is a configuration the user already had, which installing and
// removing the plugin must keep.
func (m *matrix) seedConfig(h home) (path string, original []byte) {
	if m.agent == "claude-code" {
		path = filepath.Join(h.dir, ".claude", "settings.json")
		original = []byte(`{
  "model": "sonnet",
  "permissions": {"allow": ["Bash(ls:*)"]},
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "echo user-stop-hook"}]}]}
}
`)
	} else {
		path = filepath.Join(h.dir, ".codex", "config.toml")
		original = []byte("model = \"gpt-5\"\n\n[projects.\"/work/demo\"]\ntrust_level = \"trusted\"\n")
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		m.t.Fatal(err)
	}
	return path, original
}

// kept reports what installing and removing left of the user's configuration:
// whether everything the user had survives, and what the agent added.
func (m *matrix) kept(path string, original []byte) (intact bool, residue string) {
	after, err := os.ReadFile(path)
	if err != nil {
		return false, "configuration file gone: " + err.Error()
	}
	if bytes.Equal(after, original) {
		return true, "configuration byte-identical to the original"
	}
	if m.agent == "claude-code" {
		var before, now map[string]any
		if json.Unmarshal(original, &before) != nil || json.Unmarshal(after, &now) != nil {
			return false, "configuration is not JSON afterwards"
		}
		var added []string
		for k, v := range now {
			if _, ok := before[k]; !ok {
				added = append(added, k+"="+oneLine(fmt.Sprint(v)))
			}
		}
		for k, v := range before {
			if !reflect.DeepEqual(now[k], v) {
				return false, "user key changed: " + k
			}
		}
		return true, "user keys intact; agent added " + strings.Join(added, ", ")
	}
	oldLines := strings.Split(string(original), "\n")
	newText := "\n" + string(after)
	var added []string
	for _, l := range oldLines {
		if l != "" && !strings.Contains(newText, "\n"+l+"\n") && !strings.HasSuffix(newText, "\n"+l) {
			return false, "user line lost: " + l
		}
	}
	for _, l := range strings.Split(string(after), "\n") {
		if l != "" && !strings.Contains("\n"+string(original)+"\n", "\n"+l+"\n") {
			added = append(added, l)
		}
	}
	return true, "user lines intact; agent added " + oneLine(strings.Join(added, " | "))
}

// checkPluginNative installs the plugin from this checkout through the agent's
// native commands, reads it back the way baum does, removes it and checks the
// user's own configuration is intact.
func (m *matrix) checkPluginNative() {
	h := m.newHome()
	h.setenv(m.t)
	path, original := m.seedConfig(h)
	if err := m.installLocal(h); err != nil {
		m.record("plugin", "fail", "%v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := agentplugins.New()
	status, err := client.Status(ctx, m.agent)
	if err != nil || !status.Installed || !status.Enabled || status.Version == "" || status.Version == "local" {
		m.record("plugin", "fail", "baum read the installed plugin back as %+v, %v", status, err)
		return
	}
	_, err = client.Uninstall(ctx, m.agent)
	if err != nil {
		m.record("plugin", "fail", "uninstall: %v", err)
		return
	}
	after, err := client.Status(ctx, m.agent)
	if err != nil || after.Installed {
		m.record("plugin", "fail", "after uninstall: %+v, %v", after, err)
		return
	}
	intact, residue := m.kept(path, original)
	if !intact {
		m.record("plugin", "fail", "installed %s %s, removed, but %s", agentplugins.Plugin, status.Version, residue)
		return
	}
	m.record("plugin", "pass", "installed %s %s from this checkout, read back installed and enabled, removed; %s", agentplugins.Plugin, status.Version, residue)
}

// checkTrust asks Codex, through the same private app-server baum's Settings
// uses, whether the plugin's hooks are trusted: all of them must load and
// await review (untrusted). Claude Code asks for no separate hook trust.
func (m *matrix) checkTrust() {
	if m.agent != "codex" {
		m.record("trust", "skip", "Claude Code has no separate hook trust review")
		return
	}
	h := m.newHome()
	h.setenv(m.t)
	if err := m.installLocal(h); err != nil {
		m.record("trust", "fail", "%v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	state := agentplugins.New().CodexTrust(ctx, []string{h.dir}).State
	switch {
	case state == "untrusted":
		m.record("trust", "pass", "Codex loads every baum hook and reports them untrusted until reviewed with /hooks")
	case m.support == "below":
		m.record("trust", "pass", "state %q on a release below baum's floor", state)
	default:
		m.record("trust", "fail", "hook readiness is %q, want untrusted (all hooks loaded, none trusted)", state)
	}
}

// checkBaumInstall runs baum's own installer, `baum hooks install --user`,
// which registers the public GitHub marketplace, so it needs the network.
func (m *matrix) checkBaumInstall() {
	if err := exec.Command("git", "ls-remote", "--exit-code", "https://github.com/baum-ai/baum", "HEAD").Run(); err != nil {
		m.record("baum-install", "skip", "github.com/baum-ai/baum is not reachable: %v", err)
		return
	}
	h := m.newHome()
	path, original := m.seedConfig(h)
	run := func(args ...string) (string, error) {
		out, _, err := h.run(h.dir, 3*time.Minute, "", m.baum, args...)
		return out, err
	}
	status := func() (installed, enabled bool, version, errText string) {
		out, err := run("hooks", "status", "--user", "--json")
		var s struct {
			Agents []struct {
				Agent              string
				Installed, Enabled bool
				Version, Error     string
			}
		}
		if err != nil || json.Unmarshal([]byte(out), &s) != nil {
			return false, false, "", "status: " + oneLine(out)
		}
		for _, a := range s.Agents {
			if a.Agent == m.agent {
				return a.Installed, a.Enabled, a.Version, a.Error
			}
		}
		return false, false, "", "status lists no " + m.agent
	}
	if out, err := run("hooks", "install", "--user", "--agent", m.agent); err != nil {
		if m.support == "below" {
			m.record("baum-install", "pass", "refused on a release below baum's floor: %s", oneLine(out))
		} else {
			m.record("baum-install", "fail", "install: %v: %s", err, oneLine(out))
		}
		return
	}
	installed, enabled, version, errText := status()
	if !installed || !enabled {
		m.record("baum-install", "fail", "after install: installed=%v enabled=%v version=%q %s", installed, enabled, version, errText)
		return
	}
	if out, err := run("hooks", "uninstall", "--user", "--agent", m.agent); err != nil {
		m.record("baum-install", "fail", "uninstall: %v: %s", err, oneLine(out))
		return
	}
	if installed, _, _, errText = status(); installed {
		m.record("baum-install", "fail", "still installed after uninstall %s", errText)
		return
	}
	intact, residue := m.kept(path, original)
	if !intact {
		m.record("baum-install", "fail", "installed %s, removed, but %s", version, residue)
		return
	}
	m.record("baum-install", "pass", "baum hooks install/status/uninstall --user: public plugin %s; %s", version, residue)
}
