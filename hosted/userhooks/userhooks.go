// Package userhooks installs, updates and removes baum's hooks in the
// user-wide settings of Claude Code (~/.claude/settings.json) and Codex
// (~/.codex/hooks.json), so the agents run baum in every repository rather
// than only in those that commit its hooks.
//
// The files belong to the user and their agents, so baum edits them as text:
// it adds, replaces or removes only the hook handlers whose command is its
// own user-wide dispatcher (agenthooks.UserDispatcher), keeps every other
// byte, writes nothing when nothing changes, refuses a file it cannot parse
// rather than replace it, and swaps the new file in atomically after saving
// the previous one in baum's state directory.
package userhooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/baum-ai/baum/hosted/agenthooks"
)

// maxSettingsBytes bounds the settings file baum reads; a larger one is
// refused rather than loaded.
const maxSettingsBytes = 8 << 20

// maxEdits bounds the edit loop. Each edit removes, replaces or adds one
// handler, so a settings file needs at most a few per placement.
const maxEdits = 256

// trustByPosition lists the agents that key a user's trust in a hook to its
// position in the file (Codex: path, event, group and handler index), so
// moving a hook makes the agent stop running it until it is trusted again.
var trustByPosition = map[string]bool{"codex": true}

// Agents lists the agents that have user-wide hooks, in a fixed order.
func Agents() []string {
	agents := []string{}
	for _, spec := range agenthooks.All() {
		if _, ok := agenthooks.LookupUserWide(spec.Slug); ok {
			agents = append(agents, spec.Slug)
		}
	}
	return agents
}

// Result describes what Install or Uninstall did to one agent's settings.
type Result struct {
	Agent string
	// Path is the settings file, below the home directory.
	Path string
	// Changed reports whether the file was written or removed.
	Changed bool
	// Removed reports that Uninstall deleted the file: baum had created it,
	// and nothing but an empty object was left.
	Removed bool
	// Backup is the saved previous file, when one existed and was changed.
	Backup string
	// Shifted names the user's own hooks whose position changed, for an agent
	// that keys trust to position: it skips them until the user trusts them
	// again (Codex: /hooks).
	Shifted []string
}

// Install adds baum's user-wide hooks to an agent's settings below home, or
// brings earlier ones up to date. Running it again changes nothing.
func Install(home, agent string) (Result, error) {
	return update(home, agent, true)
}

// Uninstall removes baum's user-wide hooks from an agent's settings below
// home and leaves everything else. A missing file stays missing, and a file
// baum created that is left empty is removed.
func Uninstall(home, agent string) (Result, error) {
	return update(home, agent, false)
}

// Pending reports whether Install would change an agent's settings below
// home, without changing anything.
func Pending(home, agent string) (bool, error) {
	settings, ok := agenthooks.LookupUserWide(agent)
	if !ok {
		return false, fmt.Errorf("%s has no user-wide hooks baum can install", agent)
	}
	file, err := readSettingsFile(filepath.Join(home, filepath.FromSlash(settings.SettingsFile)))
	if err != nil {
		return false, err
	}
	next, _, err := installSettings(file.contents, agent)
	if err != nil {
		return false, err
	}
	return !file.existed || !bytes.Equal(next, file.contents), nil
}

func update(home, agent string, install bool) (Result, error) {
	settings, ok := agenthooks.LookupUserWide(agent)
	if !ok {
		return Result{}, fmt.Errorf("%s has no user-wide hooks baum can install", agent)
	}
	path := filepath.Join(home, filepath.FromSlash(settings.SettingsFile))
	result := Result{Agent: agent, Path: path}
	if err := refuseForeignHome(home); err != nil {
		return result, err
	}
	state := stateDir(home)
	created := filepath.Join(state, "user-hooks", agent+".created")
	file, err := readSettingsFile(path)
	if err != nil {
		return result, err
	}
	if !file.existed && !install {
		_ = os.Remove(created)
		return result, nil
	}
	change := uninstallSettings
	if install {
		change = installSettings
	}
	next, shifted, err := change(file.contents, agent)
	if err != nil {
		return result, fmt.Errorf("%s: %w; leaving it unchanged", path, err)
	}
	if file.existed && bytes.Equal(next, file.contents) {
		return result, nil
	}
	if trustByPosition[agent] {
		result.Shifted = shifted
	}
	if file.existed {
		result.Backup = filepath.Join(state, "backups", agent+"-"+filepath.Base(path))
		if err := os.MkdirAll(filepath.Dir(result.Backup), 0o700); err != nil {
			return result, fmt.Errorf("back up %s: %w", path, err)
		}
		if err := writeAtomic(result.Backup, file.contents, 0o600, nil); err != nil {
			return result, fmt.Errorf("back up %s: %w", path, err)
		}
	}
	if !install && string(bytes.TrimSpace(next)) == "{}" && isRegular(created) {
		if err := file.unchanged(); err != nil {
			return result, fmt.Errorf("remove %s: %w", path, err)
		}
		if err := os.Remove(file.target); err != nil {
			return result, fmt.Errorf("remove %s: %w", path, err)
		}
		_ = os.Remove(created)
		result.Changed, result.Removed = true, true
		return result, nil
	}
	if !file.existed {
		if err := os.MkdirAll(filepath.Dir(file.target), 0o700); err != nil {
			return result, fmt.Errorf("create %s: %w", filepath.Dir(file.target), err)
		}
	}
	if err := writeAtomic(file.target, next, file.mode, &file); err != nil {
		return result, fmt.Errorf("write %s: %w", path, err)
	}
	result.Changed = true
	if !file.existed {
		// Remembered so that uninstalling can remove the file again; losing
		// the note only leaves an empty object behind.
		if err := os.MkdirAll(filepath.Dir(created), 0o700); err == nil {
			_ = os.WriteFile(created, []byte(path+"\n"), 0o600)
		}
	}
	return result, nil
}

func isRegular(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// InstallSettings returns the settings with baum's user-wide hooks for agent
// in place: missing handlers added at the end of their event's list, outdated
// ones rewritten where they stand, and misplaced or repeated ones removed.
// Already current settings come back byte for byte. Handlers baum already
// placed are never moved, so a change baum makes to one is reverted the next
// time it installs.
func InstallSettings(contents []byte, agent string) ([]byte, error) {
	next, _, err := installSettings(contents, agent)
	return next, err
}

// UninstallSettings returns the settings without any handler that runs baum's
// user-wide dispatcher. A group, event or hooks key left empty by that goes
// too, so uninstalling right after installing restores the original bytes.
func UninstallSettings(contents []byte, agent string) ([]byte, error) {
	next, _, err := uninstallSettings(contents, agent)
	return next, err
}

func installSettings(contents []byte, agent string) ([]byte, []string, error) {
	spec, settings, err := lookup(agent)
	if err != nil {
		return nil, nil, err
	}
	if len(bytes.TrimSpace(contents)) == 0 {
		contents = []byte("{}\n")
	}
	return settle(contents, func(document []byte, parsed parsedSettings) (step, bool, error) {
		return nextInstallStep(document, parsed, spec, settings)
	})
}

func uninstallSettings(contents []byte, agent string) ([]byte, []string, error) {
	if _, _, err := lookup(agent); err != nil {
		return nil, nil, err
	}
	if len(bytes.TrimSpace(contents)) == 0 {
		return contents, nil, nil
	}
	return settle(contents, func(document []byte, parsed parsedSettings) (step, bool, error) {
		if len(parsed.owned) == 0 {
			return step{}, false, nil
		}
		return parsed.remove(document, parsed.owned[0]), true, nil
	})
}

func lookup(agent string) (agenthooks.Spec, agenthooks.UserWide, error) {
	spec, ok := agenthooks.Lookup(agent)
	settings, userWide := agenthooks.LookupUserWide(agent)
	if !ok || !userWide {
		return spec, settings, fmt.Errorf("%s has no user-wide hooks baum can install", agent)
	}
	return spec, settings, nil
}

// step is one edit, with the user's hooks it moves to another position.
type step struct {
	edit    edit
	shifted []string
}

// settle applies one edit at a time, parsing again after each, until next
// has nothing left to do. It returns every user hook an edit moved, once.
func settle(document []byte, next func([]byte, parsedSettings) (step, bool, error)) ([]byte, []string, error) {
	var shifted []string
	seen := map[string]bool{}
	for range maxEdits {
		parsed, err := parseSettings(document)
		if err != nil {
			return nil, nil, err
		}
		change, ok, err := next(document, parsed)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			return document, shifted, nil
		}
		for _, hook := range change.shifted {
			if !seen[hook] {
				seen[hook] = true
				shifted = append(shifted, hook)
			}
		}
		document = change.edit.apply(document)
	}
	return nil, nil, errors.New("the hooks did not settle")
}

type parsedSettings struct {
	root       *jsonValue
	hooks      *jsonValue
	hooksIndex int
	owned      []ownedHandler
}

// ownedHandler is a handler running baum's user-wide dispatcher, with the
// containers around it.
type ownedHandler struct {
	agent, event string
	hookEvent    string
	matcher      string
	eventIndex   int
	eventArray   *jsonValue
	groupIndex   int
	groupHooks   *jsonValue
	handlerIndex int
	handler      *jsonValue
}

// parseSettings reads the hooks layout both agents share: an object of hook
// events, each a list of matcher groups whose "hooks" list holds handler
// objects. Anything else under "hooks" is refused, since an edit there
// could break what the agent reads.
func parseSettings(document []byte) (parsedSettings, error) {
	root, err := parseJSON(document)
	if err != nil {
		return parsedSettings{}, err
	}
	if root.kind != jsonObject {
		return parsedSettings{}, errors.New("not a JSON object")
	}
	parsed := parsedSettings{root: root}
	parsed.hooks, parsed.hooksIndex, err = root.member("hooks")
	if err != nil {
		return parsed, err
	}
	if parsed.hooks == nil {
		return parsed, nil
	}
	if parsed.hooks.kind != jsonObject {
		return parsed, errors.New(`"hooks" is not an object`)
	}
	seen := map[string]bool{}
	for eventIndex, event := range parsed.hooks.members {
		if seen[event.key] {
			return parsed, fmt.Errorf("duplicate hook event %s", quote(event.key))
		}
		seen[event.key] = true
		if event.value.kind != jsonArray {
			return parsed, fmt.Errorf("hook event %s is not a list", quote(event.key))
		}
		for groupIndex, group := range event.value.elements {
			if group.kind != jsonObject {
				return parsed, fmt.Errorf("an entry of hook event %s is not an object", quote(event.key))
			}
			handlers, _, err := group.member("hooks")
			if err != nil {
				return parsed, fmt.Errorf("hook event %s: %w", quote(event.key), err)
			}
			if handlers == nil {
				continue
			}
			if handlers.kind != jsonArray {
				return parsed, fmt.Errorf("the hooks of an entry of hook event %s are not a list", quote(event.key))
			}
			matcher, err := groupMatcher(document, group)
			if err != nil {
				return parsed, fmt.Errorf("hook event %s: %w", quote(event.key), err)
			}
			for handlerIndex, handler := range handlers.elements {
				if handler.kind != jsonObject {
					return parsed, fmt.Errorf("a handler of hook event %s is not an object", quote(event.key))
				}
				command, _, err := handler.member("command")
				if err != nil {
					return parsed, fmt.Errorf("hook event %s: %w", quote(event.key), err)
				}
				text, ok := command.stringValue(document)
				if !ok {
					continue
				}
				agent, baumEvent, ok := agenthooks.UserDispatcherEvent(text)
				if !ok {
					continue
				}
				parsed.owned = append(parsed.owned, ownedHandler{
					agent: agent, event: baumEvent, hookEvent: event.key, matcher: matcher,
					eventIndex: eventIndex, eventArray: event.value,
					groupIndex: groupIndex, groupHooks: handlers,
					handlerIndex: handlerIndex, handler: handler,
				})
			}
		}
	}
	return parsed, nil
}

// groupMatcher reads a group's matcher; absent, null and "" all match
// everything.
func groupMatcher(document []byte, group *jsonValue) (string, error) {
	value, _, err := group.member("matcher")
	if err != nil || value == nil {
		return "", err
	}
	if text, ok := value.stringValue(document); ok {
		return text, nil
	}
	return string(value.text(document)), nil
}

// remove cuts one owned handler out, taking with it the group, event and
// hooks key it would leave empty, and names the user's hooks behind it in
// the same list, whose position drops by one.
func (p parsedSettings) remove(document []byte, owned ownedHandler) step {
	switch {
	case len(owned.groupHooks.elements) > 1:
		return step{
			edit:    removeItem(owned.groupHooks, owned.handlerIndex),
			shifted: userHandlers(document, owned.hookEvent, owned.groupHooks.elements[owned.handlerIndex+1:]),
		}
	case len(owned.eventArray.elements) > 1:
		var shifted []string
		for _, group := range owned.eventArray.elements[owned.groupIndex+1:] {
			if handlers, _, _ := group.member("hooks"); handlers != nil && handlers.kind == jsonArray {
				shifted = append(shifted, userHandlers(document, owned.hookEvent, handlers.elements)...)
			}
		}
		return step{edit: removeItem(owned.eventArray, owned.groupIndex), shifted: shifted}
	case len(p.hooks.members) > 1:
		return step{edit: removeItem(p.hooks, owned.eventIndex)}
	default:
		return step{edit: removeItem(p.root, p.hooksIndex)}
	}
}

// userHandlers describes the handlers that are not baum's, as
// "Event: command".
func userHandlers(document []byte, hookEvent string, handlers []*jsonValue) []string {
	var names []string
	for _, handler := range handlers {
		command, _, _ := handler.member("command")
		text, ok := command.stringValue(document)
		if ok {
			if _, _, owned := agenthooks.UserDispatcherEvent(text); owned {
				continue
			}
		} else {
			text = "(a handler without a command)"
		}
		names = append(names, hookEvent+": "+text)
	}
	return names
}

type hookGroup struct {
	Matcher *string       `json:"matcher,omitempty"`
	Hooks   []hookHandler `json:"hooks"`
}

type hookHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

func nextInstallStep(document []byte, parsed parsedSettings, spec agenthooks.Spec, settings agenthooks.UserWide) (step, bool, error) {
	// The first handler at each placement's spot is kept where it is; every
	// other one baum owns is misplaced, repeated or for an event that is gone.
	kept := make([]int, len(settings.Placements))
	used := make([]bool, len(parsed.owned))
	for i, placement := range settings.Placements {
		kept[i] = -1
		for j, owned := range parsed.owned {
			if !used[j] && owned.agent == spec.Slug && owned.event == placement.Event &&
				owned.hookEvent == placement.HookEvent && owned.matcher == matcherText(placement.Matcher) {
				kept[i], used[j] = j, true
				break
			}
		}
	}
	for j, owned := range parsed.owned {
		if !used[j] {
			return parsed.remove(document, owned), true, nil
		}
	}
	style := detectLayout(document)
	for i, placement := range settings.Placements {
		handler := hookHandler{Type: "command", Command: agenthooks.UserDispatcher(spec, placement.Event), Timeout: placement.Timeout}
		if kept[i] < 0 {
			change, err := addPlacement(document, parsed, placement, handler, style)
			return step{edit: change}, true, err
		}
		current := parsed.owned[kept[i]].handler
		same, err := sameJSON(current.text(document), handler)
		if err != nil {
			return step{}, false, err
		}
		if !same {
			text, err := encode(handler, lineIndent(document, current.start), style)
			return step{edit: edit{current.start, current.end, text}}, true, err
		}
	}
	return step{}, false, nil
}

func matcherText(matcher *string) string {
	if matcher == nil {
		return ""
	}
	return *matcher
}

// addPlacement appends a group for one placement after everything already in
// its event's list, so no hook of the user's changes position.
func addPlacement(document []byte, parsed parsedSettings, placement agenthooks.Placement, handler hookHandler, style layout) (edit, error) {
	group := hookGroup{Matcher: placement.Matcher, Hooks: []hookHandler{handler}}
	if parsed.hooks == nil {
		value := map[string][]hookGroup{placement.HookEvent: {group}}
		return appendItem(document, parsed.root, lineIndent(document, parsed.root.start), style, func(indent string) (string, error) {
			return encodeMember("hooks", value, indent, style)
		})
	}
	events, _, err := parsed.hooks.member(placement.HookEvent)
	if err != nil {
		return edit{}, err
	}
	if events == nil {
		return appendItem(document, parsed.hooks, lineIndent(document, parsed.hooks.start), style, func(indent string) (string, error) {
			return encodeMember(placement.HookEvent, []hookGroup{group}, indent, style)
		})
	}
	return appendItem(document, events, lineIndent(document, events.start), style, func(indent string) (string, error) {
		return encode(group, indent, style)
	})
}

func sameJSON(current []byte, want any) (bool, error) {
	var have, wanted any
	if err := json.Unmarshal(current, &have); err != nil {
		return false, err
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(encoded, &wanted); err != nil {
		return false, err
	}
	return reflect.DeepEqual(have, wanted), nil
}
