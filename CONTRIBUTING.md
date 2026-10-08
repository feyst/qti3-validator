# Contributing

This document is for people who build, change or maintain qti3-validator.
How to use the service is in the [README](README.md).

The service checks QTI 3 documents in three steps: well-formedness, XSD
validity against the official 1EdTech schemas, and the ISO Schematron rules
that 1EdTech embeds in those schemas. It is written in Go, without CGo or
libxml2, and ships as a `scratch` image of about 13 MB.

## Project layout

The code is layered: a pure domain model, an application layer with the use
cases and their ports, and adapters that implement those ports. Why, and the
rules that keep it that way, are in [ARCHITECTURE.md](ARCHITECTURE.md).

| Path | Contents |
| --- | --- |
| `cmd/qti-validator` | The service; `-check` compiles the schemas and rules and exits |
| `cmd/fetchschemas` | Build step: downloads the pinned schemas, compiles the Schematron rules, including `rules/` |
| `rules/qti3-additional-checks.sch` | The validator's own Schematron rules; see [docs/additional-checks.md](docs/additional-checks.md) |
| `internal/domain/qti` | Domain model: documents, QTI versions, findings, outcomes, limits, version choice |
| `internal/app` | Use cases (validate a document, validate a package) and the ports they need |
| `internal/app/report` | The report clients receive, projected from domain results |
| `internal/adapter/httpapi` | Inbound: the HTTP API |
| `internal/adapter/xsdschema` | Outbound: XML Schema validation with `jacoelho/xsd` |
| `internal/adapter/rules` | Outbound: Schematron rules with `internal/lib/schematron` |
| `internal/adapter/schemastore` | Outbound: the embedded schemas and compiled rules; `schemas/schemas.lock` |
| `internal/adapter/ziparchive` | Outbound: content packages with `archive/zip` |
| `internal/adapter/validatorsdir` | Outbound: the mounted validators directory |
| `internal/bootstrap` | Composition root: wires adapters to the application |
| `internal/config` | Settings from environment variables |
| `internal/lib/xpath` | XPath 1.0 over a small DOM with line numbers |
| `internal/lib/schematron` | Schematron extraction, build-time compilation and the runtime engine |
| `internal/lib/xmldoc` | Root detection and well-formedness checks |
| `internal/lib/xmlenc` | UTF-16 to UTF-8 for schema files |
| `third_party/xsd` | Patched copy of `github.com/jacoelho/xsd`, see [Implementation notes](#implementation-notes) |
| `testdata` | Fixtures; see [testdata/README.md](testdata/README.md) |

## How it works

```
build time (Dockerfile, cmd/fetchschemas)        runtime (cmd/qti-validator)
─────────────────────────────────────────        ─────────────────────────────
download the 17 XSDs pinned in schemas.lock      compile the XSDs        (0.35 s)
verify each SHA-256                              load compiled rules     (ms)
extract the Schematron rules from the XSDs       per document:
compile the rules → schematron.json.gz (49 KB)     1. stream through XSD validation
embed XSDs (gzip) and compiled rules               2. DOM + Schematron rules
qti-validator -check: everything compiles
```

The XSD validator is [`github.com/jacoelho/xsd`](https://github.com/jacoelho/xsd),
patched (see [Implementation notes](#implementation-notes)). The XPath 1.0 engine and
the Schematron engine are part of this repository.

## Schemas

The schemas are not in the repository. `cmd/fetchschemas` downloads them from
`purl.imsglobal.org` at build time and checks each file against
[`internal/adapter/schemastore/schemas/schemas.lock`](internal/adapter/schemastore/schemas/schemas.lock).
The validator embeds them with `go:embed` and never downloads anything at
runtime.

- **Version:** QTI 3.0, XSD binding v1p0. The version is fixed by the URLs in
  the lock file.
- **Integrity:** every file is pinned by SHA-256. A changed upstream file fails
  the build.
- **Entry schemas:** `imsqti_asiv3p0_v1p0.xsd` (items, tests, sections, stimuli)
  and `imsqtiv3p0_imscpv1p2_v1p0.xsd` (package manifest). Their
  import/include closure is 17 files: MathML 3, SSML 1.1, XInclude, xlink,
  `xml.xsd`, LOM, QTI metadata, CP extensions and AfA DRD.
- **Imports:** the XSDs import each other through absolute `https://` URLs. The
  resolver maps those URLs to the embedded files.
- **Stored unmodified:** the files are stored gzip-compressed, which keeps the
  binary and its resident memory small, and are otherwise unchanged.
- **XSDs compiled at startup:** about 0.35 s. The XSD library cannot serialize
  a compiled schema.
- **Schematron compiled at build time:** the rules are extracted and compiled
  during the build; the image ships only the compiled result, 49 KB.
- **Build-time check:** the image build runs `qti-validator -check`, so a
  schema or rule that does not compile fails the build, not the deployment.

Accepted root elements. Both the namespace and the local name must match:

| Root | Namespace | `schema` in the result |
| --- | --- | --- |
| `qti-assessment-item` | `http://www.imsglobal.org/xsd/imsqtiasi_v3p0` | `qti-assessment-item` |
| `qti-assessment-test` | same | `qti-assessment-test` |
| `qti-assessment-section` | same | `qti-assessment-section` |
| `qti-assessment-stimulus` | same | `qti-assessment-stimulus` |
| `qti-response-processing` | same | `qti-response-processing` |
| `manifest` | `http://www.imsglobal.org/xsd/qti/qtiv3p0/imscp_v1p1` | `imscp-manifest` |
| `lom` | `http://ltsc.ieee.org/xsd/LOM` | `lom` |
| `qtiMetadata` | `http://www.imsglobal.org/xsd/imsqti_metadata_v3p0` | `qti-metadata` |

To add a root, extend `documentTypes` in `internal/domain/qti/document.go`.

## QTI versions

Each supported QTI version has its own entry schemas and Schematron rules,
compiled side by side at startup. `versions` in
`internal/domain/qti/version.go` lists them; the last entry is the default for
documents that do not declare a version. How the service picks a version per
request is described in the README.

To add a version, for example 3.0.2:

1. Add its files to `schemas.lock`: the ASI and content-package entry schemas
   and every file they import that is not pinned yet. Find them by following
   the `xs:import` locations; files shared with older versions stay listed
   once.
2. Run `go run ./cmd/fetchschemas`. It extracts and compiles the new rules
   with the others.
3. Add the version to `versions`.
4. Compare the fixtures under the old and the new version. Every difference
   must be explained by the release notes: 3.0 to 3.0.1 only added to the ASI
   schema and changed the manifest's `schemaversion` and LTI resource types.
5. Measure memory (see [Performance and memory](#performance-and-memory)):
   every version adds about 9 MiB of heap and raises the startup peak.

## Schematron

The Schematron support is generic, not tied to the current QTI rules:

- **Rules are found automatically:** every `sch:` element in every pinned
  schema is extracted. A new or changed rule needs no code change.
- **New schemas work the same way:** add a schema to `schemas.lock` and its
  embedded rules are picked up. Rules for documents in another namespace simply
  match nothing.
- **Unsupported constructs fail the build:** nothing is skipped silently.

Supported is ISO Schematron with the default query binding (`xslt`, i.e.
XSLT 1.0 patterns and XPath 1.0):

- **Elements:** `ns`, `let` (schema, pattern and rule level), `pattern`,
  abstract patterns with `is-a` and `param`, `rule`, abstract rules with
  `extends`, `assert`, `report`, `diagnostics`, `value-of`, `name`, and the
  inline elements `emph`, `dir` and `span`.
- **XPath 1.0:** the full language and function library, plus the XSLT
  functions `current()` and `generate-id()`.
- **Semantics:** within a pattern, only the first rule whose context matches a
  node applies, as Schematron prescribes. Rule contexts are XSLT patterns, so
  a relative context such as `lom:lom/lom:general` matches anywhere, including
  LOM embedded in a manifest.
- **Severity:** a `role` of `warning` or `info` puts the message under
  `warnings`; such a message does not make the document invalid. The QTI rules
  use no roles, so all their messages are errors.

Not supported, and failing the build:

- other query bindings, such as `xslt2` or `xpath2`;
- `sch:include`;
- `key()`, `document()` and `format-number()`;
- `let` with element content.

Phases are not selected: all patterns always run.

The XPath engine (`internal/lib/xpath`) exists because neither pure-Go XPath
library fits:

- **`antchfx/xpath`** has no variables (needed for `let`), no `current()`, and
  its compiled expressions keep state, so they are not safe for concurrent use.
- **`goxpath`** returns `{uri}local` from `name()` instead of the prefixed
  name, which breaks every QTI attribute-naming rule.

How the engine is checked:

- **XPath conformance:** 180 expressions are compared with libxml2. Five cases
  where libxml2 deviates from the XPath 1.0 Recommendation are recorded, with
  the section that prescribes our result.
- **ISO reference implementation:** the Schematron engine is compared with
  lxml's `isoschematron`, which runs the ISO XSLT skeleton on libxslt. The
  comparison uses two public sets:
  - the [public test corpora](#public-test-corpora): 822 documents as
    published, plus 3752 variants mutated to break XSD, 1EdTech and own rules
    (an unknown attribute, `max-choices` below `min-choices`, an undeclared
    response, duplicate choices and more); 4528 of them are well-formed;
  - 2000 random documents generated once from the element and attribute
    names in the public QTI 3.0 XSD, which fire 68 different QTI rules.

  For 1EdTech's rules (QTI 3.0.0 and LOM), all 6528 documents give the same
  messages as the reference (63,760 in total).
- **Regression test:** 100 of the generated documents, with the reference
  output, are kept in `testdata/schematron` as a test.
- **Native attribute-naming checks:** 6822 of the QTI assertions are generated
  tests of the form `string-length(name(@*[N]))=0 or string(name(@*[N]))='a'
  or … or starts-with(name(@*[N]), 'data-')`. The build compiles them to
  shared name lists instead of XPath. A test checks that both evaluations agree
  on every one of them.
- **Own rules:** `rules/qti3-additional-checks.sch` is compiled with the
  embedded rules. On the 4528 well-formed documents of the public corpora the
  engine gives the same 2339 messages as the reference implementation. The
  test documents in `testdata/additional-checks` make every check fire, and
  their reference output is kept as a test. How to change a check is in
  [docs/additional-checks.md](docs/additional-checks.md#changing-a-check).

## Building and running from source

### Locally

```sh
go run ./cmd/fetchschemas   # once, or after changing schemas.lock
go run ./cmd/qti-validator
```

Go 1.27 or newer is required.

### Docker

```sh
make image   # tags feyst/qti3-validator:dev and :latest
docker run --rm -p 8080:8080 feyst/qti3-validator
```

CI builds the image for linux/amd64 and linux/arm64 after the checks pass, and
pushes it to `ghcr.io/feyst/qti3-validator`: `latest` and `main` from main,
`1.2.3` and `1.2` from a tag `v1.2.3`, `pr-<number>` from a pull request, and
`sha-<commit>` from each. With the repository secrets `DOCKERHUB_USERNAME` and
`DOCKERHUB_TOKEN` set, the same tags except those of pull requests also go to
`feyst/qti3-validator` on Docker Hub. The Dockerfile cross-compiles, so the
arm64 image needs no emulation:

```sh
docker buildx build --platform linux/amd64,linux/arm64 .
```

### Releases

Releases are automatic. When a build of main finds that anything that goes
into the image changed since the last tag `vX.Y.Z` (code other than tests,
`go.mod`, `go.sum`, the Dockerfile, the rules), it releases the next patch
version: the image gets the tags `X.Y.Z` and `X.Y`, the binary reports that
version, and the commit gets the tag and a GitHub release with notes.

Dependabot checks Go modules and the golang image daily and the actions
weekly. Its minor and patch updates are merged by themselves once CI passes,
and a nightly run of CI releases them, so the image keeps up with security
fixes without anyone doing anything. Major updates wait for review.

For a minor or major release, start the workflow by hand: Actions → CI → Run
workflow, and pick the bump.

## Tests, linters and benchmarks

`make` is the entry point; `make help` lists the targets.

```sh
make schemas   # once, or after changing schemas.lock or rules/
make tools     # installs golangci-lint and govulncheck
make check     # linters, tests with the race detector, vulnerability check
make bench
```

`make check` is what CI runs (`.github/workflows/ci.yml`). It covers:

- **Linters:** `golangci-lint` with the configuration in `.golangci.yml`,
  including `gosec`, `errorlint`, `gocritic` and `revive`, and `depguard`
  rules that enforce the layering of [ARCHITECTURE.md](ARCHITECTURE.md). An
  import that crosses a layer the wrong way fails the build.
- **Formatting:** `make fmt` (gofumpt, goimports). `third_party/` is left as
  it is upstream.
- **Vulnerabilities:** `govulncheck` on the dependencies.

The tests are layered like the code:

- **Domain:** version choice, safe entry names, outcomes; no I/O.
- **Use cases with fakes:** the application layer runs against fake schema,
  rule and archive adapters, so its decisions are tested without compiling a
  schema: when rules run, how they share `MAX_ERRORS`, version notices, and
  every package failure, including ZIP bombs with honest and lying headers.
- **Report:** the projection onto the report, from hand-made results.
- **Integration:** the use cases with the real adapters, via
  `internal/bootstrap`: valid and invalid items, Schematron and XSD findings
  together, malformed XML, wrong namespace, DTDs and XXE, non-UTF-8, limits,
  packages, QTI versions, and the validators directory (custom roots,
  embedded rules, imports, symbolic links, collisions, compile errors);
- **Adapters:** the rules against the ISO reference implementation and the
  additional checks against their reference output; the schema store and its
  pinning; the ZIP reader; the HTTP status codes and every request shape of `/api/validate`.
- **Features:** `internal/feature` sends realistic packages and items through
  the HTTP API and compares each report with a golden file in
  `testdata/features/golden`: the test package in `testdata/features`, a
  variant of it with one problem each, the packages in `examples/`, and four
  packages from php-qti3. After a deliberate change, run
  `go test ./internal/feature -update` and review the diff of the golden files.
- **Corpus (optional):** `make corpus` fetches the public 1EdTech QTI examples
  at a pinned commit and validates all 419 files, comparing outcome and codes
  per file with `testdata/features/golden/corpus.json`. Without the corpus
  this test is skipped, so CI does not need the network for it.
- **Libraries:** the Schematron engine (every supported feature, first-match
  semantics, unsupported constructs, native naming checks) and XPath 1.0
  conformance against libxml2.

None of them need the internet once the schemas are fetched.

Benchmarks on the machine below, `make bench`. A validation includes XSD,
1EdTech's Schematron rules and the additional checks; both QTI versions are
loaded:

| Benchmark | Time/op | Allocated/op | Allocs/op |
| --- | ---: | ---: | ---: |
| Startup: compile XSDs, load rules | 0.80 s | 983 MB (transient) | 1.6M |
| Validate `testdata/valid/assessment-item.xml` (1.3 KB) | 170–178 µs | 87 KB | 2020 |
| Validate a 3-document package, references included | 416–423 µs | 190 KB | 3940 |
| 1 concurrent validation | 175–182 µs | 87 KB | 2022 |
| 10 concurrent validations | 0.88–0.90 ms | 1.2 MB | 20.6k |
| 50 concurrent validations | 3.7–3.9 ms | 6.4 MB | 103k |

## Performance and memory

**Current numbers, QTI 3.0 and 3.0.1 both loaded:** 40 MiB RSS when idle, a
startup peak of about 100 MiB while both schema sets compile, and a peak of
about 100 MiB under the load below. The image sets `GOMEMLIMIT=90MiB`; without
it the peak under the same load varied between 110 and 132 MiB over six runs. For comparison, the current validator uses more than
2 GB.

**Validators directory** (local binary, not the image; startup peak as
`VmHWM`, idle as `VmRSS` 1 s after `/health` answers, 8 runs each):

| `VALIDATORS_DIR` | Startup peak | Idle |
| --- | ---: | ---: |
| unset | 99–101 MiB | 40–43 MiB |
| one `.sch`, two small `.xsd` (one importing the other and LOM) | 100–103 MiB | 40–43 MiB |
| one `.xsd` importing the QTI 3.0.1 ASI schema | 110–113 MiB | 46–51 MiB |

`qti-validator -check` peaks at the same values (`/usr/bin/time -f %M`).

Low memory use is the main requirement of this service; it replaces a
validator that needs more than 2 GB. The budget is **128 MiB RSS** with the
default configuration, including peaks under concurrent load. A change that
affects parsing, validation or the rules must be measured as below before it
is merged, and the numbers here updated.

Environment:
Docker 29.8.2 on WSL2 (kernel 6.18.33.2), Intel Core Ultra 7 268V, 8 CPUs,
16 GB. Image `feyst/qti3-validator:dev` from this Dockerfile (13.5 MB), default
configuration (`MAX_CONCURRENT` = 8). RSS is `VmRSS`/`VmHWM` from
`/proc/<pid>/status` of the container process. Load was generated with
`curl` through `xargs -P`. Every validation includes XSD, 1EdTech's
Schematron rules and the additional checks.

Input, all public:
- `testdata/valid/assessment-item.xml` (1.3 KB);
- `qtiv3-examples/packaging/items/Example04-feedbackBlock-templateBlock.xml`
  from the 1EdTech examples (32 KB), the largest QTI 3 item there;
- `fixtures/valid-package.zip` from php-qti3 (1 MB, 74 entries: test, items,
  metadata, media).

See [Public test corpora](#public-test-corpora) for the exact versions.
Every request in the run below returned 200 (5800 requests).

| After | RSS | Time |
| --- | ---: | ---: |
| Startup (peak while the XSDs compile) | 96 MiB | |
| Idle after startup | 43 MiB | |
| 1000 sequential item validations | 59 MiB | 5.1 s |
| 2000 items, 10 concurrent | 61 MiB | 2.7 s |
| 2000 items, 50 concurrent | 61 MiB | 2.8 s |
| 500 of the largest items, 50 concurrent | 88 MiB | 1.2 s |
| 100 packages, 10 concurrent | 73 MiB | 1.4 s |
| 200 packages, 50 concurrent | 80 MiB | 2.6 s |
| 5 s idle after load | 58 MiB | |

Peak RSS over the whole run was 100 MiB (99–102 MiB over five runs). With 50 concurrent clients, 8
validations run at once and the rest wait for a slot.

Corpus check: the same build validated the 391 QTI 3 XML files and 33
packages of the public test corpora. Of the 391 files, 348 are valid and 19
valid with warnings. 10 have errors: 1EdTech Schematron findings, and two
mistakes in the examples that the additional checks report (`slider-1.xml`
uses `map_response` without a mapping; `adaptive.xml` sets
`completion_status` instead of the built-in `completionStatus`). The other 14
are rejected as a whole: 12 are document types the service does not validate
(results reports, usage data, LTI links, fragments), one has a DOCTYPE and one
is not well-formed. libxml2
(`xmllint --schema`) gives the same valid/invalid XSD verdict on every file
in `testdata`.

## Public test corpora

Comparisons, measurements and checks against real content use only public
material. Besides the files in `testdata` (see
[testdata/README.md](testdata/README.md)), these corpora are used. They are
downloaded for a run and not copied into this repository.

| Corpus | Source | Version | License | Contents used |
| --- | --- | --- | --- | --- |
| 1EdTech QTI examples | [github.com/1EdTech/qti-examples](https://github.com/1EdTech/qti-examples) | commit [`0a92fbb`](https://github.com/1EdTech/qti-examples/tree/0a92fbbb6d2e620a1f7fad19977be4c418246bc0) | none stated | `qtiv3-examples/` and `QTI3_*`: 391 XML files, 28 packages |
| php-qti3 fixtures | [github.com/kennisnet/php-qti3](https://github.com/kennisnet/php-qti3) | commit [`0ba4f78`](https://github.com/kennisnet/php-qti3/tree/0ba4f7839fd4a0657a1350fc6fb6168688ca9710/fixtures) | MIT | `fixtures/`: 5 packages |

The XML documents inside the packages count as documents of the corpus too.

## Implementation notes

- **Package references:** checked after all files of a package were read; each
  document is read a second time for its references, which costs about 12% of
  a package's validation time. They could be read from the DOM the Schematron
  rules already build, if that time ever matters.
- **SSML:** the upstream SSML 1.1 core profile uses `xs:redefine`, which the XSD
  library does not support. A small wrapper in `internal/adapter/schemastore/overrides.go`
  includes the same upstream `synthesis-nonamespace.xsd` without the redefine.
  As a result, two SSML restrictions are not enforced: `version` and
  `xml:lang` required on `<ssml:speak>`, and `name` required on `<ssml:mark>`.
- **UTF-8 only:** documents must be UTF-8 (or ASCII). A UTF-16 document gets
  `unsupported_encoding`.
- **Patched XSD library:** the library,
  [`github.com/jacoelho/xsd`](https://github.com/jacoelho/xsd) v0.6.3, rejects
  the QTI ASI schema with `cyclic complex type BasePromptInteractionDType`. That
  is a false positive: the "cycle" runs through element declarations
  (interaction → prompt → object → drawing interaction), not through type
  derivation. `third_party/xsd` holds the library's non-test sources with a
  fix, in [`third_party/xsd-element-type-recursion.patch`](third_party/xsd-element-type-recursion.patch).
  The same change is proposed upstream as
  [jacoelho/xsd#134](https://github.com/jacoelho/xsd/pull/134). It passes the
  library's full verification set, W3C corpus included, and adds regression
  tests. Once it is released, drop the `replace` directive in `go.mod` and the
  directory.
- **XSDs compiled at startup:** the XSD library cannot serialize a compiled
  schema, so the binary carries the compressed XSDs, about 1.5 MB, and
  compiles them at startup. The Schematron rules are compiled at build time.
- **id():** without a DTD no attribute has type ID, so XPath `id()` selects
  nothing. The QTI rules do not use it.
- **Report format:** `internal/app/report/report.go` turns the internal
  results into the report both endpoints return. Its shape follows the public
  report model of 1EdTech's validator engine; the comments on the types name
  the public sources each part comes from (the API description at
  https://vc.1ed.tech/v3/api-docs and the reports it returns, the open-source
  digital-credentials-public-validator, the public inspector-core and
  inspector-util libraries)
  and mark our own additions (`code`, `source`, `location.path`, a VALID item
  per document). Keep it that way: anything taken from the model needs its
  public source, and nothing comes from member-only tools. Change the report
  only by adding fields.
- **Validators directory:** `internal/adapter/validatorsdir` reads
  `VALIDATORS_DIR` once at startup, before the QTI schemas compile, through an
  `os.Root`, so neither a name nor a symbolic link can leave the directory;
  a link that stays inside, as in a Kubernetes ConfigMap volume, works. Each
  `.xsd` is compiled into its own engine; its roots are the global elements
  of that file only, not of what it includes. Its includes and imports go
  through `mountedResolver`: a bare file name to the directory, a
  `https://purl.imsglobal.org/` URL to the embedded resolver, with its
  overrides and UTF-16 handling. The `.sch` files are compiled together into
  one engine at startup with `schematron.Precompile`, which runs after the
  built-in rules in `checkRules`. Findings of mounted files get `source`; the
  built-in ones never do. `VALIDATORS_DIR` defaults to `/validators`, which the
  image provides empty; with an empty directory, or a missing default directory
  outside the image, nothing of this is loaded. An explicitly set directory
  must exist.
