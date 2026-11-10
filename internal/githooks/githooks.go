// Package githooks installs baum's Git hook dispatchers into a clone, so Git
// reports the commits made there to the daemon without parsing anything.
//
// A dispatcher runs baum's hook, then the hooks the clone had before, which
// install moved to <hook>.pre-baum. A hook baum cannot preserve is reported
// and left as it is, never replaced.
package githooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Hooks are the Git hooks baum installs a dispatcher for.
var Hooks = []string{"post-commit", "post-rewrite", "pre-push"}

const (
	// Marker identifies a dispatcher baum wrote, now or in an earlier
	// version.
	Marker       = "# Managed by baum. Re-run `baum setup` to repair."
	versionLine  = "# baum-git-hook-version: 1"
	entireMarker = "# Entire CLI hooks"
	backupSuffix = ".pre-baum"
	// legacySuffix is where earlier dispatchers kept the hook they replaced;
	// the dispatcher still runs one found there.
	legacySuffix = ".pre-entire"
	maxHookRead  = 1 << 20
)

// Outcome is what Ensure did for one hook.
type Outcome string

const (
	// Installed means the clone had no hook and now has the dispatcher.
	Installed Outcome = "installed"
	// Current means the dispatcher was already in place.
	Current Outcome = "current"
	// Repaired means an outdated or legacy dispatcher was replaced.
	Repaired Outcome = "repaired"
	// Chained means another tool's hook was moved to <hook>.pre-baum, which
	// the dispatcher runs after baum's.
	Chained Outcome = "chained"
	// Unclaimed means the hook was left as it is; Reason says why.
	Unclaimed Outcome = "unclaimed"
)

// HookResult is the outcome for one hook.
type HookResult struct {
	Hook    string
	Outcome Outcome
	Reason  string
}

// Report is what Ensure did for one clone.
type Report struct {
	// Skipped is why the clone got no hooks at all, empty when it was
	// examined.
	Skipped string
	Hooks   []HookResult
}

// Changed reports whether Ensure wrote anything.
func (r Report) Changed() bool {
	for _, h := range r.Hooks {
		if h.Outcome == Installed || h.Outcome == Repaired || h.Outcome == Chained {
			return true
		}
	}
	return false
}

// Dispatcher is the script installed for hook. Hooks whose input Git feeds on
// standard input buffer it, so baum and the chained hooks each read all of it.
// Baum never fails a hook; only a chained pre-push hook can stop a push.
func Dispatcher(hook string) string {
	buffered := hook == "post-rewrite" || hook == "pre-push"
	redirect, buffer := "", ""
	if buffered {
		redirect = ` < "$hook_stdin"`
		// Without a temp file the chained hooks would read a drained stream;
		// a missing mktemp must not fail the hook, which for pre-push would
		// refuse the push.
		buffer = fmt.Sprintf(`hook_stdin=$(mktemp "${TMPDIR:-/tmp}/baum-%s.XXXXXX") || hook_stdin=
if [ -n "$hook_stdin" ]; then
  trap 'rm -f "$hook_stdin"' EXIT
  cat > "$hook_stdin"
else
  hook_stdin=/dev/stdin
fi
`, hook)
	}
	onFailure := ""
	if hook == "pre-push" {
		onFailure = " || exit $?"
	}
	return fmt.Sprintf(`#!/bin/sh
%s
%s
%sif command -v baum >/dev/null 2>&1 && baum hooks git --check-dispatcher %s >/dev/null 2>&1; then
  baum hooks git %s "$@"%s || :
fi
hook_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
for backup in "$hook_dir/%s%s" "$hook_dir/%s%s"; do
  if [ -x "$backup" ]; then
    "$backup" "$@"%s%s
  fi
done
`, Marker, versionLine, buffer, hook, hook, redirect, hook, legacySuffix, hook, backupSuffix, redirect, onFailure)
}

// Ensure installs the dispatchers into the clone at root, repairing outdated
// ones. It is idempotent and never deletes or follows a file it did not write.
func Ensure(ctx context.Context, root string) (Report, error) {
	if configured, err := gitOutput(ctx, root, "config", "--get", "core.hooksPath"); err == nil && configured != "" {
		return Report{Skipped: "core.hooksPath is set"}, nil
	}
	state, err := gitOutput(ctx, root, "rev-parse", "--is-bare-repository", "--is-inside-work-tree")
	if fields := strings.Fields(state); err != nil || len(fields) != 2 || fields[0] == "true" || fields[1] != "true" {
		return Report{Skipped: "not a Git working tree"}, nil
	}
	dir, err := gitOutput(ctx, root, "rev-parse", "--path-format=absolute", "--git-path", "hooks")
	if err != nil || dir == "" {
		return Report{}, fmt.Errorf("find Git hooks directory: %w", err)
	}
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Report{}, fmt.Errorf("create Git hooks directory: %w", err)
	}
	var report Report
	for _, hook := range Hooks {
		result, err := ensureHook(dir, hook)
		if err != nil {
			return report, err
		}
		report.Hooks = append(report.Hooks, result)
	}
	return report, nil
}

func ensureHook(dir, hook string) (HookResult, error) {
	path := filepath.Join(dir, hook)
	script := Dispatcher(hook)
	result := HookResult{Hook: hook}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := writeAtomic(path, script, ""); err != nil {
			return result, fmt.Errorf("install Git hook %s: %w", hook, err)
		}
		result.Outcome = Installed
		return result, nil
	case err != nil:
		return result, fmt.Errorf("inspect Git hook %s: %w", hook, err)
	case !info.Mode().IsRegular():
		// A symlink belongs to whatever put it there, and a FIFO or device
		// would block a read. Only a symlink to our own script is current.
		if target, err := os.Stat(path); err == nil && target.Mode().IsRegular() {
			if contents, _ := readHook(path); contents == script {
				result.Outcome = Current
				return result, nil
			}
		}
		result.Outcome, result.Reason = Unclaimed, "the hook is not a regular file"
		return result, nil
	}
	contents, err := readHook(path)
	if err != nil {
		return result, fmt.Errorf("read Git hook %s: %w", hook, err)
	}
	if contents == script {
		result.Outcome = Current
		return result, nil
	}
	if ours(hook, contents) {
		if err := writeAtomic(path, script, ""); err != nil {
			return result, fmt.Errorf("repair Git hook %s: %w", hook, err)
		}
		result.Outcome = Repaired
		return result, nil
	}
	err = writeAtomic(path, script, path+backupSuffix)
	switch {
	case errors.Is(err, os.ErrExist):
		result.Outcome, result.Reason = Unclaimed, "the hook's backup name is taken"
	case err != nil:
		return result, fmt.Errorf("chain Git hook %s: %w", hook, err)
	default:
		result.Outcome = Chained
	}
	return result, nil
}

// ours reports whether contents is a dispatcher of baum, or of Entire, whose
// hooks baum replaced.
func ours(hook, contents string) bool {
	return strings.Contains(contents, Marker) ||
		(strings.Contains(contents, entireMarker) && strings.Contains(contents, "entire hooks git "+hook))
}

func readHook(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxHookRead))
	return string(data), err
}

// writeAtomic puts script at path through a temporary file and a rename, so
// the hook is never missing or half-written. With backup set, the file at
// path is first hard-linked there, which fails with os.ErrExist rather than
// replace a file, and the rename then leaves it as the backup.
func writeAtomic(path, script, backup string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".baum-hook-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := io.WriteString(tmp, script); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if backup != "" {
		if err := os.Link(path, backup); err != nil {
			return err
		}
	}
	return os.Rename(name, path)
}

func gitOutput(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}
