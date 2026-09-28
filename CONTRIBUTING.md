# Contributing

This document is for people who build, change or maintain qti3-validator.
How to use the service is in the [README](README.md).

The service checks QTI 3 documents in three steps: well-formedness, XSD
validity against the official 1EdTech schemas, and the ISO Schematron rules
that 1EdTech embeds in those schemas. It is written in Go, without CGo or
libxml2, and ships as a `scratch` image of about 12 MB.

## Project layout

| Path | Contents |
| --- | --- |
| `cmd/server` | The HTTP service; `-check` compiles the schemas and rules and exits |
| `cmd/fetchschemas` | Build step: downloads the pinned schemas, extracts and compiles the Schematron rules, including `rules/` |
| `rules/qti3-additional-checks.sch` | The validator's own Schematron rules; see [docs/additional-checks.md](docs/additional-checks.md) |
| `internal/server` | HTTP handlers, configuration from environment variables, limits |
| `internal/validator` | Document and package validation: root detection, XSD phase, Schematron phase, result model |
| `internal/validator/schemas` | `schemas.lock`; the downloaded schemas and compiled rules land here (git-ignored) |
| `internal/schematron` | Schematron extraction, build-time compilation and the runtime engine |
| `internal/xpath` | XPath 1.0 over a small DOM with line numbers |
| `internal/xmlenc` | UTF-16 to UTF-8 for schema files |
| `third_party/xsd` | Patched copy of `github.com/jacoelho/xsd`, see [Implementation notes](#implementation-notes) |
| `testdata` | Fixtures; `testdata/schematron` and `testdata/additional-checks` hold documents with reference-implementation output |

## How it works

```
build time (Dockerfile, cmd/fetchschemas)        runtime (cmd/server)
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
[`internal/validator/schemas/schemas.lock`](internal/validator/schemas/schemas.lock).
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

To add a root, extend `DocumentTypes` in `internal/validator/document.go`.

## QTI versions

Each supported QTI version has its own entry schemas and Schematron rules,
compiled side by side at startup. `Versions` in
`internal/validator/schemas.go` lists them; the last entry is the default for
documents that do not declare a version. How the service picks a version per
request is described in the README.

To add a version, for example 3.0.2:

1. Add its files to `schemas.lock`: the ASI and content-package entry schemas
   and every file they import that is not pinned yet. Find them by following
   the `xs:import` locations; files shared with older versions stay listed
   once.
2. Run `go run ./cmd/fetchschemas`. It extracts and compiles the new rules
   with the others.
3. Add the version to `Versions`.
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

The XPath engine (`internal/xpath`) exists because neither pure-Go XPath
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
  - 2000 documents generated from the public QTI 3.0 XSD by
    `testdata/schematron/generate.py`, which fire 68 different QTI rules.

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
go run ./cmd/server
```

Go 1.27 or newer is required.

### Docker

```sh
docker build -t qti-validator .
docker run --rm -p 8080:8080 qti-validator
```

## Tests and benchmarks

```sh
go run ./cmd/fetchschemas
go vet ./...
go test -race ./...
go test -run '^$' -bench . -benchmem ./internal/validator
```

The tests cover:

- valid and invalid items: missing element, missing attribute, bad value,
  cardinality, several errors at once;
- Schematron: an unknown attribute, `max-choices` below `min-choices`, XSD
  and Schematron errors together, and the reference-checked regression set;
- the Schematron engine itself: every supported feature, first-match
  semantics, rejection of unsupported constructs, and agreement between the
  native naming checks and XPath;
- XPath 1.0 conformance against libxml2;
- malformed XML, wrong namespace, unknown root, DTDs and XXE, non-UTF-8,
  and size and depth limits;
- packages: valid, invalid XML, missing manifest, path traversal, ZIP bombs
  with honest and lying headers, too many files and the uncompressed budget;
- the validators directory: `.sch` rules on QTI and custom documents, custom
  roots, embedded rules, sibling and purl imports, escaping imports and
  symbolic links, collisions, compile errors, a custom document in a package,
  and unchanged results for the fixtures with and without it;
- a missing schema dependency failing at startup;
- that every schema loaded is pinned in the lock file;
- the HTTP status codes.

None of them need the internet once the schemas are fetched.

Benchmark on the machine below, `go test -bench`. A validation includes XSD
and Schematron:

| Benchmark | Time/op | Allocated/op | Allocs/op |
| --- | ---: | ---: | ---: |
| Startup: compile XSDs, load rules | 362 ms | 491 MB (transient) | 794k |
| Validate `testdata/valid/assessment-item.xml` (1.3 KB) | 79 µs | 43 KB | 633 |
| Validate a 3-document package | 176 µs | 97 KB | 1360 |
| 1 concurrent validation | 87 µs | 43 KB | 635 |
| 10 concurrent validations | 504 µs | 845 KB | 6739 |
| 50 concurrent validations | 1.9 ms | 4.2 MB | 33690 |

## Performance and memory

**Current numbers, QTI 3.0 and 3.0.1 both loaded:** 40 MiB RSS when idle, a
startup peak of 100 MiB while both schema sets compile, and a peak of 109 MiB
under the load below. For comparison, the current validator uses more than
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
16 GB. Image `qti-validator:dev` from this Dockerfile (13.3 MB), default
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
| Startup (peak while the XSDs compile) | 100 MiB | |
| Idle after startup | 40 MiB | |
| 1000 sequential item validations | 57 MiB | 7.5 s |
| 2000 items, 10 concurrent | 60 MiB | 3.2 s |
| 2000 items, 50 concurrent | 60 MiB | 3.2 s |
| 500 of the largest items, 50 concurrent | 94 MiB | 1.4 s |
| 100 packages, 10 concurrent | 88 MiB | 1.3 s |
| 200 packages, 50 concurrent | 75 MiB | 2.8 s |
| 5 s idle after load | 59 MiB | |

Peak RSS over the whole run was 109 MiB. With 50 concurrent clients, 8
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

- **Package semantics:** nothing checks that manifest `href`s point to files in
  the package, or that resources reference each other correctly.
- **SSML:** the upstream SSML 1.1 core profile uses `xs:redefine`, which the XSD
  library does not support. A small wrapper in `internal/validator/schemas.go`
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
- **Report format:** `internal/validator/report.go` turns the internal
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
- **Validators directory:** `internal/validator/mounted.go` reads
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
