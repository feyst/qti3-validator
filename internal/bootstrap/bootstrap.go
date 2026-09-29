// Package bootstrap wires the adapters to the application layer: it is the
// composition root, shared by the service and the tests.
package bootstrap

import (
	"sync"

	"github.com/kennisnet/qti3-validator/internal/adapter/rules"
	"github.com/kennisnet/qti3-validator/internal/adapter/schemastore"
	"github.com/kennisnet/qti3-validator/internal/adapter/validatorsdir"
	"github.com/kennisnet/qti3-validator/internal/adapter/xsdschema"
	"github.com/kennisnet/qti3-validator/internal/adapter/ziparchive"
	"github.com/kennisnet/qti3-validator/internal/app"
	"github.com/kennisnet/qti3-validator/internal/domain/qti"
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

	cfg := app.Config{Versions: map[string]app.Profile{}, Limits: opts.Limits, OpenArchive: ziparchive.Open}
	for _, v := range qti.Versions() {
		schema, err := xsdschema.CompileVersion(resolver, v)
		if err != nil {
			return nil, err
		}
		builtin, err := rules.ForVersion(compiled, v)
		if err != nil {
			return nil, err
		}
		cfg.Versions[v.Name] = app.Profile{Schema: schema, Rules: rules.Chain(builtin, mounted.Rules)}
	}
	for _, t := range mounted.Types {
		cfg.CustomTypes = append(cfg.CustomTypes, app.CustomType{
			Type:    t.DocumentType,
			Profile: app.Profile{Schema: t.Schema, Rules: rules.Chain(t.Rules, mounted.Rules)},
		})
	}
	return app.New(cfg)
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
