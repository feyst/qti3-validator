# CLAUDE.md

An HTTP service in Go that validates QTI 3 items, tests and packages against
the 1EdTech XSDs and Schematron rules. Read these before changing code:

- `ARCHITECTURE.md`: the layers and which may import which. `depguard` in
  `.golangci.yml` enforces them; an import across a layer fails `make lint`.
- `CONTRIBUTING.md`: the layout, the schemas and how they are fetched, the
  Schematron engine, tests, performance and memory.
- `docs/`: the API's report, the additional checks and custom validators.

## Commands

Go is only in the dev container (`make shell` on the host; see
CONTRIBUTING.md). `make help` lists the targets.

- `make check`: what CI runs: lint, tests with `-race`, govulncheck.
- `make test`, `make lint`, `make fmt` (gofumpt and goimports).
- `make run`: the service on :8080.
- `make image`: the Docker image; it runs on the host, as the dev container
  has no access to the Docker daemon.

The code embeds the fetched schemas; the targets fetch them when needed,
`make schemas` always.

## Conventions

- Code, comments, docs and commit messages are in English, in the full,
  plain sentences of the existing comments.
- Standard library first; the only dependency is the patched XSD library in
  `third_party/xsd`, left as it is upstream.
- `testdata/`, `examples/` and `third_party/` stay byte for byte as they are.
