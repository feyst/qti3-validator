package main

import (
	"log/slog"
	"os"
	"runtime/debug"

	"qti3-validator/internal/config"
)

// Deriving the validation-memory budget and the Go soft memory limit from
// the container's memory limit lets the service size itself to the
// container. The spool budget (spool.go) and these share the same cgroup and
// tmpfs probes.
const (
	// validationHeadroom is the memory the process needs beside the
	// validations themselves: the idle heap, goroutine stacks and the Go
	// runtime. The idle service measures about 30 MiB; this leaves room to
	// spare so the budget never counts memory the validations cannot use.
	validationHeadroom = 48 << 20
	// defaultValidationMemory is the budget when no memory limit can be read,
	// so the container cannot be sized against. It is generous, to avoid
	// refusing documents on a large unconstrained host; a memory-constrained
	// host without a readable limit should set MAX_VALIDATION_MEMORY.
	defaultValidationMemory = 1 << 30
	// minValidationMemory keeps a tiny or miscomputed budget usable: at least
	// one small document validates at a time.
	minValidationMemory = 32 << 20
	// gomemlimitHeadroom is left to the runtime below the memory limit when
	// the soft limit is derived, for non-heap memory the GC cannot reclaim.
	gomemlimitHeadroom = 64 << 20
	// minGoMemLimit floors a derived soft limit so a very small container
	// does not set an unusable one.
	minGoMemLimit = 32 << 20
)

// validationMemoryBudget is the MaxValidationMemory the server uses: the
// explicit setting, else a share of the container's memory limit left for
// the running validations, less the uploads a tmpfs keeps in memory. When no
// limit is known it falls back to a constant and says so, because the memory
// of the validations then cannot be sized to the container.
func validationMemoryBudget(log *slog.Logger, cfg config.Config, env spoolEnv, spool int64) int64 {
	if cfg.MaxValidationMemory > 0 {
		return cfg.MaxValidationMemory
	}
	if env.MemoryLimit <= 0 {
		log.Warn("no container memory limit detected; the validation-memory budget cannot be sized to the container and falls back to a constant. "+
			"On a memory-constrained host set MAX_VALIDATION_MEMORY (and a memory limit)",
			"max_validation_memory", defaultValidationMemory)
		return defaultValidationMemory
	}
	budget := env.MemoryLimit - validationHeadroom
	tmpfsShare := int64(0)
	if env.TmpfsSize > 0 {
		// Uploads in a tmpfs are kept in memory, so they share the limit with
		// the validations.
		tmpfsShare = spool
		budget -= tmpfsShare
	}
	budget = max(budget, minValidationMemory)
	log.Info("validation memory budget derived from the memory limit",
		"memory_limit", env.MemoryLimit, "process_headroom", validationHeadroom,
		"tmpfs_spool", tmpfsShare, "max_validation_memory", budget)
	return budget
}

// applyMemoryLimit derives the Go soft memory limit (GOMEMLIMIT) from the
// container's memory limit when it is not set explicitly, so a larger
// container collects less often and a smaller one sooner. It leaves
// GOMEMLIMIT untouched when it is set, keeping today's behaviour for the
// default image, which sets GOMEMLIMIT=90MiB. Running the binary with
// GOMEMLIMIT empty (-e GOMEMLIMIT=) opts into the derived limit.
func applyMemoryLimit(log *slog.Logger, env spoolEnv) {
	if v, ok := os.LookupEnv("GOMEMLIMIT"); ok && v != "" {
		return // set explicitly; the runtime already applied it
	}
	if env.MemoryLimit <= 0 {
		return // nothing to derive from
	}
	soft := max(env.MemoryLimit-gomemlimitHeadroom, minGoMemLimit)
	debug.SetMemoryLimit(soft)
	log.Info("derived GOMEMLIMIT from the memory limit",
		"memory_limit", env.MemoryLimit, "headroom", gomemlimitHeadroom, "gomemlimit", soft)
}
