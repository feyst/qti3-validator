//go:build !linux

package main

// tmpfsSize reports no tmpfs: only Linux is inspected.
func tmpfsSize(string) int64 { return 0 }

// memoryLimit reports no limit: only Linux cgroups are inspected.
func memoryLimit() int64 { return 0 }
