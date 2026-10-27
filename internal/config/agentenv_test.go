package config

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestValidateAgentEnv(t *testing.T) {
	if err := ValidateAgentEnv(map[string]string{"GIT_CONFIG_GLOBAL": "~/agent.gitconfig", "_x1": ""}); err != nil {
		t.Fatal(err)
	}
	for _, env := range []map[string]string{
		{"1X": "a"},
		{"A-B": "a"},
		{"": "a"},
		{"A": "a\x00b"},
		{"A": strings.Repeat("a", 4<<10+1)},
	} {
		if err := ValidateAgentEnv(env); err == nil {
			t.Errorf("%q passed", env)
		}
	}
	many := map[string]string{}
	for i := range MaxAgentEnv + 1 {
		many["V"+strings.Repeat("A", i)] = ""
	}
	if err := ValidateAgentEnv(many); err == nil {
		t.Error("more than MaxAgentEnv variables passed")
	}
}

func TestAgentEnvironExpandsLikeAShell(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	t.Setenv("BAUM_TEST_DIR", "/opt/x")
	cfg := Config{AgentEnv: map[string]string{"B": "~/.config/git/agent.gitconfig", "A": "$BAUM_TEST_DIR/${BAUM_TEST_DIR}", "C": "a~b"}}
	want := []string{"A=/opt/x//opt/x", "B=" + home + "/.config/git/agent.gitconfig", "C=a~b"}
	if got := cfg.AgentEnviron(); !slices.Equal(got, want) {
		t.Fatalf("AgentEnviron = %q, want %q", got, want)
	}
	if (Config{}).AgentEnviron() != nil {
		t.Fatal("no agent_env must add nothing")
	}
}
