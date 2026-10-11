package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"qti3-validator/internal/config"
)

func TestSpoolBudget(t *testing.T) {
	const mib = 1 << 20
	cfg := config.Default() // MAX_SPOOL_SIZE 400 MiB, MAX_REQUEST_SIZE 10 MiB
	tests := []struct {
		name string
		env  spoolEnv
		want int64
		log  string // level of the message logged, if any
	}{
		{"disk", spoolEnv{MemoryLimit: 256 * mib}, 400 * mib, ""},
		{"tmpfs without a memory limit", spoolEnv{TmpfsSize: 4096 * mib}, 400 * mib, "WARN"},
		{"small tmpfs", spoolEnv{TmpfsSize: 300 * mib}, 300 * mib, "WARN"},
		{"tmpfs in 256 MiB", spoolEnv{TmpfsSize: 4096 * mib, MemoryLimit: 256 * mib}, 96 * mib, "WARN"},
		{"tmpfs smaller than the memory", spoolEnv{TmpfsSize: 64 * mib, MemoryLimit: 1024 * mib}, 64 * mib, "WARN"},
		{"tmpfs in 1 GiB", spoolEnv{TmpfsSize: 4096 * mib, MemoryLimit: 1024 * mib}, 400 * mib, ""},
		{"room for no document", spoolEnv{TmpfsSize: 4096 * mib, MemoryLimit: 165 * mib}, 5 * mib, "ERROR"},
		{"no room at all", spoolEnv{TmpfsSize: 4096 * mib, MemoryLimit: 128 * mib}, 1, "ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			if got := spoolBudget(log, cfg, tt.env); got != tt.want {
				t.Errorf("budget %d MiB, want %d MiB", got/mib, tt.want/mib)
			}
			out := buf.String()
			if tt.log == "" && out != "" || tt.log != "" && !strings.Contains(out, "level="+tt.log) {
				t.Errorf("log %q, want level %q", out, tt.log)
			}
			if tt.log != "" && !strings.Contains(out, "max_spool_size=") {
				t.Errorf("log %q names no numbers", out)
			}
		})
	}
}
