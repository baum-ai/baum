package cursor

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/lastpersonlabs/reviewpal/sessions/collector/model"
	"github.com/lastpersonlabs/reviewpal/sessions/cursortranscript"
	"github.com/lastpersonlabs/reviewpal/sessions/vendors"
)

const payloadName = "cursor-tree.json"

// FindSessions returns Cursor sessions under storePaths that belong to one of
// the repository roots. Each storePaths entry is a state.vscdb file or a
// directory searched for one; empty storePaths means the default global store.
func FindSessions(storePaths, roots []string) ([]model.Session, model.ScanReport, error) {
	paths := sessionStorePaths(storePaths)
	slices.Sort(paths)
	var report model.ScanReport
	byID := map[string]model.Session{}
	for _, path := range paths {
		found, storeReport := findStoreSessions(path, roots)
		report.Merge(storeReport)
		for _, candidate := range found {
			current, exists := byID[candidate.SessionID]
			if !exists || current.ModifiedAt.Before(candidate.ModifiedAt) ||
				(current.ModifiedAt.Equal(candidate.ModifiedAt) && candidate.Path < current.Path) {
				byID[candidate.SessionID] = candidate
			}
		}
	}

	sessions := make([]model.Session, 0, len(byID))
	for _, session := range byID {
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool {
		if !sessions[i].ModifiedAt.Equal(sessions[j].ModifiedAt) {
			return sessions[i].ModifiedAt.Before(sessions[j].ModifiedAt)
		}
		return sessions[i].Path < sessions[j].Path
	})
	return sessions, report, nil
}

func findStoreSessions(path string, roots []string) ([]model.Session, model.ScanReport) {
	var report model.ScanReport
	store, err := cursortranscript.Open(path)
	if err != nil {
		report.Add(path, err.Error())
		return nil, report
	}
	defer store.Close()

	discovery, err := store.Discover()
	if err != nil {
		report.Add(path, err.Error())
		return nil, report
	}
	info, _ := os.Stat(path)
	var sessions []model.Session
	for _, root := range discovery.Roots() {
		if !root.HasData {
			continue
		}
		workingDir := workspaceWorkingDir(root.WorkspaceIdentifier)
		if workingDir == "" || !model.PathWithinAny(roots, workingDir) {
			continue
		}
		tree, err := store.Tree(root.ComposerID)
		if err != nil {
			report.Add(path+"#"+root.ComposerID, err.Error())
			continue
		}
		body, err := cursortranscript.EncodeTreePayload(tree)
		if err != nil {
			report.Add(path+"#"+root.ComposerID, err.Error())
			continue
		}
		modifiedAt := cursorTime(root.LastUpdatedAt)
		if modifiedAt.IsZero() && info != nil {
			modifiedAt = info.ModTime().UTC()
		}
		sessions = append(sessions, model.Session{
			Source: vendors.Cursor, Path: path + "#" + root.ComposerID,
			SessionID: root.ComposerID, WorkingDir: workingDir, ModifiedAt: modifiedAt,
			Files: []model.SessionFile{{Data: body, Name: payloadName, Kind: model.KindTranscript}},
		})
	}
	return sessions, report
}

// SessionWorkingDirs returns the root session working directories and update
// times in a Cursor store without loading any transcript trees.
func SessionWorkingDirs(path string) ([]struct {
	WorkingDir string
	ModifiedAt time.Time
}, error) {
	store, err := cursortranscript.Open(path)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	discovery, err := store.Discover()
	if err != nil {
		return nil, err
	}
	var sessions []struct {
		WorkingDir string
		ModifiedAt time.Time
	}
	for _, root := range discovery.Roots() {
		workingDir := workspaceWorkingDir(root.WorkspaceIdentifier)
		if root.HasData && workingDir != "" {
			sessions = append(sessions, struct {
				WorkingDir string
				ModifiedAt time.Time
			}{workingDir, cursorTime(root.LastUpdatedAt)})
		}
	}
	return sessions, nil
}

func workspaceWorkingDir(identifier *cursortranscript.WorkspaceIdentifier) string {
	if identifier == nil {
		return ""
	}
	path := identifier.URI.FilePath()
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func cursorTime(milliseconds int64) time.Time {
	if milliseconds <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(milliseconds).UTC()
}
