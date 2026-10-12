package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"qti3-validator/internal/adapter/httpapi"
	"qti3-validator/internal/config"
)

// The runtime gets one thread more than there are CPUs, and what is sized
// to the CPUs keeps their number: one validation per CPU, the queue and the
// memory of the small documents that follow from that. A GOMAXPROCS in the
// environment is left alone.
func TestAddThread(t *testing.T) {
	const mib = 1 << 20
	for _, tt := range []struct {
		name       string
		env        string
		runtime    int   // what the runtime chose, or took from the environment
		wantSet    []int // the calls that change the setting
		wantCPUs   int
		wantThread string // gomaxprocs in the log
	}{
		{"two CPUs", "", 2, []int{3}, 2, "gomaxprocs=3"},
		{"one CPU", "", 1, []int{2}, 1, "gomaxprocs=2"},
		{"eight CPUs", "", 8, []int{9}, 8, "gomaxprocs=9"},
		{"GOMAXPROCS set", "2", 2, nil, 2, "gomaxprocs=2"},
		{"GOMAXPROCS set to one", "1", 1, nil, 1, "gomaxprocs=1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MAX_CONCURRENT", "")
			t.Setenv("MAX_QUEUE", "")
			var buf bytes.Buffer
			var calls []int
			current := tt.runtime
			set := func(n int) int {
				previous := current
				if n > 0 {
					calls = append(calls, n)
					current = n
				}
				return previous
			}
			cpus := addThread(slog.New(slog.NewTextHandler(&buf, nil)), tt.env, set)
			if cpus != tt.wantCPUs || !slices.Equal(calls, tt.wantSet) {
				t.Fatalf("got %d CPUs and GOMAXPROCS set to %v, want %d and %v", cpus, calls, tt.wantCPUs, tt.wantSet)
			}
			if out := buf.String(); !strings.Contains(out, "cpus="+strconv.Itoa(tt.wantCPUs)) || !strings.Contains(out, tt.wantThread) {
				t.Errorf("log %q, want the CPUs and %s", out, tt.wantThread)
			}
			// The defaults follow the CPUs, not the threads.
			cfg, err := config.FromEnvFor(cpus)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MaxConcurrent != tt.wantCPUs || cfg.MaxQueue != 4*tt.wantCPUs {
				t.Errorf("MAX_CONCURRENT %d and MAX_QUEUE %d with %d CPUs", cfg.MaxConcurrent, cfg.MaxQueue, tt.wantCPUs)
			}
			hopts := httpOptions(cfg)
			if want := min(int64(tt.wantCPUs)*4*mib, 184*mib/8); httpapi.SmallMemoryShare(184*mib, hopts.MaxConcurrent) != want {
				t.Errorf("memory for small documents %d, want %d", httpapi.SmallMemoryShare(184*mib, hopts.MaxConcurrent), want)
			}
		})
	}
}

// The startup log names the room for the bodies in memory, which comes on
// top of the budget of the validations.
func TestSizeToContainerLogsBodyMemory(t *testing.T) {
	const mib = 1 << 20
	var buf bytes.Buffer
	cfg := config.DefaultFor(2)
	hopts := httpOptions(cfg)
	sizeToContainer(slog.New(slog.NewTextHandler(&buf, nil)), cfg, spoolEnv{MemoryLimit: 256 * mib}, &hopts)
	if hopts.MaxValidationMemory != 184*mib {
		t.Fatalf("budget %d MiB", hopts.MaxValidationMemory/mib)
	}
	for _, want := range []string{"max_validation_memory=192937984", "for_small_documents=8388608", "most_for_one_document=184549376", "bodies_in_memory=6029312"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log %q, want %s", buf.String(), want)
		}
	}
}

// logged returns the JSON log lines in buf.
func logged(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for line := range strings.Lines(buf.String()) {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		lines = append(lines, entry)
	}
	return lines
}

// How the service ends is in the log: the signal and that the requests in
// progress finished, or that they did not within the grace period, or the
// error that stopped the server.
func TestServeLogsHowItEnds(t *testing.T) {
	listening := func(t *testing.T, addr string) {
		t.Helper()
		for range 500 {
			if conn, err := net.Dial("tcp", addr); err == nil {
				conn.Close()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("the server does not listen")
	}
	freeAddr := func(t *testing.T) string {
		t.Helper()
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		return l.Addr().String()
	}

	t.Run("signal", func(t *testing.T) {
		var buf bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&buf, nil))
		srv := &http.Server{Addr: freeAddr(t), Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
		signals := make(chan os.Signal, 1)
		done := make(chan error, 1)
		go func() { done <- serve(log, srv, signals, 5*time.Second) }()
		listening(t, srv.Addr)
		signals <- syscall.SIGTERM
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		lines := logged(t, &buf)
		if len(lines) != 3 || lines[1]["msg"] != "shutting down" || lines[1]["signal"] != "terminated" || lines[1]["grace_ms"] != float64(5000) ||
			lines[2]["msg"] != "shutdown complete" || lines[2]["level"] != "INFO" {
			t.Fatalf("log %s", buf.String())
		}
	})

	t.Run("requests outlast the grace period", func(t *testing.T) {
		var buf bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&buf, nil))
		started, release := make(chan struct{}), make(chan struct{})
		defer close(release)
		srv := &http.Server{Addr: freeAddr(t), ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			close(started)
			<-release
		})}
		signals := make(chan os.Signal, 1)
		done := make(chan error, 1)
		go func() { done <- serve(log, srv, signals, 50*time.Millisecond) }()
		listening(t, srv.Addr)
		go func() {
			if resp, err := http.Get("http://" + srv.Addr + "/"); err == nil {
				resp.Body.Close()
			}
		}()
		<-started
		signals <- os.Interrupt
		err := <-done
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "did not complete within 50ms") {
			t.Fatalf("got %v, want an error that says the shutdown did not complete in time", err)
		}
		if lines := logged(t, &buf); len(lines) != 2 || lines[1]["signal"] != "interrupt" {
			t.Fatalf("log %s", buf.String())
		}
	})

	t.Run("the server stops by itself", func(t *testing.T) {
		taken, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer taken.Close()
		var buf bytes.Buffer
		srv := &http.Server{Addr: taken.Addr().String(), Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
		err = serve(slog.New(slog.NewJSONHandler(&buf, nil)), srv, make(chan os.Signal), time.Second)
		if err == nil || !strings.Contains(err.Error(), "the HTTP server stopped unexpectedly") || !strings.Contains(err.Error(), "address already in use") {
			t.Fatalf("got %v, want the error of the server, as an unexpected stop", err)
		}
	})
}
