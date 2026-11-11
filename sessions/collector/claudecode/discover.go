package claudecode

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/sessions/traceprocessing"

	"github.com/baum-ai/baum/sessions/collector/model"
	"github.com/baum-ai/baum/sessions/vendors"
)

// probeLines caps how many leading records are scanned when probing a
// transcript for its cwd.
const probeLines = 100

type candidate struct {
	path    string
	name    string
	size    int64
	modTime time.Time
}

// HeadCache remembers what the head of each transcript recorded, keyed on its
// path, size and modification time, so repeated scans of an unchanged store do
// not open the files again. A pass is bracketed by BeginPass and EndPass; the
// entries no scan in it asked for (deleted files) are dropped at the end.
// A nil *HeadCache caches nothing. It is safe for concurrent use.
type HeadCache struct {
	mu      sync.Mutex
	pass    uint64
	reads   int
	entries map[string]headEntry
}

// Reads is how many transcript heads were read from disk rather than served
// from the cache.
func (c *HeadCache) Reads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

type headEntry struct {
	size    int64
	modTime time.Time
	cwd     string
	started time.Time
	pass    uint64
}

// Len is how many transcripts the cache holds a head for.
func (c *HeadCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// BeginPass starts a pass whose lookups EndPass keeps.
func (c *HeadCache) BeginPass() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.pass++
	c.mu.Unlock()
}

// EndPass drops the entries no lookup since BeginPass asked for.
func (c *HeadCache) EndPass() {
	if c == nil {
		return
	}
	c.mu.Lock()
	for path, e := range c.entries {
		if e.pass != c.pass {
			delete(c.entries, path)
		}
	}
	c.mu.Unlock()
}

// head returns the transcript's head, reading the file only when the cache has
// no entry for its current size and modification time. Failures are not cached.
func (c *HeadCache) head(cand candidate) (string, time.Time, error) {
	if c == nil {
		return sessionHead(cand.path)
	}
	c.mu.Lock()
	e, ok := c.entries[cand.path]
	if ok && e.size == cand.size && e.modTime.Equal(cand.modTime) {
		e.pass = c.pass
		c.entries[cand.path] = e
		c.mu.Unlock()
		return e.cwd, e.started, nil
	}
	c.reads++
	c.mu.Unlock()
	cwd, started, err := sessionHead(cand.path)
	if err != nil {
		return "", time.Time{}, err
	}
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]headEntry{}
	}
	c.entries[cand.path] = headEntry{size: cand.size, modTime: cand.modTime, cwd: cwd, started: started, pass: c.pass}
	c.mu.Unlock()
	return cwd, started, nil
}

type Files = model.Files

// FindSessions scans the Claude Code project store.
func FindSessions(projectsDir string, roots []string) ([]model.Session, model.ScanReport, error) {
	return FindSessionsSince(projectsDir, roots, time.Time{})
}

// FindSessionsSince is FindSessions without the transcripts last modified
// before since, which are skipped before they are opened. A zero since keeps
// every transcript.
func FindSessionsSince(projectsDir string, roots []string, since time.Time) ([]model.Session, model.ScanReport, error) {
	return FindSessionsCached(projectsDir, roots, since, nil)
}

// FindSessionsCached is FindSessionsSince reading transcript heads through
// heads, which may be nil.
func FindSessionsCached(projectsDir string, roots []string, since time.Time, heads *HeadCache) ([]model.Session, model.ScanReport, error) {
	return findSessions(projectsDir, roots, since, traceprocessing.Local, heads)
}

func FindSessionsWith(projectsDir string, roots []string, since time.Time, profile traceprocessing.MappingProfile) ([]model.Session, model.ScanReport, error) {
	return findSessions(projectsDir, roots, since, profile, nil)
}

func findSessions(projectsDir string, roots []string, since time.Time, profile traceprocessing.MappingProfile, heads *HeadCache) ([]model.Session, model.ScanReport, error) {
	candidates, report, err := loadCandidates(projectsDir, roots, since, profile)
	if err != nil {
		return nil, report, err
	}
	var sessions []model.Session
	for _, c := range candidates {
		cwd, started, ok, err := eligible(c, roots, heads)
		if err != nil {
			report.Add(c.path, err.Error())
			continue
		}
		if !ok {
			continue
		}
		sessions = append(sessions, buildSession(c, cwd, started))
	}
	return sessions, report, nil
}

// loadCandidates walks the project store for top-level .jsonl files under
// project directories that encode one of the roots (or a path beneath it).
// Claude Code names a project directory after the directory a session started
// in, with every character but a letter or digit replaced by '-', so a
// directory's name can also be the prefix of an unrelated sibling's
// (/repo and /repo-other). Those are listed here and ruled out by the working
// directory the transcript records.
func loadCandidates(projectsDir string, roots []string, since time.Time, profile traceprocessing.MappingProfile) ([]candidate, model.ScanReport, error) {
	var report model.ScanReport
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, report, nil
		}
		return nil, report, err
	}
	var candidates []candidate
	scanned := map[string]bool{}
	for _, root := range roots {
		encoded := EncodeProjectDir(root)
		for _, entry := range entries {
			if !entry.IsDir() || scanned[entry.Name()] || !encodesWithinProfile(encoded, entry.Name(), profile) {
				continue
			}
			scanned[entry.Name()] = true
			projectDir := filepath.Join(projectsDir, entry.Name())
			files, err := os.ReadDir(projectDir)
			if err != nil {
				report.Add(projectDir, fmt.Sprintf("listing project directory: %v", err))
				continue
			}
			for _, file := range files {
				// Only top-level transcripts: subdirectories hold sidecar data
				// (subagents, memory) that is not a session of its own.
				if file.IsDir() || filepath.Ext(file.Name()) != ".jsonl" {
					continue
				}
				path := filepath.Join(projectDir, file.Name())
				info, err := file.Info()
				if err != nil {
					report.Add(path, err.Error())
					continue
				}
				if !since.IsZero() && info.ModTime().Before(since) {
					continue
				}
				candidates = append(candidates, candidate{
					path:    path,
					name:    file.Name(),
					size:    info.Size(),
					modTime: info.ModTime().UTC(),
				})
			}
		}
	}
	return candidates, report, nil
}

// eligible reports whether a candidate transcript belongs under one of the
// repository roots, using the working directory the transcript recorded.
func eligible(c candidate, roots []string, heads *HeadCache) (cwd string, started time.Time, ok bool, err error) {
	cwd, started, err = heads.head(c)
	if err != nil {
		return "", time.Time{}, false, err
	}
	return cwd, started, model.PathWithinAny(roots, cwd) && !model.IsAgentRun(cwd), nil
}

// buildSession names the files an eligible transcript needs for upload.
func buildSession(c candidate, cwd string, started time.Time) model.Session {
	return model.Session{
		Source:     vendors.ClaudeCode,
		Path:       c.path,
		SessionID:  strings.TrimSuffix(c.name, ".jsonl"),
		WorkingDir: cwd,
		ModifiedAt: c.modTime,
		StartedAt:  started,
		Files:      SessionFiles(c.path),
	}
}

// SessionFiles names the tree of the session whose main transcript is at mainPath.
func SessionFiles(mainPath string) Files {
	files := Files{{
		Path: mainPath, Name: filepath.Base(mainPath), Kind: model.KindTranscript,
	}}
	stem := strings.TrimSuffix(filepath.Base(mainPath), filepath.Ext(mainPath))
	subagentsDir := filepath.Join(filepath.Dir(mainPath), stem, "subagents")
	entries, err := os.ReadDir(subagentsDir)
	if err != nil {
		return files
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "agent-") ||
			strings.HasPrefix(entry.Name(), "agent-.") {
			continue
		}
		kind := model.KindSubTranscript
		switch {
		case strings.HasSuffix(entry.Name(), ".meta.json"):
			kind = model.KindSubMetadata
		case !strings.HasSuffix(entry.Name(), ".jsonl"):
			continue
		}
		files = append(files, model.SessionFile{
			Path: filepath.Join(subagentsDir, entry.Name()), Name: entry.Name(), Kind: kind,
		})
	}
	sort.Slice(files[1:], func(i, j int) bool { return files[i+1].Name < files[j+1].Name })
	return files
}

// encodesWithin reports whether a project directory name could be the root
// directory encoded, or a directory beneath it. On Windows, whose paths ignore
// case, the comparison does too.
func encodesWithin(encoded, name string) bool {
	if len(name) < len(encoded) || !model.SameText(name[:len(encoded)], encoded, model.FoldCase) {
		return false
	}
	return len(name) == len(encoded) || name[len(encoded)] == '-'
}

func EncodeProjectDir(path string) string {
	var b strings.Builder
	b.Grow(len(path))
	for _, r := range path {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// sessionCWD returns the first working directory recorded in a transcript.
func sessionCWD(path string) (string, error) {
	cwd, _, err := sessionHead(path)
	return cwd, err
}

// sessionHead returns the first working directory recorded in a transcript
// and the earliest time a record up to it carries, zero when none does.
func sessionHead(path string) (string, time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", time.Time{}, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var started time.Time
	for range probeLines {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var rec struct {
				CWD       string `json:"cwd"`
				Timestamp any    `json:"timestamp"`
			}
			if json.Unmarshal(line, &rec) == nil {
				if text, ok := rec.Timestamp.(string); ok {
					if at, err := time.Parse(time.RFC3339Nano, text); err == nil && (started.IsZero() || at.Before(started)) {
						started = at.UTC()
					}
				}
				if rec.CWD != "" {
					return filepath.Clean(rec.CWD), started, nil
				}
			}
		}
		if err != nil {
			break
		}
	}
	return "", time.Time{}, fmt.Errorf("no working directory recorded in the first %d records", probeLines)
}

// SessionWorkingDir returns the working directory recorded near the start of
// one Claude Code transcript without decoding the transcript body.
func SessionWorkingDir(path string) (string, error) {
	return sessionCWD(path)
}

func encodesWithinProfile(encoded, name string, profile traceprocessing.MappingProfile) bool {
	if profile == traceprocessing.Hosted {
		return name == encoded || strings.HasPrefix(name, encoded+"-")
	}
	return encodesWithin(encoded, name)
}
