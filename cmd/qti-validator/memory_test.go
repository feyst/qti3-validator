package main

import (
	"bytes"
	"log/slog"
	"math"
	"runtime/debug"
	"strings"
	"testing"

	"qti3-validator/internal/config"
)

func TestValidationMemoryBudget(t *testing.T) {
	const mib = 1 << 20
	explicit := config.Default()
	explicit.MaxValidationMemory = 100 * mib
	tests := []struct {
		name  string
		cfg   config.Config
		env   spoolEnv
		spool int64
		want  int64
		log   string // level of the message logged
	}{
		{"256 MiB with a volume", config.Default(), spoolEnv{MemoryLimit: 256 * mib}, 400 * mib, 184 * mib, "INFO"},
		{"1 GiB with a volume", config.Default(), spoolEnv{MemoryLimit: 1024 * mib}, 400 * mib, 952 * mib, "INFO"},
		{"256 MiB with a tmpfs", config.Default(), spoolEnv{MemoryLimit: 256 * mib, TmpfsSize: 4096 * mib}, 96 * mib, 88 * mib, "INFO"},
		{"too small for anything", config.Default(), spoolEnv{MemoryLimit: 64 * mib}, 400 * mib, minValidationMemory, "INFO"},
		{"no limit", config.Default(), spoolEnv{}, 400 * mib, defaultValidationMemory, "WARN"},
		{"no limit on a large machine", config.Default(), spoolEnv{PhysicalMemory: 16384 * mib}, 400 * mib, defaultValidationMemory, "WARN"},
		{"no limit on a small machine", config.Default(), spoolEnv{PhysicalMemory: 512 * mib}, 400 * mib, 440 * mib, "WARN"},
		{"set", explicit, spoolEnv{MemoryLimit: 256 * mib}, 400 * mib, 100 * mib, ""},
		{"set, no limit", explicit, spoolEnv{}, 400 * mib, 100 * mib, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			if got := validationMemoryBudget(log, tt.cfg, tt.env, tt.spool); got != tt.want {
				t.Errorf("budget %d MiB, want %d MiB", got/mib, tt.want/mib)
			}
			out := buf.String()
			if tt.log == "" && out != "" || tt.log != "" && !strings.Contains(out, "level="+tt.log) {
				t.Errorf("log %q, want level %q", out, tt.log)
			}
			if tt.log != "" && !strings.Contains(out, "max_validation_memory=") {
				t.Errorf("log %q names no numbers", out)
			}
		})
	}
}

// The soft limit is low while the schemas compile, and the one of the
// service once the validator is ready: GOMEMLIMIT when that is set.
func TestLimitStartupMemory(t *testing.T) {
	const mib = 1 << 20
	const none = math.MaxInt64 // what the runtime reports without a limit
	tests := []struct {
		name       string
		gomemlimit string // in the environment
		applied    int64  // by the runtime, from the environment
		env        spoolEnv
		startup    int64
		running    int64
		log        string
	}{
		{"256 MiB container", "", none, spoolEnv{MemoryLimit: 256 * mib}, startupMemoryLimit, 224 * mib, "derived"},
		{"1 GiB container", "", none, spoolEnv{MemoryLimit: 1024 * mib}, startupMemoryLimit, 992 * mib, "derived"},
		{"container smaller than the startup limit", "", none, spoolEnv{MemoryLimit: 96 * mib}, 64 * mib, 64 * mib, "derived"},
		{"container just above it", "", none, spoolEnv{MemoryLimit: 128 * mib}, startupMemoryLimit, 96 * mib, "derived"},
		{"tiny container", "", none, spoolEnv{MemoryLimit: 40 * mib}, minGoMemLimit, minGoMemLimit, "derived"},
		{"no container limit", "", none, spoolEnv{}, startupMemoryLimit, none, ""},
		{"GOMEMLIMIT above the startup limit", "512MiB", 512 * mib, spoolEnv{MemoryLimit: 256 * mib}, startupMemoryLimit, 512 * mib, "GOMEMLIMIT is set"},
		{"GOMEMLIMIT below the startup limit", "48MiB", 48 * mib, spoolEnv{MemoryLimit: 256 * mib}, 48 * mib, 48 * mib, "GOMEMLIMIT is set"},
		{"GOMEMLIMIT without a container limit", "200MiB", 200 * mib, spoolEnv{}, startupMemoryLimit, 200 * mib, "GOMEMLIMIT is set"},
		{"GOMEMLIMIT=off", "off", none, spoolEnv{MemoryLimit: 256 * mib}, startupMemoryLimit, none, "GOMEMLIMIT is set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			limit := tt.applied
			var sets []int64
			set := func(n int64) int64 {
				previous := limit
				if n >= 0 { // a negative limit only reads it, as debug.SetMemoryLimit
					limit = n
					sets = append(sets, n)
				}
				return previous
			}
			ready := limitStartupMemory(log, tt.env, tt.gomemlimit, set)
			if limit != tt.startup {
				t.Errorf("while compiling the limit is %d MiB, want %d MiB", limit/mib, tt.startup/mib)
			}
			if buf.Len() != 0 {
				t.Errorf("logged before the validator is ready: %s", buf.String())
			}
			ready()
			if limit != tt.running {
				t.Errorf("once ready the limit is %d MiB, want %d MiB", limit/mib, tt.running/mib)
			}
			if len(sets) != 2 {
				t.Errorf("the limit was set %d times, want once for each phase: %v", len(sets), sets)
			}
			out := buf.String()
			if tt.log == "" && out != "" || !strings.Contains(out, tt.log) {
				t.Errorf("log %q, want %q", out, tt.log)
			}
		})
	}
}

// With the runtime's own setter: the limit it holds is the one set.
func TestLimitStartupMemoryOfTheRuntime(t *testing.T) {
	const mib = 1 << 20
	before := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(before) })
	quiet := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	debug.SetMemoryLimit(math.MaxInt64)
	ready := limitStartupMemory(quiet, spoolEnv{MemoryLimit: 1024 * mib}, "", debug.SetMemoryLimit)
	if got := debug.SetMemoryLimit(-1); got != startupMemoryLimit {
		t.Errorf("while compiling the limit is %d MiB", got/mib)
	}
	ready()
	if got := debug.SetMemoryLimit(-1); got != 1024*mib-gomemlimitHeadroom {
		t.Errorf("once ready the limit is %d MiB", got/mib)
	}
}
