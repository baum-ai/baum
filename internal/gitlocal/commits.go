package gitlocal

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
)

// MaxCommits is the most commits Commits lists. A pull request with more is
// reported as bundle.ErrOutputTooLarge rather than listed in part.
const MaxCommits = 2000

const (
	maxCommitLogBytes   = 16 << 20
	maxCommitFilesBytes = 1 << 20
	commitFieldSep      = "\x1f"
)

// Commit is one commit in a range, as the matcher and a reader need it.
type Commit struct {
	OID     string
	Parents []string
	// AuthorName and AuthorEmail are the author's identity as recorded.
	AuthorName  string
	AuthorEmail string
	// CommittedAt is the committer date in RFC 3339, as git prints %cI.
	CommittedAt string
	Subject     string
	// Trailers are the "Key: value" lines of the message's trailer block,
	// continuation lines unfolded.
	Trailers []string
	// Message is the whole commit message, as git prints %B without its final
	// newline. Entire-Checkpoint lines are read from all of it, not from the
	// trailer block alone (entirecheckpoint.ParseMessage).
	Message string
	// Files are the repository-relative paths the commit changed, both sides
	// of a rename or copy. A merge commit lists none.
	Files []string
}

// Commits lists the commits reachable from head and not from base, oldest
// first, the way a pull request lists its commits. It reads only objects
// already in the clone and never fetches: a base or head that is absent,
// including one a partial clone has not downloaded, is ErrObjectNotFound.
func (r *Repo) Commits(ctx context.Context, base, head string) ([]Commit, error) {
	if !validOID(base) || !validOID(head) {
		return nil, ErrInvalidInput
	}
	done, err := r.begin()
	if err != nil {
		return nil, err
	}
	defer done()
	for _, oid := range []string{base, head} {
		if !r.hasCommit(ctx, oid) {
			return nil, fmt.Errorf("%w: commit %s is not in this clone", ErrObjectNotFound, oid)
		}
	}
	const format = "%H" + commitFieldSep + "%P" + commitFieldSep + "%an" + commitFieldSep + "%ae" + commitFieldSep +
		"%cI" + commitFieldSep + "%s" + commitFieldSep + "%(trailers:only,unfold)" + commitFieldSep + "%B"
	out, err := r.read(ctx, maxCommitLogBytes, "log", "--reverse", "--no-color", "--no-ext-diff", "-z", "--format="+format, base+".."+head)
	if err != nil {
		return nil, rangeError(err)
	}
	var commits []Commit
	for _, record := range strings.Split(string(out), "\x00") {
		if strings.TrimSpace(record) == "" {
			continue
		}
		// The message is the last field, so a separator byte inside it stays
		// in it.
		fields := strings.SplitN(record, commitFieldSep, 8)
		if len(fields) != 8 {
			return nil, errors.New("reading commits: unexpected git log output")
		}
		oid := strings.TrimSpace(fields[0])
		if !validOID(oid) {
			return nil, errors.New("reading commits: unexpected git log output")
		}
		c := Commit{
			OID: oid, Parents: strings.Fields(fields[1]), AuthorName: fields[2], AuthorEmail: fields[3],
			CommittedAt: fields[4], Subject: fields[5], Message: strings.TrimSuffix(fields[7], "\n"),
		}
		for _, line := range strings.Split(fields[6], "\n") {
			if line = strings.TrimSpace(line); line != "" {
				c.Trailers = append(c.Trailers, line)
			}
		}
		commits = append(commits, c)
		if len(commits) > MaxCommits {
			return nil, fmt.Errorf("reading commits: more than %d commits: %w", MaxCommits, errOutputTooLarge)
		}
	}
	for i := range commits {
		if len(commits[i].Parents) > 1 {
			continue
		}
		files, err := r.read(ctx, maxCommitFilesBytes, "diff-tree", "--no-commit-id", "--name-status", "-z", "-r", "--no-renames", "--root", commits[i].OID)
		if err != nil {
			return nil, rangeError(err)
		}
		commits[i].Files = pathsFromNameStatus(files)
		slices.Sort(commits[i].Files)
	}
	return commits, nil
}

// rangeError reports a git failure while walking a range whose ends exist: an
// object the walk needs but the clone lacks is a missing object, not a fault.
func rangeError(err error) error {
	if passThrough(err) || errors.Is(err, errOutputTooLarge) {
		return err
	}
	var gitErr *Error
	if errors.As(err, &gitErr) {
		stderr := strings.ToLower(gitErr.Stderr)
		for _, marker := range []string{"unable to read", "bad object", "missing", "could not read", "cannot read", "not a valid object", "bad revision"} {
			if strings.Contains(stderr, marker) {
				return fmt.Errorf("%w: %w", ErrObjectNotFound, err)
			}
		}
	}
	return err
}

// pathsFromNameStatus parses diff-tree --name-status -z: a status, then one
// path, or two for a rename or copy.
func pathsFromNameStatus(out []byte) []string {
	tokens := strings.Split(string(out), "\x00")
	var paths []string
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	for i := 0; i < len(tokens); i++ {
		status := tokens[i]
		if status == "" {
			continue
		}
		if i+1 >= len(tokens) {
			break
		}
		i++
		add(tokens[i])
		if (strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C")) && i+1 < len(tokens) {
			i++
			add(tokens[i])
		}
	}
	return paths
}

// PrivateDir is the private repository reads run in: a bare repository with
// no remote and no refs that borrows the clone's objects. A caller that runs
// its own read-only git commands against objects points them here, so git has
// no remote to fetch a missing object from. It exists until Close.
func (r *Repo) PrivateDir() string { return r.mirror }

// RemoteURL is the configured URL of the clone's remote exactly as written in
// its configuration, or "" when the remote is not configured. It reads
// configuration only and never contacts the remote.
func (r *Repo) RemoteURL(ctx context.Context) (string, error) {
	done, err := r.begin()
	if err != nil {
		return "", err
	}
	defer done()
	out, err := run(ctx, runSpec{dir: r.dir, env: userEnv(), limit: 8 << 10, timeout: r.opts.CommandTimeout},
		"config", "--get", "remote."+r.opts.Remote+".url")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", nil // the key is not set
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
