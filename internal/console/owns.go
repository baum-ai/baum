package console

import (
	"path/filepath"
	"strings"

	"github.com/baum-ai/baum/internal/agentcontext"
)

// Owns names the conversation a session belongs to, for the Sessions page
// (livesessions.Tracker.Panel): the one whose agent session it is, or the
// one whose directory baum made (its empty directory, or a worktree baum
// added) holds cwd. A person's own worktree a conversation runs in is matched
// by session only, so the person's own sessions there stay theirs. It reads
// an index kept in memory and never blocks on a turn.
func (s *Service) Owns(agent, id, cwd string) (string, bool) {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	if conversation, ok := s.sessions[agentcontext.SessionKey(agent, id)]; ok && id != "" {
		return conversation, true
	}
	if cwd == "" {
		return "", false
	}
	cwd = filepath.Clean(cwd)
	for dir, conversation := range s.dirs {
		if within(dir, cwd) {
			return conversation, true
		}
	}
	return "", false
}

// within reports whether path is dir or inside it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// indexLocked records c in the index Owns reads, replacing what it held for
// c. s.cmu is held.
func (s *Service) indexLocked(c Conversation) {
	for key, conversation := range s.sessions {
		if conversation == c.ID {
			delete(s.sessions, key)
		}
	}
	for dir, conversation := range s.dirs {
		if conversation == c.ID {
			delete(s.dirs, dir)
		}
	}
	if c.Session != "" {
		s.sessions[agentcontext.SessionKey(c.Agent, c.Session)] = c.ID
	}
	if c.Repository == "" || c.CreatedWorktree {
		dir := filepath.Clean(c.Directory)
		s.dirs[dir] = c.ID
		// Hooks may report the directory with its links resolved.
		if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != dir {
			s.dirs[resolved] = c.ID
		}
	}
}

// index records c for Owns.
func (s *Service) index(c Conversation) {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	s.indexLocked(c)
}
