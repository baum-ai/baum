//go:build linux

package livesessions

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// sameProcess reports whether p's pid is still the process that registered
// the session: in this daemon's pid namespace, started when the entry says.
// It returns ErrAgentGone for another process, and an error when it cannot
// tell.
func sameProcess(p ClaudeProcess) error {
	if p.PIDDomain != "" {
		own, err := os.Readlink("/proc/self/ns/pid")
		if err != nil {
			return fmt.Errorf("reading this pid namespace: %w", err)
		}
		if !inDomain(p.PIDDomain, machineID(), own) {
			// Another machine's or namespace's pid (a container's) names
			// something else here.
			return fmt.Errorf("claude code session in pid namespace %s, not this one", p.PIDDomain)
		}
	}
	if p.ProcStart == "" {
		return nil
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(p.PID) + "/stat")
	if err != nil {
		return ErrAgentGone
	}
	// The command name, in parentheses, can hold anything; the fields after
	// it start with the third.
	i := bytes.LastIndexByte(stat, ')')
	fields := strings.Fields(string(stat[i+1:]))
	if i < 0 || len(fields) < 20 {
		return fmt.Errorf("unreadable /proc/%d/stat", p.PID)
	}
	if fields[19] != p.ProcStart {
		return ErrAgentGone
	}
	return nil
}

// inDomain reports whether a pidDomain Claude Code wrote, "linux:<machine
// id>:<namespace>" (the machine id empty where there is none), is this
// daemon's: the same pid namespace and, when both know it, the same machine.
func inDomain(domain, machine, namespace string) bool {
	rest, ok := strings.CutPrefix(domain, "linux:")
	if !ok {
		return false
	}
	theirs, ns, ok := strings.Cut(rest, ":")
	if !ok || ns != namespace {
		return false
	}
	return theirs == "" || machine == "" || theirs == machine
}

// machineID is this machine's id, as systemd and D-Bus keep it, or "".
func machineID() string {
	for _, path := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if data, err := os.ReadFile(path); err == nil {
			if id := strings.TrimSpace(string(data)); id != "" {
				return id
			}
		}
	}
	return ""
}
