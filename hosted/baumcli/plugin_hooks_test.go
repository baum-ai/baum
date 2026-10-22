//go:build !windows

package baumcli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/hosted/agenthooks"
)

func TestShippedPluginHooksKeepExistingOptInsAndProjectDeduplication(t *testing.T) {
	for _, agent := range []string{"claude-code", "codex"} {
		t.Run(agent, func(t *testing.T) {
			world := newHookWorld(t)
			commitEntireSettings(t, world.repo)
			data, err := os.ReadFile(filepath.Join("..", "..", "plugins", "baum-"+agent, "hooks", "hooks.json"))
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Hooks map[string][]struct{ Hooks []struct{ Command string } }
			}
			if err := json.Unmarshal(data, &config); err != nil {
				t.Fatal(err)
			}
			command := config.Hooks["Stop"][0].Hooks[0].Command
			run := func() []string {
				t.Helper()
				cmd := exec.Command("sh", "-c", command)
				cmd.Dir = world.repo
				cmd.Stdin = strings.NewReader(`{"session_id":"s"}`)
				if output, err := cmd.CombinedOutput(); err != nil || len(output) != 0 {
					t.Fatalf("plugin: %v %s", err, output)
				}
				_, entire := world.logs(t)
				return entire
			}
			if got := run(); len(got) != 0 {
				t.Fatalf("no opt-in: Entire=%v", got)
			}
			t.Setenv(tokenEnvVar, "provisioned")
			if got := run(); len(got) != 1 {
				t.Fatalf("opted-in plugin: Entire=%v", got)
			}
			spec, _ := agenthooks.Lookup(agent)
			file := ".claude/settings.json"
			if agent == "codex" {
				file = ".codex/hooks.json"
			}
			projectCommand := agenthooks.EntireCommand(spec, "stop")
			settings := strings.Replace(claudeSettings(t, projectCommand), "SessionStart", "Stop", 1)
			commitFile(t, world.repo, file, settings)
			if agent == "codex" {
				trustCodex(t, world.repo, filepath.Join(world.repo, filepath.FromSlash(file))+":stop:0:0")
			}
			if got := run(); len(got) != 0 {
				t.Fatalf("plugin duplicated project hook: Entire=%v", got)
			}
			if world.requests.Load() != 0 {
				t.Fatal("plugin contacted hosted server")
			}
		})
	}
}

func TestShippedPluginHooksFailQuietlyWithoutCompatibleCLI(t *testing.T) {
	for _, agent := range []string{"claude-code", "codex"} {
		t.Run(agent, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "plugins", "baum-"+agent, "hooks", "hooks.json"))
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Hooks map[string][]struct{ Hooks []struct{ Command string } }
			}
			if err := json.Unmarshal(data, &config); err != nil {
				t.Fatal(err)
			}
			for _, old := range []bool{false, true} {
				bin := t.TempDir()
				if err := os.Symlink("/bin/sh", filepath.Join(bin, "sh")); err != nil {
					t.Fatal(err)
				}
				if old {
					writeExecutable(t, filepath.Join(bin, "baum"), "#!/bin/sh\necho unsupported CLI >&2\nexit 2\n")
				}
				cmd := exec.Command("/bin/sh", "-c", config.Hooks["Stop"][0].Hooks[0].Command)
				cmd.Env = append(os.Environ(), "PATH="+bin)
				if output, err := cmd.CombinedOutput(); err != nil || len(output) != 0 {
					t.Fatalf("old=%v: %v %q", old, err, output)
				}
			}
		})
	}
}
