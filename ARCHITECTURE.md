# Architecture

qti3-validator is small, but it talks to four heavy things: an XML Schema
library, a Schematron engine, ZIP files and HTTP. The architecture keeps the
decisions about QTI apart from those four, so the decisions can be read and
tested on their own, and a library can be replaced without touching them.

It follows the ports-and-adapters (hexagonal) style, with a small
domain-driven core. Patterns are applied where they pay for themselves; the
end of this document says where they are deliberately not.

## Layers

```
                 ┌──────────────────────────── adapters ────────────────────────────┐
  HTTP request → │ httpapi ─┐                                                       │
                 │          ▼                                                       │
                 │   ┌─────────────── app ────────────────┐                         │
                 │   │ ValidateDocument  ValidatePackage   │  ports:                │
                 │   │        │                            │  SchemaChecker ◄── xsdschema ──► jacoelho/xsd
                 │   │        ▼                            │  RuleChecker   ◄── rules ──────► lib/schematron
                 │   │  ┌── domain/qti ──┐                 │  Archive       ◄── ziparchive ─► archive/zip
                 │   │  │ documents,     │   report        │                         │
                 │   │  │ versions,      │ (read model)    │                         │
                 │   │  │ findings       │                 │                         │
                 │   │  └────────────────┘                 │                         │
                 │   └─────────────────────────────────────┘                         │
                 │      schemastore, validatorsdir: what xsdschema and rules load    │
                 └───────────────────────────────────────────────────────────────────┘
                        bootstrap wires it all together; cmd/qti-validator runs it
```

| Layer | Packages | Depends on |
| --- | --- | --- |
| Domain | `internal/domain/qti` | the standard library only |
| Application | `internal/app`, `internal/app/report` | the domain, `internal/lib/xmldoc` |
| Adapters | `internal/adapter/*` | the application and the domain; libraries |
| Libraries | `internal/lib/*` | each other and the standard library; nothing about QTI |
| Composition | `internal/bootstrap`, `cmd/*`, `internal/config` | everything |

Dependencies point inwards. The linter enforces it: `depguard` in
`.golangci.yml` fails `make lint` when the domain imports anything but the
standard library, when the application imports an adapter, the XSD library
or the Schematron engine, when a library imports QTI code, or when the HTTP
adapter imports another adapter.

## Domain: `internal/domain/qti`

The language of the problem: a **document** of a **document type**, validated
against a **QTI version**, producing **findings** with a **code** and an
**outcome**; a **package** with a **manifest** and **entries**. All of it
values, no I/O.

The one real domain rule is version choice (`ChooseVersion`): a forced
version wins, then the version a manifest declares, then the package's, then
the latest; a manifest whose declared version is overridden or unsupported
gets a notice instead of a schema error. It lives here because it is a QTI
rule, not a technical one, and it is unit-tested without a schema.

## Application: `internal/app`

Two use cases on `Validator`: `ValidateDocument` and `ValidatePackage`.
They decide the order of the checks, when rules may run, how findings share
the `MAX_ERRORS` budget, which XML files of a package are skipped, and how
ZIP bombs are stopped. They reach the outside world only through ports, which
this package owns:

| Port | What it does | Adapter |
| --- | --- | --- |
| `SchemaChecker` | Validate a document against an XML Schema | `adapter/xsdschema` |
| `RuleChecker` | Run Schematron rules on a document | `adapter/rules` |
| `Archive`, `ArchiveOpener` | Read a package's entries | `adapter/ziparchive` |

A `Profile` pairs a schema with its rules: one per QTI version, and one per
document type a mounted XSD adds. The application does not know where they
come from.

`internal/app/report` projects the domain results onto the report clients
read, the public 1EdTech report model.

## Adapters: `internal/adapter`

- **Inbound:** `httpapi` turns HTTP requests into use-case queries and results
  into reports. It depends on a small `Validator` interface, not on the
  concrete application.
- **Outbound:** `xsdschema`, `rules` and `ziparchive` implement the ports.
  `schemastore` serves the embedded schemas and compiled rules;
  `validatorsdir` turns a mounted directory into extra profiles and rules.

## Composition: `internal/bootstrap`

`bootstrap.NewValidator` compiles every QTI version, loads the validators
directory and builds the application from adapters. `cmd/qti-validator`
reads the configuration, calls it and starts the HTTP server. The tests use
the same function, so they run the code that runs in production.

## What is deliberately not here

- **No command side, no command bus.** The service changes no state: both use
  cases are queries. In CQRS terms the "query" is a plain struct
  (`app.ValidateDocument`) passed to a plain method; the report is the read
  model. A bus or handler registry would add indirection and nothing else.
- **No aggregates, repositories or domain events.** There is nothing to
  persist and no invariant spans several objects. The domain is values and
  one rule.
- **No interface for every type.** Ports exist where there is a real second
  implementation (the fakes in the application tests) or a library to keep
  out. The libraries in `internal/lib` are concrete.
- **No `pkg/` directory.** Everything is under `internal/`, which the Go
  toolchain keeps private to this module.

## Where to make a change

| Change | Where |
| --- | --- |
| A new check that Schematron can express | `rules/qti3-additional-checks.sch`, see [docs/additional-checks.md](docs/additional-checks.md) |
| A new check that needs code, such as one across files of a package | A new port in `internal/app` and an adapter, or the package use case if it is pure logic |
| A new QTI version | `internal/domain/qti/version.go` and `schemas.lock`, see [CONTRIBUTING.md](CONTRIBUTING.md#qti-versions) |
| A new endpoint or request shape | `internal/adapter/httpapi` |
| Another XML Schema library | A new `SchemaChecker` adapter; nothing else changes |
