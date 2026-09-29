package app

import (
	"io"

	"github.com/kennisnet/qti3-validator/internal/domain/qti"
)

// ValidateDocument asks to validate one XML document.
type ValidateDocument struct {
	Document io.Reader
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
