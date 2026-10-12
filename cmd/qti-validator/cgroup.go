package main

import (
	"bufio"
	"bytes"
	"io/fs"
	"path"
	"strconv"
	"strings"
)

// memoryLimitIn finds how much memory the process may use, in the file
// system root, which is "/" outside the tests: the least of the limits of
// its control group and of every group above it, and the memory of the
// machine. A limit may be set on any of them: on the container, on the pod
// or the systemd slice it is part of, and as a hard limit (memory.max) or
// as the point where the kernel starts to reclaim and throttle
// (memory.high). limit is 0 when there is none, or none can be read;
// physical is 0 when the memory of the machine cannot be read.
func memoryLimitIn(root fs.FS) (limit, physical int64) {
	least := func(n int64) {
		if n > 0 && (limit == 0 || n < limit) {
			limit = n
		}
	}
	for _, group := range controlGroups(root) {
		// The group and every group above it. Inside a container the path
		// may name groups that are not visible; the root always is.
		for dir := path.Clean("/" + group.path); ; dir = path.Dir(dir) {
			for _, name := range group.files {
				if data, err := fs.ReadFile(root, path.Join(group.base, dir, name)); err == nil {
					least(parseMemoryLimit(string(data)))
				}
			}
			if dir == "/" {
				break
			}
		}
	}
	physical = physicalMemory(root)
	if physical > 0 && limit > physical {
		limit = physical
	}
	return limit, physical
}

// controlGroup is where the memory limits of a control group hierarchy are:
// the directory of the hierarchy, the path of the process's group in it and
// the files that hold a limit.
type controlGroup struct {
	base, path string
	files      []string
}

var (
	cgroupV2 = controlGroup{base: "sys/fs/cgroup", path: "/", files: []string{"memory.max", "memory.high"}}
	cgroupV1 = controlGroup{base: "sys/fs/cgroup/memory", path: "/", files: []string{"memory.limit_in_bytes"}}
)

// controlGroups reads the groups of the process from /proc/self/cgroup:
// "0::/path" for the unified hierarchy of cgroup v2, "N:memory:/path" for
// the memory controller of v1. Without that file it looks at the root of
// both.
func controlGroups(root fs.FS) []controlGroup {
	data, err := fs.ReadFile(root, "proc/self/cgroup")
	if err != nil {
		return []controlGroup{cgroupV2, cgroupV1}
	}
	var groups []controlGroup
	for sc := bufio.NewScanner(bytes.NewReader(data)); sc.Scan(); {
		fields := strings.SplitN(sc.Text(), ":", 3)
		if len(fields) != 3 {
			continue
		}
		switch {
		case fields[0] == "0" && fields[1] == "":
			g := cgroupV2
			g.path = fields[2]
			groups = append(groups, g)
		case strings.Contains(","+fields[1]+",", ",memory,"):
			g := cgroupV1
			g.path = fields[2]
			groups = append(groups, g)
		}
	}
	if len(groups) == 0 {
		return []controlGroup{cgroupV2, cgroupV1}
	}
	return groups
}

// physicalMemory reads MemTotal from /proc/meminfo, in bytes.
func physicalMemory(root fs.FS) int64 {
	data, err := fs.ReadFile(root, "proc/meminfo")
	if err != nil {
		return 0
	}
	for sc := bufio.NewScanner(bytes.NewReader(data)); sc.Scan(); {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kib, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil || kib <= 0 || kib > 1<<50 {
				return 0
			}
			return kib << 10
		}
	}
	return 0
}

// parseMemoryLimit reads a control group's memory limit: "max" in v2, and a
// value near the largest int64 in v1, mean none.
func parseMemoryLimit(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n <= 0 || n >= 1<<60 {
		return 0
	}
	return n
}
