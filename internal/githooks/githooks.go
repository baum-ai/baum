// Package githooks installs baum's Git hook dispatchers into a clone, so Git
// reports the commits made there to the daemon without parsing anything.
//
// A dispatcher runs baum's hook, then the hooks the clone had before, which
// install moved to <hook>.pre-baum. A hook baum cannot preserve is reported
// and left as it is, never replaced. `git config baum.hooks false` opts a
// clone out: Ensure then takes the dispatchers out again.
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
	// Marker identifies a dispatcher baum wrote.
	Marker = "# Managed by the baum desktop app."
	// legacyMarker identified the dispatchers of earlier versions, which Ensure
	// still recognises and repairs.
	legacyMarker = "# Managed by baum. Re-run `baum setup` to repair."
	versionLine  = "# baum-git-hook-version: 2"
	entireMarker = "# Entire CLI hooks"
	backupSuffix = ".pre-baum"
	// legacySuffix is where earlier dispatchers kept the hook they replaced;
	// the dispatcher still runs one found there.
	legacySuffix = ".pre-entire"
	maxHookRead  = 1 << 20
	// activeEnv names the hooks whose dispatcher is running, so a hook
	// that leads back to it ends the loop.
	activeEnv = "BAUM_GIT_HOOK_ACTIVE"
)

// dispatchesOnName are what a hook that decides what to do from its own file
// name contains. Run as <hook>.pre-baum it would do nothing.
var dispatchesOnName = []string{`basename "$0"`, `basename $0`, `basename "${0}"`, `File.basename($0)`, `${0##*/}`, "husky", "overcommit"}

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
	// Restored means the clone opted out and its own hook is back in place.
	Restored Outcome = "restored"
	// Removed means the clone opted out and the dispatcher, which had no hook
	// to restore, is gone.
	Removed Outcome = "removed"
	// Absent means the clone opted out and has no dispatcher of baum's.
	Absent Outcome = "absent"
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
		switch h.Outcome {
		case Installed, Repaired, Chained, Restored, Removed:
			return true
		}
	}
	return false
}

// Options adjust Ensure and Restore.
type Options struct {
	// Baum is the absolute path of the baum binary a dispatcher falls back to
	// when none is on PATH; empty for none.
	Baum string
	// Env is added to the environment of the git commands.
	Env []string
}

// Dispatcher is the script installed for hook. Hooks whose input Git feeds on
// standard input buffer it, so baum and the chained hooks each read all of it;
// when it cannot be buffered, baum is not run and the chained hooks read the
// original input. Baum never fails a hook; only a chained pre-push hook can
// stop a push.
func Dispatcher(hook, baum string) string {
	buffered := hook == "post-rewrite" || hook == "pre-push"
	buffer, needsInput := "", ""
	if buffered {
		needsInput = ` && [ -n "$buffered" ]`
		buffer = fmt.Sprintf(`if hook_stdin=$(mktemp "${TMPDIR:-/tmp}/baum-%s.XXXXXX" 2>/dev/null); then
  trap 'rm -f "$hook_stdin"' EXIT
  trap 'exit 1' INT TERM HUP
  if cat > "$hook_stdin" 2>/dev/null; then buffered=1; fi
fi
`, hook)
	}
	fallback := ""
	if baum != "" {
		fallback = fmt.Sprintf("if [ -z \"$baum_bin\" ] && [ -x %[1]s ]; then baum_bin=%[1]s; fi\n", shellQuote(baum))
	}
	onFailure := ""
	if hook == "pre-push" {
		onFailure = " || exit $?"
	}
	return fmt.Sprintf(`#!/bin/sh
%[1]s
%[2]s
hook_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
hook_id="$hook_dir/%[3]s"
case ":${%[4]s-}:" in *":$hook_id:"*) exit 0 ;; esac
%[4]s="${%[4]s:+$%[4]s:}$hook_id"
export %[4]s
buffered=
%[5]srun() { if [ -n "$buffered" ]; then "$@" < "$hook_stdin"; else "$@"; fi; }
baum_bin=$(command -v baum 2>/dev/null) || baum_bin=
%[6]sif [ -n "$baum_bin" ]%[7]s && "$baum_bin" hooks git --check-dispatcher %[3]s >/dev/null 2>&1; then
  run "$baum_bin" hooks git %[3]s "$@" || :
fi
for backup in "$hook_dir/%[3]s%[8]s" "$hook_dir/%[3]s%[9]s"; do
  [ -x "$backup" ] || continue
  [ "$backup" -ef "$hook_id" ] && continue
  grep -qF -e '%[10]s' -e '%[11]s' "$backup" 2>/dev/null && continue
  run "$backup" "$@"%[12]s
done
exit 0
`, Marker, versionLine, hook, activeEnv, buffer, fallback, needsInput, legacySuffix, backupSuffix, Marker, legacyMarker, onFailure)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Ensure installs the dispatchers into the clone at root, repairing outdated
// ones, or takes them out when the clone opted out. It is idempotent and never
// deletes or follows a file it did not write. It goes on past a hook it cannot
// handle and returns what failed, joined.
func Ensure(ctx context.Context, root string) (Report, error) {
	return EnsureWith(ctx, root, Options{})
}

// EnsureWith is Ensure with options.
func EnsureWith(ctx context.Context, root string, opts Options) (Report, error) {
	dir, skipped, err := hooksDirectory(ctx, root, opts)
	if err != nil || skipped != "" {
		return Report{Skipped: skipped}, err
	}
	optedOut := false
	if out, err := gitOutput(ctx, root, opts, "config", "--bool", "--get", "baum.hooks"); err == nil && out == "false" {
		optedOut = true
	}
	var report Report
	var errs []error
	if !optedOut {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Report{}, fmt.Errorf("create Git hooks directory: %w", err)
		}
	}
	for _, hook := range Hooks {
		var result HookResult
		if optedOut {
			result, err = restoreHook(dir, hook)
		} else {
			result, err = ensureHook(dir, hook, opts)
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		report.Hooks = append(report.Hooks, result)
	}
	return report, errors.Join(errs...)
}

// Restore takes baum's dispatchers out of the clone at root and puts back the
// hooks they replaced, as Ensure does for a clone that opted out. The daemon
// uses it for a clone it no longer registers.
func Restore(ctx context.Context, root string, opts Options) (Report, error) {
	dir, skipped, err := hooksDirectory(ctx, root, opts)
	if err != nil || skipped != "" {
		return Report{Skipped: skipped}, err
	}
	var report Report
	var errs []error
	for _, hook := range Hooks {
		result, err := restoreHook(dir, hook)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		report.Hooks = append(report.Hooks, result)
	}
	return report, errors.Join(errs...)
}

// hooksDirectory is the hooks directory of the clone at root, or why the
// clone has none baum manages.
func hooksDirectory(ctx context.Context, root string, opts Options) (dir, skipped string, err error) {
	if configured, err := gitOutput(ctx, root, opts, "config", "--get", "core.hooksPath"); err == nil && configured != "" {
		return "", "core.hooksPath is set", nil
	}
	state, err := gitOutput(ctx, root, opts, "rev-parse", "--is-bare-repository", "--is-inside-work-tree")
	if fields := strings.Fields(state); err != nil || len(fields) != 2 || fields[0] == "true" || fields[1] != "true" {
		return "", "not a Git working tree", nil
	}
	dir, err = gitOutput(ctx, root, opts, "rev-parse", "--path-format=absolute", "--git-path", "hooks")
	if err != nil || dir == "" {
		return "", "", fmt.Errorf("find Git hooks directory: %w", err)
	}
	return filepath.Clean(dir), "", nil
}

func ensureHook(dir, hook string, opts Options) (HookResult, error) {
	path := filepath.Join(dir, hook)
	script := Dispatcher(hook, opts.Baum)
	result := HookResult{Hook: hook}
	unclaimed := func(reason string) (HookResult, error) {
		result.Outcome, result.Reason = Unclaimed, reason
		return result, nil
	}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := writeAtomic(path, script, "", 0o755); err != nil {
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
		return unclaimed("the hook is not a regular file")
	}
	contents, err := readHook(path)
	if err != nil {
		return result, fmt.Errorf("read Git hook %s: %w", hook, err)
	}
	switch {
	case contents == script:
		result.Outcome = Current
		return result, nil
	case ours(contents):
		// The mode stays: a dispatcher the user made non-executable is off.
		if err := writeAtomic(path, script, "", info.Mode().Perm()); err != nil {
			return result, fmt.Errorf("repair Git hook %s: %w", hook, err)
		}
		result.Outcome = Repaired
		return result, nil
	case strings.Contains(contents, entireMarker) && strings.Contains(contents, "entire hooks git "+hook):
		return unclaimed("managed by Entire")
	}
	for _, name := range dispatchesOnName {
		if strings.Contains(contents, name) {
			return unclaimed("the hook acts on its own file name, which a chained hook does not keep")
		}
	}
	if name := siblingDispatcher(dir, hook); name != "" {
		return unclaimed("the hooks directory has another dispatcher of baum's, " + name)
	}
	err = writeAtomic(path, script, path+backupSuffix, 0o755)
	switch {
	case errors.Is(err, os.ErrExist):
		return unclaimed("the hook's backup name is taken")
	case err != nil:
		return result, fmt.Errorf("chain Git hook %s: %w", hook, err)
	}
	result.Outcome = Chained
	return result, nil
}

// restoreHook takes baum's dispatcher out of dir and puts its backup back.
func restoreHook(dir, hook string) (HookResult, error) {
	path := filepath.Join(dir, hook)
	result := HookResult{Hook: hook, Outcome: Absent}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return result, nil
	}
	contents, err := readHook(path)
	if err != nil {
		return result, fmt.Errorf("read Git hook %s: %w", hook, err)
	}
	if !ours(contents) {
		return result, nil
	}
	backup := path + backupSuffix
	info, err := os.Lstat(backup)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if _, err := os.Lstat(path + legacySuffix); err == nil {
			result.Outcome, result.Reason = Unclaimed, "a "+legacySuffix+" backup still runs through the dispatcher"
			return result, nil
		}
		if err := os.Remove(path); err != nil {
			return result, fmt.Errorf("remove Git hook %s: %w", hook, err)
		}
		result.Outcome = Removed
		return result, nil
	case err != nil:
		return result, fmt.Errorf("inspect Git hook backup %s: %w", hook, err)
	}
	if !info.Mode().IsRegular() {
		result.Outcome, result.Reason = Unclaimed, "the hook's backup is not a regular file"
		return result, nil
	}
	if saved, err := readHook(backup); err != nil || ours(saved) {
		result.Outcome, result.Reason = Unclaimed, "the hook's backup is not the hook it replaced"
		return result, nil
	}
	if err := os.Rename(backup, path); err != nil {
		return result, fmt.Errorf("restore Git hook %s: %w", hook, err)
	}
	result.Outcome = Restored
	return result, nil
}

// ours reports whether contents is a dispatcher of baum's, of any version.
func ours(contents string) bool {
	return strings.Contains(contents, Marker) || strings.Contains(contents, legacyMarker)
}

// siblingDispatcher is a file of dir named <hook>.<something> that holds a
// dispatcher of baum's, such as one another tool moved aside. Chaining the hook
// in front of it could run the dispatchers in a circle. It returns the name.
func siblingDispatcher(dir, hook string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, hook+".") || strings.HasSuffix(name, ".sample") || !entry.Type().IsRegular() {
			continue
		}
		if contents, err := readHook(filepath.Join(dir, name)); err == nil && ours(contents) {
			return name
		}
	}
	return ""
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

// writeAtomic puts script at path with mode through a temporary file and a
// rename, so the hook is never missing or half-written. With backup set, the
// file at path is first hard-linked there, which fails with os.ErrExist rather
// than replace a file, and the rename then leaves it as the backup.
func writeAtomic(path, script, backup string, mode os.FileMode) error {
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
	if err := tmp.Chmod(mode); err != nil {
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

func gitOutput(ctx context.Context, root string, opts Options, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), opts.Env...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}
