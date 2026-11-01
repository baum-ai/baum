// Package codexconfig configures the terminal title baum needs to focus Codex.
package codexconfig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

var titleMu sync.Mutex

// EnsureTerminalTitle puts thread-id last in the user's title, preserving their
// other items. It returns whether Codex needs restarting to load a change.
func EnsureTerminalTitle() (bool, error) {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return false, err
		}
		home = filepath.Join(userHome, ".codex")
	}
	return ensureTerminalTitle(filepath.Join(home, "config.toml"))
}

func ensureTerminalTitle(path string) (bool, error) {
	titleMu.Lock()
	defer titleMu.Unlock()
	// Edit a symlink's target without replacing the link itself.
	if _, err := os.Lstat(path); err == nil {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return false, err
		}
		path = resolved
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read Codex config: %w", err)
	}
	updated, err := terminalTitle(data)
	if err != nil || bytes.Equal(data, updated) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".baum-title-*")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err = file.Write(updated); err == nil {
		err = file.Chmod(mode)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	current, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if !bytes.Equal(data, current) {
		return false, errors.New("Codex config changed during focus setup; try again")
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return false, err
	}
	return true, nil
}

func terminalTitle(data []byte) ([]byte, error) {
	var config map[string]any
	if err := toml.Unmarshal(data, &config); err != nil {
		// Decoder errors include source lines, which may contain credentials.
		return nil, errors.New("Codex config.toml is invalid TOML; fix it in Codex and retry")
	}
	tui, ok := config["tui"].(map[string]any)
	if _, exists := config["tui"]; exists && !ok {
		return nil, errors.New("Codex tui setting must be a table")
	}
	items := []string{"spinner", "project"}
	if value, exists := tui["terminal_title"]; exists {
		array, ok := value.([]any)
		if !ok {
			return nil, errors.New("Codex tui.terminal_title must be an array of strings")
		}
		items = nil
		for _, item := range array {
			s, ok := item.(string)
			if !ok {
				return nil, errors.New("Codex tui.terminal_title must be an array of strings")
			}
			items = append(items, s)
		}
	}
	if len(items) > 0 && items[len(items)-1] == "thread-id" {
		return data, nil
	}
	items = append(slices.DeleteFunc(items, func(s string) bool { return s == "thread-id" }), "thread-id")
	encoded, err := toml.Marshal(map[string]any{"terminal_title": items})
	if err != nil {
		return nil, err
	}
	assignment := bytes.TrimSpace(encoded)
	value := bytes.TrimSpace(bytes.SplitN(assignment, []byte("="), 2)[1])
	var parser unstable.Parser
	parser.Reset(data)
	var table []string
	var updated []byte
	insert := -1
	for parser.NextExpression() {
		node := parser.Expression()
		switch node.Kind {
		case unstable.Table, unstable.ArrayTable:
			table = keys(node.Key())
			if node.Kind == unstable.Table && slices.Equal(table, []string{"tui"}) {
				it := node.Key()
				it.Next()
				start := int(it.Node().Raw.Offset)
				if end := bytes.IndexByte(data[start:], '\n'); end >= 0 {
					insert = start + end + 1
				} else {
					insert = len(data)
				}
			}
		case unstable.KeyValue:
			if updated == nil {
				updated = patchTitle(data, node, table, value, assignment)
			}
		}
	}
	if err := parser.Error(); err != nil {
		return nil, errors.New("Could not read Codex terminal title settings")
	}
	if updated == nil {
		if insert >= 0 {
			entry := append(slices.Clone(assignment), '\n')
			if insert > 0 && data[insert-1] != '\n' {
				entry = append([]byte{'\n'}, entry...)
			}
			updated = splice(data, insert, insert, entry)
		} else {
			// A root dotted key also works when tui has only dotted keys or
			// child tables; declaring [tui] would redefine a dotted table.
			updated = append([]byte("tui."+string(assignment)+"\n"), data...)
		}
	}
	var check map[string]any
	if err := toml.Unmarshal(updated, &check); err != nil {
		return nil, errors.New("Could not safely update Codex terminal title settings; config unchanged")
	}
	return updated, nil
}

func keys(it unstable.Iterator) []string {
	var parts []string
	for it.Next() {
		parts = append(parts, string(it.Node().Data))
	}
	return parts
}

func patchTitle(data []byte, kv *unstable.Node, prefix []string, value, assignment []byte) []byte {
	key := append(slices.Clone(prefix), keys(kv.Key())...)
	end := int(kv.Raw.Offset + kv.Raw.Length)
	if slices.Equal(key, []string{"tui", "terminal_title"}) {
		it := kv.Key()
		var last *unstable.Node
		for it.Next() {
			last = it.Node()
		}
		start := int(last.Raw.Offset + last.Raw.Length)
		start += bytes.IndexByte(data[start:end], '=') + 1
		for start < end && strings.ContainsRune(" \t", rune(data[start])) {
			start++
		}
		return splice(data, start, end, value)
	}
	if kv.Value().Kind != unstable.InlineTable {
		return nil
	}
	children := kv.Value().Children()
	for children.Next() {
		if updated := patchTitle(data, children.Node(), key, value, assignment); updated != nil {
			return updated
		}
	}
	if slices.Equal(key, []string{"tui"}) {
		start := int(kv.Raw.Offset)
		body := bytes.TrimSpace(data[start : end-1])
		sep := " "
		if last := body[len(body)-1]; last != '{' && last != ',' {
			sep = ", "
		}
		return splice(data, end-1, end-1, []byte(sep+string(assignment)+" "))
	}
	return nil
}

func splice(data []byte, start, end int, replacement []byte) []byte {
	out := append(slices.Clone(data[:start]), replacement...)
	return append(out, data[end:]...)
}
