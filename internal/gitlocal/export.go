package gitlocal

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
)

// ExportCommit streams the full tree of commit commitOID as a tar archive,
// built exactly as the hosted git mirror builds its export:
//
//	git archive --format=tar --worktree-attributes <commitOID>
//
// run in the private bare repository, so the clone's configuration,
// attributes and .gitmodules do not reach it and the archive holds the
// committed bytes. The archive streams from git's stdout; it is never
// buffered whole. Reading more than Options.MaxArchiveBytes kills git and
// fails with bundle.ErrOutputTooLarge, and Options.ArchiveTimeout bounds the
// whole export. A git that fails part way is an error from Read, never a
// silently truncated archive. An unknown commit is ErrObjectNotFound.
//
// The caller must Close the result, which kills git if it is still running
// and waits for it. Repo.Close waits for exports still open.
func (r *Repo) ExportCommit(ctx context.Context, commitOID string) (io.ReadCloser, error) {
	return r.export(ctx, commitOID, "")
}

// ExportCommitPath is ExportCommit for one path of the commit, a file or a
// directory, as the hosted mirror does:
//
//	git archive --format=tar --worktree-attributes <commitOID> -- :(literal)<path>
//
// A path the commit does not hold is ErrObjectNotFound.
func (r *Repo) ExportCommitPath(ctx context.Context, commitOID, path string) (io.ReadCloser, error) {
	if !validGitPath(path) {
		return nil, ErrInvalidInput
	}
	return r.export(ctx, commitOID, path)
}

func (r *Repo) export(ctx context.Context, commitOID, exportPath string) (io.ReadCloser, error) {
	if !validOID(commitOID) {
		return nil, ErrInvalidInput
	}
	done, err := r.begin()
	if err != nil {
		return nil, err
	}
	// From here done runs when the reader is closed, or on failure.
	started := false
	defer func() {
		if !started {
			done()
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, r.opts.ArchiveTimeout)
	defer func() {
		if !started {
			cancel()
		}
	}()
	// The hosted service checks the commit and the path first, so a missing
	// one is "not found" and not a failed archive.
	if _, err := r.read(ctx, 0, "cat-file", "-e", commitOID+"^{commit}"); err != nil {
		if passThrough(err) {
			return nil, err
		}
		return nil, ErrObjectNotFound
	}
	if exportPath != "" {
		if _, err := r.read(ctx, 0, "cat-file", "-e", commitOID+":"+exportPath); err != nil {
			if passThrough(err) {
				return nil, err
			}
			return nil, ErrObjectNotFound
		}
	}
	args := []string{"archive", "--format=tar", "--worktree-attributes", commitOID}
	if exportPath != "" {
		args = append(args, "--", ":(literal)"+exportPath)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.mirror
	cmd.Env = mirrorEnv(r.mirror)
	cmd.WaitDelay = waitDelay
	stderr := &capWriter{max: maxStderrBytes}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, &Error{Command: "archive", Err: err}
	}
	started = true
	return &archiveReader{
		repo: r, ctx: ctx, cancel: cancel, done: done, cmd: cmd, stdout: stdout,
		stderr: stderr, remaining: r.opts.MaxArchiveBytes,
	}, nil
}

// archiveReader streams git archive's stdout under a byte cap.
type archiveReader struct {
	repo   *Repo
	ctx    context.Context
	cancel context.CancelFunc
	done   func()
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr *capWriter

	remaining int64
	// err is what Read returns once the stream has ended.
	err error

	once    sync.Once
	waitErr error
	closed  bool
}

func (a *archiveReader) Read(p []byte) (int, error) {
	if a.err != nil {
		return 0, a.err
	}
	if a.closed {
		return 0, errors.New("read of a closed export")
	}
	if len(p) == 0 {
		return 0, nil
	}
	// Ask for one byte more than the cap allows, so a stream that ends
	// exactly at the cap is not mistaken for one that goes past it.
	want := int64(len(p))
	if want > a.remaining+1 {
		want = a.remaining + 1
	}
	n, err := a.stdout.Read(p[:want])
	if int64(n) > a.remaining {
		a.err = errOutputTooLarge
		a.cancel()
		return 0, a.err
	}
	a.remaining -= int64(n)
	if err != nil {
		if errors.Is(err, io.EOF) {
			// Only a successful exit makes this a complete archive.
			if werr := a.wait(); werr != nil {
				a.err = werr
			} else {
				a.err = io.EOF
			}
			return n, a.err
		}
		a.cancel()
		if werr := a.wait(); werr != nil {
			err = werr
		}
		a.err = err
		return n, a.err
	}
	return n, nil
}

// wait reaps git once, and reports its failure with the sanitised stderr and,
// for a timeout or cancellation, the context's error.
func (a *archiveReader) wait() error {
	a.once.Do(func() {
		err := a.cmd.Wait()
		if err != nil {
			a.waitErr = a.failure(err)
		}
		a.cancel()
		a.done()
	})
	return a.waitErr
}

func (a *archiveReader) failure(err error) error {
	if ctxErr := a.ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
		err = errors.Join(err, ctxErr)
	}
	return a.repo.missingObjects(&Error{Command: "archive", Stderr: sanitize(a.stderr.buf.Bytes()), Err: err})
}

// Close kills git if it is still running and waits for it. It is safe to
// call more than once. It reports git's failure only when git failed on its
// own, not because Close stopped it.
func (a *archiveReader) Close() error {
	if a.closed {
		return nil
	}
	a.closed = true
	finished := a.err != nil
	if !finished {
		a.cancel()
	}
	_ = a.stdout.Close()
	err := a.wait()
	if !finished {
		return nil
	}
	if errors.Is(a.err, io.EOF) || errors.Is(a.err, errOutputTooLarge) {
		return nil
	}
	return err
}
