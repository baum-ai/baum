//go:build unix

package workstats

import "syscall"

func cpuTimes() (user, system float64) {
	var usage syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) != nil {
		return 0, 0
	}
	seconds := func(t syscall.Timeval) float64 { return float64(t.Sec) + float64(t.Usec)/1e6 }
	return seconds(usage.Utime), seconds(usage.Stime)
}
