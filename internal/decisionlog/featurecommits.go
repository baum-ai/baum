package decisionlog

import (
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/baum-ai/baum/internal/gitlocal"
)

// Feature links repeatedly read the same recorded PR ranges. Their objects
// are immutable; the shallow boundary is the only mutable input to this read.
// Keep full successful answers only, with independent copies for callers.
const featureCommitBytes = 16 << 20
const featureCommitRanges = 128

type featureCommitKey struct {
	dir, base, head string
	shallow         [sha256.Size]byte
}
type featureCommitEntry struct {
	key     featureCommitKey
	commits []gitlocal.Commit
	cost    int
}
type featureCommitCache struct {
	mu      sync.Mutex
	order   *list.List
	entries map[featureCommitKey]*list.Element
	bytes   int
}

func (c *featureCommitCache) read(ctx context.Context, clone *gitlocal.Repo, dir string, p StoredPull, full bool) ([]gitlocal.Commit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	shallow, err := os.ReadFile(filepath.Join(clone.PrivateDir(), "shallow"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return pullCommits(ctx, clone, p)
	}
	key := featureCommitKey{dir: dir, base: p.BaseSHA, head: p.HeadSHA, shallow: sha256.Sum256(shallow)}
	if !full {
		c.mu.Lock()
		if e := c.entries[key]; e != nil {
			c.order.MoveToFront(e)
			result := cloneFeatureCommits(e.Value.(featureCommitEntry).commits)
			c.mu.Unlock()
			return result, nil
		}
		c.mu.Unlock()
	}
	commits, err := pullCommits(ctx, clone, p)
	if err != nil {
		return nil, err
	}
	cost := len(dir) + len(p.BaseSHA) + len(p.HeadSHA) + 256
	for _, v := range commits {
		cost += 256 + len(v.OID) + len(v.AuthorName) + len(v.AuthorEmail) + len(v.CommittedAt) + len(v.Subject) + len(v.Message)
		for _, values := range [][]string{v.Parents, v.Trailers, v.Files} {
			for _, value := range values {
				cost += 32 + len(value)
			}
		}
	}
	if cost > featureCommitBytes {
		return commits, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.order == nil {
		c.order = list.New()
		c.entries = map[featureCommitKey]*list.Element{}
	}
	if old := c.entries[key]; old != nil {
		c.bytes -= old.Value.(featureCommitEntry).cost
		c.order.Remove(old)
	}
	c.entries[key] = c.order.PushFront(featureCommitEntry{key, cloneFeatureCommits(commits), cost})
	c.bytes += cost
	for c.bytes > featureCommitBytes || len(c.entries) > featureCommitRanges {
		last := c.order.Back()
		entry := last.Value.(featureCommitEntry)
		c.bytes -= entry.cost
		delete(c.entries, entry.key)
		c.order.Remove(last)
	}
	return commits, nil
}

func cloneFeatureCommits(in []gitlocal.Commit) []gitlocal.Commit {
	out := slices.Clone(in)
	stringsCopy := func(in []string) []string {
		out := slices.Clone(in)
		for i, s := range out {
			out[i] = strings.Clone(s)
		}
		return out
	}
	for i := range out {
		c := &out[i]
		c.OID = strings.Clone(c.OID)
		c.AuthorName = strings.Clone(c.AuthorName)
		c.AuthorEmail = strings.Clone(c.AuthorEmail)
		c.CommittedAt = strings.Clone(c.CommittedAt)
		c.Subject = strings.Clone(c.Subject)
		c.Message = strings.Clone(c.Message)
		c.Parents = stringsCopy(c.Parents)
		c.Trailers = stringsCopy(c.Trailers)
		c.Files = stringsCopy(c.Files)
	}
	return out
}
