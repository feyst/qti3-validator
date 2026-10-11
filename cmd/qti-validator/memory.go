package main

import (
	"log/slog"

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
	// startupMemoryLimit is the soft limit while the schemas are compiled.
	// Measured on two CPUs, the process then peaks at 88 MiB instead of
	// 124 MiB, and is ready some 40 ms later (0.72 s instead of 0.67 s). A
	// lower limit costs more time than it saves memory: 70 MiB gives a peak
	// of 78 MiB for another 50 ms, and below 64 MiB the two QTI versions no
	// longer compile at the same time (bootstrap), which takes 1.0 s.
	// CONTRIBUTING.md has the measurements.
	startupMemoryLimit = 80 << 20
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

// limitStartupMemory sets the soft memory limit of the Go runtime
// (GOMEMLIMIT) for the compilation of the schemas, and returns the function
// that sets the one the service runs with, to call once the validator is
// ready. set is debug.SetMemoryLimit, and gomemlimit the value of
// GOMEMLIMIT in the environment.
//
// The service runs with GOMEMLIMIT if that is set, and else with the
// container's memory limit less gomemlimitHeadroom. Near the soft limit the
// collector works harder, so the process stays within the container; far
// below it the collector runs as usual, when the heap has doubled. Without
// a memory limit there is nothing to stay within, and no soft limit.
//
// The compilation gets startupMemoryLimit, or the limit of the service if
// that is lower. It makes nearly a gigabyte of garbage around a live heap
// that ends at some 30 MiB: with the limit of the service, which is far
// away, the collector lets that garbage pile up to the highest memory use
// of the whole process.
func limitStartupMemory(log *slog.Logger, env spoolEnv, gomemlimit string, set func(int64) int64) (ready func()) {
	running := set(-1) // GOMEMLIMIT as the runtime read it, or no limit
	explicit := gomemlimit != ""
	derived := !explicit && env.MemoryLimit > 0
	if derived {
		running = max(env.MemoryLimit-gomemlimitHeadroom, minGoMemLimit)
	}
	startup := min(running, startupMemoryLimit)
	set(startup)
	return func() {
		set(running)
		switch {
		case explicit:
			log.Info("GOMEMLIMIT is set and kept", "gomemlimit", gomemlimit, "memory_limit", env.MemoryLimit,
				"while_compiling", startup)
		case derived:
			log.Info("soft memory limit of the Go runtime derived from the memory limit",
				"memory_limit", env.MemoryLimit, "headroom", gomemlimitHeadroom, "gomemlimit", running,
				"while_compiling", startup)
		}
	}
}
