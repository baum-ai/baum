// Package entire finds Entire checkpoints in the user's own clone. Entire
// keeps every agent session as a checkpoint in one of two stores, and a
// repository may hold both:
//
//   - one ref per checkpoint, refs/entire/checkpoints/<last 2 characters of the
//     id>/<id>, whose commit's root tree is the checkpoint (Entire's git-refs
//     store, the only one that holds 26-character ULID ids; "entire migrate"
//     moves the 12-hex ids of the older store there too);
//   - the branch entire/checkpoints/v1, laid out as <2 hex>/<10 hex>/..., in
//     the repository the session ran in.
//
// Where the same 12-hex id is in both, the ref wins: it is the newer copy.
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

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/sessions/entirecheckpoint"
	"github.com/baum-ai/baum/sessions/traceprocessing"
)

const (
	CheckpointBranch = "entire/checkpoints/v1"
	// RefPrefix is where the git-refs store keeps one ref per checkpoint.
	RefPrefix = "refs/entire/checkpoints/"
	// FetchHint is the command that downloads the git-refs store, for a
	// warning to name when a checkpoint is not in the clone. The + forces the
	// update, since a rewritten ref (as "entire migrate" leaves) is otherwise
	// rejected as a non-fast-forward. Nothing here runs it.
	FetchHint = "git fetch origin '+refs/entire/checkpoints/*:refs/entire/checkpoints/*'"
	// BranchFetchHint downloads the legacy checkpoint branch, which holds the
	// 12-hex checkpoints a migration has not moved.
	BranchFetchHint = "git fetch origin entire/checkpoints/v1"

	maxCheckpointBytes = 256 << 20
	maxCheckpointFiles = 512
	branchIDLength     = 12
)

var checkpointPathPattern = regexp.MustCompile(`(?i)^[0-9a-f]{2}/[0-9a-f]{10}$`)

// Path is the two-level branch path for a 12-hex checkpoint id.
func Path(checkpointID string) string {
	return checkpointID[:2] + "/" + checkpointID[2:]
}

// Checkpoint is one checkpoint in a store. TreeOID identifies its content, so
// an unchanged checkpoint can be recognised without reading it.
type Checkpoint struct {
	ID      string
	TreeOID string
}

// Store is where a clone keeps its checkpoints: the commit of the checkpoint
// branch, if it has one, and the commit of each per-checkpoint ref, by
// canonical id.
type Store struct {
	Branch string
	Refs   map[string]string
}

// location is where one checkpoint's files are: a commit and the directory in
// its tree, which is empty for a ref, whose root tree is the checkpoint.
type location struct {
	commit, dir string
}

// spec names a file of the checkpoint as git cat-file does.
func (l location) spec(rel string) string {
	if l.dir == "" {
		return l.commit + ":" + rel
	}
	return l.commit + ":" + l.dir + "/" + rel
}

// locate finds the checkpoint with the canonical id: its ref if it has one,
// else the branch. The branch holds 12-hex ids only.
func (s Store) locate(id string) (location, bool) {
	// A commit is checked where it is used, so a Store built by a caller
	// cannot hand git an option or a revision expression.
	if commit, ok := s.Refs[id]; ok {
		return location{commit: commit}, commitPattern.MatchString(commit)
	}
	if s.Branch != "" && commitPattern.MatchString(s.Branch) && len(id) == branchIDLength {
		return location{commit: s.Branch, dir: Path(id)}, true
	}
	return location{}, false
}

// Resolve finds the checkpoint stores in the clone at repoRoot: the commit the
// checkpoint branch points at (the local branch first, then any
// remote-tracking copy of it) and every refs/entire/checkpoints/<shard>/<id>
// ref. A ref is used only when its shard is the last two characters of its
// id, which must be a valid checkpoint id, and it has no further path
// segments; anything else under the prefix is ignored. ok is false when the
// clone has neither store. Nothing is fetched.
func Resolve(ctx context.Context, repoRoot string) (store Store, ok bool, err error) {
	out, err := git(ctx, repoRoot, "for-each-ref", "--format=%(objectname) %(refname)",
		"refs/heads/"+CheckpointBranch, "refs/remotes/*/"+CheckpointBranch, RefPrefix)
	if err != nil {
		return Store{}, false, fmt.Errorf("listing Entire checkpoint refs: %w", err)
	}
	var local, remote string
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		oid, ref, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		if strings.HasPrefix(ref, RefPrefix) {
			if id, valid := refID(ref); valid && commitPattern.MatchString(oid) {
				if _, dup := refs[id]; !dup {
					refs[id] = oid
				}
			}
			continue
		}
		switch {
		case ref == "refs/heads/"+CheckpointBranch:
			local = oid
		case remote == "":
			remote = oid
		}
	}
	store = Store{Branch: local, Refs: refs}
	if store.Branch == "" {
		store.Branch = remote
	}
	return store, store.Branch != "" || len(refs) > 0, nil
}

// refID is the canonical checkpoint id of a ref, which must be named exactly
// RefPrefix, the last two characters of the canonical id, a slash and the
// canonical id (hex lower-case, ULID upper-case), as the platform mirror
// reads them. Any other spelling is not a checkpoint Entire wrote, and a
// case variant must not shadow the real one.
func refID(ref string) (string, bool) {
	rest, ok := strings.CutPrefix(ref, RefPrefix)
	if !ok {
		return "", false
	}
	_, name, found := strings.Cut(rest, "/")
	if !found {
		return "", false
	}
	id, ok := entirecheckpoint.CanonicalID(name)
	if !ok || ref != RefPrefix+id[len(id)-2:]+"/"+id {
		return "", false
	}
	return id, true
}

// Located reports which of ids the store holds, by canonical id: a ref is
// there, and the branch holds a 12-hex id whose directory is in its tree
// (which a partial clone has even when the checkpoint's blobs were never
// downloaded). A checkpoint the store holds but Sessions leaves out is
// unreadable; one it does not hold is missing. repoRoot is a private
// repository, as for List.
func Located(ctx context.Context, repoRoot string, store Store, ids []string) (map[string]bool, error) {
	held := map[string]bool{}
	var check []string
	var specs []string
	seen := map[string]bool{}
	for _, id := range ids {
		canonical, ok := entirecheckpoint.CanonicalID(id)
		if !ok || seen[canonical] {
			continue
		}
		seen[canonical] = true
		loc, ok := store.locate(canonical)
		switch {
		case !ok:
		case loc.dir == "":
			held[canonical] = true
		default:
			check = append(check, canonical)
			specs = append(specs, loc.commit+":"+loc.dir)
		}
	}
	if len(specs) == 0 {
		return held, nil
	}
	out, err := runGit(ctx, gitlocal.ReadEnv(repoRoot), repoRoot, strings.NewReader(strings.Join(specs, "\n")+"\n"),
		"cat-file", "--batch-check")
	if err != nil {
		return nil, fmt.Errorf("looking for Entire checkpoints: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != len(check) {
		return held, nil
	}
	for i, id := range check {
		if fields := strings.Fields(lines[i]); len(fields) == 3 && fields[1] == "tree" {
			held[id] = true
		}
	}
	return held, nil
}

// List returns every checkpoint in the store, in id order; a checkpoint in
// both the branch and the refs is listed once, as its ref. repoRoot is a
// private repository that borrows the clone's objects, such as gitlocal's.
func List(ctx context.Context, repoRoot string, store Store) ([]Checkpoint, error) {
	byID := map[string]Checkpoint{}
	if store.Branch != "" {
		branch, err := listBranch(ctx, repoRoot, store.Branch)
		if err != nil {
			return nil, err
		}
		for _, checkpoint := range branch {
			byID[checkpoint.ID] = checkpoint
		}
	}
	if len(store.Refs) > 0 {
		trees, err := refTrees(ctx, repoRoot, store.Refs)
		if err != nil {
			return nil, err
		}
		for id := range store.Refs {
			byID[id] = Checkpoint{ID: id, TreeOID: trees[id]}
		}
	}
	checkpoints := make([]Checkpoint, 0, len(byID))
	for _, checkpoint := range byID {
		checkpoints = append(checkpoints, checkpoint)
	}
	sort.Slice(checkpoints, func(i, j int) bool { return checkpoints[i].ID < checkpoints[j].ID })
	return checkpoints, nil
}

func listBranch(ctx context.Context, repoRoot, commit string) ([]Checkpoint, error) {
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
		if id, ok := entirecheckpoint.CanonicalID(strings.ReplaceAll(path, "/", "")); ok {
			checkpoints = append(checkpoints, Checkpoint{ID: id, TreeOID: fields[2]})
		}
	}
	return checkpoints, nil
}

// refTrees returns the root tree of each ref's commit, by id, with one git
// cat-file --batch-check. A commit that is not there leaves its id out.
func refTrees(ctx context.Context, repoRoot string, refs map[string]string) (map[string]string, error) {
	ids := make([]string, 0, len(refs))
	for id := range refs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var input bytes.Buffer
	for _, id := range ids {
		input.WriteString(refs[id] + "^{tree}\n")
	}
	out, err := runGit(ctx, gitlocal.ReadEnv(repoRoot), repoRoot, &input, "cat-file", "--batch-check")
	if err != nil {
		return nil, fmt.Errorf("listing Entire checkpoint refs: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	trees := make(map[string]string, len(ids))
	if len(lines) != len(ids) {
		return trees, nil
	}
	for i, id := range ids {
		if fields := strings.Fields(lines[i]); len(fields) == 3 && fields[1] == "tree" {
			trees[id] = fields[0]
		}
	}
	return trees, nil
}

// Read exports one checkpoint from the store as an fs.FS rooted at the
// checkpoint, the shape entirecheckpoint.ParseCheckpoint reads. A checkpoint
// in both the branch and the refs is read from its ref.
func Read(ctx context.Context, repoRoot string, store Store, checkpointID string) (fs.FS, error) {
	id, ok := entirecheckpoint.CanonicalID(checkpointID)
	if !ok {
		return nil, fmt.Errorf("invalid Entire checkpoint id %q", checkpointID)
	}
	loc, ok := store.locate(id)
	if !ok {
		return nil, fmt.Errorf("entire checkpoint %s is not in this clone; fetch it with: %s", id, FetchHint)
	}
	args := []string{"archive", "--format=tar", loc.commit}
	if loc.dir != "" {
		args = append(args, "--", loc.dir)
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoRoot}, args...)...)
	cmd.Env = gitlocal.ReadEnv(repoRoot)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("exporting Entire checkpoint %s: %w", id, err)
	}
	fsys, openErr := Open(stdout, loc.dir)
	// Drain so git can exit; a truncated read above is reported by Open.
	_, _ = io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("exporting Entire checkpoint %s: %w: %s", id, err, strings.TrimSpace(stderr.String()))
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
	return runGit(ctx, gitEnv(), repoRoot, nil, args...)
}

// privateGit runs a read of objects in a private repository such as the one
// gitlocal makes. It runs with gitlocal's read environment: no global or
// system configuration, so no filter, hook, signature program or credential
// helper of the user's can run on a tree that came from a clone nobody has
// vetted, and a home directory with nothing in it.
func privateGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return runGit(ctx, gitlocal.ReadEnv(dir), dir, nil, args...)
}

func runGit(ctx context.Context, env []string, repoRoot string, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoRoot}, args...)...)
	cmd.Env = env
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Open reads a git-archive of one checkpoint into an fs.FS. checkpointPath is
// the checkpoint's directory in the archive, or empty when the archive's root
// is the checkpoint, as for a ref.
func Open(reader io.Reader, checkpointPath string) (fs.FS, error) {
	result := map[string][]byte{}
	tarReader := tar.NewReader(io.LimitReader(reader, maxCheckpointBytes+1))
	prefix := ""
	if checkpointPath != "" {
		prefix = checkpointPath + "/"
	}
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
