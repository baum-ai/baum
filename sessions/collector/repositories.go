package collector

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/baum-ai/baum/sessions/collector/claudecode"
	"github.com/baum-ai/baum/sessions/collector/codex"
	"github.com/baum-ai/baum/sessions/collector/cursor"
	"github.com/baum-ai/baum/sessions/vendors"
)

// Repository is a local Git checkout referenced by one or more agent sessions.
type Repository struct {
	FullName       string
	Root           string
	SessionCount   int
	LatestSession  time.Time
	OtherCheckouts []string
	Roots          []string
}

// FindRepositories maps whole-machine session metadata to GitHub checkouts.
// When multiple checkouts have the same origin, the one with more sessions (or
// the newest session on a tie) wins and the alternatives remain reportable.
func FindRepositories(ctx context.Context, opts StoreOptions) (map[string]Repository, ScanReport, error) {
	var report ScanReport
	stores, err := FindStores(opts)
	if err != nil {
		return nil, report, err
	}
	type activity struct {
		count  int
		latest time.Time
	}
	byRoot := map[string]activity{}
	for _, store := range stores {
		var sessions []struct {
			dir string
			at  time.Time
		}
		switch store.Source {
		case vendors.ClaudeCode:
			dir, probeErr := claudecode.SessionWorkingDir(store.Path)
			if probeErr != nil {
				report.Add(store.Path, probeErr.Error())
			} else if dir != "" {
				sessions = append(sessions, struct {
					dir string
					at  time.Time
				}{dir, store.ModifiedAt})
			}
		case vendors.Codex:
			dir, root, probeErr := codex.SessionWorkingDir(store.Path)
			if probeErr != nil {
				report.Add(store.Path, probeErr.Error())
			} else if root {
				sessions = append(sessions, struct {
					dir string
					at  time.Time
				}{dir, store.ModifiedAt})
			}
		case vendors.Cursor:
			found, probeErr := cursor.SessionWorkingDirs(store.Path)
			if probeErr != nil {
				report.Add(store.Path, probeErr.Error())
			} else {
				for _, session := range found {
					sessions = append(sessions, struct {
						dir string
						at  time.Time
					}{session.WorkingDir, session.ModifiedAt})
				}
			}
		}
		for _, session := range sessions {
			root, rootErr := repoRootFromRecordedPath(session.dir)
			if rootErr != nil {
				continue
			}
			current := byRoot[root]
			current.count++
			if current.latest.Before(session.at) {
				current.latest = session.at
			}
			byRoot[root] = current
		}
	}

	roots := make([]string, 0, len(byRoot))
	for root := range byRoot {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	result := map[string]Repository{}
	for _, root := range roots {
		fullName, originErr := repositoryOrigin(ctx, root)
		if originErr != nil {
			continue
		}
		candidate := Repository{FullName: fullName, Root: root, SessionCount: byRoot[root].count, LatestSession: byRoot[root].latest}
		current, exists := result[fullName]
		if !exists {
			result[fullName] = candidate
			continue
		}
		candidateWins := candidate.SessionCount > current.SessionCount ||
			(candidate.SessionCount == current.SessionCount && candidate.LatestSession.After(current.LatestSession))
		if candidateWins {
			candidate.OtherCheckouts = append(candidate.OtherCheckouts, current.Root)
			candidate.OtherCheckouts = append(candidate.OtherCheckouts, current.OtherCheckouts...)
			result[fullName] = candidate
		} else {
			current.OtherCheckouts = append(current.OtherCheckouts, candidate.Root)
			result[fullName] = current
		}
	}
	for fullName, repository := range result {
		repository.Roots = append([]string{repository.Root}, repository.OtherCheckouts...)
		sort.Strings(repository.Roots)
		result[fullName] = repository
	}
	return result, report, nil
}

// VerifyRepositoryRoots keeps only existing Git checkouts whose GitHub origin
// is the requested repository. The returned roots are normalized, unique, and
// deterministic.
func VerifyRepositoryRoots(ctx context.Context, fullName string, candidates []string) []string {
	seen := make(map[string]bool)
	var roots []string
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		root, err := RepoRoot(candidate)
		if err != nil {
			continue
		}
		origin, err := repositoryOrigin(ctx, root)
		if err != nil || !strings.EqualFold(origin, fullName) {
			continue
		}
		root = filepath.Clean(root)
		if !seen[root] {
			seen[root] = true
			roots = append(roots, root)
		}
	}
	sort.Strings(roots)
	return roots
}

func repositoryOrigin(ctx context.Context, root string) (string, error) {
	output, err := exec.CommandContext(ctx, "git", "-C", root, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return "", err
	}
	return NormalizeGitHubRemote(strings.TrimSpace(string(output)))
}

// NormalizeGitHubRemote converts a supported GitHub origin URL to owner/name.
func NormalizeGitHubRemote(remote string) (string, error) {
	var host, path string
	if !strings.Contains(remote, "://") {
		before, after, ok := strings.Cut(remote, ":")
		if !ok {
			return "", errors.New("origin remote is not a supported GitHub URL")
		}
		host = before
		if _, value, ok := strings.Cut(host, "@"); ok {
			host = value
		}
		path = after
	} else {
		parsed, err := url.Parse(remote)
		if err != nil {
			return "", fmt.Errorf("parsing origin remote: %w", err)
		}
		host, path = parsed.Hostname(), parsed.Path
	}
	if !strings.EqualFold(host, "github.com") && !strings.EqualFold(host, "www.github.com") && !strings.EqualFold(host, "ssh.github.com") {
		return "", fmt.Errorf("origin remote host %q is not supported; expected github.com", host)
	}
	path = strings.Trim(strings.TrimSuffix(path, ".git"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", errors.New("origin remote does not identify a GitHub repository")
	}
	return filepath.ToSlash(parts[0] + "/" + parts[1]), nil
}
