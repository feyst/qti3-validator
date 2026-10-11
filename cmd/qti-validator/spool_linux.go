package main

import (
	"math"
	"os"
	"strconv"
	"strings"
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
	bsize := uint64(st.Bsize) //nolint:gosec // positive, checked above
	if st.Blocks > math.MaxInt64/bsize {
		return math.MaxInt64
	}
	return int64(st.Blocks * bsize) //nolint:gosec // bounded by the check above
}

// memoryLimit is the memory limit of the cgroup, v2 or v1, or 0 when there
// is none or it cannot be read.
func memoryLimit() int64 {
	for _, file := range []string{
		"/sys/fs/cgroup/memory.max",                   // cgroup v2
		"/sys/fs/cgroup/memory/memory.limit_in_bytes", // cgroup v1
	} {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		return parseMemoryLimit(string(data))
	}
	return 0
}

// parseMemoryLimit reads a cgroup memory limit: "max" in v2, and a value
// near the largest int64 in v1, mean none.
func parseMemoryLimit(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n <= 0 || n >= 1<<60 {
		return 0
	}
	return n
}
