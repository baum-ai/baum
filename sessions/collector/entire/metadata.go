package entire

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/gitlocal"
)

// maxMetadataBytes bounds one metadata.json read by Sessions; a larger one is
// skipped, and its checkpoint is left for a whole read to judge.
const maxMetadataBytes = 1 << 20

var (
	commitPattern     = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	sessionDirPattern = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)
)

// Snapshot is one session a checkpoint records, as the checkpoint's metadata
// names it.
type Snapshot struct {
	SessionID string
	// CreatedAt is when this snapshot of the session was written.
	CreatedAt time.Time
}

// Sessions reads the metadata of the checkpoints ids names, in the branch
// commit, and returns the sessions each records, without reading a
// transcript: a checkpoint holds the whole transcript of every session it
// records, so reading one is slow, and this says which checkpoints are worth
// reading. A checkpoint whose metadata is absent, unreadable or not as
// expected is left out of the result, for a caller to read whole and find out
// why. repoRoot is a private repository that borrows the clone's objects, as
// for List; nothing is fetched.
func Sessions(ctx context.Context, repoRoot, commit string, ids []string) (map[string][]Snapshot, error) {
	if !commitPattern.MatchString(commit) {
		return nil, fmt.Errorf("invalid Entire checkpoint branch commit %q", commit)
	}
	var valid []string
	for _, id := range ids {
		if checkpointIDPattern.MatchString(id) {
			valid = append(valid, strings.ToLower(id))
		}
	}
	if len(valid) == 0 {
		return map[string][]Snapshot{}, nil
	}

	tops := make([]string, len(valid))
	for i, id := range valid {
		tops[i] = Path(id) + "/metadata.json"
	}
	topData, err := catBlobs(ctx, repoRoot, commit, tops)
	if err != nil {
		return nil, err
	}
	sessionPaths := map[string][]string{}
	var wanted []string
	for i, id := range valid {
		data, ok := topData[tops[i]]
		if !ok {
			continue
		}
		var top struct {
			Sessions []struct {
				Metadata string `json:"metadata"`
			} `json:"sessions"`
		}
		if json.Unmarshal(data, &top) != nil || len(top.Sessions) == 0 {
			continue
		}
		paths := make([]string, 0, len(top.Sessions))
		for index, refs := range top.Sessions {
			dir, ok := sessionDir(refs.Metadata, index)
			if !ok {
				paths = nil
				break
			}
			paths = append(paths, Path(id)+"/"+dir+"/metadata.json")
		}
		if paths != nil {
			sessionPaths[id] = paths
			wanted = append(wanted, paths...)
		}
	}
	sessionData, err := catBlobs(ctx, repoRoot, commit, wanted)
	if err != nil {
		return nil, err
	}

	result := make(map[string][]Snapshot, len(sessionPaths))
next:
	for _, id := range valid {
		paths, ok := sessionPaths[id]
		if !ok {
			continue
		}
		snapshots := make([]Snapshot, 0, len(paths))
		for _, p := range paths {
			var meta struct {
				SessionID string `json:"session_id"`
				CreatedAt string `json:"created_at"`
			}
			data, ok := sessionData[p]
			if !ok || json.Unmarshal(data, &meta) != nil || meta.SessionID == "" {
				continue next
			}
			at, err := time.Parse(time.RFC3339Nano, meta.CreatedAt)
			if err != nil {
				continue next
			}
			snapshots = append(snapshots, Snapshot{SessionID: meta.SessionID, CreatedAt: at.UTC()})
		}
		result[id] = snapshots
	}
	return result, nil
}

// sessionDir is the directory inside a checkpoint holding its index-th
// session, from the session's metadata ref as entirecheckpoint resolves it:
// the ref's parent directory name, or the index when the ref names none.
func sessionDir(ref string, index int) (string, bool) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(ref), "/")
	if trimmed == "" {
		return strconv.Itoa(index), true
	}
	if slices.Contains(strings.Split(trimmed, "/"), "..") {
		return "", false
	}
	dir := path.Base(path.Dir(path.Clean(trimmed)))
	if dir == "." || dir == "/" {
		return strconv.Itoa(index), true
	}
	if !sessionDirPattern.MatchString(dir) {
		return "", false
	}
	return dir, true
}

// catBlobs reads the blobs at paths in commit with one git cat-file --batch,
// in the private repository, and returns those that exist and fit
// maxMetadataBytes. A path that is missing, including a blob a partial clone
// never downloaded, is simply absent.
func catBlobs(ctx context.Context, repoRoot, commit string, paths []string) (map[string][]byte, error) {
	found := make(map[string][]byte, len(paths))
	if len(paths) == 0 {
		return found, nil
	}
	var input bytes.Buffer
	for _, p := range paths {
		if strings.ContainsAny(p, "\n\r\x00") {
			return nil, fmt.Errorf("invalid checkpoint path %q", p)
		}
		input.WriteString(commit + ":" + p + "\n")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "cat-file", "--batch")
	cmd.Env = gitlocal.ReadEnv(repoRoot)
	cmd.Stdin = &input
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("reading Entire checkpoint metadata: %w", err)
	}
	readErr := readBatch(bufio.NewReader(stdout), paths, found)
	_, _ = io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("reading Entire checkpoint metadata: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return found, readErr
}

// readBatch reads one cat-file --batch answer per path: "<oid> <type>
// <size>" and the content, or "<name> missing" for one that is not there.
func readBatch(r *bufio.Reader, paths []string, found map[string][]byte) error {
	for _, p := range paths {
		header, err := r.ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading Entire checkpoint metadata: %w", err)
		}
		fields := strings.Fields(header)
		if len(fields) != 3 {
			continue // missing, ambiguous: no content follows
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			return errors.New("reading Entire checkpoint metadata: unexpected git cat-file output")
		}
		var data []byte
		if fields[1] == "blob" && size <= maxMetadataBytes {
			data = make([]byte, size)
			if _, err := io.ReadFull(r, data); err != nil {
				return fmt.Errorf("reading Entire checkpoint metadata: %w", err)
			}
		} else if _, err := io.CopyN(io.Discard, r, size); err != nil {
			return fmt.Errorf("reading Entire checkpoint metadata: %w", err)
		}
		if _, err := r.Discard(1); err != nil { // the newline after the content
			return fmt.Errorf("reading Entire checkpoint metadata: %w", err)
		}
		if data != nil {
			found[p] = data
		}
	}
	return nil
}
