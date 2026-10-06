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
// attributes and .gitmodules do not reach it, and the archive is byte for
// byte the hosted one, including the attributes it applies: it still applies
// working-tree-encoding from the tree. The archive streams from git's stdout;
// it is never buffered whole. Reading more than Options.MaxArchiveBytes kills git and
// fails with bundle.ErrOutputTooLarge, and Options.ArchiveTimeout bounds the
// whole export. A git that fails part way is an error from Read, never a
// silently truncated archive. An unknown commit is ErrObjectNotFound.
//
// The caller must Close the result, which kills git if it is still running
// and waits for it. Repo.Close cancels and reaps exports still open.
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
	a := &archiveReader{
		repo: r, ctx: ctx, cancel: cancel, done: done, cmd: cmd, stdout: stdout,
		stderr: stderr, remaining: r.opts.MaxArchiveBytes,
	}
	r.mu.Lock()
	r.readers[a] = struct{}{}
	closed := r.closed
	r.mu.Unlock()
	if closed {
		cancel()
	}
	// Whatever ends the export's context (the timeout, the caller, Close,
	// Repo.Close) kills git, and this reaps it and frees the Repo's slot, so
	// a reader that is never closed cannot hold Repo.Close up.
	go func() {
		<-ctx.Done()
		_ = a.reap()
	}()
	return a, nil
}

// archiveReader streams git archive's stdout under a byte cap. Read is for
// one goroutine at a time; Close may be called from another.
type archiveReader struct {
	repo   *Repo
	ctx    context.Context
	cancel context.CancelFunc
	done   func()
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr *capWriter

	// mu guards the fields below, and is not held across a blocking read.
	mu        sync.Mutex
	remaining int64
	// err is what Read returns once the stream has ended.
	err    error
	closed bool

	// reap runs git's Wait exactly once; waitErr is valid after it returns.
	once    sync.Once
	waitErr error
}

func (a *archiveReader) Read(p []byte) (int, error) {
	a.mu.Lock()
	if a.err != nil {
		defer a.mu.Unlock()
		return 0, a.err
	}
	if a.closed {
		a.mu.Unlock()
		return 0, errors.New("read of a closed export")
	}
	if len(p) == 0 {
		a.mu.Unlock()
		return 0, nil
	}
	// Ask for one byte more than the cap allows, so a stream that ends
	// exactly at the cap is not mistaken for one that goes past it.
	want := int64(len(p))
	if want > a.remaining+1 {
		want = a.remaining + 1
	}
	a.mu.Unlock()

	n, err := a.stdout.Read(p[:want])

	a.mu.Lock()
	if int64(n) > a.remaining {
		a.err = errOutputTooLarge
		a.mu.Unlock()
		a.cancel()
		return 0, errOutputTooLarge
	}
	a.remaining -= int64(n)
	if err == nil {
		a.mu.Unlock()
		return n, nil
	}
	a.mu.Unlock()
	if errors.Is(err, io.EOF) {
		// Only a successful exit makes this a complete archive.
		if werr := a.reap(); werr != nil {
			err = werr
		}
	} else {
		a.cancel()
		if werr := a.reap(); werr != nil {
			err = werr
		}
	}
	a.mu.Lock()
	if a.err == nil {
		a.err = err
	}
	err = a.err
	a.mu.Unlock()
	return n, err
}

// reap waits for git once, frees the Repo's slot, and reports git's failure
// with the sanitised stderr and, for a timeout or cancellation, the
// context's error.
func (a *archiveReader) reap() error {
	a.once.Do(func() {
		err := a.cmd.Wait()
		if err != nil {
			a.waitErr = a.failure(err)
		}
		a.cancel()
		a.repo.mu.Lock()
		delete(a.repo.readers, a)
		a.repo.mu.Unlock()
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
// call more than once, and from another goroutine while a Read is running,
// which then ends with an error. It reports git's failure only when git
// failed on its own, not because Close stopped it.
func (a *archiveReader) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	finished := a.err != nil
	prior := a.err
	a.mu.Unlock()
	if !finished {
		a.cancel()
	}
	_ = a.stdout.Close()
	err := a.reap()
	if !finished || errors.Is(prior, io.EOF) || errors.Is(prior, errOutputTooLarge) {
		return nil
	}
	return err
}
