# Custom validators

You can add your own rules and document types by mounting a directory with
extra validators on `/validators`. The image contains that directory, empty:

```sh
docker run --rm -p 8080:8080 \
  -v ./validators:/validators:ro \
  ghcr.io/feyst/qti3-validator
```

Only files directly in the directory are used. Subdirectories and other files
are ignored, with a line in the log.

- **`*.sch`:** an ISO Schematron schema (root `sch:schema`). Its rules run on
  every document the service validates, built-in and custom, after the
  built-in rules.
- **`*.xsd`:** an XML Schema that adds document types. Every global element
  it declares becomes a recognised root, matched on namespace and local name.
  Such a document is validated against this XSD, then against the Schematron
  rules embedded in it, if any, then against the `.sch` files. QTI versions do
  not apply to it.

An XSD may include or import another file in the same directory by its bare
name (`schemaLocation="common.xsd"`), and the built-in 1EdTech schemas by
their `https://purl.imsglobal.org/...` URL. Any other location is refused;
nothing is fetched from the network.

## In the report

A finding from a custom validator names its file in `source` and in
`generator`:

```json
{
  "title": "Schematron validation (house-rules.sch)",
  "message": "Item title \"Hoofdstad\" is shorter than 10 characters.",
  "location": {
    "resource": "item.xml",
    "line": 2,
    "column": 1,
    "path": "/qti-assessment-item[1]"
  },
  "generator": "schematron|house-rules.sch#house-title",
  "code": "schematron",
  "source": "house-rules.sch",
  "detailsMessage": null
}
```

## Checking your files

The files are read once, at startup. A file that does not compile, a root
that is already a built-in QTI document or declared by another XSD, or a file
larger than 64 MiB stops the service from starting, with an error that names
the file. To check your files in CI before you deploy them, run the image
with the same mount and `-check`:

```sh
docker run --rm \
  -v ./validators:/validators:ro \
  ghcr.io/feyst/qti3-validator -check
```

## Limits

- XSD 1.0 only.
- Schematron with the default query binding only (XSLT 1.0 patterns, XPath
  1.0). `xslt2`, `sch:include`, `key()` and `document()` are refused at
  startup. All patterns run; phases are not selected.
- An XSD that imports the QTI schemas compiles its own copy of them, which
  adds about 10 MB of memory.
