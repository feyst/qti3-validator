package app

import (
	"io"

	"github.com/kennisnet/qti3-validator/internal/domain/qti"
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
}

// RuleChecker runs Schematron rules on a well-formed document and returns at
// most maxFindings findings; zero means no limit.
type RuleChecker interface {
	CheckRules(data []byte, maxFindings int) ([]RuleFinding, error)
}

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

// ArchiveOpener reads an archive's directory.
type ArchiveOpener func(r io.ReaderAt, size int64) (Archive, error)
