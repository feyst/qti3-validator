# The validation report

`POST /api/validate` answers every request it could validate with HTTP 200
and a report, whatever the report found. The verdict is `summary.outcome`.

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

## Report fields

| Field | Description |
| --- | --- |
| `id` | Unique id of this report, a UUID |
| `generated` | When the report was made, in UTC, for example `2026-10-07T08:44:28` |
| `generator` | The service and its version |
| `input` | `name` and `type` (`XML` or `ZIP`) of what was validated |
| `specification` | What it was validated against: `pid` (`qti300.pid` or `qti301.pid`), `shortName` `qti`, `version`, `title` |
| `summary.outcome` | The most severe outcome in the report; see [Outcomes](#outcomes) |
| `summary.*` | Number of items per outcome. `totalRun` counts the checks that ran: reading each document, its XML Schema validation and its Schematron rules, and for a package the package itself. `valid` counts the documents that passed |
| `fatals`, `errors`, `warnings`, `exceptions`, `notRun`, `valids` | One list per outcome, always present |

Each item in those lists:

| Field | Description |
| --- | --- |
| `title` | The check, for example `XML Schema validation` or `Schematron validation` |
| `message` | What was found. Schematron messages are 1EdTech's own texts, rule label included |
| `location.resource` | The document: its name, or in a package its path from the package root |
| `location.line`, `location.column` | Position in the document, starting at 1 |
| `location.path` | The element, as an XPath. Schema paths have no positions; Schematron paths do (`[1]`) |
| `generator` | The check that found it: `xsd\|<schema URL>`, `schematron\|<rule set>`, `schematron\|<file>#<rule>` for the [additional checks](additional-checks.md) and [custom validators](custom-validators.md), or `parse`, `package`, `version`, `limits`, `document-type` |
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
| `EXCEPTION` | The service itself failed; the response is then HTTP 500 | no |

A report is valid when its outcome is `VALID` or `WARNING`.

## Codes

| Code | Outcome | Meaning |
| --- | --- | --- |
| `valid` | VALID | The document passed |
| `validation` | ERROR | The document does not conform to the QTI XML Schemas |
| `schematron` | ERROR | The document breaks a Schematron rule (WARNING for a rule marked as a warning) |
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
