package main

import "testing"

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
	if got := tmpfsSize("/proc"); got != 0 {
		t.Errorf("/proc is no tmpfs, got size %d", got)
	}
	if got := tmpfsSize("/dev/shm"); got < 0 {
		t.Errorf("/dev/shm: size %d", got)
	}
	_ = memoryLimit()
}
