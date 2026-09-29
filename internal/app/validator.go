package app

import (
	"encoding/xml"
	"errors"
	"fmt"

	"github.com/kennisnet/qti3-validator/internal/domain/qti"
)

// Validator runs the validation use cases. It is immutable and safe for
// concurrent use.
type Validator struct {
	versions    map[string]Profile
	custom      map[xml.Name]Profile
	customTypes map[xml.Name]qti.DocumentType
	limits      qti.Limits
	openArchive ArchiveOpener
}

// Config is what a Validator is built from.
type Config struct {
	// Versions holds a profile for every supported QTI version, by name.
	Versions map[string]Profile
	// CustomTypes are document types added by mounted validators.
	CustomTypes []CustomType
	// Limits apply to every document; zero fields select qti.DefaultLimits.
	Limits qti.Limits
	// OpenArchive reads packages.
	OpenArchive ArchiveOpener
}

// New returns a Validator. It fails when a supported version has no
// profile, or a custom type clashes with a QTI document type or another
// custom type.
func New(cfg Config) (*Validator, error) {
	v := &Validator{
		versions:    map[string]Profile{},
		custom:      map[xml.Name]Profile{},
		customTypes: map[xml.Name]qti.DocumentType{},
		limits:      cfg.Limits.WithDefaults(),
		openArchive: cfg.OpenArchive,
	}
	for _, name := range qti.SupportedVersions() {
		p, ok := cfg.Versions[name]
		if !ok || p.Schema == nil {
			return nil, fmt.Errorf("no schema for QTI %s", name)
		}
		v.versions[name] = p
	}
	for _, ct := range cfg.CustomTypes {
		name := ct.Type.Name()
		if _, ok := qti.LookupDocumentType(name); ok {
			return nil, fmt.Errorf("custom document type {%s}%s is a QTI document type", name.Space, name.Local)
		}
		if _, ok := v.custom[name]; ok {
			return nil, fmt.Errorf("custom document type {%s}%s is declared twice", name.Space, name.Local)
		}
		v.custom[name], v.customTypes[name] = ct.Profile, ct.Type
	}
	if v.openArchive == nil {
		return nil, errors.New("no archive opener")
	}
	return v, nil
}

// Limits returns the limits in effect.
func (v *Validator) Limits() qti.Limits { return v.limits }

// WithLimits returns a validator that applies other limits; zero fields
// select qti.DefaultLimits. The compiled checks are shared.
func (v *Validator) WithLimits(l qti.Limits) *Validator {
	c := *v
	c.limits = l.WithDefaults()
	return &c
}
