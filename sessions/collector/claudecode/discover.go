package claudecode

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/baumai/baum/sessions/collector/model"
	"github.com/baumai/baum/sessions/vendors"
)

// probeLines caps how many leading records are scanned when probing a
// transcript for its cwd.
const probeLines = 100

type candidate struct {
	path    string
	name    string
	modTime time.Time
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
	candidates, report, err := loadCandidates(projectsDir, roots, since)
	if err != nil {
		return nil, report, err
	}
	var sessions []model.Session
	for _, c := range candidates {
		cwd, ok, err := eligible(c, roots)
		if err != nil {
			report.Add(c.path, err.Error())
			continue
		}
		if !ok {
			continue
		}
		sessions = append(sessions, buildSession(c, cwd))
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
func loadCandidates(projectsDir string, roots []string, since time.Time) ([]candidate, model.ScanReport, error) {
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
			if !entry.IsDir() || scanned[entry.Name()] || !encodesWithin(encoded, entry.Name()) {
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
					modTime: info.ModTime().UTC(),
				})
			}
		}
	}
	return candidates, report, nil
}

// eligible reports whether a candidate transcript belongs under one of the
// repository roots, using the working directory the transcript recorded.
func eligible(c candidate, roots []string) (cwd string, ok bool, err error) {
	cwd, err = sessionCWD(c.path)
	if err != nil {
		return "", false, err
	}
	return cwd, model.PathWithinAny(roots, cwd) && !model.IsAgentRun(cwd), nil
}

// buildSession names the files an eligible transcript needs for upload.
func buildSession(c candidate, cwd string) model.Session {
	return model.Session{
		Source:     vendors.ClaudeCode,
		Path:       c.path,
		SessionID:  strings.TrimSuffix(c.name, ".jsonl"),
		WorkingDir: cwd,
		ModifiedAt: c.modTime,
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
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for range probeLines {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var rec struct {
				CWD string `json:"cwd"`
			}
			if json.Unmarshal(line, &rec) == nil && rec.CWD != "" {
				return filepath.Clean(rec.CWD), nil
			}
		}
		if err != nil {
			break
		}
	}
	return "", fmt.Errorf("no working directory recorded in the first %d records", probeLines)
}

// SessionWorkingDir returns the working directory recorded near the start of
// one Claude Code transcript without decoding the transcript body.
func SessionWorkingDir(path string) (string, error) {
	return sessionCWD(path)
}
