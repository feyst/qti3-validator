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
	// UnresolvedIDRefs are the indexes in Findings of IDREFs whose ID is not
	// in the document. In a fragment that is no error: the ID may be in the
	// document that includes it.
	UnresolvedIDRefs []int
}

// SchemaElements are the names of the elements a schema declares in its
// target namespace.
type SchemaElements struct {
	Namespace string
	// Global are the elements declared at the top level: a document, or a
	// fragment, with such a root can be validated as it is.
	Global map[string]bool
	// Local are the elements declared inside a type or a group, which are
	// valid only where that type allows them.
	Local map[string]bool
	// ManyTyped are the elements declared with more than one type: which
	// one such an element has depends on the element it stands in, as a
	// qti-rubric-block of an item is another type than one of a section.
	ManyTyped map[string]bool
}

// Declares reports whether the schema declares an element of that name,
// anywhere.
func (e SchemaElements) Declares(local string) bool { return e.Global[local] || e.Local[local] }

// RuleChecker runs Schematron rules on a well-formed document and returns
// the findings within limits. When it stops before the end, because the
// rules take more work than it allows for one document or ctx is done, it
// returns the findings so far with an error wrapping ErrTooComplex or
// ctx.Err(). When a rule fails, it returns the findings so far with any
// other error.
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
	// errors after it, and add one warning with qti.CodeWarningsOmitted that
	// says how many warnings they left out. Zero means no limit.
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
	// Elements are the elements of the QTI schema, for checking the
	// fragments of a package. Without them the content of a fragment whose
	// root is not a global element is not checked, which a warning says.
	Elements *SchemaElements
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

// Memory is the room in memory one validation holds, out of what the
// service allows all validations together. A validation reserves what it is
// about to take before it takes it: little while it reads a document and
// checks it against the schema, and what the document needs in memory for
// the rules once that is known. The validations in progress then fit in
// memory whatever the documents are, and a document that cannot fit is
// refused instead of taking the service down.
//
// A Memory belongs to one validation and is not safe for concurrent use.
// Without one (see WithMemory) nothing is reserved.
type Memory interface {
	// Reserve sets what the validation reserves to n bytes in all, more or
	// less than before. Reserving more may wait until others have released
	// theirs. It returns ErrMemoryExceeded when n is more than Limit, which
	// no waiting helps; ErrMemoryBusy when no room came free in time; or
	// ctx.Err(). After an error the validation stops; it may still reserve
	// less.
	Reserve(ctx context.Context, n int64) error
	// Limit is the most one validation may reserve, in bytes.
	Limit() int64
}

// Errors of Memory.Reserve.
var (
	ErrMemoryExceeded = errors.New("more memory than the service allows a validation")
	ErrMemoryBusy     = errors.New("no memory came free in time")
)

// MemoryEstimate is what checking a document takes in memory.
type MemoryEstimate struct {
	Bytes int64
	// Nodes is the number of nodes of the document: its elements,
	// attributes, pieces of text, comments and processing instructions.
	// They, not the bytes of the document, decide what it takes.
	Nodes int
}

// MemoryEstimator is a RuleChecker that can tell what CheckRules will take
// in memory for a document, before it runs. The estimate must not be less
// than what it takes, and should not be much more.
type MemoryEstimator interface {
	EstimateMemory(data []byte) MemoryEstimate
}

type memoryKey struct{}

// WithMemory returns a context in which a validation reserves its memory
// from m.
func WithMemory(ctx context.Context, m Memory) context.Context {
	return context.WithValue(ctx, memoryKey{}, m)
}

// MemoryFrom returns the Memory of the validation in ctx, or nil.
func MemoryFrom(ctx context.Context) Memory {
	m, _ := ctx.Value(memoryKey{}).(Memory)
	return m
}
