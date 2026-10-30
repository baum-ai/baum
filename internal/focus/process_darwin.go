package focus

import (
	"bytes"
	"fmt"
	"strconv"

	"golang.org/x/sys/unix"

	"github.com/baum-ai/baum/internal/hookevents"
)

// readProcess reads a process's name, start and parent with sysctl
// kern.proc.pid. Start is its start time in seconds and microseconds.
func readProcess(pid int) (hookevents.Process, int, bool) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	// A process that does not exist reads as zeroes.
	if err != nil || int(info.Proc.P_pid) != pid {
		return hookevents.Process{}, 0, false
	}
	start := info.Proc.P_starttime
	return hookevents.Process{
		PID:   pid,
		Start: strconv.FormatInt(int64(start.Sec), 10) + "." + strconv.FormatInt(int64(start.Usec), 10),
		Name:  unix.ByteSliceToString(info.Proc.P_comm[:]),
	}, int(info.Eproc.Ppid), true
}

// executable is a process's executable file, or "": the path that
// kern.procargs2 holds after the argument count.
func executable(pid int) string {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(buf) < 4 {
		return ""
	}
	path := buf[4:]
	if i := bytes.IndexByte(path, 0); i >= 0 {
		path = path[:i]
	}
	return string(path)
}

// ptyMajor is the device major number of macOS's pseudo-terminals,
// /dev/ttysNNN.
const ptyMajor = 16

// controllingTTY is a process's controlling terminal when it is a
// pseudo-terminal, the only kind a terminal application shows.
func controllingTTY(pid int) string {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(info.Proc.P_pid) != pid || info.Eproc.Tdev == -1 {
		return ""
	}
	dev := uint32(info.Eproc.Tdev)
	if dev>>24 != ptyMajor {
		return ""
	}
	return fmt.Sprintf("/dev/ttys%03d", dev&0xffffff)
}
