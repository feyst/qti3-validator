package main

import (
	"math"
	"os"
	"syscall"
)

// tmpfsMagic is the f_type statfs reports for a tmpfs.
const tmpfsMagic = 0x01021994

// tmpfsSize is the size of the tmpfs dir is on, or 0 when it is on another
// filesystem or cannot be inspected.
func tmpfsSize(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil || st.Type != tmpfsMagic || st.Bsize <= 0 {
		return 0
	}
	bsize := uint64(st.Bsize)
	if st.Blocks > math.MaxInt64/bsize {
		return math.MaxInt64
	}
	return int64(st.Blocks * bsize) //nolint:gosec // bounded by the check above
}

// memoryLimit is the memory the process may use and the memory of the
// machine; see memoryLimitIn.
func memoryLimit() (limit, physical int64) {
	return memoryLimitIn(os.DirFS("/"))
}
