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
// the previous one beside it.
package userhooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/baum-ai/baum/hosted/agenthooks"
)

// BackupSuffix names the copy of a settings file as it was before baum's
// last change to it.
const BackupSuffix = ".baum-backup"

// maxSettingsBytes bounds the settings file baum reads; a larger one is
// refused rather than loaded.
const maxSettingsBytes = 8 << 20

// maxEdits bounds the edit loop. Each edit removes, replaces or adds one
// handler, so a settings file needs at most a few per placement.
const maxEdits = 256

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
	// Changed reports whether the file was written.
	Changed bool
	// Backup is the saved previous file, when one existed and was changed.
	Backup string
}

// Install adds baum's user-wide hooks to an agent's settings below home, or
// brings earlier ones up to date. Running it again changes nothing.
func Install(home, agent string) (Result, error) {
	return update(home, agent, InstallSettings, true)
}

// Uninstall removes baum's user-wide hooks from an agent's settings below
// home and leaves everything else. A missing file stays missing.
func Uninstall(home, agent string) (Result, error) {
	return update(home, agent, UninstallSettings, false)
}

func update(home, agent string, change func([]byte, string) ([]byte, error), create bool) (Result, error) {
	settings, ok := agenthooks.LookupUserWide(agent)
	if !ok {
		return Result{}, fmt.Errorf("%s has no user-wide hooks baum can install", agent)
	}
	path := filepath.Join(home, filepath.FromSlash(settings.SettingsFile))
	result := Result{Agent: agent, Path: path}
	file, err := readSettingsFile(path)
	if err != nil {
		return result, err
	}
	if !file.existed && !create {
		return result, nil
	}
	next, err := change(file.contents, agent)
	if err != nil {
		return result, fmt.Errorf("%s: %w; leaving it unchanged", path, err)
	}
	if file.existed && bytes.Equal(next, file.contents) {
		return result, nil
	}
	if file.existed {
		result.Backup = file.target + BackupSuffix
		if err := writeAtomic(result.Backup, file.contents, file.mode, nil); err != nil {
			return result, fmt.Errorf("back up %s: %w", path, err)
		}
	} else if err := os.MkdirAll(filepath.Dir(file.target), 0o700); err != nil {
		return result, fmt.Errorf("create %s: %w", filepath.Dir(file.target), err)
	}
	if err := writeAtomic(file.target, next, file.mode, &file); err != nil {
		return result, fmt.Errorf("write %s: %w", path, err)
	}
	result.Changed = true
	return result, nil
}

type settingsFile struct {
	// target is the file written: the settings path, or where it links to,
	// so a settings file kept in a dotfiles repository stays linked.
	target   string
	contents []byte
	existed  bool
	mode     os.FileMode
}

func readSettingsFile(path string) (settingsFile, error) {
	file := settingsFile{target: path, mode: 0o600}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return file, fmt.Errorf("inspect %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return file, fmt.Errorf("%s is a symbolic link baum cannot follow: %w", path, err)
		}
		file.target = target
		if info, err = os.Stat(target); err != nil {
			return file, fmt.Errorf("inspect %s: %w", target, err)
		}
	}
	if !info.Mode().IsRegular() {
		return file, fmt.Errorf("%s is not a regular file; leaving it unchanged", path)
	}
	if info.Size() > maxSettingsBytes {
		return file, fmt.Errorf("%s is larger than %d bytes; leaving it unchanged", path, maxSettingsBytes)
	}
	contents, err := readBounded(file.target)
	if err != nil {
		return file, fmt.Errorf("read %s: %w", path, err)
	}
	file.contents, file.existed, file.mode = contents, true, info.Mode().Perm()
	return file, nil
}

func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	contents, err := io.ReadAll(io.LimitReader(f, maxSettingsBytes+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maxSettingsBytes {
		return nil, fmt.Errorf("larger than %d bytes", maxSettingsBytes)
	}
	return contents, nil
}

// writeAtomic replaces path with contents through a temporary file in the
// same directory and a rename, so a reader sees the old file or the new one,
// never a partial write. With before set, the rename happens only if path
// still holds what was read: an agent that rewrote its settings meanwhile
// keeps its change, and the command can simply be run again.
func writeAtomic(path string, contents []byte, mode os.FileMode, before *settingsFile) error {
	dir, base := filepath.Split(path)
	temp, err := os.CreateTemp(dir, "."+base+".baum-*")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temp.Name())
		}
	}()
	if _, err := temp.Write(contents); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if before != nil {
		current, err := readBounded(path)
		switch {
		case errors.Is(err, os.ErrNotExist) && !before.existed:
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return err
		case err != nil || !before.existed || !bytes.Equal(current, before.contents):
			return errors.New("the file changed while baum was updating it; run the command again")
		}
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return err
	}
	committed = true
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// InstallSettings returns the settings with baum's user-wide hooks for agent
// in place: missing handlers added at the end of their event's list, outdated
// ones rewritten where they stand, and misplaced or repeated ones removed.
// Already current settings come back byte for byte.
func InstallSettings(contents []byte, agent string) ([]byte, error) {
	spec, settings, err := lookup(agent)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(contents)) == 0 {
		contents = []byte("{}\n")
	}
	return settle(contents, func(document []byte, parsed parsedSettings) (edit, bool, error) {
		return nextInstallEdit(document, parsed, spec, settings)
	})
}

// UninstallSettings returns the settings without any handler that runs baum's
// user-wide dispatcher. A group, event or hooks key left empty by that goes
// too, so uninstalling right after installing restores the original bytes.
func UninstallSettings(contents []byte, agent string) ([]byte, error) {
	if _, _, err := lookup(agent); err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(contents)) == 0 {
		return contents, nil
	}
	return settle(contents, func(_ []byte, parsed parsedSettings) (edit, bool, error) {
		if len(parsed.owned) == 0 {
			return edit{}, false, nil
		}
		return parsed.remove(parsed.owned[0]), true, nil
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

// settle applies one edit at a time, parsing again after each, until next
// has nothing left to do.
func settle(document []byte, next func([]byte, parsedSettings) (edit, bool, error)) ([]byte, error) {
	for range maxEdits {
		parsed, err := parseSettings(document)
		if err != nil {
			return nil, err
		}
		change, ok, err := next(document, parsed)
		if err != nil {
			return nil, err
		}
		if !ok {
			return document, nil
		}
		document = change.apply(document)
	}
	return nil, errors.New("the hooks did not settle")
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
// hooks key it would leave empty.
func (p parsedSettings) remove(owned ownedHandler) edit {
	switch {
	case len(owned.groupHooks.elements) > 1:
		return removeItem(owned.groupHooks, owned.handlerIndex)
	case len(owned.eventArray.elements) > 1:
		return removeItem(owned.eventArray, owned.groupIndex)
	case len(p.hooks.members) > 1:
		return removeItem(p.hooks, owned.eventIndex)
	default:
		return removeItem(p.root, p.hooksIndex)
	}
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

func nextInstallEdit(document []byte, parsed parsedSettings, spec agenthooks.Spec, settings agenthooks.UserWide) (edit, bool, error) {
	// The first handler at each placement's spot is kept; every other one
	// baum owns is misplaced, repeated or for an event that is gone.
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
			return parsed.remove(owned), true, nil
		}
	}
	style := detectLayout(document)
	for i, placement := range settings.Placements {
		handler := hookHandler{Type: "command", Command: agenthooks.UserDispatcher(spec, placement.Event), Timeout: placement.Timeout}
		if kept[i] < 0 {
			change, err := addPlacement(document, parsed, placement, handler, style)
			return change, true, err
		}
		current := parsed.owned[kept[i]].handler
		same, err := sameJSON(current.text(document), handler)
		if err != nil {
			return edit{}, false, err
		}
		if !same {
			text, err := encode(handler, lineIndent(document, current.start), style)
			return edit{current.start, current.end, text}, true, err
		}
	}
	return edit{}, false, nil
}

func matcherText(matcher *string) string {
	if matcher == nil {
		return ""
	}
	return *matcher
}

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
