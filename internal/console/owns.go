package console

import (
	"path/filepath"

	"github.com/baum-ai/baum/internal/agentcontext"
)

// Owns names the conversation a session belongs to, for the Sessions page
// (livesessions.Tracker.Panel): the one whose agent session it is, or the
// one whose empty directory holds cwd. A conversation in a pull request's
// worktree, whether the person's or one baum added, is matched by session
// only: the worktree is a checkout the person may work in too, and stays
// after the conversation closes, so the person's own sessions there stay
// theirs. It reads an index kept in memory: a map lookup per directory above
// cwd, under a lock no turn holds for long.
func (s *Service) Owns(agent, id, cwd string) (string, bool) {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	if id != "" {
		if conversation, ok := s.sessions[agentcontext.SessionKey(agent, id)]; ok {
			return conversation, true
		}
	}
	if cwd == "" {
		return "", false
	}
	for dir := filepath.Clean(cwd); ; {
		if conversation, ok := s.dirs[dir]; ok {
			return conversation, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// indexDirectory records the empty directory of a conversation with no
// repository for Owns, as it is spelled and with its links resolved, since
// hooks may report either. The links are resolved before the lock is taken.
func (s *Service) indexDirectory(c Conversation) {
	if c.Repository != "" {
		return
	}
	dir := filepath.Clean(c.Directory)
	resolved, err := filepath.EvalSymlinks(dir)
	s.cmu.Lock()
	defer s.cmu.Unlock()
	s.dirs[dir] = c.ID
	if err == nil {
		s.dirs[resolved] = c.ID
	}
}

// indexSession records c's agent session for Owns, replacing the one it
// held, if any.
func (s *Service) indexSession(c Conversation) {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	for key, conversation := range s.sessions {
		if conversation == c.ID {
			delete(s.sessions, key)
		}
	}
	if c.Session != "" {
		s.sessions[agentcontext.SessionKey(c.Agent, c.Session)] = c.ID
	}
}
