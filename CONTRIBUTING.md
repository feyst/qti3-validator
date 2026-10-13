# Contributing

This document is for people who build, change or maintain qti3-validator.
How to use the service is in the [README](README.md).

The service checks QTI 3 documents in three steps: well-formedness, XSD
validity against the official 1EdTech schemas, and the ISO Schematron rules
that 1EdTech embeds in those schemas. It is written in Go, without CGo or
libxml2, and ships as a `scratch` image of about 21 MB.

## Project layout

The code is layered: a pure domain model, an application layer with the use
cases and their ports, and adapters that implement those ports. Why, and the
rules that keep it that way, are in [ARCHITECTURE.md](ARCHITECTURE.md).

| Path | Contents |
| --- | --- |
| `cmd/qti-validator` | The service; `-check` compiles the schemas and rules, verifies the element names and exits |
| `cmd/fetchschemas` | Build step: downloads the pinned schemas, compiles the Schematron rules, including `rules/`, and reads the element names of the QTI schemas |
| `rules/qti3-additional-checks.sch` | The validator's own Schematron rules; see [docs/additional-checks.md](docs/additional-checks.md) |
| `internal/domain/qti` | Domain model: documents, QTI versions, findings, outcomes, limits, version choice |
| `internal/app` | Use cases (validate a document, validate a package) and the ports they need |
| `internal/app/report` | The report clients receive, projected from domain results |
| `internal/adapter/httpapi` | Inbound: the HTTP API |
| `internal/adapter/xsdschema` | Outbound: XML Schema validation with `jacoelho/xsd` |
| `internal/adapter/rules` | Outbound: Schematron rules with `internal/lib/schematron` |
| `internal/adapter/schemastore` | Outbound: the embedded schemas, compiled rules and element names; `schemas/schemas.lock` |
| `internal/adapter/ziparchive` | Outbound: content packages with `archive/zip` |
| `internal/adapter/validatorsdir` | Outbound: the mounted validators directory |
| `internal/bootstrap` | Composition root: wires adapters to the application |
| `internal/config` | Settings from environment variables |
| `internal/lib/xpath` | XPath 1.0 over a small DOM with line numbers |
| `internal/lib/schematron` | Schematron extraction, build-time compilation and the runtime engine |
| `internal/lib/xmldoc` | Root detection and well-formedness checks |
| `internal/lib/xmlenc` | UTF-16 to UTF-8 for schema files |
| `internal/lib/xsdelements` | The names of the elements an XSD declares, read at build time and loaded at startup |
| `third_party/xsd` | Patched copy of `github.com/jacoelho/xsd`, see [Implementation notes](#implementation-notes) |
| `testdata` | Fixtures; see [testdata/README.md](testdata/README.md) |

## How it works

```
build time (Dockerfile, cmd/fetchschemas)        runtime (cmd/qti-validator)
─────────────────────────────────────────        ─────────────────────────────
download the 22 XSDs pinned in schemas.lock      compile the XSDs        (0.7 s)
verify each SHA-256                              load compiled rules     (ms)
remove files the lock no longer lists            load element names      (ms)
extract the Schematron rules from the XSDs       per document:
compile the rules → schematron.json.gz (95 KB)     1. stream through XSD validation
read element names → elements.json.gz (2 KB)       2. DOM + Schematron rules
embed XSDs (gzip), compiled rules, element names
qti-validator -check: everything compiles, and
  the element names are those of the XSDs
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
  the build, and nothing of it is stored. A file stored by an earlier run is
  used only if it still has the pinned checksum. Only `https` URLs with a
  host name are accepted, and a redirect is followed only over `https` to
  the same host. The schema directory is read and written through an
  `os.Root`, so a link in it cannot lead the build outside it.
- **Nothing else is embedded:** the validator embeds the whole directory of
  the schema host, so `fetchschemas` removes every file there that the lock
  file does not list, such as a schema that was dropped from it.
- **Entry schemas:** `imsqti_asiv3p0_v1p0.xsd` (items, tests, sections, stimuli)
  and `imsqtiv3p0_imscpv1p2_v1p0.xsd` (package manifest). Their
  import/include closure is 17 files: MathML 3, SSML 1.1, XInclude, xlink,
  `xml.xsd`, LOM, QTI metadata, CP extensions and AfA DRD. QTI 3.0.1 adds
  its own five QTI schemas and shares the other twelve, so the lock file
  pins 22 files.
- **Imports:** the XSDs import each other through absolute `https://` URLs. The
  resolver maps those URLs to the embedded files.
- **Stored unmodified:** the files are stored gzip-compressed, which keeps the
  binary and its resident memory small, and are otherwise unchanged.
- **XSDs compiled at startup:** about 0.7 s on two CPUs for the two QTI
  versions, which compile at the same time; see
  [Performance and memory](#performance-and-memory). The XSD library cannot
  serialize a compiled schema.
- **Schematron compiled at build time:** the rules are extracted and compiled
  during the build; the image ships only the compiled result, 95 KB.
- **Element names read at build time:** to check the fragments of a package,
  the validator needs the names of the elements each QTI version's ASI schema
  declares, and which of them it declares with more than one type. Reading
  them takes a pass over the whole schema with `encoding/xml`, half as long
  again as compiling it, so `fetchschemas` does it and stores the names, with
  the checksum of the schema they were read from, in `elements.json.gz`
  (2 KB). The service only loads that file. The build and the tests read the
  names with the same function, `xsdelements.Read`;
  `TestCompiledElementsAreThoseOfTheSchemas` fails when the file is not the
  one of the pinned schemas and the QTI versions, and tells you to run
  `fetchschemas` again.
- **Build-time check:** the image build runs `qti-validator -check`, so a
  schema or rule that does not compile fails the build, not the deployment.
  The check also verifies the stored element names: it fails when their
  checksum is not the SHA-256 of the embedded schema, and when reading the
  names from the embedded schemas again gives other ones. The service does
  not do this when it starts: hashing the two schemas, 18 MB each
  uncompressed, takes 60 ms, and reading the names longer. A `fetchschemas`
  run that fails removes `elements.json.gz`, so a failed run cannot leave
  the names of the schemas before a change beside the changed schemas.

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
2. Add the version to `versions`.
3. Run `go run ./cmd/fetchschemas`. It extracts and compiles the new rules
   with the others, and reads the element names of the new ASI schema; the
   service does not start for a version without them.
4. Compare the fixtures under the old and the new version. Every difference
   must be explained by the release notes: 3.0 to 3.0.1 only added to the ASI
   schema and changed the manifest's `schemaversion` and LTI resource types.
5. Measure memory (see [Performance and memory](#performance-and-memory)):
   every version adds about 9 MiB of heap and raises the startup peak, as
   the versions compile at the same time when there is a CPU for each.

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
- **Indexed identifier counts:** the uniqueness checks of the own rules count,
  for each element, the elements with the same identifier:
  `count(SCOPE//qti:*[self::… or …][@identifier = current()/@identifier])`.
  As XPath that walks the scope again for every element, so 20,000 choices in
  one interaction took minutes. The build rewrites counts of this form into
  `count-by-attribute`, an XPath extension in a reserved namespace that
  mounted rules cannot call. It answers by binary search in one index per
  document, so nesting does not multiply the work, and charges its steps to
  the step budget; the `.sch` file stays plain XPath. Tests check that the
  rules give the same findings both ways on generated documents, and that the
  uniqueness checks are rewritten.
- **XPath shortcuts:** the engine evaluates some forms faster without
  changing their results, by static analysis at compile time: a step ending
  in `[1]` stops at its first match (the nearest ancestor, for a reverse
  axis), unless a predicate before the `[1]` could raise a type error on a
  later node, such as `$v/x` with a variable that may not be a node-set;
  `//a[@id = current()/@ref]` looks the value up in an index of
  `//a` by attribute; a path that is only asked whether it selects a node,
  as an operand of `or`, `and`, `not()` or `boolean()` or as a predicate,
  stops at the first; descendant steps by name use an index of the document,
  and skip context nodes nested in others; `//a[@x]` is one descendant step;
  `=` between a node-set or a string and a remembered path or a variable of
  a schema, a pattern or a rule uses a hash set, as long as the
  string-values a session keeps for such sets fit in 4 MiB.
  `TestOptimisedEvaluationMatchesPlain` in `internal/lib/xpath`
  compares each with plain evaluation, values and errors, and
  `TestOptimisedMatchesPlainOnGeneratedExpressions` does so for generated
  expressions and documents (`FZ_SEEDS=40 go test -run GeneratedExpressions
  ./internal/lib/xpath` for a longer run).
- **Scaling suite:** `TestValidationScalesLinearly` in `internal/bootstrap`
  runs the whole validator on generated documents and packages of size n
  and 4n: a table of generators with many of one thing each — elements,
  attributes, values of every base type, operands of every operator,
  violations and non-violations of every one of the own rules, files and
  references of a package. The rules and the type checks count their work
  in XPath steps (`rules.StepsCharged`), which do not depend on the machine
  or its load: the larger input may take five times the steps of the
  smaller. For the other stages the suite measures the CPU time of the
  process, the least of some runs at a size that stands out from the noise,
  and fails a case only when it is over seven times in each of five
  attempts. A new check, or a new kind of finding, gets a generator there.
  `SCALING='^rules/' go test -v -run ValidationScalesLinearly
  ./internal/bootstrap` runs a part of it and prints the table.
- **Own rules:** `rules/qti3-additional-checks.sch` is compiled with the
  embedded rules. On the 4528 well-formed documents of the public corpora the
  engine gives the same 2339 messages as the reference implementation. The
  test documents in `testdata/additional-checks` make every check fire, and
  their reference output is kept as a test. How to change a check is in
  [docs/additional-checks.md](docs/additional-checks.md#changing-a-check).

## Building and running from source

### Dev container

The dev container has everything the `make` targets need: the Go of the
image build, gopls, the GitHub CLI and Claude Code, so the host needs only
Docker and make. It is defined in `docker-compose.dev.yml` and
`docker/Dockerfile.dev`, and runs as a user with your uid and gid, so the
files it makes in the checkout are yours.

```sh
cp .env.example .env   # once, and adjust
make shell             # on the host: builds and starts the container, opens a shell in it
make tools             # in the container, once: golangci-lint and govulncheck
make check
```

VS Code and JetBrains open it from `.devcontainer/devcontainer.json`, which
runs `make tools schemas` when the container is made. The service is on
port 8080 of the host, or on `DEV_PORT` from `.env`, after `make run`.

The container mounts the SSH agent, `~/.gitconfig` and the Claude Code login
in `~/.claude`, so git and `claude` work as on the host. It does not mount
the host's Docker socket, which would give whatever runs in it control of the
host: `make image` and the other `docker` commands run on the host. Go's
modules, the tools and the build cache are in the volumes `go` and `cache`,
which survive a rebuild of the image;
`docker compose -f docker-compose.dev.yml down -v` removes them.

### Locally

```sh
go run ./cmd/fetchschemas   # once, or after changing schemas.lock, rules/ or the QTI versions
go run ./cmd/qti-validator
```

The code embeds the fetched schemas, so nothing compiles before the first
line ran. The `make` targets run it themselves when it is needed.

Go 1.27 or newer is required.

### Docker

```sh
make image   # on the host; tags feyst/qti3-validator:dev and :latest
docker run --rm -p 8080:8080 feyst/qti3-validator
```

An image built this way reports the version `dev-<commit>` in `/version` and
in reports, `dev-<commit>-dirty` when the work tree has uncommitted changes,
and `dev` outside a git checkout; only the images CI builds carry a release
number.

CI builds the image for linux/amd64 and linux/arm64 after the checks pass, and
pushes it to `ghcr.io/feyst/qti3-validator`: `latest` and `main` from main,
`1.2.3` and `1.2` from a tag `v1.2.3`, `pr-<number>` from a pull request, and
`sha-<commit>` from each. With the repository secrets `DOCKERHUB_USERNAME` and
`DOCKERHUB_TOKEN` set, the same tags except those of pull requests also go to
`feyst/qti3-validator` on Docker Hub, and README.md becomes the overview there
(the token then needs the Read, Write, Delete scope). The Dockerfile
cross-compiles, so the arm64 image needs no emulation:

```sh
docker buildx build --platform linux/amd64,linux/arm64 .
```

### Releases

Releases are automatic. When a build of main finds that anything that goes
into the image changed since the last tag `vX.Y.Z` (`go.mod`, `go.sum`, the
Dockerfile, the rules, `third_party` and the code in `cmd` and `internal`,
without tests, `testdata` directories and `internal/testutil`), it releases
the next patch version: the image gets the tags `X.Y.Z` and `X.Y`, the binary
reports that version, and the commit gets the tag and a GitHub release with
notes.

For a minor or major release, start the workflow by hand: Actions → CI → Run
workflow, and pick the bump.

Runs of main and of tags are never cancelled while running and run one after
the other, so two releases never pick the same version. GitHub keeps only one
waiting run per group, so a waiting run (for example a manual release) is
replaced when another one is queued behind it: start it again if that
happens.

### Dependency updates

Dependabot checks Go modules and the golang image daily and the actions
weekly. Its minor and patch updates are merged by themselves once CI passes,
and a nightly run of CI releases them, so the image keeps up with those
security fixes without anyone doing anything. Major updates wait for review.

The actions in `.github/workflows` are pinned by commit, with the version in
a comment behind it (`uses: actions/checkout@<sha> # v7.0.1`), so a moved tag
cannot change what runs. Dependabot updates the commit and the comment
together; pin a new action the same way, with the commit the tag points to:

```sh
git ls-remote https://github.com/actions/checkout.git 'refs/tags/v7.0.1*'
```

For an annotated tag that prints two lines; take the one ending in `^{}`.

Two things do not update by themselves:

- **The XSD library.** `go.mod` replaces `github.com/jacoelho/xsd` with the
  patched copy in `third_party/xsd`, which Dependabot cannot see or update.
  The workflow `.github/workflows/xsd-upstream.yml` compares the version
  `go.mod` requires with the latest upstream release every week and opens an
  issue "jacoelho/xsd vX.Y.Z is available" when a newer one is out; a later
  release updates that issue instead of opening another. Closing it skips
  that release: the workflow does not open it again, and reports the next
  release in a new issue. The workflow knows its own issues by their author,
  the workflow's token, and the label `xsd-upstream`; an issue with the same
  title that someone else opened or closed changes nothing. What to do with
  the issue is under [Updating the XSD library](#updating-the-xsd-library).
- **The schemas.** They are pinned by checksum on purpose; see
  [Schemas](#schemas).

### Updating the XSD library

The library parses every upload, so upstream's fixes should not wait long.
With `vX.Y.Z` the new release:

1. Read the changes since the current version. If the release contains the
   fix of [jacoelho/xsd#134](https://github.com/jacoelho/xsd/pull/134), the
   patch is no longer needed: remove the `replace` line from `go.mod`,
   `third_party/xsd` and the patch, run
   `go get github.com/jacoelho/xsd@vX.Y.Z`, and go to step 6.
2. Check out upstream at the release:
   `git clone --branch vX.Y.Z --depth 1 https://github.com/jacoelho/xsd.git /tmp/xsd`.
3. Apply the patch there:
   `git -C /tmp/xsd apply --3way "$PWD/third_party/xsd-element-type-recursion.patch"`.
   Resolve what does not apply, and run upstream's tests (`go test ./...` in
   `/tmp/xsd`); the patch brings its own regression tests.
4. Stage the result (`git -C /tmp/xsd add -A`), write the patch again
   (`git -C /tmp/xsd diff --cached > third_party/xsd-element-type-recursion.patch`),
   and replace `third_party/xsd` with the patched sources: `LICENSE`, `go.mod`
   and the `.go` files that are not tests, in the same directories.
5. Set the version in `go.mod` (`require github.com/jacoelho/xsd vX.Y.Z`),
   which is what the workflow compares, and under
   [Implementation notes](#implementation-notes).
6. Run `make check` and `make corpus`, and measure memory as described under
   [Performance and memory](#performance-and-memory).

## Tests, linters and benchmarks

`make` is the entry point; `make help` lists the targets.

```sh
make tools     # installs golangci-lint and govulncheck
make check     # linters, tests with the race detector, vulnerability check
make bench
```

The targets that compile the code fetch the schemas first when the compiled
rules or one of the files in `schemas.lock` is missing, so `make check` works
on a fresh clone and after removing the schema directory. They do so again
when `schemas.lock`, `rules/` or the code that compiles the rules changed,
without downloading what is already there. `make schemas` does it
regardless. The Makefile runs one recipe at a time, also under `-j`.

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
  semantics, unsupported constructs, native naming checks, indexed counts)
  and XPath 1.0 conformance against libxml2.
- **Build step:** `cmd/fetchschemas` against a local HTTPS server: the lock
  file's syntax, a changed upstream file, a stored file that was changed,
  URLs that leave the output directory, redirects, the size limit, the
  removal of files the lock file does not list, and the element names.
- **Embedded element names:** equal to what `xsdschema.DeclaredElements`
  reads from the embedded schemas, with the checksums of `schemas.lock`
  (`internal/adapter/xsdschema`); stale or missing names fail the check and
  the start (`internal/bootstrap`).

None of them need the internet once the schemas are fetched. A test that
needs the fetched schemas fails without them; it does not skip. Only the
corpus test, and the slowest test under `-short` or `-race`, skip.

Benchmarks on the machine below, `make bench`. A validation includes XSD,
1EdTech's Schematron rules and the additional checks; both QTI versions are
loaded. The startup row is from another machine (AMD Ryzen 9 3950X, Go
1.27.2, `-cpu 2`), where v1.0.0 took 1.0–1.2 s: the two versions now compile
at the same time. One after the other they take 1.0–1.1 s.

| Benchmark | Time/op | Allocated/op | Allocs/op |
| --- | ---: | ---: | ---: |
| Startup: compile XSDs, load rules | 0.67–0.76 s | 927 MB (transient) | 1.6M |
| Validate `testdata/valid/assessment-item.xml` (1.3 KB) | 170–178 µs | 87 KB | 2020 |
| Validate a 3-document package, references included | 416–423 µs | 190 KB | 3940 |
| 1 concurrent validation | 175–182 µs | 87 KB | 2022 |
| 10 concurrent validations | 0.88–0.90 ms | 1.2 MB | 20.6k |
| 50 concurrent validations | 3.7–3.9 ms | 6.4 MB | 103k |

## Performance and memory

**Current numbers, QTI 3.0 and 3.0.1 both loaded:** about 30 MiB when idle
in the container, and a startup peak of about 88 MiB while both schema sets
compile. For comparison, the validator this one replaces uses more than
2 GB.

**Soft memory limit.** The image sets no `GOMEMLIMIT`. The service derives
the soft memory limit of the Go runtime from the container's memory limit,
less 32 MiB (224 MiB in a container of 256 MiB), and an explicit `GOMEMLIMIT`
is kept. While the schemas compile it is 80 MiB, or the limit of the service
if that is lower: the compilation makes nearly a gigabyte of garbage around
a live heap of some 30 MiB, and with a limit that is far away the collector
lets it pile up to the highest memory use of the whole process. The load
table further down dates from when the image set `GOMEMLIMIT=90MiB`; run the
container with `-e GOMEMLIMIT=90MiB` to compare with it.

**Startup** (local binary, not the image, on an AMD Ryzen 9 3950X with
`GOMAXPROCS=2` and `GOMEMLIMIT=224MiB`, as in a container of 256 MiB with
two CPUs; time is `compile_ms` of the `validator ready` log line, peak is
`VmHWM` when the service listens; medians of 11 runs, alternating):

| Build | Time to ready | Startup peak |
| --- | ---: | ---: |
| v1.0.0, with the `GOMEMLIMIT=90MiB` its image set | 1.05 s | 98 MiB |
| Element names read from the XSDs at startup, one version after the other | 1.53 s | 115 MiB |
| Element names from the build, versions compiled at the same time | 0.70 s | 87 MiB |
| The same on one CPU (`GOMAXPROCS=1`), one version after the other | 1.27 s | 87 MiB |

The image built from the last one, in a container of 256 MiB with two CPUs
on the same machine, answers `/health` 1.2 s after `docker run` (v1.0.0:
1.4 s; with the element names read at startup: 1.9 s), with a `compile_ms`
of 710–753 and 31–34 MiB when idle (`docker stats`, five starts).

The soft limit while the schemas compile trades time for memory. With the
last build and two CPUs:

| Soft limit while compiling | Time to ready | Startup peak |
| --- | ---: | ---: |
| none | 0.67 s | 124 MiB |
| 90 MiB | 0.69 s | 98 MiB |
| **80 MiB** | 0.72 s | 88 MiB |
| 75 MiB | 0.72–0.75 s | 83 MiB |
| 70 MiB | 0.77 s | 78 MiB |
| 60 MiB, one version after the other | 1.03 s | 68 MiB |
| 50 MiB, one version after the other | 1.12 s | 66 MiB |

Below a soft limit of 64 MiB, and on one CPU, the versions compile one after
the other (`roomToCompileTogether` in `internal/bootstrap`): two compilations
in 50 MiB take longer than one after the other, 1.26 s, and more memory,
71 MiB.

**Validators directory** (the same binary and settings; idle as `VmRSS` when
the service listens, 8 runs each):

| `VALIDATORS_DIR` | Time to ready | Startup peak | Idle |
| --- | ---: | ---: | ---: |
| unset | 0.67–0.77 s | 86–89 MiB | 42–43 MiB |
| one `.sch`, two small `.xsd` (one importing the other and LOM) | 0.65–0.72 s | 86–88 MiB | 41–43 MiB |
| one `.xsd` importing the QTI 3.0.1 ASI schema | 1.15–1.49 s | 87–88 MiB | 47–49 MiB |

A local binary takes more when idle than the container, 42 MiB against 30:
it is not stripped, and its `VmRSS` counts the pages of the binary. The
mounted validators compile before the QTI schemas, one after the other.

`qti-validator -check` takes longer and peaks higher than a start, 1.2 s and
90 MiB (`/usr/bin/time -f '%M %e'`): it also reads the element names from
the schemas.

Low memory use is the main requirement of this service; it replaces a
validator that needs more than 2 GB. The budget is **128 MiB RSS** with the
default configuration, including peaks under concurrent load. A change that
affects parsing, validation or the rules must be measured as below before it
is merged, and the numbers here updated.

Environment:
Docker 29.8.2 on WSL2 (kernel 6.18.33.2), Intel Core Ultra 7 268V, 8 CPUs,
16 GB. Image `feyst/qti3-validator:dev` from this Dockerfile (13.5 MB then; now
20.7 MB as Docker reports it, with a binary of 14.3 MB), `GOMEMLIMIT=90MiB`, default
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
| Startup (peak while the XSDs compile; now 88 MiB, see above) | 96 MiB | |
| Idle after startup (now about 30 MiB) | 43 MiB | |
| 1000 sequential item validations | 59 MiB | 5.1 s |
| 2000 items, 10 concurrent | 61 MiB | 2.7 s |
| 2000 items, 50 concurrent | 61 MiB | 2.8 s |
| 500 of the largest items, 50 concurrent | 88 MiB | 1.2 s |
| 100 packages, 10 concurrent | 73 MiB | 1.4 s |
| 200 packages, 50 concurrent | 80 MiB | 2.6 s |
| 5 s idle after load | 58 MiB | |

Peak RSS over the whole run was 100 MiB (99–102 MiB over five runs). With 50 concurrent clients, 8
validations run at once and the rest wait for a slot.

Corpus check: the same build validated the 391 QTI 3 XML files and 28
packages of the 1EdTech examples, and the 5 packages of php-qti3. Of the 391
files, 345 are valid and 20 valid with warnings. 13 have errors: three break
the XML Schema (`data-attributes.xml` and the manifests of the two LTI
packages), three a 1EdTech Schematron rule (`alt` on `object` in the
graphic gap match items), and seven have mistakes that the additional checks
report (`slider-1.xml` uses `map_response` without a mapping; the two copies
of `adaptive.xml` set `completion_status` instead of the built-in
`completionStatus`; `feedback_adaptive.xml` and the three `fractions` items
have values or expressions of the wrong type). The other 13 are rejected as
a whole: 11 are document types the service does not validate (results
reports, usage data, LTI links, a fragment sent on its own), one has a
DOCTYPE and one is not well-formed. These figures are the outcomes of the
`.xml` entries in `testdata/features/golden/corpus.json`. libxml2
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
  directory. Until then the copy is updated by hand; see
  [Updating the XSD library](#updating-the-xsd-library).
- **XSDs compiled at startup:** the XSD library cannot serialize a compiled
  schema, so the binary carries the compressed XSDs, about 2.7 MB, and
  compiles them at startup. The Schematron rules are compiled at build time,
  and the element names of the QTI schemas are read then. The compilation is
  nearly all of the startup time, and all of it is inside the XSD library:
  building the content models takes two thirds of it, parsing the schemas a
  quarter. The QTI versions share twelve of their schema files, but each
  compilation parses them for itself, as the library compiles a schema set
  as a whole. `bootstrap.compileVersions` therefore compiles the versions at
  the same time when there is a CPU for each and the soft memory limit
  leaves room for both, and `cmd/qti-validator` keeps that limit low while
  it does (`limitStartupMemory`).
- **Fragment contexts:** which of the contexts in `internal/app/fragment.go`
  allow a root element is found by validating a probe document, on the first
  fragment with that root and remembered per QTI version (`contextCache`).
  Nothing of it runs at startup.
- **Mounted XSDs need no element names:** the names serve the fragments of a
  package, which are QTI. A document type that a mounted XSD adds has no
  fragments, so nothing is read from those files beyond their global
  elements.
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
