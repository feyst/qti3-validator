package main

import (
	"log/slog"
	"os"

	"qti3-validator/internal/config"
)

// processHeadroom is the memory the process itself needs beside the uploads
// in a tmpfs: about 100 MB at its peak, with room to spare.
const processHeadroom = 160 << 20

// spoolEnv is what the service knows about where request bodies are kept.
type spoolEnv struct {
	TmpfsSize   int64 // size of the tmpfs holding the temporary directory; 0 when it is not one
	MemoryLimit int64 // the container's memory limit; 0 when unknown or unlimited
	// PhysicalMemory is the memory of the machine; 0 when unknown. A
	// MemoryLimit is never more.
	PhysicalMemory int64
}

// probeSpoolEnv inspects the temporary directory and the cgroup.
func probeSpoolEnv() spoolEnv {
	limit, physical := memoryLimit()
	return spoolEnv{TmpfsSize: tmpfsSize(os.TempDir()), MemoryLimit: limit, PhysicalMemory: physical}
}

// spoolBudget is the MaxSpoolSize the server uses. Request bodies in a tmpfs
// are kept in memory and count against the container's memory limit, so
// there the budget is at most the tmpfs and the memory the process leaves:
// a full budget then refuses uploads with 503 instead of the container
// being killed for running out of memory.
func spoolBudget(log *slog.Logger, cfg config.Config, env spoolEnv) int64 {
	budget := cfg.MaxSpoolSize
	if env.TmpfsSize <= 0 {
		return budget
	}
	budget = min(budget, env.TmpfsSize)
	if env.MemoryLimit > 0 {
		budget = min(budget, env.MemoryLimit-processHeadroom)
	}
	// 0 would select the default; 1 byte refuses every body.
	budget = max(budget, 1)
	attrs := []any{
		"dir", os.TempDir(), "max_spool_size", cfg.MaxSpoolSize, "tmpfs_size", env.TmpfsSize,
		"memory_limit", env.MemoryLimit, "process_headroom", processHeadroom, "spool_budget", budget,
	}
	if env.MemoryLimit <= 0 {
		// The spool cannot be clamped to the container's memory, so uploads
		// kept in the tmpfs could exhaust it. Warn even when the budget still
		// equals MAX_SPOOL_SIZE: the risk is the tmpfs, not the number.
		log.Warn("the temporary directory is a tmpfs but no container memory limit was detected, so the upload spool cannot be clamped to it; "+
			"uploads kept in the tmpfs count against memory and may exhaust it. Mount a size-limited tmpfs or a disk volume on it, or set a memory limit", attrs...)
		return budget
	}
	if budget == cfg.MaxSpoolSize {
		return budget
	}
	if budget < cfg.MaxRequestSize {
		log.Error("the temporary directory is a tmpfs without room for uploads; requests larger than the budget will be refused with 413. "+
			"Mount a disk volume on it, or raise the memory limit", attrs...)
		return budget
	}
	log.Warn("the temporary directory is a tmpfs: uploads are kept in memory, so MAX_SPOOL_SIZE is lowered to fit", attrs...)
	return budget
}
