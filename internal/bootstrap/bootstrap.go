// Package bootstrap wires the adapters to the application layer: it is the
// composition root, shared by the service and the tests.
package bootstrap

import (
	"runtime"
	"runtime/debug"
	"sync"

	"qti3-validator/internal/adapter/qtirefs"
	"qti3-validator/internal/adapter/rules"
	"qti3-validator/internal/adapter/schemastore"
	"qti3-validator/internal/adapter/validatorsdir"
	"qti3-validator/internal/adapter/xsdschema"
	"qti3-validator/internal/adapter/ziparchive"
	"qti3-validator/internal/app"
	"qti3-validator/internal/domain/qti"
)

// Options configure NewValidator.
type Options struct {
	// Store holds the schemas and rules; the zero value selects the embedded
	// ones.
	Store *schemastore.Store
	// Limits apply to every document; zero fields select qti.DefaultLimits.
	Limits qti.Limits
	// ValidatorsDir is a directory with extra validators, .xsd and .sch
	// files; empty adds none.
	ValidatorsDir string
	// VerifyElements also checks that the element names compiled in are
	// those of the QTI schemas: that they carry the SHA-256 of the embedded
	// schema, and that reading the names from the schemas, as the build
	// did, gives the same. It takes about half as long again as the
	// compilation, and the checksums alone 60 ms: for the check of the
	// image build, not for the start of the service.
	VerifyElements bool
	// CPUs is the number of CPUs to compile the schemas with; 0 selects
	// the GOMAXPROCS of the runtime. The service runs with a thread more
	// than it has CPUs, and passes the CPUs.
	CPUs int
	// OnSchemaLoaded is called once for every schema file that is loaded.
	OnSchemaLoaded func(url string)
	// OnValidatorLoaded is called for every file loaded from ValidatorsDir,
	// with the roots of the document types an XSD adds.
	OnValidatorLoaded func(file string, roots []string)
	// OnValidatorIgnored is called for every entry of ValidatorsDir that is
	// not used.
	OnValidatorIgnored func(file, reason string)
}

// NewValidator compiles the schemas and rules and returns the validator. It
// fails if any schema, or any include or import it needs, is missing or
// invalid, so problems surface at startup.
func NewValidator(opts Options) (*app.Validator, error) {
	store := schemastore.Embedded()
	if opts.Store != nil {
		store = *opts.Store
	}
	resolver := xsdschema.Resolver{Store: store, Loaded: once(opts.OnSchemaLoaded)}
	compiled, err := store.Rules()
	if err != nil {
		return nil, err
	}
	// The element names of the QTI schemas were read at build time: reading
	// them here would parse each schema a second time.
	declared, err := store.Elements()
	if err != nil {
		return nil, err
	}

	// The validators directory goes first: its mistakes are the likelier
	// ones, and they fail before the long compilation of the QTI schemas.
	var mounted validatorsdir.Validators
	if opts.ValidatorsDir != "" {
		loaded, err := validatorsdir.Load(validatorsdir.Options{
			Dir: opts.ValidatorsDir, Embedded: resolver,
			Loaded: opts.OnValidatorLoaded, Ignored: opts.OnValidatorIgnored,
		})
		if err != nil {
			return nil, err
		}
		mounted = *loaded
	}

	cfg := app.Config{
		Versions:    map[string]app.Profile{},
		Limits:      opts.Limits,
		OpenArchive: ziparchive.Open,
		References:  qtirefs.Reader{},
	}
	versions := qti.Versions()
	cpus := opts.CPUs
	if cpus <= 0 {
		cpus = runtime.GOMAXPROCS(0)
	}
	schemas, err := compileVersions(resolver, versions, roomToCompileTogether(cpus, debug.SetMemoryLimit(-1)))
	if err != nil {
		return nil, err
	}
	for i, v := range versions {
		schema := schemas[i]
		builtin, err := rules.ForVersion(compiled, v)
		if err != nil {
			return nil, err
		}
		elements, err := xsdschema.CompiledElements(declared, v.ASI)
		if err != nil {
			return nil, err
		}
		if opts.VerifyElements {
			if err := xsdschema.VerifyElements(resolver, declared, v.ASI); err != nil {
				return nil, err
			}
		}
		cfg.Versions[v.Name] = app.Profile{Schema: schema, Rules: rules.Chain(builtin, mounted.Rules), Elements: &elements}
	}
	for _, t := range mounted.Types {
		cfg.CustomTypes = append(cfg.CustomTypes, app.CustomType{
			Type:    t.DocumentType,
			Profile: app.Profile{Schema: t.Schema, Rules: rules.Chain(t.Rules, mounted.Rules)},
		})
	}
	return app.New(cfg)
}

// minMemoryToCompileTogether is the soft memory limit of the Go runtime
// below which the QTI versions are compiled one after the other. Two
// compilations at once hold twice the working memory: measured with two
// CPUs, they still finish sooner than one after the other with a limit of
// 60 MiB (0.88 s against 1.03 s), and later with 50 MiB (1.26 s against
// 1.12 s) and less, where the collector runs all the time to keep both
// within the limit, and does not manage: the process then takes more
// memory than with one compilation at a time.
const minMemoryToCompileTogether = 64 << 20

// roomToCompileTogether reports whether compiling the QTI versions at the
// same time is the faster way: with a CPU for each and memory for both. On
// one CPU they would only take turns, and hold the memory of both.
func roomToCompileTogether(cpus int, softMemoryLimit int64) bool {
	return cpus > 1 && softMemoryLimit >= minMemoryToCompileTogether
}

// compileVersions compiles the schemas of the QTI versions, at the same
// time or one after the other, and returns them in the order of versions.
// The compilation is nearly all of what starting the service takes, and the
// versions share nothing but the read-only schema store. The error is that
// of the first version that fails, either way.
func compileVersions(resolver xsdschema.Resolver, versions []qti.Version, together bool) ([]*xsdschema.Checker, error) {
	schemas := make([]*xsdschema.Checker, len(versions))
	errs := make([]error, len(versions))
	var wg sync.WaitGroup
	for i, v := range versions {
		compile := func() { schemas[i], errs[i] = xsdschema.CompileVersion(resolver, v) }
		if together {
			wg.Go(compile)
			continue
		}
		compile()
		if errs[i] != nil {
			return nil, errs[i]
		}
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return schemas, nil
}

// once wraps f so it is called at most once per URL, from any goroutine.
func once(f func(url string)) func(url string) {
	if f == nil {
		return nil
	}
	var mu sync.Mutex
	seen := map[string]bool{}
	return func(url string) {
		mu.Lock()
		defer mu.Unlock()
		if !seen[url] {
			seen[url] = true
			f(url)
		}
	}
}
