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
`file`. An `href` is resolved against it as RFC 3986 says, with one
difference: an `xml:base` is always a folder, with or without a trailing
slash, so `xml:base="items"` and `xml:base="items/"` both make `a.xml` into
`items/a.xml`. An `href` with a scheme (`https:`) points outside the package;
a `:` further on, as in `img/a:b.png`, is part of a file name. An `href` that
starts with `/` does not get the `xml:base` and is outside the package.

| File | Checked as |
| --- | --- |
| `imsmanifest.xml` | A QTI 3 content package manifest, root `{http://www.imsglobal.org/xsd/qti/qtiv3p0/imscp_v1p1}manifest`. Anything else, such as a manifest of plain IMS Content Packaging (`http://www.imsglobal.org/xsd/imscp_v1p1`) or a QTI item, is `unsupported_document` and nothing more: it is not validated as the document it is instead, and the files are then not reported as missing from the manifest one by one. The exception is a document type that a [custom validator](custom-validators.md) adds: that validator checks it |
| A file of a QTI resource (`imsqti_item_xmlv3p0`, `imsqti_test_xmlv3p0`, `imsqti_outcomes_xmlv3p0`, …) | A QTI document; anything else, such as a QTI 2.x item, is `unsupported_document`. An outcomes resource holds a `qti-outcome-declaration`. A file that a QTI document of the package includes with XInclude is checked as the next row says instead, with one exception: the `href` of such a resource whose root is not in the QTI namespace, such as a QTI 2.x item, stays `unsupported_document` |
| A file of a fragment resource (`imsqti_fragment_xmlv3p0`), or a file that a QTI document of the package includes with `xi:include` (unless with `parse="text"`) | A QTI document if its root is one; otherwise a QTI fragment, see below |
| Any other XML file | A QTI document if its root is one; otherwise not run (`skipped`) |

A QTI fragment is XML that another document includes with XInclude, such as
a `div` with shared text. An item may list it as a fragment resource, as one
of its own files, or not at all: the `xi:include` is what makes it a
fragment. Its root must be an element of the QTI schema, but not every
element can be checked on its own. The table says which roots are validated
and which only get a warning:

| Root of the fragment | Result |
| --- | --- |
| Not in the QTI namespace, such as MathML | Not QTI content: not run (`skipped`), whether the manifest does not list the file or lists it as a `file` of the item that includes it. It is `unsupported_document` when a fragment resource (`imsqti_fragment_xmlv3p0`) lists it, and when it is the `href` of another QTI resource, such as an item: that resource says it is a QTI 3 document |
| In the QTI namespace, but not an element of the QTI version's schema, such as `qti-bogus` | A `validation` error |
| A global element of the schema: the HTML content elements (`div`, `p`, `table`, …), `qti-outcome-processing` | Validated against the schema as it is |
| An element the schema allows only inside another, and that the validator knows a place for | Validated inside the elements below that allow it, as the next paragraphs say, with the lines, columns and paths of the fragment itself |
| Any other element of the schema; in QTI 3.0.0 and 3.0.1 the 33 listed below | Not checked: a `not_checked` warning, a NOT_RUN item, and no XML Schema check counted in `totalRun` |

Most elements are the same type wherever they stand, so it does not matter
in which of the elements that allow them they are validated. The schema
declares five of the roots that are checked with more than one type, and
for those it does: a `qti-rubric-block` of an item must have a `use`
attribute and one of a section need not, and the `qti-content-body` of a
feedback block may hold another feedback block while that of modal feedback
may not. The validator reads which names these are from the schema. Where a
fragment is validated follows from where it is included:

| The fragment | Validated |
| --- | --- |
| A document of the package includes it, in an element the validator knows a place for | In that place: a `qti-rubric-block` that a section includes is validated as the rubric block of a section. The place is recognised by the element the `xi:include` stands in, and when the schema declares that one with more than one type too, by the elements around it. A fragment that is included in several places must be valid in each of them |
| A document includes it in any other element, such as an `li`; or no document includes it, as a fragment resource may be listed without | In every element below that allows its root. It is valid when it is valid in at least one of them, and an error only when it is valid in none: the errors are then those of the element that gives the fewest |

So a fragment is never an error because of a place it does not stand in.
The other way around it can still pass where it should not: a fragment
whose place is not known is valid when it is valid anywhere, and the
validator does not report a root that is not allowed in the element that
includes it at all, such as a `qti-rubric-block` in a `div`.

An error of one of the five roots says after its message in which element it
was found, for example `missing required attribute use (checked as a child
of qti-item-body, where items/item-1.xml includes the fragment)`, or
`(checked as a child of qti-modal-feedback: the fragment is valid in none
of the 7 places the validator knows for a qti-content-body, and has the
fewest errors in this one)`.

The elements a fragment is validated inside, in the order they are tried,
and the roots each allows:

| Validated inside | Roots |
| --- | --- |
| `qti-outcome-processing`, `qti-response-processing`, `qti-template-processing` | Outcome, response and template rules, such as `qti-outcome-condition`, `qti-response-condition` and `qti-set-template-value` |
| `qti-item-body`, `div`, `p` | Interactions, feedback and template blocks, the `qti-rubric-block` of an item (in `qti-item-body` only), inline content such as `qti-text-entry-interaction` |
| `qti-response-if` | Expressions, such as `qti-match` |
| A response, outcome or template condition | Its if, else-if and else branches |
| `qti-assessment-item` | Declarations, `qti-item-body`, `qti-modal-feedback`, `qti-response-processing`, … |
| `qti-response-declaration` | `qti-correct-response`, `qti-mapping`, `qti-area-mapping` |
| `qti-choice-interaction` | `qti-prompt`, `qti-simple-choice` |
| `qti-assessment-stimulus` | `qti-stimulus-body` |
| `qti-assessment-section`, `qti-assessment-test`, `qti-test-part` | Their parts, such as `qti-assessment-item-ref`, the `qti-rubric-block` of a test, `qti-test-part` and `qti-item-session-control` |
| `qti-associate-interaction`, `qti-match-interaction`, `qti-gap-match-interaction`, `qti-inline-choice-interaction`, `qti-hotspot-interaction`, `qti-graphic-associate-interaction` | Their choices: `qti-simple-associable-choice`, `qti-simple-match-set`, `qti-gap-text`, `qti-gap-img`, `qti-inline-choice`, `qti-hotspot-choice`, `qti-associable-hotspot` |
| `qti-modal-feedback` | Its `qti-content-body` |
| `qti-default-value`, `qti-mapping`, `qti-area-mapping` | `qti-value`, `qti-map-entry`, `qti-area-map-entry` |
| `qti-outcome-declaration`, `qti-match-table`, `qti-interpolation-table` | `qti-match-table`, `qti-interpolation-table` and their entries |
| `qti-assessment-item-ref` | `qti-weight`, `qti-variable-mapping`, `qti-template-default` |
| `qti-feedback-block`, `qti-rubric-block` and `qti-template-block` of an item body, `qti-template-block` in the rubric block of an item, `qti-test-feedback`, `qti-rubric-block` of a section | Their `qti-content-body`, each its own type |
| The `qti-content-body` of the rubric block of an item, and of a template block | `qti-template-block` and `qti-template-inline` in a rubric block, `qti-feedback-block` in a template block: other types than in an item body |

The five roots with more than one type, and the elements each is validated
in:

| Root | Types | Validated inside |
| --- | --- | --- |
| `qti-rubric-block` | 2 | `qti-item-body` (of an item: `use` is required); `qti-assessment-section`, `qti-assessment-test`, `qti-test-part` (of a test: `use` is optional, and the content is narrower) |
| `qti-content-body` | 7 | `qti-modal-feedback`; `qti-feedback-block`, `qti-rubric-block` and `qti-template-block` of an item body; `qti-template-block` in the rubric block of an item; `qti-test-feedback`; `qti-rubric-block` of a section |
| `qti-feedback-block` | 2 | `qti-item-body`, `div`; the `qti-content-body` of a template block |
| `qti-template-block` | 2 | `qti-item-body`, `div`, the `qti-content-body` of a template block; the `qti-content-body` of the rubric block of an item |
| `qti-template-inline` | 2 | `div`, `p`, the `qti-content-body` of a template block; the `qti-content-body` of the rubric block of an item |

The schema allows `xi:include` in the content of these elements, not next
to a `qti-content-body`, so a `qti-content-body` as a fragment is normally
one that no document includes. In QTI 3.0.0 and 3.0.1, 42 roots may stand in
more than one of the elements above; apart from these five they are the same
type in each, such as a `qti-choice-interaction` in a `qti-item-body` or a
`div`. The schema declares three more names with more than one type,
`qti-file-href`, `qti-major-increment` and `qti-minor-increment`; they are
among the roots that are not checked.

The roots that are not checked:

| Part of | Roots |
| --- | --- |
| A catalog | `qti-catalog`, `qti-card`, `qti-card-entry`, `qti-html-content`, `qti-file-href` |
| Companion materials | `qti-calculator`, `qti-calculator-info`, `qti-calculator-type`, `qti-description`, `qti-rule`, `qti-rule-system-si`, `qti-rule-system-us`, `qti-minimum-length`, `qti-minor-increment`, `qti-major-increment`, `qti-increment-si`, `qti-increment-us`, `qti-protractor`, `qti-digital-material`, `qti-physical-material`, `qti-resource-icon` |
| A portable custom interaction | `qti-interaction-markup`, `qti-interaction-modules`, `qti-interaction-module`, `qti-template-variable`, `qti-context-variable` |
| An adaptive section | `qti-adaptive-engine-ref`, `qti-adaptive-settings-ref`, `qti-usagedata-ref`, `qti-metadata-ref` |
| Other elements | `param` (of an `object`), `qti-label` (of a `qti-inline-choice-interaction`), `qti-position-object-interaction` (of a `qti-position-object-stage`) |

In a fragment an IDREF without its ID, such as a `response-identifier`, is no
error: the ID may be in the document that includes it. XIncludes are not
resolved: a fragment is validated on its own, as the type it has where it is
included, and not together with the content around the `xi:include`. The
Schematron rules do not run on a fragment. A fragment that passed is
reported as `Valid qti-fragment`.

A file that could not be read (larger than `MAX_FILE_SIZE`, a corrupt entry,
not well-formed before its type is known) gets its own finding, and a NOT_RUN
item for its XML Schema and Schematron checks that says why; the documents
that refer to it get no finding about its type.

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
| `NOT_RUN` | A check did not run: a file that is not a QTI document, or the checks of a document that could not be read, is not a supported document type, is too large, or is a fragment that cannot be checked on its own | does not count |
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
| `too_large` | ERROR | A file inside the package is larger than `MAX_FILE_SIZE`, or has more nodes than the service has memory to check: the message gives the numbers (see [Memory and CPU](deployment.md#memory-and-cpu)) |
| `limit_exceeded` | ERROR | The document is nested deeper than `MAX_DEPTH`, or exceeds another parser limit |
| `warnings_omitted` | WARNING | The note that the rules gave more warnings than `MAX_ERRORS`, and how many were left out (see [Limits](#limits)) |
| `invalid_zip` | FATAL | The body is not a readable ZIP (ERROR when one entry is corrupt) |
| `invalid_xml` | FATAL | The document is not well-formed XML |
| `unsupported_encoding` | FATAL | The document is not UTF-8 |
| `unsupported_xml` | FATAL | The document has a `DOCTYPE`, which is not accepted |
| `unsupported_document` | FATAL | Not a supported QTI 3 document, for example a QTI 2.x item or the right element name in the wrong namespace; in a package also an `imsmanifest.xml` that is not a QTI 3 manifest, or a file of a fragment resource whose root is not in the QTI namespace (see [Packages](#packages)) |
| `version_overridden` | WARNING | The `version` parameter differs from the manifest's `<schemaversion>` |
| `skipped` | NOT_RUN | An XML file in a package that is not a QTI document, and that the manifest does not list as a QTI resource; or XML outside the QTI namespace, such as MathML, that a QTI document includes with XInclude, unless a fragment resource lists it or it is the `href` of a QTI resource |
| `not_run` | NOT_RUN | The XML Schema and Schematron checks of a document that did not run; the message says why: the document could not be read, is not a supported document type, is too large, or is a fragment that cannot be checked apart from the document that includes it |
| `not_checked` | WARNING | A QTI fragment whose root is valid only inside an element the validator cannot check it in; its content was not checked (see [Packages](#packages)) |
| `too_complex` | EXCEPTION | Validation stopped before the end: the document's rules need more work than one document may take (a budget of XPath steps that grows with the number of nodes in the document, up to a fixed maximum of some 20 to 30 seconds of work; the rules of plain content stay well within it at any size up to `MAX_REQUEST_SIZE`), or validation took longer than `REQUEST_TIMEOUT`. The findings before it are kept. In a package it is reported in the file that stopped; when time ran out, a package finding also names the files that were not validated, or the included files that were not validated as QTI fragments. HTTP 422 |
| `internal` | EXCEPTION | An unexpected error in the service, such as a rule that fails on the document. The findings before it are kept: those of the XML Schema and of the rules that ran before the failure, built-in or mounted |

## Limits

`MAX_ERRORS` (100 by default) limits the findings of each document:

- **Errors:** at most `MAX_ERRORS` errors from the XML Schema and the rules
  together. The rules run only while the XML Schema left room, and stop
  once the errors reach the limit. Warnings do not count towards it, so they
  never hide an error.
- **Warnings of the rules:** at most `MAX_ERRORS`, apart from the errors.
  Once they reach that, further warnings are left out, but the rules go on
  looking for errors. One last warning with code `warnings_omitted` then marks
  the cut: "3 further warnings of the rules were left out: only the first
  100 are reported (MAX_ERRORS)." It counts the warnings the rules found
  until they ended; rules that stop early, at the limit on errors or with
  `too_complex` or `internal`, do not look for more.
- **Not limited:** a `too_complex` or `internal` finding, which comes on top
  of the errors; a `version_overridden` warning; the `warnings_omitted` note
  about warnings left out, which comes on top of the warnings; and in a package the
  `reference` findings and the findings about the package itself, which
  come after the documents were validated.
