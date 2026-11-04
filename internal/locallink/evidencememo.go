package locallink

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/collector"
)

// EvidenceMaxAge bounds how long BuildSessionEvidence reuses an answer whose
// inputs look unchanged. What the key cannot see, such as a file created in
// the checkout that a session touched, shows within this time.
const EvidenceMaxAge = 10 * time.Minute

// maxEvidenceEntries bounds the remembered answers: one per pull request and
// set of caps, each a few kilobytes.
const maxEvidenceEntries = 256

// evidenceMemo remembers BuildSessionEvidence's answers, so a refresh of a
// pull request whose commits, sessions and checkout did not change reads no
// transcript. Callers get copies: they cap and redact the evidence in place.
var evidenceMemo = newEvidenceCache(EvidenceMaxAge)

// frozenMemo keeps the evidence last built for the commits of a pull request
// that is closed or merged (EvidenceRequest.Frozen), for the life of the
// process: those commits will not change, and a session that began after
// them is past the bound the evidence reads (sessionsUntil).
var frozenMemo = newEvidenceCache(0)

type evidenceCache struct {
	mu      sync.Mutex
	order   *list.List
	entries map[string]*list.Element
	now     func() time.Time
	// maxAge is how long an answer is reused; zero keeps it until evicted.
	maxAge time.Duration
}

type evidenceEntry struct {
	key      string
	at       time.Time
	sessions []bundle.Session
}

func newEvidenceCache(maxAge time.Duration) *evidenceCache {
	return &evidenceCache{order: list.New(), entries: map[string]*list.Element{}, now: time.Now, maxAge: maxAge}
}

func (c *evidenceCache) get(key string) ([]bundle.Session, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	entry := element.Value.(*evidenceEntry)
	if c.maxAge > 0 && c.now().Sub(entry.at) >= c.maxAge {
		c.order.Remove(element)
		delete(c.entries, key)
		return nil, false
	}
	c.order.MoveToFront(element)
	return deepCopy(entry.sessions), true
}

func (c *evidenceCache) put(key string, evidence []bundle.Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[key]; ok {
		c.order.Remove(old)
	}
	c.entries[key] = c.order.PushFront(&evidenceEntry{key: key, at: c.now(), sessions: deepCopy(evidence)})
	for c.order.Len() > maxEvidenceEntries {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.entries, last.Value.(*evidenceEntry).key)
	}
}

// evidenceKey describes everything BuildSessionEvidence reads: the request,
// the sessions discovered for it (sessions.Discovery.Fingerprint, which
// covers the clone's worktrees and Entire refs), the home directory, and the
// state of the clone's checkouts, whose files bound the paths a session
// touched. ok is false when any of it cannot be described.
func evidenceKey(req EvidenceRequest, dir string, d *sessions.Discovery) (string, bool) {
	discovered, ok := d.Fingerprint()
	if !ok {
		return "", false
	}
	clone, err := collector.RepoRoot(dir)
	if err != nil {
		return "", false
	}
	home, _ := os.UserHomeDir()
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d\x00%d\x00%#v\x00", discovered, clone, home,
		req.MaxPromptsPerSession, req.MaxCommandEventsPerSession, req.Sessions)
	for _, c := range req.Commits {
		fmt.Fprintf(h, "%#v\x01", c)
	}
	for _, root := range append([]string{clone}, d.Worktrees()...) {
		state, ok := checkoutState(root)
		if !ok {
			return "", false
		}
		fmt.Fprintf(h, "%s\x01", state)
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

// frozenEvidenceKey describes a request without the session stores: the
// clone, the commits, the caps and the store options.
func frozenEvidenceKey(req EvidenceRequest, dir string) string {
	clone, err := collector.RepoRoot(dir)
	if err != nil {
		clone = dir
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%d\x00%d\x00%#v\x00", clone, req.MaxPromptsPerSession, req.MaxCommandEventsPerSession, req.Sessions)
	for _, c := range req.Commits {
		fmt.Fprintf(h, "%#v\x01", c)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// checkoutState describes what a checkout has checked out: what HEAD names
// and, for a branch, the commit the branch is at. A checkout or a commit
// changes it; "git status", which editors run all the time and which
// rewrites the index, does not. A linked worktree's .git is a file naming its
// git directory, whose commondir file names where its branches live.
func checkoutState(root string) (string, bool) {
	dotGit := filepath.Join(root, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return "", false
	}
	gitDir := dotGit
	if !info.IsDir() {
		data, err := os.ReadFile(dotGit)
		if err != nil {
			return "", false
		}
		target, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
		if !ok {
			return "", false
		}
		gitDir = strings.TrimSpace(target)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(root, gitDir)
		}
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", false
	}
	state := gitDir + "\x00" + strings.TrimSpace(string(head))
	ref, symbolic := strings.CutPrefix(strings.TrimSpace(string(head)), "ref:")
	if !symbolic {
		return state, true
	}
	commonDir := gitDir
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		commonDir = strings.TrimSpace(string(data))
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(gitDir, commonDir)
		}
	}
	ref = filepath.FromSlash(strings.TrimSpace(ref))
	if !filepath.IsLocal(ref) {
		return "", false
	}
	// A loose ref holds the branch's commit; a packed one is in packed-refs.
	if data, err := os.ReadFile(filepath.Join(commonDir, ref)); err == nil {
		return state + "\x00" + strings.TrimSpace(string(data)), true
	}
	info, err = os.Stat(filepath.Join(commonDir, "packed-refs"))
	if err != nil {
		// An unborn branch has no commit yet.
		return state + "\x00unborn", true
	}
	return fmt.Sprintf("%s\x00packed\x00%d\x00%d", state, info.Size(), info.ModTime().UnixNano()), true
}

// deepCopy copies v and everything it points to, keeping nil and empty
// slices and maps apart, so a caller that changes its copy changes nothing
// else.
func deepCopy[T any](v T) T {
	in := reflect.ValueOf(&v).Elem()
	out := reflect.New(in.Type()).Elem()
	copyValue(out, in)
	return out.Interface().(T)
}

func copyValue(dst, src reflect.Value) {
	switch src.Kind() {
	case reflect.Pointer:
		if src.IsNil() {
			return
		}
		p := reflect.New(src.Elem().Type())
		copyValue(p.Elem(), src.Elem())
		dst.Set(p)
	case reflect.Slice:
		if src.IsNil() {
			return
		}
		s := reflect.MakeSlice(src.Type(), src.Len(), src.Len())
		for i := range src.Len() {
			copyValue(s.Index(i), src.Index(i))
		}
		dst.Set(s)
	case reflect.Map:
		if src.IsNil() {
			return
		}
		m := reflect.MakeMapWithSize(src.Type(), src.Len())
		iter := src.MapRange()
		for iter.Next() {
			value := reflect.New(iter.Value().Type()).Elem()
			copyValue(value, iter.Value())
			m.SetMapIndex(iter.Key(), value)
		}
		dst.Set(m)
	case reflect.Struct:
		// Structs with unexported fields (time.Time) are values to copy whole.
		for i := range src.NumField() {
			if !dst.Field(i).CanSet() {
				dst.Set(src)
				return
			}
		}
		for i := range src.NumField() {
			copyValue(dst.Field(i), src.Field(i))
		}
	case reflect.Interface:
		if src.IsNil() {
			return
		}
		value := reflect.New(src.Elem().Type()).Elem()
		copyValue(value, src.Elem())
		dst.Set(value)
	default:
		dst.Set(src)
	}
}
