package main

import "testing"

func TestProbes(t *testing.T) {
	if got := tmpfsSize("/proc"); got != 0 {
		t.Errorf("/proc is no tmpfs, got size %d", got)
	}
	if got := tmpfsSize("/dev/shm"); got < 0 {
		t.Errorf("/dev/shm: size %d", got)
	}
	if limit, physical := memoryLimit(); physical <= 0 || limit > physical {
		t.Errorf("limit %d, physical memory %d", limit, physical)
	}
}
