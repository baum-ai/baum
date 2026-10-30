package focus

import (
	"os"

	"github.com/baum-ai/baum/internal/hookevents"
)

// Capture describes where the running hook was started: the variables of
// hookevents.TerminalEnv it has, its ancestors, its parent first, and the
// controlling terminal of the nearest ancestor that has one. Agents start
// hooks in a session of their own, so the hook itself has none. It returns
// nil where none of that can be read.
func Capture() *hookevents.Terminal {
	terminal := &hookevents.Terminal{Env: map[string]string{}}
	for _, name := range hookevents.TerminalEnv {
		if value, ok := os.LookupEnv(name); ok {
			terminal.Env[name] = value
		}
	}
	terminal.Processes = ancestors(os.Getppid())
	for _, p := range terminal.Processes {
		if tty := controllingTTY(p.PID); tty != "" {
			terminal.TTY = tty
			break
		}
	}
	if len(terminal.Env) == 0 && len(terminal.Processes) == 0 {
		return nil
	}
	return terminal
}

// ancestors are pid and its ancestors up to, not including, the first
// process (pid 1), at most maxAncestors of them.
func ancestors(pid int) []hookevents.Process {
	var out []hookevents.Process
	for pid > 1 && len(out) < maxAncestors {
		p, parent, ok := readProcess(pid)
		if !ok {
			break
		}
		out = append(out, p)
		pid = parent
	}
	return out
}

// maxAncestors bounds a chain, as hookevents bounds what an event carries.
const maxAncestors = 24
