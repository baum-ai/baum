package livesessions

import (
	"sync"
	"time"
)

// ReconfirmEvery is how long ClaudeProcesses trusts a process it confirmed
// (sameProcess) while its pid still exists, before confirming it again.
const ReconfirmEvery = 30 * time.Second

// ClaudeProcesses tells the list and the tracker which Claude Code sessions
// still run and which End would end, as FindClaude and ClaudeExit.Check do,
// cheaply enough to ask every second for every session: each look reads
// home's ~/.claude/sessions once for all the sessions it is asked about, and
// a process confirmed as the one that wrote its entry is trusted for
// ReconfirmEvery while its pid exists, rather than confirmed again (on macOS,
// a `ps` per session).
//
// Its answers only decide what the list offers and how long a silent session
// stays: ClaudeExit.End and ClaudeInbox.Send confirm the process themselves
// before they reach it.
type ClaudeProcesses struct {
	// Home holds ~/.claude; empty is the user's home.
	Home string

	mu        sync.Mutex
	confirmed map[processIdentity]confirmation

	// Tests replace the clock, the registry read, running and sameProcess.
	now    func() time.Time
	read   func(home string) (map[string][]ClaudeProcess, error)
	exists func(pid int) bool
	same   func(ClaudeProcess) error
}

// processIdentity is the process a registry entry names.
type processIdentity struct {
	pid              int
	start, pidDomain string
}

// confirmation is what sameProcess said of a process, and when.
type confirmation struct {
	err error
	at  time.Time
}

// EndCheck is an EndChecker: ClaudeExit.Check's answer for each session one
// list asks about.
func (c *ClaudeProcesses) EndCheck() EndCheck {
	find := c.look()
	return func(id string) error {
		_, err := idleClaude(find(id))
		return err
	}
}

// Alive is a Tracker's Alive: whether the process of a Claude Code session
// still runs. Other agents register no process, so theirs are never
// confirmed.
func (c *ClaudeProcesses) Alive(agent, id string) bool {
	if agent != AgentClaudeCode {
		return false
	}
	_, err := c.look()(id)
	return err == nil
}

// look finds sessions' processes as FindClaude does, from one read of the
// registry, made when it is first asked.
func (c *ClaudeProcesses) look() func(id string) (ClaudeProcess, error) {
	registry := sync.OnceValues(func() (map[string][]ClaudeProcess, error) {
		home, err := orHome(c.Home)
		if err != nil {
			return nil, err
		}
		read := c.read
		if read == nil {
			read = readClaudeRegistry
		}
		byID, err := read(home)
		if err == nil {
			c.forgetUnlisted(byID)
		}
		return byID, err
	})
	return func(id string) (ClaudeProcess, error) {
		if !safeID.MatchString(id) {
			return ClaudeProcess{}, ErrAgentGone
		}
		byID, err := registry()
		if err != nil {
			return ClaudeProcess{}, err
		}
		return pickClaude(byID[id], c.confirm)
	}
}

// confirm is sameProcess's answer for p, asked again only once it is
// ReconfirmEvery old. A pid that no longer exists is gone at once.
//
// An answer stands while the pid exists: Claude Code removes the entry of a
// session that exits, so the same entry naming a live pid names the same
// process, unless the session was killed and another process took its pid
// between two looks. The list then trusts the old answer for at most
// ReconfirmEvery; End confirms the process itself.
func (c *ClaudeProcesses) confirm(p ClaudeProcess) error {
	identity := processIdentity{p.PID, p.ProcStart, p.PIDDomain}
	exists := c.exists
	if exists == nil {
		exists = running
	}
	if !exists(p.PID) {
		c.mu.Lock()
		delete(c.confirmed, identity)
		c.mu.Unlock()
		return ErrAgentGone
	}
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	c.mu.Lock()
	known, ok := c.confirmed[identity]
	c.mu.Unlock()
	if ok && now.Sub(known.at) < ReconfirmEvery {
		return known.err
	}
	same := c.same
	if same == nil {
		same = sameProcess
	}
	err := same(p)
	c.mu.Lock()
	if c.confirmed == nil {
		c.confirmed = map[processIdentity]confirmation{}
	}
	c.confirmed[identity] = confirmation{err, now}
	c.mu.Unlock()
	return err
}

// forgetUnlisted drops what was confirmed of processes no registry entry
// names any more, so it holds no more than the registry does.
func (c *ClaudeProcesses) forgetUnlisted(byID map[string][]ClaudeProcess) {
	listed := map[processIdentity]bool{}
	for _, entries := range byID {
		for _, p := range entries {
			listed[processIdentity{p.PID, p.ProcStart, p.PIDDomain}] = true
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for identity := range c.confirmed {
		if !listed[identity] {
			delete(c.confirmed, identity)
		}
	}
}
