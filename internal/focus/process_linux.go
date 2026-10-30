package focus

import (
	"os"
	"strconv"
	"strings"

	"github.com/baum-ai/baum/internal/hookevents"
)

// procRoot is /proc; tests point it elsewhere.
var procRoot = "/proc"

// readProcess reads a process's name, start and parent from /proc/<pid>/stat.
// Start is the starttime field: clock ticks since boot.
func readProcess(pid int) (hookevents.Process, int, bool) {
	data, err := os.ReadFile(procRoot + "/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return hookevents.Process{}, 0, false
	}
	return parseStat(pid, string(data))
}

// parseStat parses a stat line. The name is between the first "(" and the
// last ")", since it may hold both, and spaces.
func parseStat(pid int, stat string) (hookevents.Process, int, bool) {
	open, end := strings.IndexByte(stat, '('), strings.LastIndexByte(stat, ')')
	if open < 0 || end < open {
		return hookevents.Process{}, 0, false
	}
	// After the name: state ppid pgrp session tty_nr tpgid flags minflt
	// cminflt majflt cmajflt utime stime cutime cstime priority nice
	// num_threads itrealvalue starttime ...
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 20 {
		return hookevents.Process{}, 0, false
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return hookevents.Process{}, 0, false
	}
	return hookevents.Process{PID: pid, Start: fields[19], Name: stat[open+1 : end]}, parent, true
}

// executable is a process's executable file, or "".
func executable(pid int) string {
	path, _ := os.Readlink(procRoot + "/" + strconv.Itoa(pid) + "/exe")
	return path
}

// controllingTTY is the terminal a process's standard input, output or error
// is, or "" when none is.
func controllingTTY(pid int) string {
	for _, fd := range []string{"0", "1", "2"} {
		target, err := os.Readlink(procRoot + "/" + strconv.Itoa(pid) + "/fd/" + fd)
		if err == nil && (strings.HasPrefix(target, "/dev/pts/") || strings.HasPrefix(target, "/dev/tty")) {
			return target
		}
	}
	return ""
}
