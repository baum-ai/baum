// Package entire finds Entire checkpoints in the checkpoint branch of the
// user's own clone. Entire records every agent session as a checkpoint
// directory under the branch entire/checkpoints/v1, laid out as
// <2 hex>/<10 hex>/..., in the repository the session ran in.
package entire

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/lastpersonlabs/baum/internal/gitlocal"
	"github.com/lastpersonlabs/baum/sessions/traceprocessing"
)

const (
	CheckpointBranch   = "entire/checkpoints/v1"
	maxCheckpointBytes = 256 << 20
	maxCheckpointFiles = 512
)

var (
	checkpointIDPattern   = regexp.MustCompile(`(?i)^[0-9a-f]{12}$`)
	checkpointPathPattern = regexp.MustCompile(`(?i)^[0-9a-f]{2}/[0-9a-f]{10}$`)
)

// Path is the two-level store path for a 12-hex checkpoint id.
func Path(checkpointID string) string {
	return checkpointID[:2] + "/" + checkpointID[2:]
}

// Checkpoint is one checkpoint directory in the branch. TreeOID identifies its
// content, so an unchanged checkpoint can be recognised without reading it.
type Checkpoint struct {
	ID      string
	TreeOID string
}

// Resolve finds the commit the checkpoint branch points at in the clone at
// repoRoot: the local branch first, then any remote-tracking copy of it. ok is
// false when the clone has no checkpoint branch at all.
func Resolve(ctx context.Context, repoRoot string) (commit string, ok bool, err error) {
	out, err := git(ctx, repoRoot, "for-each-ref", "--format=%(objectname) %(refname)",
		"refs/heads/"+CheckpointBranch, "refs/remotes/*/"+CheckpointBranch)
	if err != nil {
		return "", false, fmt.Errorf("listing Entire checkpoint branches: %w", err)
	}
	var local, remote string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		oid, ref, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		switch {
		case ref == "refs/heads/"+CheckpointBranch:
			local = oid
		case remote == "":
			remote = oid
		}
	}
	if local != "" {
		return local, true, nil
	}
	if remote != "" {
		return remote, true, nil
	}
	return "", false, nil
}

// List returns every checkpoint in the branch commit, in id order. repoRoot is
// a private repository that borrows the clone's objects, such as gitlocal's.
func List(ctx context.Context, repoRoot, commit string) ([]Checkpoint, error) {
	out, err := privateGit(ctx, repoRoot, "ls-tree", "-r", "-d", "--full-tree", "-z", commit)
	if err != nil {
		return nil, fmt.Errorf("listing Entire checkpoint store: %w", err)
	}
	var checkpoints []Checkpoint
	for _, entry := range bytes.Split(out, []byte{0}) {
		// <mode> SP <type> SP <oid> TAB <path>
		meta, path, found := strings.Cut(string(entry), "\t")
		if !found || !checkpointPathPattern.MatchString(path) {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 || fields[1] != "tree" {
			continue
		}
		id := strings.ReplaceAll(path, "/", "")
		if checkpointIDPattern.MatchString(id) {
			checkpoints = append(checkpoints, Checkpoint{ID: id, TreeOID: fields[2]})
		}
	}
	sort.Slice(checkpoints, func(i, j int) bool { return checkpoints[i].ID < checkpoints[j].ID })
	return checkpoints, nil
}

// Read exports one checkpoint directory from the branch commit as an fs.FS
// rooted at the checkpoint, the shape entirecheckpoint.ParseCheckpoint reads.
func Read(ctx context.Context, repoRoot, commit, checkpointID string) (fs.FS, error) {
	if !checkpointIDPattern.MatchString(checkpointID) {
		return nil, fmt.Errorf("invalid Entire checkpoint id %q", checkpointID)
	}
	path := Path(checkpointID)
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "archive", "--format=tar", commit, "--", path)
	cmd.Env = gitlocal.ReadEnv(repoRoot)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("exporting Entire checkpoint %s: %w", checkpointID, err)
	}
	fsys, openErr := Open(stdout, path)
	// Drain so git can exit; a truncated read above is reported by Open.
	_, _ = io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("exporting Entire checkpoint %s: %w: %s", checkpointID, err, strings.TrimSpace(stderr.String()))
	}
	return fsys, openErr
}

// gitEnv is the environment of every git command here. They only read: refs
// from the user's clone, and objects, which a caller that must not download
// anything reads from a private repository with no remote (see the repoRoot
// of List and Read). As a second line of defence a partial clone's missing
// objects are never fetched on demand (GIT_NO_LAZY_FETCH, git 2.44 and
// newer), nothing prompts, and no GIT_ variable from the caller redirects git
// to another repository or changes how it reads, except those naming
// configuration files, which tests and some setups rely on.
func gitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "GIT_") && upper != "GIT_EXEC_PATH" &&
			upper != "GIT_CONFIG_GLOBAL" && upper != "GIT_CONFIG_NOSYSTEM" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat", "LC_ALL=C")
}

// git runs a read of refs in the user's clone.
func git(ctx context.Context, repoRoot string, args ...string) ([]byte, error) {
	return runGit(ctx, gitEnv(), repoRoot, args...)
}

// privateGit runs a read of objects in a private repository such as the one
// gitlocal makes. It runs with gitlocal's read environment: no global or
// system configuration, so no filter, hook, signature program or credential
// helper of the user's can run on a tree that came from a clone nobody has
// vetted, and a home directory with nothing in it.
func privateGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return runGit(ctx, gitlocal.ReadEnv(dir), dir, args...)
}

func runGit(ctx context.Context, env []string, repoRoot string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoRoot}, args...)...)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Open reads a git-archive of one checkpoint directory into an fs.FS.
func Open(reader io.Reader, checkpointPath string) (fs.FS, error) {
	result := map[string][]byte{}
	tarReader := tar.NewReader(io.LimitReader(reader, maxCheckpointBytes+1))
	prefix := checkpointPath + "/"
	var total int64
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading Entire checkpoint archive: %w", err)
		}
		// git archive leads with a pax_global_header carrying the commit id and
		// emits an entry per directory; neither contributes checkpoint content.
		if header.Typeflag != tar.TypeReg {
			continue
		}
		name, err := checkpointMemberName(header.Name, prefix)
		if err != nil {
			return nil, err
		}
		if len(result) >= maxCheckpointFiles || header.Size < 0 ||
			header.Size > maxCheckpointBytes-total {
			return nil, errors.New("the Entire checkpoint archive exceeds limits")
		}
		data, err := readCheckpointMember(tarReader, name, header.Size)
		if err != nil {
			return nil, err
		}
		total += header.Size
		result[name] = data
	}
	if _, ok := result["metadata.json"]; !ok {
		return nil, errors.New("the Entire checkpoint archive has no metadata.json")
	}
	return traceprocessing.BytesFS(result, traceprocessing.TreeFilePolicy), nil
}

func checkpointMemberName(entry, prefix string) (string, error) {
	if !strings.HasPrefix(entry, prefix) {
		return "", fmt.Errorf("invalid Entire checkpoint archive entry %q", entry)
	}
	name := strings.TrimPrefix(entry, prefix)
	// fs.ValidPath allows "."; reject it so a directory entry cannot become a
	// file keyed as the checkpoint root.
	if name == "." || !fs.ValidPath(name) {
		return "", fmt.Errorf("invalid Entire checkpoint archive path %q", entry)
	}
	return name, nil
}

func readCheckpointMember(r io.Reader, name string, size int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, size+1))
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("reading Entire checkpoint file %q: archive is truncated: %w", name, err)
		}
		return nil, fmt.Errorf("reading Entire checkpoint file %q: %w", name, err)
	}
	if int64(len(data)) != size {
		return nil, fmt.Errorf(
			"reading Entire checkpoint file %q: archive is truncated (read %d bytes, expected %d)",
			name, len(data), size,
		)
	}
	return data, nil
}
