// Command server runs the QTI 3 validation service.
package main

import (
	"context"
	"errors"
	"flag"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/kennisnet/qti3-validator/internal/server"
	"github.com/kennisnet/qti3-validator/internal/validator"
)

func main() {
	check := flag.Bool("check", false, "compile the schemas and exit; used in the image build")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if *check {
		cfg, err := server.ConfigFromEnv()
		if err == nil {
			_, err = validator.New(validatorsOptions(log, cfg))
		}
		if err != nil {
			log.Error("schema check failed", "error", err)
			os.Exit(1)
		}
		log.Info("schemas compile", "qti_versions", validator.SupportedVersions())
		return
	}
	if err := run(log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

// validatorsOptions selects the validators directory and logs what is
// loaded from it. The default directory may be missing, for example when
// the binary runs outside the image; then there are no extra validators.
func validatorsOptions(log *slog.Logger, cfg server.Config) validator.Options {
	dir := cfg.ValidatorsDir
	if _, err := os.Stat(dir); !cfg.ValidatorsDirSet && errors.Is(err, fs.ErrNotExist) {
		dir = ""
	}
	return validator.Options{
		ValidatorsDir: dir,
		OnValidatorLoaded: func(file string, roots []string) {
			attrs := []any{"dir", cfg.ValidatorsDir, "file", file}
			if len(roots) > 0 {
				attrs = append(attrs, "roots", roots)
			}
			log.Info("loaded validator", attrs...)
		},
		OnValidatorIgnored: func(file, reason string) {
			log.Info("ignored validators directory entry", "dir", cfg.ValidatorsDir, "file", file, "reason", reason)
		},
	}
}

func run(log *slog.Logger) error {
	cfg, err := server.ConfigFromEnv()
	if err != nil {
		return err
	}

	start := time.Now()
	opts := validatorsOptions(log, cfg)
	opts.Limits = validator.Limits{
		MaxDocumentSize: cfg.MaxRequestSize,
		MaxErrors:       cfg.MaxErrors,
		MaxDepth:        cfg.MaxDepth,
	}
	opts.OnSchemaLoaded = func(url string) { log.Info("loaded schema", "url", url) }
	v, err := validator.New(opts)
	if err != nil {
		return err
	}
	// The schema sources are garbage once compiled; return them to the OS
	// so the idle footprint reflects only the compiled engine.
	debug.FreeOSMemory()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	log.Info("validator ready", "qti_versions", validator.SupportedVersions(),
		"compile_ms", time.Since(start).Milliseconds(), "heap_mib", mem.HeapAlloc>>20)

	srv := server.New(cfg, v, log).HTTPServer()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.RequestTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
