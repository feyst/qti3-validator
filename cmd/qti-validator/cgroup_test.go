package main

import (
	"testing"
	"testing/fstest"
)

func TestParseMemoryLimit(t *testing.T) {
	for in, want := range map[string]int64{
		"268435456\n":           268435456,
		"max\n":                 0,
		"9223372036854771712\n": 0, // cgroup v1 without a limit
		"":                      0,
	} {
		if got := parseMemoryLimit(in); got != want {
			t.Errorf("%q: got %d, want %d", in, got, want)
		}
	}
}

func TestMemoryLimitIn(t *testing.T) {
	const mib = 1 << 20
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	meminfo := file("MemTotal:        8388608 kB\nMemFree:          123456 kB\n") // 8 GiB
	tests := []struct {
		name     string
		files    fstest.MapFS
		limit    int64
		physical int64
	}{
		{"cgroup v2 in a container", fstest.MapFS{
			"proc/self/cgroup":         file("0::/\n"),
			"sys/fs/cgroup/memory.max": file("268435456\n"),
			"proc/meminfo":             meminfo,
		}, 256 * mib, 8192 * mib},
		{"cgroup v2, no limit", fstest.MapFS{
			"proc/self/cgroup":          file("0::/\n"),
			"sys/fs/cgroup/memory.max":  file("max\n"),
			"sys/fs/cgroup/memory.high": file("max\n"),
			"proc/meminfo":              meminfo,
		}, 0, 8192 * mib},
		{"cgroup v2, memory.high", fstest.MapFS{
			"proc/self/cgroup":          file("0::/\n"),
			"sys/fs/cgroup/memory.max":  file("max\n"),
			"sys/fs/cgroup/memory.high": file("536870912\n"),
			"proc/meminfo":              meminfo,
		}, 512 * mib, 8192 * mib},
		{"limit on the pod above the container", fstest.MapFS{
			"proc/self/cgroup": file("0::/kubepods.slice/pod1.slice/container.scope\n"),
			"sys/fs/cgroup/kubepods.slice/pod1.slice/container.scope/memory.max": file("max\n"),
			"sys/fs/cgroup/kubepods.slice/pod1.slice/memory.max":                 file("1073741824\n"),
			"sys/fs/cgroup/kubepods.slice/memory.max":                            file("max\n"),
			"proc/meminfo": meminfo,
		}, 1024 * mib, 8192 * mib},
		{"the least of the limits above", fstest.MapFS{
			"proc/self/cgroup": file("0::/system.slice/qti.service\n"),
			"sys/fs/cgroup/system.slice/qti.service/memory.max": file("1073741824\n"),
			"sys/fs/cgroup/system.slice/memory.high":            file("402653184\n"),
			"sys/fs/cgroup/memory.max":                          file("max\n"),
		}, 384 * mib, 0},
		{"a group that is not visible: the root is", fstest.MapFS{
			"proc/self/cgroup":         file("0::/docker/abc\n"),
			"sys/fs/cgroup/memory.max": file("268435456\n"),
		}, 256 * mib, 0},
		{"cgroup v1", fstest.MapFS{
			"proc/self/cgroup": file("12:cpu,cpuacct:/docker/abc\n7:memory:/docker/abc\n1:name=systemd:/docker/abc\n"),
			"sys/fs/cgroup/memory/docker/abc/memory.limit_in_bytes": file("9223372036854771712\n"),
			"sys/fs/cgroup/memory/docker/memory.limit_in_bytes":     file("536870912\n"),
			"sys/fs/cgroup/memory/memory.limit_in_bytes":            file("9223372036854771712\n"),
			"proc/meminfo": meminfo,
		}, 512 * mib, 8192 * mib},
		{"cgroup v1 in a container", fstest.MapFS{
			"proc/self/cgroup":                           file("7:memory:/docker/abc\n"),
			"sys/fs/cgroup/memory/memory.limit_in_bytes": file("268435456\n"),
		}, 256 * mib, 0},
		{"no /proc/self/cgroup", fstest.MapFS{
			"sys/fs/cgroup/memory/memory.limit_in_bytes": file("268435456\n"),
		}, 256 * mib, 0},
		{"a limit above the memory of the machine", fstest.MapFS{
			"proc/self/cgroup":         file("0::/\n"),
			"sys/fs/cgroup/memory.max": file("68719476736\n"),
			"proc/meminfo":             meminfo,
		}, 8192 * mib, 8192 * mib},
		{"nothing to read", fstest.MapFS{}, 0, 0},
		{"garbage", fstest.MapFS{
			"proc/self/cgroup":         file("nonsense\n::\n0::\n"),
			"sys/fs/cgroup/memory.max": file("lots\n"),
			"proc/meminfo":             file("MemTotal: many kB\n"),
		}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit, physical := memoryLimitIn(tt.files)
			if limit != tt.limit || physical != tt.physical {
				t.Errorf("limit %d MiB, physical %d MiB; want %d and %d", limit/mib, physical/mib, tt.limit/mib, tt.physical/mib)
			}
		})
	}
}
