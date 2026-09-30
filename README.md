# QTI 3 Validator

An HTTP service that checks QTI 3.0 and 3.0.1 items, tests and complete content
packages against the official 1EdTech schemas and rules. Send it a document or a
ZIP package; it tells you whether it is valid and, if not, exactly what is
wrong and where.

- **Complete checks:** well-formed XML, the QTI 3 XML Schemas (XSD), the 7,400+
  Schematron rules 1EdTech publishes alongside them, and
  [additional checks](docs/additional-checks.md) on how the parts of an item fit
  together.
- **Precise errors:** line, column and element path for every problem.
- **Small and fast:** a ~13 MB image that uses about 40 MB of memory and
  validates a typical item in well under a millisecond.
- **Self-contained:** the schemas are built in. The service never contacts the
  internet and never stores or logs the documents you send.

## Quick start

Start the service:

```sh
docker run --rm -p 8080:8080 kennisnet/qti3-validator
```

Validate a package:

```sh
curl -X POST http://localhost:8080/api/validate \
  -H 'Content-Type: application/zip' \
  --data-binary @package.zip
```

Or a single item:

```sh
curl -X POST http://localhost:8080/api/validate \
  -H 'Content-Type: application/xml' \
  --data-binary @item.xml
```

The answer is a [validation report](docs/report.md). `summary.outcome` is the
verdict:

```json
{
  "summary": {
    "outcome": "VALID",
    "fatals": 0,
    "errors": 0,
    "warnings": 0,
    "exceptions": 0,
    "notRun": 0,
    "totalRun": 3,
    "valid": 1
  },
  "errors": [],
  …
}
```

## What is checked

Every document goes through four checks:

1. **Well-formedness:** the document must be well-formed XML in UTF-8.
2. **XML Schema:** the document must conform to the QTI XSDs of its version,
   including what QTI imports, such as MathML 3, SSML 1.1 and IEEE LOM.
3. **Schematron rules:** the rules 1EdTech embeds in those schemas, for what an
   XSD cannot express, such as allowed attributes, `max-choices` against
   `min-choices` and ARIA roles.
4. **[Additional checks](docs/additional-checks.md):** whether the parts of an
   item or test fit together, for example that every interaction is bound to a
   declared response variable.

A package is checked file by file. It needs an `imsmanifest.xml` in its root;
every XML file is validated, media files are not opened, and XML files that
are not a QTI document are reported as not run.

Accepted documents are QTI items, tests, sections, stimuli, response
processing templates, package manifests, LOM and QTI metadata. Others,
including QTI 2.x, are rejected with `unsupported_document`, unless a
[custom validator](docs/custom-validators.md) adds them.

### QTI versions

QTI 3.0 (`3.0.0`) and 3.0.1 are supported, each with its own official schemas
and rules. Both use the same XML namespace, so the service picks the version
like this:

| Situation | Version used |
| --- | --- |
| `?version=3.0.0` or `?version=3.0.1` in the request | That version, for every document |
| A package, or a manifest on its own | The manifest's `<schemaversion>` |
| Any other document | The latest version, 3.0.1 |

The report says which version was used, in `specification.version`. To check
3.0 content against 3.0.1, pass `?version=3.0.1`: the manifest's
`<schemaversion>3.0.0</schemaversion>` then gives a `version_overridden`
warning instead of an error.

## API

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/api/validate` | Validate a package or a single XML document |
| `GET` | `/api/validators` | List the `validatorId` values |
| `GET` | `/health` | Liveness and readiness check |
| `GET` | `/version` | Service and QTI versions |

### `POST /api/validate`

The `Content-Type` says how the input is sent:

| `Content-Type` | Body | Validated as |
| --- | --- | --- |
| `application/zip` | A ZIP file | A package |
| `application/xml`, `text/xml` | One XML document | A single document |
| `application/octet-stream` | A ZIP or an XML document | Decided by the content |
| `multipart/form-data` | The file in the form field `file` | Decided by the content |

Query parameters, all optional:

| Parameter | Meaning |
| --- | --- |
| `version` | `3.0.0` or `3.0.1`: the QTI version every document is validated against |
| `name` | The name of the input in the report; default the uploaded file's name, else `package.zip` or `document.xml` |
| `validatorId` | `Qti30Inspector`; see `GET /api/validators` |

For example, to validate a 3.0 package against QTI 3.0.1:

```sh
curl -X POST 'http://localhost:8080/api/validate?version=3.0.1&name=toets.zip' \
  -H 'Content-Type: application/zip' \
  --data-binary @toets.zip
```

### Status codes

Every request that could be validated gets **HTTP 200** and a report, also
when the content is invalid: the verdict is `summary.outcome`. A report is
valid when its outcome is `VALID` or `WARNING`; it is not when it is `ERROR`,
`FATAL` or `EXCEPTION`. [The validation report](docs/report.md) describes
every field, outcome and code.

A request that could not be validated at all gets an error status and a short
JSON body:

```json
{
  "code": "unsupported_media_type",
  "message": "Content-Type must be multipart/form-data, application/xml or application/zip"
}
```

| HTTP | Code | When |
| --- | --- | --- |
| 400 | `unsupported_version` | `version` is not a supported version |
| 400 | `unknown_validator` | `validatorId` is not listed by `GET /api/validators` |
| 400 | `invalid_request` | The body could not be read, or a form has no `file` field |
| 413 | `too_large`, `too_many_files` | The request, package, or the XML in a package exceeds a limit |
| 415 | `unsupported_media_type` | The `Content-Type` is not one of the above |
| 500 | `internal` | The service failed; the body is then a report with outcome `EXCEPTION` |
| 503 | `busy` | All validation slots stayed busy for `REQUEST_TIMEOUT`; retry later |

## Configuration

All settings are environment variables. Sizes are in bytes.

| Variable | Default | Description |
| --- | --- | --- |
| `ADDR` | `:8080` | Address and port to listen on |
| `MAX_REQUEST_SIZE` | `10485760` (10 MiB) | Largest single XML document |
| `MAX_PACKAGE_SIZE` | `104857600` (100 MiB) | Largest request body for a package |
| `MAX_FILE_SIZE` | `10485760` (10 MiB) | Largest uncompressed XML file inside a package |
| `MAX_FILES` | `1000` | Most entries a package may contain |
| `MAX_UNCOMPRESSED_SIZE` | `268435456` (256 MiB) | Largest total of uncompressed XML in one package |
| `MAX_ERRORS` | `100` | Most errors reported per document |
| `MAX_DEPTH` | `256` | Deepest element nesting accepted |
| `MAX_CONCURRENT` | number of CPUs | Validations that run at the same time; further requests wait |
| `REQUEST_TIMEOUT` | `60s` | Time limit per request, including waiting for a free slot |
| `VALIDATORS_DIR` | `/validators` | Directory with extra validators; see [Custom validators](docs/custom-validators.md) |

For example:

```sh
docker run --rm -p 8080:8080 \
  -e MAX_PACKAGE_SIZE=52428800 \
  -e MAX_CONCURRENT=4 \
  kennisnet/qti3-validator
```

[Running in production](docs/deployment.md) covers memory, a read-only
container, Docker Compose, Kubernetes and logging.

## Limitations

- **Package consistency:** whether files a manifest or an item refers to exist
  in the package is not checked yet.
- **Scoring:** whether response processing gives the scores you intend is not
  checked; the additional checks only check that it can run.
- **Encoding:** documents must be UTF-8 (ASCII is fine). A `DOCTYPE` is not
  accepted.
- **SSML:** two SSML restrictions are not enforced: `version` and `xml:lang` on
  `<ssml:speak>`, and `name` on `<ssml:mark>`.

## Documentation

- [The validation report](docs/report.md): fields, outcomes and codes
- [Additional checks](docs/additional-checks.md): the checks beyond the 1EdTech rules
- [Custom validators](docs/custom-validators.md): your own rules and document types
- [Running in production](docs/deployment.md)
- [Contributing](CONTRIBUTING.md) and [Architecture](ARCHITECTURE.md): for developers
