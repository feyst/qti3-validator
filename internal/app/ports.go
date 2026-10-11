package app

import (
	"context"
	"errors"
	"io"

	"qti3-validator/internal/domain/qti"
)

// SchemaChecker validates a document against an XML Schema.
type SchemaChecker interface {
	CheckSchema(data []byte, limits qti.Limits) SchemaVerdict
}

// SchemaVerdict is the result of a schema check.
type SchemaVerdict struct {
	Findings []qti.Finding
	Outcome  qti.Outcome // OutcomeValid when Findings is empty
	// Conclusive reports that the whole document was assessed, so rules can
	// run on it too.
	Conclusive bool
	// RootUndeclared reports that the schema declares no global element for
	// the document's root, so its content was not assessed against it.
	RootUndeclared bool
}

// RuleChecker runs Schematron rules on a well-formed document and returns
// the findings within limits. When it stops before the end, because the
// rules take more work than it allows for one document or ctx is done, it
// returns the findings so far with an error wrapping ErrTooComplex or
// ctx.Err().
type RuleChecker interface {
	CheckRules(ctx context.Context, data []byte, limits RuleLimits) ([]RuleFinding, error)
}

// RuleLimits bound the findings of a RuleChecker. Errors and warnings are
// bounded apart, so warnings never take the room of an error.
type RuleLimits struct {
	// Errors is the number of errors after which the rules stop; zero means
	// no limit.
	Errors int
	// Warnings is the number of warnings kept; the rules go on looking for
	// errors after it. Zero means no limit.
	Warnings int
}

// ErrTooComplex reports that checking a document takes more work than a
// checker allows for one document.
var ErrTooComplex = errors.New("the document is too complex to check")

// RuleFinding is a finding of a rule. A warning leaves the document valid.
type RuleFinding struct {
	qti.Finding
	Warning bool
}

// Profile is what a document is checked against: its schema, and the rules
// that run after it. Rules may be nil.
type Profile struct {
	Schema SchemaChecker
	Rules  RuleChecker
}

// CustomType is a document type a mounted validator adds. It has no QTI
// version.
type CustomType struct {
	Type qti.DocumentType
	Profile
}

// ReferenceReader reads what a well-formed document refers to: other files
// of its package, and for tests the variables of their items.
type ReferenceReader interface {
	ReadReferences(data []byte) (qti.DocumentReferences, error)
}

// Archive is a package's container, a ZIP file.
type Archive interface {
	Entries() []ArchiveEntry
}

// ArchiveEntry is one entry of an Archive.
type ArchiveEntry interface {
	Name() string
	IsDir() bool
	// DeclaredSize is the uncompressed size the archive states, which may be
	// a lie; reading enforces the limits.
	DeclaredSize() uint64
	Open() (io.ReadCloser, error)
}

// ArchiveOpener reads an archive's directory. An archive with more than
// maxEntries entries is refused with ErrTooManyEntries, before its directory
// is read into memory.
type ArchiveOpener func(r io.ReaderAt, size int64, maxEntries int) (Archive, error)

// ErrTooManyEntries is returned by an ArchiveOpener for an archive with more
// entries than allowed.
var ErrTooManyEntries = errors.New("archive has too many entries")
