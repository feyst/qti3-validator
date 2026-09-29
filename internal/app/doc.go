// Package app is the application layer: it runs the validation use cases and
// owns the ports the adapters implement.
//
// The service has no state to change, so in CQRS terms every use case is a
// query: ValidateDocument and ValidatePackage take a query value and return
// a domain result. Package report projects those results onto the report
// that clients read.
//
// The checks themselves sit behind ports: SchemaChecker (XML Schema),
// RuleChecker (Schematron) and Archive (the package format). The adapters in
// internal/adapter implement them; internal/bootstrap wires them together.
package app
