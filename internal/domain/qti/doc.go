// Package qti is the domain model of QTI 3 validation: the documents and
// packages that are validated, the QTI versions they are validated against,
// and the findings and outcomes a validation produces.
//
// The package is pure: it does no I/O and knows nothing of XML Schema,
// Schematron or HTTP. The application layer (internal/app) drives the
// validation; adapters (internal/adapter) implement the checks.
package qti
