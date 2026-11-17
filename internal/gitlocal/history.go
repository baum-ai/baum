package gitlocal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Bounds on what the history reads return.
const (
	// MaxLogPaths is the most paths Log filters by.
	MaxLogPaths = 50
	// MaxLogCommits is the most commits Log returns.
	MaxLogCommits = 200
	// MaxBlameLines is the most lines Blame covers.
	MaxBlameLines = 2000
	// MaxChangedPaths is the most paths ChangedSince lists.
	MaxChangedPaths = 3000

	maxHistoryOutputBytes = 16 << 20
)

// LogCommit is one commit Log lists.
type LogCommit struct {
	SHA     string
	Parents []string
	Author  string
	// Date is the author date, RFC 3339.
	Date    string
	Subject string
}

// BlameLine is one line Blame attributes to the commit that last changed it.
type BlameLine struct {
	// Line is the line's number at the blamed revision, from 1.
	Line   int
	SHA    string
	Author string
	// Date is the author time, RFC 3339 in UTC.
	Date    string
	Summary string
	Text    string
}

// Log lists the commits reachable from rev that touch any of paths (every
// commit when there are none), newest first, at most limit (1 to
// MaxLogCommits), committed at or after since when it is not zero. It reports
// whether more commits matched than limit. With exactly one path it follows
// renames. rev may be abbreviated. Nothing is fetched: a commit the clone lacks
// is ErrObjectNotFound.
func (r *Repo) Log(ctx context.Context, rev string, paths []string, limit int, since time.Time) ([]LogCommit, bool, error) {
	if limit < 1 || limit > MaxLogCommits || len(paths) > MaxLogPaths {
		return nil, false, ErrInvalidInput
	}
	for _, p := range paths {
		if !validGitPath(p) {
			return nil, false, ErrInvalidInput
		}
	}
	oid, err := r.ResolveCommit(ctx, rev)
	if err != nil {
		return nil, false, err
	}
	done, err := r.begin()
	if err != nil {
		return nil, false, err
	}
	defer done()
	args := []string{"log", "-z", "--no-color", "--no-ext-diff", "--no-textconv", "--no-show-signature",
		"--format=%H%x1f%P%x1f%an%x1f%aI%x1f%s", "-n", strconv.Itoa(limit + 1)}
	if !since.IsZero() {
		args = append(args, "--since="+since.UTC().Format(time.RFC3339))
	}
	if len(paths) == 1 {
		args = append(args, "--follow")
	}
	args = append(args, "--end-of-options", oid, "--")
	for _, p := range paths {
		args = append(args, ":(literal)"+p)
	}
	out, err := r.read(ctx, maxHistoryOutputBytes, args...)
	if err != nil {
		return nil, false, fmt.Errorf("reading the log: %w", rangeError(err))
	}
	var commits []LogCommit
	for _, record := range bytes.Split(out, []byte{0}) {
		record = bytes.TrimLeft(record, "\n")
		if len(record) == 0 {
			continue
		}
		fields := strings.SplitN(string(record), "\x1f", 5)
		if len(fields) != 5 || !validOID(fields[0]) {
			return nil, false, errors.New("reading the log: unexpected output")
		}
		parents := []string{}
		if fields[1] != "" {
			parents = strings.Fields(fields[1])
		}
		commits = append(commits, LogCommit{SHA: fields[0], Parents: parents, Author: fields[2], Date: fields[3], Subject: fields[4]})
	}
	truncated := len(commits) > limit
	if truncated {
		commits = commits[:limit]
	}
	return commits, truncated, nil
}

// Blame attributes lines start to end (1-based, inclusive, at most
// MaxBlameLines) of path at rev to the commits that last changed them. A path
// rev does not hold, or a range past the end of the file, is ErrInvalidInput.
func (r *Repo) Blame(ctx context.Context, rev, path string, start, end int) ([]BlameLine, error) {
	if start < 1 || end < start || end-start+1 > MaxBlameLines || !validGitPath(path) {
		return nil, ErrInvalidInput
	}
	oid, err := r.ResolveCommit(ctx, rev)
	if err != nil {
		return nil, err
	}
	done, err := r.begin()
	if err != nil {
		return nil, err
	}
	defer done()
	out, err := r.read(ctx, maxHistoryOutputBytes, "blame", "--porcelain", "-L", strconv.Itoa(start)+","+strconv.Itoa(end),
		oid, "--", path)
	if err != nil {
		var gitErr *Error
		if errors.As(err, &gitErr) && !passThrough(err) {
			stderr := strings.ToLower(gitErr.Stderr)
			if strings.Contains(stderr, "no such path") || strings.Contains(stderr, "has only") || strings.Contains(stderr, "no such file") {
				return nil, fmt.Errorf("%w: %s has no such lines at %s", ErrInvalidInput, path, oid)
			}
		}
		return nil, fmt.Errorf("blaming %s: %w", path, rangeError(err))
	}
	return parseBlame(out)
}

// parseBlame reads git blame --porcelain: per line a header
// "<sha> <original line> <final line> [<group size>]", then, only the first
// time a commit appears, its fields, then the line's text after a tab.
func parseBlame(out []byte) ([]BlameLine, error) {
	type meta struct{ author, date, summary string }
	commits := map[string]*meta{}
	var lines []BlameLine
	var current *BlameLine
	var info *meta
	for _, line := range strings.Split(string(out), "\n") {
		if current != nil {
			if text, ok := strings.CutPrefix(line, "\t"); ok {
				current.Text = text
				current.Author, current.Date, current.Summary = info.author, info.date, info.summary
				lines = append(lines, *current)
				current = nil
				continue
			}
			key, value, _ := strings.Cut(line, " ")
			switch key {
			case "author":
				info.author = value
			case "author-time":
				if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
					info.date = time.Unix(seconds, 0).UTC().Format(time.RFC3339)
				}
			case "summary":
				info.summary = value
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || !validOID(fields[0]) {
			continue
		}
		number, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, errors.New("reading the blame: unexpected output")
		}
		if commits[fields[0]] == nil {
			commits[fields[0]] = &meta{}
		}
		info = commits[fields[0]]
		current = &BlameLine{Line: number, SHA: fields[0]}
	}
	return lines, nil
}

// ChangedSince lists the paths a pull request with head on base changes: those
// that differ between the merge base of base and head and head, a renamed file
// under both its names, sorted, at most MaxChangedPaths. base and head may be
// abbreviated. It returns the merge base's full id.
func (r *Repo) ChangedSince(ctx context.Context, base, head string) (string, []string, bool, error) {
	baseOID, err := r.ResolveCommit(ctx, base)
	if err != nil {
		return "", nil, false, err
	}
	headOID, err := r.ResolveCommit(ctx, head)
	if err != nil {
		return "", nil, false, err
	}
	done, err := r.begin()
	if err != nil {
		return "", nil, false, err
	}
	defer done()
	mergeBase, err := r.mergeBase(ctx, baseOID, headOID)
	if err != nil {
		return "", nil, false, err
	}
	out, err := r.read(ctx, maxHistoryOutputBytes, "diff", "--no-ext-diff", "--no-textconv", "--no-color",
		"--name-only", "-z", "--no-renames", mergeBase, headOID)
	if err != nil {
		return "", nil, false, fmt.Errorf("listing changed files: %w", rangeError(err))
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	truncated := len(paths) > MaxChangedPaths
	if truncated {
		paths = paths[:MaxChangedPaths]
	}
	return mergeBase, paths, truncated, nil
}
