package uiserver

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"

	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/viewserver"
)

const maxCachedDocuments = 16
const maxLocalReads = 4

type cachedDocument struct {
	server *viewserver.Server
	used   uint64
	active int
}
type documentCache struct {
	mu              sync.Mutex
	entries         map[string]*cachedDocument
	clock           uint64
	closed          bool
	requests, reads chan struct{}
	resolve         func(context.Context, locallink.Request) (locallink.Result, error)
}

func newDocumentCache() *documentCache {
	return &documentCache{entries: map[string]*cachedDocument{}, requests: make(chan struct{}, maxLocalReads), reads: make(chan struct{}, maxLocalReads), resolve: locallink.Resolve}
}
func acquire(ctx context.Context, slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}
func (c *documentCache) get(clone repos.Clone, document []byte) (*viewserver.Server, func(), error) {
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%x", clone.ID, clone.Repository, clone.CommonDir, sha256.Sum256(document))
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, nil, errors.New("document cache closed")
	}
	c.clock++
	if entry := c.entries[key]; entry != nil {
		entry.active++
		entry.used = c.clock
		c.mu.Unlock()
		return entry.server, func() { c.release(entry) }, nil
	}
	var old *viewserver.Server
	if len(c.entries) >= maxCachedDocuments {
		oldestKey := ""
		var oldest uint64
		for k, entry := range c.entries {
			if entry.active == 0 && (oldestKey == "" || entry.used < oldest) {
				oldestKey = k
				oldest = entry.used
			}
		}
		if oldestKey == "" {
			c.mu.Unlock()
			return nil, nil, errors.New("document cache busy")
		}
		old = c.entries[oldestKey].server
		delete(c.entries, oldestKey)
	}
	patcher := new(locallink.Patcher)
	server, err := viewserver.New(viewserver.Options{Document: document, Sessions: func(ctx context.Context, v viewserver.Review) (locallink.Result, error) {
		if !acquire(ctx, c.reads) {
			return locallink.Result{}, ctx.Err()
		}
		defer func() { <-c.reads }()
		return c.resolve(ctx, locallink.Request{Dir: clone.Path, Repository: v.Repository, Head: v.Head, Base: v.Base, MergeBase: v.MergeBase})
	}, Patch: func(ctx context.Context, v viewserver.Review, path string) (locallink.PatchResult, error) {
		if !acquire(ctx, c.reads) {
			return locallink.PatchResult{}, ctx.Err()
		}
		defer func() { <-c.reads }()
		return patcher.Patch(ctx, locallink.PatchRequest{Dir: clone.Path, Repository: v.Repository, Head: v.Head, Base: v.Base, MergeBase: v.MergeBase, Path: path})
	}})
	if err != nil {
		c.mu.Unlock()
		if old != nil {
			old.Close()
		}
		return nil, nil, err
	}
	entry := &cachedDocument{server: server, used: c.clock, active: 1}
	c.entries[key] = entry
	c.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return server, func() { c.release(entry) }, nil
}
func (c *documentCache) release(entry *cachedDocument) { c.mu.Lock(); entry.active--; c.mu.Unlock() }
func (c *documentCache) close() {
	c.mu.Lock()
	c.closed = true
	entries := c.entries
	c.entries = map[string]*cachedDocument{}
	c.mu.Unlock()
	for _, entry := range entries {
		entry.server.Close()
	}
}
