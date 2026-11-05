//go:build !unix

package workstats

func cpuTimes() (user, system float64) { return 0, 0 }
