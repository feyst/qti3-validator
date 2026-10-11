package main

import (
	"bytes"
	"log/slog"
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
		{"256 MiB with a volume", config.Default(), spoolEnv{MemoryLimit: 256 * mib}, 400 * mib, 192 * mib, "INFO"},
		{"1 GiB with a volume", config.Default(), spoolEnv{MemoryLimit: 1024 * mib}, 400 * mib, 960 * mib, "INFO"},
		{"256 MiB with a tmpfs", config.Default(), spoolEnv{MemoryLimit: 256 * mib, TmpfsSize: 4096 * mib}, 96 * mib, 96 * mib, "INFO"},
		{"too small for anything", config.Default(), spoolEnv{MemoryLimit: 64 * mib}, 400 * mib, minValidationMemory, "INFO"},
		{"no limit", config.Default(), spoolEnv{}, 400 * mib, defaultValidationMemory, "WARN"},
		{"no limit on a large machine", config.Default(), spoolEnv{PhysicalMemory: 16384 * mib}, 400 * mib, defaultValidationMemory, "WARN"},
		{"no limit on a small machine", config.Default(), spoolEnv{PhysicalMemory: 512 * mib}, 400 * mib, 448 * mib, "WARN"},
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

func TestApplyMemoryLimit(t *testing.T) {
	const mib = 1 << 20
	before := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(before) })
	current := func() int64 { return debug.SetMemoryLimit(-1) }
	quiet := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	// Set explicitly: left alone.
	t.Setenv("GOMEMLIMIT", "90MiB")
	debug.SetMemoryLimit(90 * mib)
	applyMemoryLimit(quiet, spoolEnv{MemoryLimit: 1024 * mib})
	if got := current(); got != 90*mib {
		t.Errorf("GOMEMLIMIT set: the limit became %d MiB", got/mib)
	}
	// Empty: derived from the container's limit.
	t.Setenv("GOMEMLIMIT", "")
	applyMemoryLimit(quiet, spoolEnv{MemoryLimit: 1024 * mib})
	if got := current(); got != 1024*mib-gomemlimitHeadroom {
		t.Errorf("derived limit %d MiB", got/mib)
	}
	applyMemoryLimit(quiet, spoolEnv{MemoryLimit: 40 * mib})
	if got := current(); got != minGoMemLimit {
		t.Errorf("derived limit in a tiny container: %d MiB", got/mib)
	}
	// No container limit: nothing to derive from.
	debug.SetMemoryLimit(123 * mib)
	applyMemoryLimit(quiet, spoolEnv{})
	if got := current(); got != 123*mib {
		t.Errorf("without a container limit the limit became %d MiB", got/mib)
	}
}
