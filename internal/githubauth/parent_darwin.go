package githubauth

import (
	"bytes"

	"golang.org/x/sys/unix"
)

func parentExecutable(pid int) (string, error) {
	// KERN_PROCARGS2 begins with argc followed by the kernel's executable path,
	// before the caller-controlled argument and environment strings.
	data, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(data) <= 4 {
		return "", ErrStore
	}
	end := bytes.IndexByte(data[4:], 0)
	if end <= 0 {
		return "", ErrStore
	}
	return string(data[4 : 4+end]), nil
}
