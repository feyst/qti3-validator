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
	// validationHeadroom is the memory the process takes beside what the
	// validations reserve: some 35 MiB when idle, a few for the connections
	// and the stacks, and gomemlimitHeadroom. The budget, of which the
	// reservations are never less than what the validations take, and the
	// idle process then stay within the soft limit of the runtime: in a
	// container of 256 MiB, 184 + 35 + 5 = 224 MiB.
	validationHeadroom = 72 << 20
	// defaultValidationMemory is the budget when no memory limit can be
	// read: enough for any document up to the default MAX_REQUEST_SIZE,
	// whatever it is made of. A host with less to spare sets
	// MAX_VALIDATION_MEMORY.
	defaultValidationMemory = 1 << 30
	// minValidationMemory keeps the budget of a container that is too small
	// usable: typical items, of a few thousand nodes, still validate.
	minValidationMemory = 32 << 20
	// gomemlimitHeadroom is what the soft limit of the Go runtime stays
	// below the memory limit: the runtime counts what it manages, the heap
	// and the stacks, and the limit of the container counts the rest too,
	// the binary and the buffers of the kernel. Measured with documents made
	// to fill the budget, the process peaked some 10 MiB above its soft
	// limit; this leaves twice as much again.
	gomemlimitHeadroom = 32 << 20
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
		budget := int64(defaultValidationMemory)
		if env.PhysicalMemory > 0 {
			budget = max(min(budget, env.PhysicalMemory-validationHeadroom), minValidationMemory)
		}
		log.Warn("no memory limit detected, on the container or above it; the memory of the validations cannot be sized to it and falls back to a constant. "+
			"On a host with less to spare set MAX_VALIDATION_MEMORY, or a memory limit",
			"physical_memory", env.PhysicalMemory, "max_validation_memory", budget)
		return budget
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

// applyMemoryLimit sets the soft memory limit of the Go runtime (GOMEMLIMIT)
// from the container's memory limit, unless GOMEMLIMIT is set, which is
// kept. Near the soft limit the collector works harder, so the process
// stays within the container; far below it the collector runs as usual,
// when the heap has doubled. Without a memory limit there is nothing to
// stay within, and no soft limit.
func applyMemoryLimit(log *slog.Logger, env spoolEnv) {
	if v, ok := os.LookupEnv("GOMEMLIMIT"); ok && v != "" {
		log.Info("GOMEMLIMIT is set and kept", "gomemlimit", v, "memory_limit", env.MemoryLimit)
		return // the runtime applied it
	}
	if env.MemoryLimit <= 0 {
		return
	}
	soft := max(env.MemoryLimit-gomemlimitHeadroom, minGoMemLimit)
	debug.SetMemoryLimit(soft)
	log.Info("soft memory limit of the Go runtime derived from the memory limit",
		"memory_limit", env.MemoryLimit, "headroom", gomemlimitHeadroom, "gomemlimit", soft)
}
