# QTI 3 Validator

An HTTP service that checks QTI 3.0 and 3.0.1 items, tests and complete content
packages against the official 1EdTech schemas and rules. Send it a document or a
ZIP package; it tells you whether it is valid and, if not, exactly what is
wrong and where.

- **Complete checks:** well-formed XML, the QTI 3.0 XML Schemas (XSD), and the
  7,400+ Schematron rules that 1EdTech publishes alongside them.
- **Precise errors:** line, column and element path for every problem.
- **Small and fast:** a ~12 MB image that uses about 45 MB of memory and
  validates a typical item in well under a millisecond.
- **Self-contained:** the schemas are built in. The service never contacts the
  internet and never stores or logs the documents you send.

## Quick start

```sh
docker run --rm -p 8080:8080 kennisnet/qti3-validator
```

Validate an item:

```sh
curl -X POST \
  -H 'Content-Type: application/xml' \
  --data-binary @item.xml \
  http://localhost:8080/v1/validate
```

The answer is a validation report; `summary.outcome` is the verdict:

```json
{"summary": {"outcome": "VALID", "fatals": 0, "errors": 0, "warnings": 0, "exceptions": 0, "notRun": 0, "totalRun": 1, "valid": 1}, …}
```

Validate a complete package:

```sh
curl -X POST \
  -H 'Content-Type: application/zip' \
  --data-binary @package.zip \
  http://localhost:8080/v1/validate/package
```

## What is checked

Every document goes through four checks:

1. **Well-formedness:** the document must be well-formed XML in UTF-8.
2. **XML Schema:** the document must conform to the QTI XSDs of its version,
   including the parts QTI imports, such as MathML 3, SSML 1.1 and IEEE LOM
   metadata.
3. **Schematron rules:** the document must pass the rules 1EdTech embeds in
   those schemas. They cover what an XSD cannot express, for example:
   - only known attributes or `data-*` attributes on QTI elements;
   - `max-choices` not lower than `min-choices`;
   - allowed ARIA roles;
   - interactions that must not be nested.
4. **Additional checks:** whether the parts of an item or test fit together,
   for example that every interaction is bound to a declared response
   variable, and that response processing has the variables its template
   needs. Each check is either required by the QTI 3 specification or
   needed for the item to run at all. See
   [Additional checks](docs/additional-checks.md).

XSD and Schematron problems in the same document are reported together.

### QTI versions

QTI 3.0 (`3.0.0`) and QTI 3.0.1 are supported, each with its own official
schemas and rules. Both versions use the same XML namespace, so the service
picks the version as follows:

| Situation | Version used |
| --- | --- |
| `?version=3.0.0` or `?version=3.0.1` in the request | That version, for every document |
| A package, no `version` parameter | The `<schemaversion>` in its `imsmanifest.xml` |
| A single manifest, no `version` parameter | Its own `<schemaversion>` |
| A single item, test or other document, no `version` parameter | The latest version, 3.0.1 |

The report always says which version was used, in `specification.version`.

QTI 3.0.1 only adds to 3.0 for items, tests and sections: anything valid under
3.0 is valid under 3.0.1. Manifests are the exception. Each version accepts
only its own `<schemaversion>`, and 3.0.1 changed the LTI resource types.

To check 3.0 content against 3.0.1, pass `?version=3.0.1`. The manifest's
`<schemaversion>3.0.0</schemaversion>` is then not reported as an error.
Instead, a `version_overridden` warning says the version was overridden; it
does not make the package invalid.

### Supported documents

The service recognises a document by its root element **and** namespace:

| Root element | Namespace | `schema` in the response |
| --- | --- | --- |
| `qti-assessment-item` | `http://www.imsglobal.org/xsd/imsqtiasi_v3p0` | `qti-assessment-item` |
| `qti-assessment-test` | same | `qti-assessment-test` |
| `qti-assessment-section` | same | `qti-assessment-section` |
| `qti-assessment-stimulus` | same | `qti-assessment-stimulus` |
| `qti-response-processing` | same | `qti-response-processing` |
| `manifest` | `http://www.imsglobal.org/xsd/qti/qtiv3p0/imscp_v1p1` | `imscp-manifest` |
| `lom` | `http://ltsc.ieee.org/xsd/LOM` | `lom` |
| `qtiMetadata` | `http://www.imsglobal.org/xsd/imsqti_metadata_v3p0` | `qti-metadata` |

Other documents, including QTI 2.x, are rejected with `unsupported_document`,
unless a [custom validator](#custom-validators) declares their root.

### What is not checked

- **Package consistency:** the service does not check that files referenced in
  `imsmanifest.xml` or in items exist in the package, or that resources and
  dependencies match up.
- **Scoring behaviour:** whether response processing produces the scores you
  intend is outside what schemas and rules can express. The
  [additional checks](docs/additional-checks.md) only check that it can run.
- **Media and stylesheets:** images, audio, video and CSS in a package are not
  inspected.

## Custom validators

You can add your own rules and document types by mounting a directory with
extra validators on `/validators`. The image contains that directory, empty:

```sh
docker run --rm -p 8080:8080 \
  -v ./validators:/validators:ro \
  kennisnet/qti3-validator
```

Only files directly in the directory are used; subdirectories and other files
are ignored, with a line in the log.

- **`*.sch`:** an ISO Schematron schema (root `sch:schema`). Its rules run on
  every document the service validates, built-in and custom, after the
  built-in rules.
- **`*.xsd`:** an XML Schema that adds document types. Every global element
  it declares becomes a recognised root, matched on namespace and local name.
  Such a document is validated against this XSD, then against the Schematron
  rules embedded in it, if any, then against the `.sch` files. `schema` in
  the response is the root's local name; `version` is absent, as QTI versions
  do not apply.

An XSD may include or import another file in the same directory by its bare
name (`schemaLocation="common.xsd"`), and the built-in 1EdTech schemas by
their `https://purl.imsglobal.org/...` URL. Any other location is refused;
nothing is fetched from the network.

A finding from a custom validator carries `source`, the file it came from,
and names that file in `generator`:

```json
{"title": "Schematron validation (house-rules.sch)", "message": "Item title \"Hoofdstad\" is shorter than 10 characters.", "location": {"resource": "item.xml", "line": 2, "column": 1, "path": "/qti-assessment-item[1]"}, "generator": "schematron|house-rules.sch#house-title", "code": "schematron", "source": "house-rules.sch", "detailsMessage": null}
```

The files are read once, at startup. A file that does not compile, a root
that is already a built-in QTI document or declared by another XSD, or a
file larger than 64 MiB stops the service from starting, with an error that
names the file. To check your files in CI before you deploy them, run the
image with the same mount and `-check`:

```sh
docker run --rm -v ./validators:/validators:ro kennisnet/qti3-validator -check
```

Limits:
- XSD 1.0 only.
- Schematron with the default query binding only (XSLT 1.0 patterns, XPath
  1.0); `xslt2`, `sch:include`, `key()` and `document()` are refused at
  startup. All patterns run; phases are not selected.
- An XSD that imports the QTI schemas compiles its own copy of them, which
  adds about 10 MB of memory.

## API

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | [`/v1/validate`](#post-v1validate) | Validate one XML document |
| `POST` | [`/v1/validate/package`](#post-v1validatepackage) | Validate a QTI package (ZIP) |
| `POST` | [`/api/validate`](#post-apivalidate) | Validate an uploaded package or XML file (multipart) |
| `GET` | [`/api/validators`](#get-apivalidators) | List the `validatorId` values for `/api/validate` |
| `GET` | [`/health`](#get-health) | Liveness and readiness check |
| `GET` | [`/version`](#get-version) | Service and schema version |

All validation endpoints answer with the same [validation report](#the-validation-report),
for a single document and for a package alike.

### `POST /v1/validate`

Validates one XML document sent as the request body.

- **Header:** `Content-Type: application/xml`. `text/xml` and any `+xml` type
  are accepted as well.
- **Query parameters (optional):**
  - `version=3.0.0` or `version=3.0.1`; see [QTI versions](#qti-versions);
  - `name=item.xml`, the name used for the document in the report. The
    default is `document.xml`.
- **Body:** the XML document, at most 10 MiB by default.

### `POST /v1/validate/package`

Validates a QTI content package. Every `.xml` file in the ZIP is validated on
its own, exactly as `/v1/validate` would.

- **Header:** `Content-Type: application/zip`. `application/x-zip-compressed`
  and `application/octet-stream` are accepted as well.
- **Query parameters (optional):** `version`, as above, and `name`, the
  package's name in the report. The default is `package.zip`.
- **Body:** the ZIP file, at most 100 MiB by default.

What the endpoint does with a package:
- **Manifest:** an `imsmanifest.xml` in the root of the package is required.
- **Other XML:** XML files that are not a supported QTI document are listed
  under `notRun` and do not make the package invalid.
- **Other files:** media and other non-XML files are not opened.

### `POST /api/validate`

Validates an uploaded file.

- **Header:** `Content-Type: multipart/form-data`.
- **Body:** the file in the form field `file`. A ZIP is validated as a
  package, as by `/v1/validate/package`; anything else as one XML document,
  as by `/v1/validate`. The same size limits apply.
- **Query parameters (optional):**
  - `validatorId=Qti30Inspector`; see [`GET /api/validators`](#get-apivalidators).
    The QTI version still follows from the input;
  - `version` and `name`, as above. The default name is the uploaded file's name.

```bash
curl -F file=@my-test.zip 'http://localhost:8080/api/validate?validatorId=Qti30Inspector'
```

### `GET /api/validators`

Lists the values `validatorId` accepts:

```json
[{"id": "Qti30Inspector", "name": "QTI 3.0 Validator", "desc": "Validates QTI 3.0 packages and XML files"}]
```

### The validation report

Every request that could be validated gets HTTP 200 and a report, whatever
the report found. The verdict is `summary.outcome`.

The report follows the public report model of 1EdTech's own validators, so it
will look familiar if you have used them. Its source is the
[API description of the public 1EdTech validator](https://vc.1ed.tech/v3/api-docs)
and the open-source [digital-credentials-public-validator](https://github.com/1EdTech/digital-credentials-public-validator).
A finding's position is in `location.line` and `location.column`, the names the
public engine library uses for a position in a text file.

A valid item:

```json
{
  "id": "bd38b287-12bd-4ed4-bc55-3f5db37237c1",
  "generated": "2026-10-07T09:27:57",
  "generator": "qti-validator 0.1.0",
  "input": { "name": "item.xml", "type": "XML" },
  "specification": { "pid": "qti301.pid", "shortName": "qti", "version": "3.0.1", "title": "QTI 3.0.1" },
  "summary": { "outcome": "VALID", "fatals": 0, "errors": 0, "warnings": 0, "exceptions": 0, "notRun": 0, "totalRun": 3, "valid": 1 },
  "fatals": [], "errors": [], "warnings": [], "exceptions": [], "notRun": [],
  "valids": [
    {
      "title": "Document",
      "message": "Valid qti-assessment-item (QTI 3.0.1)",
      "location": { "resource": "item.xml" },
      "generator": "validation",
      "code": "valid",
      "detailsMessage": null
    }
  ]
}
```

The `errors` of an item with an invalid attribute value and an unknown
attribute on the same element (`summary.outcome` is `ERROR`):

```json
[
  {
    "title": "XML Schema validation",
    "message": "invalid attribute shuffle: invalid boolean",
    "location": { "resource": "item.xml", "line": 18, "column": 5, "path": "/qti-assessment-item/qti-item-body/qti-choice-interaction" },
    "generator": "xsd|https://purl.imsglobal.org/spec/qti/v3p0/schema/xsd/imsqti_asiv3p0p1_v1p0.xsd",
    "code": "validation",
    "detailsMessage": null
  },
  {
    "title": "Schematron validation",
    "message": "[RULE LOCAL ELEMENT (qti-choice-interaction): Assertion 4] Invalid XML attribute in position 4 with name of colour.",
    "location": { "resource": "item.xml", "line": 18, "column": 5, "path": "/qti-assessment-item[1]/qti-item-body[1]/qti-choice-interaction[1]" },
    "generator": "schematron|RULESET_LOCALELEMENT_DATAEXTENSIONRULES",
    "code": "schematron",
    "detailsMessage": null
  }
]
```

In a package report, `location.resource` is the file's path from the root of
the package, with a leading slash (`/items/item-1.xml`), and every document
that passed gets a `valids` item.

#### Report fields

| Field | Description |
| --- | --- |
| `id` | Unique id of this report, a UUID |
| `generated` | When the report was made, in UTC, for example `2026-10-07T08:44:28` |
| `generator` | The service and its version |
| `input` | `name` and `type` (`XML` or `ZIP`) of what was validated |
| `specification` | What it was validated against: `pid` (`qti300.pid` or `qti301.pid`), `shortName` `qti`, `version`, `title` |
| `summary.outcome` | The most severe outcome in the report; see below |
| `summary.*` | Number of items per outcome. `totalRun` counts the checks that ran: reading each document, its XML Schema validation and its Schematron rules, and for a package the package itself. `valid` counts the documents that passed |
| `fatals`, `errors`, `warnings`, `exceptions`, `notRun`, `valids` | One list per outcome, always present |

Each item in those lists:

| Field | Description |
| --- | --- |
| `title` | The check, for example `XML Schema validation` or `Schematron validation` |
| `message` | What was found. Schematron messages are 1EdTech's own texts, rule label included |
| `location.resource` | The document: its name, or in a package its path from the package root (`/items/item-1.xml`) |
| `location.line`, `location.column` | Position in the document, starting at 1 |
| `location.path` | The element, as an XPath. Schema paths have no positions; Schematron paths do (`[1]`) |
| `generator` | The check that found it: `xsd\|<schema URL>`, `schematron\|<rule set>`, `schematron\|<file>#<rule>` for a custom validator, or `parse`, `package`, `version`, `limits`, `document-type` |
| `code` | Stable code for the kind of finding; see [Codes](#codes) |
| `source` | Only for a custom validator: the file in `/validators` |
| `detailsMessage` | Reserved for extra detail; currently always `null` |

#### Outcomes

From least to most severe:

| Outcome | Meaning | Valid? |
| --- | --- | --- |
| `NOT_RUN` | A check did not run: a file that is not a QTI document, or the checks after a document that could not be read | does not count |
| `VALID` | A document passed | yes |
| `WARNING` | Something to look at, such as an overridden QTI version | yes |
| `ERROR` | The document breaks the XML Schema or a Schematron rule, or the package is incomplete | no |
| `FATAL` | The input could not be read: not well-formed XML, not UTF-8, not a QTI document, not a ZIP | no |
| `EXCEPTION` | The service itself failed; the response is then HTTP 500 | no |

A report is valid when its outcome is `VALID` or `WARNING`.

#### Codes

| Code | Outcome | Meaning |
| --- | --- | --- |
| `valid` | VALID | The document passed |
| `validation` | ERROR | The document does not conform to the QTI XML Schemas |
| `schematron` | ERROR | The document breaks a QTI Schematron rule (WARNING for a rule marked as a warning) |
| `unsupported_version` | ERROR | A manifest declares a `<schemaversion>` the service does not support; it was validated against the latest version instead |
| `missing_manifest` | ERROR | There is no `imsmanifest.xml` in the root of the package |
| `unsafe_path` | ERROR | An entry name in the package is absolute or contains `..` |
| `too_large` | ERROR | A file inside the package is larger than `MAX_FILE_SIZE` |
| `limit_exceeded` | ERROR | The document is nested deeper than `MAX_DEPTH`, or exceeds another parser limit |
| `invalid_zip` | FATAL | The body is not a readable ZIP (ERROR when one entry is corrupt) |
| `invalid_xml` | FATAL | The document is not well-formed XML |
| `unsupported_encoding` | FATAL | The document is not UTF-8 |
| `unsupported_xml` | FATAL | The document has a `DOCTYPE`, which is not accepted |
| `unsupported_document` | FATAL | Not a supported QTI 3 document, for example a QTI 2.x item or the right element name in the wrong namespace |
| `version_overridden` | WARNING | The `version` parameter differs from the manifest's `<schemaversion>` |
| `skipped` | NOT_RUN | An XML file in a package that is not a QTI document |
| `not_run` | NOT_RUN | Checks that could not run after a FATAL finding |
| `internal` | EXCEPTION | An unexpected error in the service |

At most `MAX_ERRORS` findings (100 by default) are reported per document.

### When there is no report

A request that could not be validated at all gets an HTTP error status and a
short JSON body:

```json
{"code": "unsupported_media_type", "message": "Content-Type must be application/xml"}
```

| HTTP | Code | When |
| --- | --- | --- |
| 400 | `unsupported_version` | The `version` query parameter is not a supported version |
| 400 | `unknown_validator` | The `validatorId` query parameter is not listed by `GET /api/validators` |
| 400 | `invalid_request` | The request body could not be read, or has no `file` field |
| 413 | `too_large`, `too_many_files` | The request, package, or the XML in a package exceeds a limit |
| 415 | `unsupported_media_type` | The `Content-Type` header does not match the endpoint |
| 503 | `busy` | All validation slots stayed busy for `REQUEST_TIMEOUT`; retry later |

### `GET /health`

Returns HTTP 200 with `{"status": "ok"}` once the service is ready to
validate. Use it for liveness and readiness probes.

### `GET /version`

```json
{"name": "qti-validator", "version": "0.1.0", "go_version": "go1.27.1", "qti_versions": ["3.0.0", "3.0.1"], "default_qti_version": "3.0.1"}
```

## Configuration

All settings are environment variables. Sizes are in bytes.

| Variable | Default | Description |
| --- | --- | --- |
| `ADDR` | `:8080` | Address and port to listen on |
| `MAX_REQUEST_SIZE` | `10485760` (10 MiB) | Largest document for `/v1/validate` |
| `MAX_PACKAGE_SIZE` | `104857600` (100 MiB) | Largest ZIP for `/v1/validate/package` and largest upload for `/api/validate` |
| `MAX_FILE_SIZE` | `10485760` (10 MiB) | Largest uncompressed XML file inside a package |
| `MAX_FILES` | `1000` | Most entries a package may contain |
| `MAX_UNCOMPRESSED_SIZE` | `268435456` (256 MiB) | Largest total of uncompressed XML in one package |
| `MAX_ERRORS` | `100` | Most errors reported per document |
| `MAX_DEPTH` | `256` | Deepest element nesting accepted |
| `MAX_CONCURRENT` | number of CPUs | Validations that run at the same time; further requests wait |
| `REQUEST_TIMEOUT` | `60s` | Time limit per request, including waiting for a free slot |
| `VALIDATORS_DIR` | `/validators` | Directory with extra `.xsd` and `.sch` validators; see [Custom validators](#custom-validators). An explicitly set directory must exist. |

Example:

```sh
docker run --rm -p 8080:8080 \
  -e MAX_PACKAGE_SIZE=52428800 \
  -e MAX_CONCURRENT=4 \
  kennisnet/qti3-validator
```

## Running in production

### Memory and CPU

The service uses about 45 MB of memory when idle. Measured with the defaults
on 8 CPUs, the peak was about 115 MB, with 50 clients sending 1 MB packages
(74 files each) at the same time.

Memory grows with the size of the documents being validated at that moment,
times `MAX_CONCURRENT`. With typical QTI content, a memory limit of 256 MiB
leaves ample headroom. If you raise `MAX_REQUEST_SIZE` or `MAX_FILE_SIZE`
substantially, lower `MAX_CONCURRENT` or raise the limit.

Startup takes under a second, while the schemas of both QTI versions are
compiled; memory briefly peaks at about 100 MB.

### Hardening

The image contains only the service binary. It runs as an unprivileged user
and needs no network access. It writes only to `/tmp`, where an uploaded
package is kept while it is validated. It can therefore run with a read-only
root filesystem:

```sh
docker run --rm -p 8080:8080 \
  --read-only --tmpfs /tmp \
  --memory 256m \
  kennisnet/qti3-validator
```

### Docker Compose

```yaml
services:
  qti-validator:
    image: kennisnet/qti3-validator
    ports:
      - "8080:8080"
    read_only: true
    tmpfs:
      - /tmp
    mem_limit: 256m
    restart: unless-stopped
```

The image contains no shell or `curl`, so a Docker `HEALTHCHECK` inside the
container is not possible. Check `GET /health` from your orchestrator or load
balancer instead.

### Kubernetes

```yaml
containers:
  - name: qti-validator
    image: kennisnet/qti3-validator
    ports:
      - containerPort: 8080
    resources:
      requests: { memory: 64Mi, cpu: 100m }
      limits: { memory: 256Mi }
    securityContext:
      readOnlyRootFilesystem: true
      runAsNonRoot: true
      allowPrivilegeEscalation: false
    volumeMounts:
      - { name: tmp, mountPath: /tmp }
    readinessProbe:
      httpGet: { path: /health, port: 8080 }
    livenessProbe:
      httpGet: { path: /health, port: 8080 }
volumes:
  - name: tmp
    emptyDir: {}
```

### Logging

The service logs one JSON line per request to standard output, with method,
path, status, size and duration. Document contents are never logged. An
uploaded package is deleted as soon as its request finishes.

## Limitations

- **QTI versions:** only QTI 3.0 and 3.0.1 are supported; QTI 2.x is not.
- **Encoding:** documents must be UTF-8 (ASCII is fine). A `DOCTYPE` is not
  accepted.
- **SSML:** two SSML restrictions are not enforced: `version` and `xml:lang` on
  `<ssml:speak>`, and `name` on `<ssml:mark>`.
- **Paths:** the `path` of a schema error and that of a Schematron error are
  written slightly differently. Schematron paths include positions such as
  `[1]`.

## Contributing

How the service is built, tested and measured is described in
[CONTRIBUTING.md](CONTRIBUTING.md).
