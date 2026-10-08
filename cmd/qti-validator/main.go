// Command qti-validator runs the QTI 3 validation service.
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

	"qti3-validator/internal/adapter/httpapi"
	"qti3-validator/internal/bootstrap"
	"qti3-validator/internal/config"
	"qti3-validator/internal/domain/qti"
)

func main() {
	check := flag.Bool("check", false, "compile the schemas and exit; used in the image build")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if *check {
		if err := runCheck(log); err != nil {
			log.Error("schema check failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := run(log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

// runCheck compiles the schemas, rules and validators directory and exits.
func runCheck(log *slog.Logger) error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	if _, err := bootstrap.NewValidator(validatorOptions(log, cfg)); err != nil {
		return err
	}
	log.Info("schemas compile", "qti_versions", qti.SupportedVersions())
	return nil
}

// validatorOptions selects the limits and the validators directory, and logs
// what is loaded. The default directory may be missing, for example when the
// binary runs outside the image; then there are no extra validators.
func validatorOptions(log *slog.Logger, cfg config.Config) bootstrap.Options {
	dir := cfg.ValidatorsDir
	if _, err := os.Stat(dir); !cfg.ValidatorsDirSet && errors.Is(err, fs.ErrNotExist) {
		dir = ""
	}
	return bootstrap.Options{
		Limits: qti.Limits{
			MaxDocumentSize: cfg.MaxRequestSize,
			MaxErrors:       cfg.MaxErrors,
			MaxDepth:        cfg.MaxDepth,
		},
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

func httpOptions(cfg config.Config) httpapi.Options {
	return httpapi.Options{
		Addr:           cfg.Addr,
		MaxPackageSize: cfg.MaxPackageSize,
		PackageLimits: qti.PackageLimits{
			MaxFiles:            cfg.MaxFiles,
			MaxFileSize:         cfg.MaxFileSize,
			MaxUncompressedSize: cfg.MaxUncompressedSize,
		},
		MaxConcurrent:  cfg.MaxConcurrent,
		RequestTimeout: cfg.RequestTimeout,
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}

	start := time.Now()
	opts := validatorOptions(log, cfg)
	opts.OnSchemaLoaded = func(url string) { log.Info("loaded schema", "url", url) }
	v, err := bootstrap.NewValidator(opts)
	if err != nil {
		return err
	}
	// The schema sources are garbage once compiled; return them to the OS
	// so the idle footprint reflects only the compiled engine.
	debug.FreeOSMemory()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	log.Info("validator ready", "qti_versions", qti.SupportedVersions(),
		"compile_ms", time.Since(start).Milliseconds(), "heap_mib", mem.HeapAlloc>>20)

	srv := httpapi.New(httpOptions(cfg), v, log).HTTPServer()
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
