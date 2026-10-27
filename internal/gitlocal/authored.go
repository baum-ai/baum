package gitlocal

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// UserEmail is the clone's configured user.email, as git would record it on a
// commit made there, or "" when none is configured. It reads configuration
// only.
func (r *Repo) UserEmail(ctx context.Context) (string, error) {
	done, err := r.begin()
	if err != nil {
		return "", err
	}
	defer done()
	out, err := run(ctx, runSpec{dir: r.dir, env: userEnv(), limit: 8 << 10, timeout: r.opts.CommandTimeout},
		"config", "--get", "user.email")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// AuthoredCommits lists the commits on the clone's local branches that email
// authored and committed at or after since, newest first and each once,
// without their files. Merge commits are left out: they record a merge, not
// work. It reads only what the clone holds and lists at most MaxCommits; more
// is errOutputTooLarge, as Commits reports it.
func (r *Repo) AuthoredCommits(ctx context.Context, since time.Time, email string) ([]Commit, error) {
	email = strings.TrimSpace(email)
	if email == "" || strings.ContainsAny(email, "\x00\n") {
		return nil, ErrInvalidInput
	}
	done, err := r.begin()
	if err != nil {
		return nil, err
	}
	defer done()
	// The private repository has no refs, so the branch heads come from the
	// clone and the walk runs over its objects.
	heads, err := run(ctx, runSpec{dir: r.dir, env: userEnv(), limit: 4 << 20, timeout: r.opts.CommandTimeout},
		"for-each-ref", "--format=%(objectname)", "refs/heads")
	if err != nil {
		return nil, err
	}
	var tips []string
	for _, oid := range strings.Fields(string(heads)) {
		if validOID(oid) {
			tips = append(tips, oid)
		}
	}
	if len(tips) == 0 {
		return nil, nil
	}
	const format = "%H" + commitFieldSep + "%P" + commitFieldSep + "%an" + commitFieldSep + "%ae" + commitFieldSep +
		"%cI" + commitFieldSep + "%s" + commitFieldSep + "%(trailers:only,unfold)" + commitFieldSep + "%B"
	// --fixed-strings keeps an address's "+" or "." literal; the match is a
	// substring, so the exact address is checked below.
	args := []string{"log", "--no-merges", "--no-color", "--no-ext-diff", "-z", "--fixed-strings",
		"--author=" + email, "--since=" + since.UTC().Format("2006-01-02 15:04:05 +0000"), "--format=" + format}
	out, err := r.read(ctx, maxCommitLogBytes, append(append(args, tips...), "--")...)
	if err != nil {
		return nil, rangeError(err)
	}
	var commits []Commit
	for _, record := range strings.Split(string(out), "\x00") {
		if strings.TrimSpace(record) == "" {
			continue
		}
		fields := strings.SplitN(record, commitFieldSep, 8)
		if len(fields) != 8 || !validOID(strings.TrimSpace(fields[0])) {
			return nil, errors.New("reading commits: unexpected git log output")
		}
		if !strings.EqualFold(strings.TrimSpace(fields[3]), email) {
			continue
		}
		c := Commit{
			OID: strings.TrimSpace(fields[0]), Parents: strings.Fields(fields[1]), AuthorName: fields[2], AuthorEmail: fields[3],
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
	return commits, nil
}
