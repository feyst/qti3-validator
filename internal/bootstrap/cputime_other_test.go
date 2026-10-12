//go:build !unix

package bootstrap

import "time"

// cpuTime is the time on the clock where the CPU time of the process is not
// to be had.
func cpuTime() time.Duration { return time.Duration(time.Now().UnixNano()) }
