package qti

// Limits bound the work per document.
type Limits struct {
	MaxDocumentSize int64 // bytes per XML document
	MaxErrors       int   // findings reported per document
	MaxDepth        int   // nesting depth of XML elements
}

// DefaultLimits are used for zero fields.
var DefaultLimits = Limits{
	MaxDocumentSize: 10 << 20,
	MaxErrors:       100,
	MaxDepth:        256,
}

// WithDefaults returns l with zero or negative fields set to DefaultLimits.
func (l Limits) WithDefaults() Limits {
	if l.MaxDocumentSize <= 0 {
		l.MaxDocumentSize = DefaultLimits.MaxDocumentSize
	}
	if l.MaxErrors <= 0 {
		l.MaxErrors = DefaultLimits.MaxErrors
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultLimits.MaxDepth
	}
	return l
}

// PackageLimits bound the work per QTI package.
type PackageLimits struct {
	MaxFiles            int   // entries in the ZIP, directories included
	MaxFileSize         int64 // uncompressed bytes per XML entry
	MaxUncompressedSize int64 // uncompressed bytes over all XML entries
}

// DefaultPackageLimits are used for zero fields.
var DefaultPackageLimits = PackageLimits{
	MaxFiles:            1000,
	MaxFileSize:         10 << 20,
	MaxUncompressedSize: 256 << 20,
}

// WithDefaults returns l with zero or negative fields set to
// DefaultPackageLimits.
func (l PackageLimits) WithDefaults() PackageLimits {
	if l.MaxFiles <= 0 {
		l.MaxFiles = DefaultPackageLimits.MaxFiles
	}
	if l.MaxFileSize <= 0 {
		l.MaxFileSize = DefaultPackageLimits.MaxFileSize
	}
	if l.MaxUncompressedSize <= 0 {
		l.MaxUncompressedSize = DefaultPackageLimits.MaxUncompressedSize
	}
	return l
}
