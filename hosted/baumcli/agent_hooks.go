package baumcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/baum-ai/baum/hosted/agenthooks"
)

// The committed hook vocabulary lives in hosted/agenthooks because the hosted
// service's onboarding pull request writes the same strings into repositories nobody has
// checked out. Both paths share the command format; their file sets can differ
// because setup enables only the coding agents detected locally.
const entireAgentSessionStart = agenthooks.SessionStartEvent

type agentHookSpec = agenthooks.Spec

func lookupAgentHookSpec(agent string) (agentHookSpec, bool) { return agenthooks.Lookup(agent) }

func validateAgentHook(agent, event string) error { return agenthooks.Validate(agent, event) }

// validateHook checks a hook's agent and event: a user-wide hook also runs
// the events baum only records, which no committed hook carries.
func validateHook(userWide bool, agent, event string) error {
	if userWide {
		return agenthooks.ValidateUserWide(agent, event)
	}
	return validateAgentHook(agent, event)
}

func missingEntireNotice(spec agentHookSpec, event string) string {
	return agenthooks.MissingEntireNotice(spec, event)
}

// ownEntireAgentHooks rewrites the hook commands Entire wrote into the
// committed agent settings so they dispatch through baum. It reports the
// files it changed; they are tracked, so the user commits the result.
func ownEntireAgentHooks(root string) ([]string, error) {
	changed := make([]string, 0, len(agenthooks.All()))
	for _, spec := range agenthooks.All() {
		path := filepath.Join(root, filepath.FromSlash(spec.SettingsFile))
		contents, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", spec.SettingsFile, err)
		}
		if !json.Valid(contents) {
			return nil, fmt.Errorf("read %s: not valid JSON", spec.SettingsFile)
		}
		rewritten, err := agenthooks.OwnCommands(contents)
		if err != nil {
			return nil, fmt.Errorf("rewrite %s: %w", spec.SettingsFile, err)
		}
		if bytes.Equal(rewritten, contents) {
			continue
		}
		if err := os.WriteFile(path, rewritten, 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", spec.SettingsFile, err)
		}
		changed = append(changed, spec.SettingsFile)
	}
	return changed, nil
}

// agentHooksOwned reports whether a coding agent's committed settings already
// carry a hook command baum recognises. `entire status` does not list
// such an agent: Entire recognises only its own wrapper, so once the commands
// dispatch through baum it sees no hooks at all, and asking it to add the
// agent again would append a second copy of every hook.
func agentHooksOwned(root, agent string) bool {
	spec, ok := lookupAgentHookSpec(agent)
	if !ok {
		return false
	}
	contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(spec.SettingsFile)))
	if err != nil {
		return false
	}
	return agenthooks.ContainsOwnedCommand(contents)
}
