package app

import (
	"io"

	"qti3-validator/internal/domain/qti"
)

// ValidateDocument asks to validate one XML document.
type ValidateDocument struct {
	// Document is read to its end. When it is an io.Seeker too, as a file
	// or a reader of bytes in memory is, a validation that must wait for
	// memory (see Memory) lets go of what it read and reads the document
	// again from its start afterwards; one that cannot be read again
	// cannot wait.
	Document io.Reader
	// Size is the size of the document in bytes when it is known, or 0. It
	// lets the validation reserve memory before it reads the document, and
	// read it into a buffer of that size.
	Size int64
	// Version forces a QTI version; empty lets the document decide.
	Version string
}

// ValidatePackage asks to validate a QTI content package.
type ValidatePackage struct {
	Package io.ReaderAt
	Size    int64
	Limits  qti.PackageLimits // zero fields select qti.DefaultPackageLimits
	// Version forces a QTI version for every document; empty lets the
	// manifest decide.
	Version string
}
