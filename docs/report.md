# The validation report

`POST /api/validate` answers every request it could validate with HTTP 200
and a report, whatever the report found. The verdict is `summary.outcome`.
A validation that stopped before the end (code `too_complex`) is HTTP 422,
and one the service failed (code `internal`) HTTP 500; both still come with
a report.

The report follows the public report model of 1EdTech's own validators, so it
looks familiar if you have used them. Its sources are the
[API description of the public 1EdTech validator](https://vc.1ed.tech/v3/api-docs)
and the open-source
[digital-credentials-public-validator](https://github.com/1EdTech/digital-credentials-public-validator).

## Examples

A valid item:

```json
{
  "id": "bd38b287-12bd-4ed4-bc55-3f5db37237c1",
  "generated": "2026-10-07T09:27:57",
  "generator": "qti-validator 0.1.0",
  "input": {
    "name": "item.xml",
    "type": "XML"
  },
  "specification": {
    "pid": "qti301.pid",
    "shortName": "qti",
    "version": "3.0.1",
    "title": "QTI 3.0.1"
  },
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
  "fatals": [],
  "errors": [],
  "warnings": [],
  "exceptions": [],
  "notRun": [],
  "valids": [
    {
      "title": "Document",
      "message": "Valid qti-assessment-item (QTI 3.0.1)",
      "location": {
        "resource": "item.xml"
      },
      "generator": "validation",
      "code": "valid",
      "detailsMessage": null
    }
  ]
}
```

The `errors` of an item with an invalid attribute value and an unknown
attribute on the same element; `summary.outcome` is `ERROR`:

```json
[
  {
    "title": "XML Schema validation",
    "message": "invalid attribute shuffle: invalid boolean",
    "location": {
      "resource": "item.xml",
      "line": 18,
      "column": 5,
      "path": "/qti-assessment-item/qti-item-body/qti-choice-interaction"
    },
    "generator": "xsd|https://purl.imsglobal.org/spec/qti/v3p0/schema/xsd/imsqti_asiv3p0p1_v1p0.xsd",
    "code": "validation",
    "detailsMessage": null
  },
  {
    "title": "Schematron validation",
    "message": "[RULE LOCAL ELEMENT (qti-choice-interaction): Assertion 4] Invalid XML attribute in position 4 with name of colour.",
    "location": {
      "resource": "item.xml",
      "line": 18,
      "column": 5,
      "path": "/qti-assessment-item[1]/qti-item-body[1]/qti-choice-interaction[1]"
    },
    "generator": "schematron|RULESET_LOCALELEMENT_DATAEXTENSIONRULES",
    "code": "schematron",
    "detailsMessage": null
  }
]
```

In a package report, `location.resource` is the file's path from the root of
the package, with a leading slash (`/items/item-1.xml`), and every document
that passed gets a `valids` item.

## Packages

What the manifest lists decides how an XML file of a package is checked.
Paths in the manifest are resolved with `xml:base` applied, as IMS Content
Packaging chains it from `manifest` through `resources` and `resource` to
`file`.

| File | Checked as |
| --- | --- |
| `imsmanifest.xml` | A QTI 3 content package manifest, root `{http://www.imsglobal.org/xsd/qti/qtiv3p0/imscp_v1p1}manifest`. Anything else, such as a manifest of plain IMS Content Packaging (`http://www.imsglobal.org/xsd/imscp_v1p1`), is `unsupported_document`, and the files are then not reported as missing from the manifest one by one |
| A file of a QTI resource (`imsqti_item_xmlv3p0`, `imsqti_test_xmlv3p0`, `imsqti_outcomes_xmlv3p0`, …) | A QTI document; anything else, such as a QTI 2.x item, is `unsupported_document`. An outcomes resource holds a `qti-outcome-declaration` |
| A file of a fragment resource (`imsqti_fragment_xmlv3p0`) | A QTI fragment: XML that another document includes with XInclude, such as a `div`. Its root must be in the QTI namespace. When the schema declares that root as a global element, as it does for the HTML content elements, the fragment is validated against it; other roots, such as a `qti-response-condition`, are valid only where they are included, so their content is not checked. XIncludes are not resolved, and the Schematron rules do not run on a fragment. A fragment that passed is reported as `Valid qti-fragment` |
| Any other XML file | A QTI document if its root is one; otherwise not run (`skipped`) |

A file that could not be read (larger than `MAX_FILE_SIZE`, a corrupt entry,
not well-formed before its type is known) gets its own finding; the documents
that refer to it get none about its type.

## Report fields

| Field | Description |
| --- | --- |
| `id` | Unique id of this report, a UUID |
| `generated` | When the report was made, in UTC, for example `2026-10-07T08:44:28` |
| `generator` | The service and its version |
| `input` | `name` and `type` (`XML` or `ZIP`) of what was validated |
| `specification` | What it was validated against: `pid` (`qti300.pid` or `qti301.pid`), `shortName` `qti`, `version`, `title`. A document that could not be read far enough to know its type (not well-formed, not a QTI 3 document) names the version it would have been validated against: the `version` parameter, or the latest. A document of a type a [custom validator](custom-validators.md) adds has no QTI version: `pid` is `qti.pid`, `title` is `QTI`, and there is no `version` |
| `summary.outcome` | The most severe outcome in the report; see [Outcomes](#outcomes) |
| `summary.*` | Number of items per outcome. `totalRun` counts the checks that ran: reading each document, its XML Schema validation and its Schematron rules, and for a package the package itself. Checks that did not run are not counted: the XML Schema and the rules of a document that could not be read, and the rules when the XML Schema check could not assess the whole document, when it found `MAX_ERRORS` errors, or when validation stopped before the rules. A check that started and stopped (`too_complex`) counts. `valid` counts the documents that passed |
| `fatals`, `errors`, `warnings`, `exceptions`, `notRun`, `valids` | One list per outcome, always present |

Each item in those lists:

| Field | Description |
| --- | --- |
| `title` | The check, for example `XML Schema validation` or `Schematron validation` |
| `message` | What was found. Schematron messages are 1EdTech's own texts, rule label included |
| `location.resource` | The document: its name, or in a package its path from the package root |
| `location.line`, `location.column` | Position in the document, starting at 1 |
| `location.path` | The element, as an XPath. Schema paths have no positions; Schematron paths do (`[1]`) |
| `generator` | The check that found it: `xsd\|<schema URL>`, `schematron\|<rule set>`, `schematron\|<file>#<rule>` for the [additional checks](additional-checks.md) and [custom validators](custom-validators.md), `reference\|<check>` and `value-type\|<check>` for the additional checks in Go, or `parse`, `package`, `version`, `limits`, `document-type` |
| `code` | Stable code for the kind of finding; see [Codes](#codes) |
| `source` | Only for a custom validator: the file in `/validators` |
| `detailsMessage` | Reserved for extra detail; currently always `null` |

## Outcomes

From least to most severe:

| Outcome | Meaning | Valid? |
| --- | --- | --- |
| `NOT_RUN` | A check did not run: a file that is not a QTI document, or the checks after a document that could not be read | does not count |
| `VALID` | A document passed | yes |
| `WARNING` | Something to look at, such as an overridden QTI version | yes |
| `ERROR` | The document breaks the XML Schema or a rule, or the package is incomplete | no |
| `FATAL` | The input could not be read: not well-formed XML, not UTF-8, not a QTI document, not a ZIP | no |
| `EXCEPTION` | The validation did not finish: it stopped because a document is too complex or took longer than `REQUEST_TIMEOUT` (HTTP 422), or the service itself failed (HTTP 500) | no |

A report is valid when its outcome is `VALID` or `WARNING`.

## Codes

| Code | Outcome | Meaning |
| --- | --- | --- |
| `valid` | VALID | The document passed |
| `validation` | ERROR | The document does not conform to the QTI XML Schemas |
| `schematron` | ERROR | The document breaks a Schematron rule (WARNING for a rule marked as a warning) |
| `reference` | ERROR | A reference to a file, item or variable does not resolve in the package (WARNING for an unlisted file, a used fallback or a missing `template-location`); see [additional checks](additional-checks.md#references-within-a-package) |
| `value_type` | ERROR | A value or expression has the wrong base type or cardinality; see [additional checks](additional-checks.md#value-types) |
| `unsupported_version` | ERROR | A manifest declares a `<schemaversion>` the service does not support; it was validated against the latest version instead |
| `missing_manifest` | ERROR | There is no `imsmanifest.xml` in the root of the package |
| `unsafe_path` | ERROR | An entry name in the package is absolute or contains `..` |
| `too_large` | ERROR | A file inside the package is larger than `MAX_FILE_SIZE` |
| `limit_exceeded` | ERROR | The document is nested deeper than `MAX_DEPTH`, or exceeds another parser limit. As a WARNING: the note that the rules gave more warnings than `MAX_ERRORS` (see [Limits](#limits)) |
| `invalid_zip` | FATAL | The body is not a readable ZIP (ERROR when one entry is corrupt) |
| `invalid_xml` | FATAL | The document is not well-formed XML |
| `unsupported_encoding` | FATAL | The document is not UTF-8 |
| `unsupported_xml` | FATAL | The document has a `DOCTYPE`, which is not accepted |
| `unsupported_document` | FATAL | Not a supported QTI 3 document, for example a QTI 2.x item or the right element name in the wrong namespace; in a package also an `imsmanifest.xml` that is not a QTI 3 manifest, or a QTI fragment whose root is not in the QTI namespace (see [Packages](#packages)) |
| `version_overridden` | WARNING | The `version` parameter differs from the manifest's `<schemaversion>` |
| `skipped` | NOT_RUN | An XML file in a package that is not a QTI document, and that the manifest does not list as a QTI resource |
| `not_run` | NOT_RUN | Checks that could not run after a FATAL finding |
| `too_complex` | EXCEPTION | Validation stopped before the end: the document's rules need more work than one document may take (a budget of XPath steps that grows with the number of nodes in the document; the rules of plain content stay well within it at any size up to `MAX_REQUEST_SIZE`), or validation took longer than `REQUEST_TIMEOUT`. The findings before it are kept. In a package it is reported in the file that stopped; when time ran out, a package finding also names the files that were not validated. HTTP 422 |
| `internal` | EXCEPTION | An unexpected error in the service, such as a rule that fails on the document. The findings before it are kept: those of the XML Schema and of the rules that ran before the failure, built-in or mounted |

## Limits

`MAX_ERRORS` (100 by default) limits the findings of each document:

- **Errors:** at most `MAX_ERRORS` errors from the XML Schema and the rules
  together. The rules run only while the XML Schema left room, and stop
  once the errors reach the limit. Warnings do not count towards it, so they
  never hide an error.
- **Warnings of the rules:** at most `MAX_ERRORS`, apart from the errors.
  Once they reach that, further warnings are left out, but the rules go on
  looking for errors. One last warning with code `limit_exceeded` then marks
  the cut: "3 further warnings of the rules were left out: only the first
  100 are reported (MAX_ERRORS)." It counts the warnings the rules found
  until they ended; rules that stop early, at the limit on errors or with
  `too_complex` or `internal`, do not look for more.
- **Not limited:** a `too_complex` or `internal` finding, which comes on top
  of the errors; a `version_overridden` warning; the `limit_exceeded` note
  about warnings left out, which comes on top of the warnings; and in a package the
  `reference` findings and the findings about the package itself, which
  come after the documents were validated.
