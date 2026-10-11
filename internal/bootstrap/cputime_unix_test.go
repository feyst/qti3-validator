//go:build unix

package bootstrap

import (
	"syscall"
	"time"
)

// cpuTime returns the CPU time the process has used so far, in user and in
// system mode. Unlike the time on the clock, it does not grow while other
// processes have the processor.
func cpuTime() time.Duration {
	var u syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &u); err != nil {
		return time.Duration(time.Now().UnixNano())
	}
	return time.Duration(u.Utime.Nano() + u.Stime.Nano())
}
