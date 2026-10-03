# Additional checks

The official XSDs and the Schematron rules 1EdTech embeds in them check the
structure of a QTI document. They do not check whether the parts of a
document fit together: whether an interaction is bound to a response
variable that exists, or whether response processing has the variables its
template needs. The validator adds those checks.

The checks are not about style or quality. Each one checks either something
the [QTI 3.0 specification](https://www.imsglobal.org/spec/qti/v3p0/info)
requires in so many words, or something that makes an item impossible to run
as written. The quotes below are from that specification.

Most checks live in one ISO Schematron file,
[`rules/qti3-additional-checks.sch`](../rules/qti3-additional-checks.sch), and
run on every QTI 3.0 and 3.0.1 item and test, after the XSD and 1EdTech's
rules. Checks that Schematron cannot express are written in Go:
[references within a package](#references-within-a-package), which need the
other files of the package, and [value types](#value-types), which follow
types through nested expressions.

## In the report

A finding is a normal Schematron finding: `code` is `schematron`, and
`generator` names the file and the check, for example:

```json
{
  "title": "Schematron validation",
  "message": "qti-choice-interaction is bound to 'RESPONSE', which is not a declared response variable.",
  "location": {
    "resource": "/items/item-1.xml",
    "line": 18,
    "column": 5,
    "path": "/qti-assessment-item[1]/qti-item-body[1]/qti-choice-interaction[1]"
  },
  "generator": "schematron|qti3-additional-checks.sch#response-declaration-exists",
  "code": "schematron",
  "detailsMessage": null
}
```

The part after `#` is the check's id from the tables below. The checks in Go
have their own codes and generators: `reference|<id>` with code `reference`,
and `value-type|<id>` with code `value_type`. Ids do not change once
released, so you can filter on them.

An **error** makes the document invalid. A **warning** does not.

## References

| Check | What it checks | Basis | Level |
| --- | --- | --- | --- |
| `response-declaration-exists` | An interaction's `response-identifier`, and a text entry's `string-identifier`, name a declared response variable | "All variables must be declared"; `response-identifier` is "The response variable associated with the interaction" | error |
| `variable-declared` | `qti-variable`, `qti-default`, `qti-correct`, `qti-map-response`, `qti-map-response-point` and `qti-printed-variable` name a declared variable of the right kind, or a built-in one (`numAttempts`, `duration`, `completionStatus`, `QTI_CONTEXT`). In a test: a variable of the test, or `duration` | "All variables must be declared except for the built-in session variables" | error |
| `set-target-declared` | In items and tests: `qti-set-outcome-value` and `qti-lookup-outcome-value` set a declared outcome variable, `qti-set-template-value` a template variable, `qti-set-correct-response` a response variable, `qti-set-default-value` a response or outcome variable | "must match the identifier in a corresponding 'template declaration'" | error |
| `show-hide-variable` | `outcome-identifier` on feedback and `template-identifier` on conditional content name a declared variable with base-type identifier and single or multiple cardinality | "The identifier of an outcome variable that must have a base-type of identifier and be of either single or multiple cardinality" | error |
| `builtin-not-declared` | `numAttempts` and `duration` are not declared as response variables, `completionStatus` not as an outcome variable | "declared implicitly and must not appear in a 'response declaration'" | error |
| `identifier-unique` | Variables are declared once, and choices within one interaction have distinct identifiers. 1EdTech's own examples reuse choice identifiers in different interactions of one item, so the check stays within the interaction | "unique within the scope of the instance"; a choice identifier "must not be used by any other choice" | error |
| `test-identifier-unique` | Test parts, sections, section references and item references have distinct identifiers | "must be unique within the test and must not be the identifier of any testPart" | error |
| `branch-target-exists` | A branch rule's `target` is an item or section later in the same test part, another test part, or `EXIT_SECTION`, `EXIT_TESTPART` or `EXIT_TEST` | "the target must refer to an item or section in the same test-part that has not yet been presented" | error |
| `test-item-variable` | In a test, a variable named `X.VAR` names an item reference, section or test part `X` of the test | "All variables must be declared" | error |

## Response processing

| Check | What it checks | Basis | Level |
| --- | --- | --- | --- |
| `response-processing-template` | The standard templates have what they need: `match_correct` a `RESPONSE` with a correct response; `map_response` a `RESPONSE` with a `qti-mapping`; `map_response_point` a `RESPONSE` of base-type point with a `qti-area-mapping`; all of them an outcome `SCORE`. A template is known by its name after `/rptemplates/`, so the Common Cartridge variants and the `www.imsglobal.org/question/qti_v3p0/rptemplates/` URLs count too. A correct response set in template processing counts | "A response variable called RESPONSE must have been declared and have an associated correct value. Similarly, the outcome variable SCORE must also have been declared." | error |
| `response-processing-template` | A template QTI 3 does not define has a `template-location` | A delivery engine that does not know the template cannot run it | warning |
| `mapping-declared` | `qti-map-response` uses a response variable with a `qti-mapping`; `qti-map-response-point` one of base-type point with a `qti-area-mapping` | "using the associated mapping, which must have been declared" | error |
| `template-processing-scope` | Template processing does not use the value of a response or outcome variable | "An expression used in a qti-template-rule must not refer to the value of a response variable or outcome variable" | error |
| `interaction-response-type` | Each interaction is bound to a response variable of the base-type and cardinality the specification sets for it, for example identifier and single or multiple for `qti-choice-interaction` | "The qti-choice-interaction must be bound to a response variable with a base-type of identifier and single or multiple cardinality", and likewise for each interaction | error |
| `choice-value-exists` | A correct response or map key names only choices of its interaction; a pair names a choice on both sides. Checked when every interaction bound to the response offers fixed choices | The candidate can never give that answer | warning |
| `variable-used` | Every declared outcome variable is used in response processing, and every template variable in template processing. Items without response or template processing are not checked; scoring by hand is allowed | "every declared variable must be referenced in the corresponding outcomes processing" | warning |

## References within a package

These run when a package is validated, after every file in it was read: they
compare what the documents refer to with the files the package holds.
References to other hosts (`https://…`) are not checked; whether they can be
reached depends on the moment, not on the package. A reference that leaves
the package (`../…`) counts as missing.

| Check | What it checks | Basis | Level |
| --- | --- | --- | --- |
| `manifest-file-exists` | Every `href` of a resource and its files in `imsmanifest.xml` is a file in the package (with `xml:base` applied) | Package files "MUST be contained in the corresponding QTI content package" | error |
| `file-in-manifest` | Every file in the package is listed in the manifest | [Beginner's Guide](https://www.imsglobal.org/spec/qti/v3p0/guide): a manifest "lists all the assets contained within the package" | warning |
| `item-ref-exists` | A `qti-assessment-item-ref` refers to an item in the package, a `qti-assessment-section-ref` to a section. An LTI link counts as an item, as the [Implementation Guide](https://www.imsglobal.org/spec/qti/v3p0/impl) describes in "Package with a Test and Items with LTI resources" | The test cannot load the item | error |
| `stimulus-ref-exists` | A `qti-assessment-stimulus-ref` refers to a stimulus in the package | The item cannot show the stimulus | error |
| `asset-exists` | `img`, `object`, `audio`, `video` (and its poster), `source`, `track`, `qti-stylesheet` and XInclude references are files in the package | The item cannot load them | error |
| `pci-module-exists` | The `primary-path` or `fallback-path` of a PCI module, and its module configurations, are files in the package; `.js` may be left out. A missing `primary-path` with a `fallback-path` that exists is a warning | The interaction cannot load | error, or warning |
| `catalog-file-exists` | The `qti-file-href` of a catalog card is a file in the package | "these files MUST be contained in the corresponding QTI content package" | error |
| `template-location-exists` | A local `template-location` of response processing is a file in the package | A delivery engine that does not know the template cannot fetch it | warning |
| `test-item-variable-declared` | A test's `ITEM.VARIABLE` refers to a variable the item declares, or a built-in one | "All variables must be declared" | error |

## Value types

These follow the types of the declarations through every expression. An
operand whose type is not known, such as the result of a
`qti-custom-operator` or a variable of another item, is never reported.

| Check | What it checks | Basis | Level |
| --- | --- | --- | --- |
| `value-type` | Default and correct values, map keys and `qti-base-value`s are valid for their base type: an identifier is a name, an integer a whole number, a pair two identifiers, a point two numbers. An empty value is NULL and always valid | The value space of the base type; "Empty containers and empty strings are always treated as NULL values" | error |
| `expression-type` | Every operator gets the base type and cardinality it takes, for example numbers for `qti-sum`, two values of the same type for `qti-match`, a single value then a container for `qti-member`, and a single boolean for a condition | The description of each expression in section 2.11 of the information model, for example "The qti-sum operator takes 1 or more sub-expressions which all have numerical base-types" | error |
| `assignment-type` | `qti-set-outcome-value`, `qti-set-template-value`, `qti-set-correct-response` and `qti-set-default-value` assign a value of the variable's base type and cardinality; an integer may be assigned to a float | "must result in a value with base-type and cardinality matching the declaration" | error |

## Limits

- **One package at a time.** A test that includes sections from files outside
  the package, or items on another host, is checked only as far as the
  package goes.
- **Known types only.** The type checks report only what is certain; a
  record field or a custom operator ends what they can follow.

## Changing a check

The checks in Go are in `internal/domain/qti/references.go` (references) and
`internal/adapter/rules/types.go` (value types), with their tests next to
them. The Schematron file is plain ISO Schematron with XPath 1.0, so it can be read and edited
like any other. Each pattern starts with a comment that quotes its basis.
After a change:

1. Run `go run ./cmd/fetchschemas` to compile the rules into the build. A
   test fails as long as you have not.
2. Add or adjust a document in `testdata/additional-checks/docs/` that makes
   the check fire, and one case that must pass.
3. Regenerate `testdata/additional-checks/expected.json` with the ISO
   Schematron reference implementation, as `reference.py` there describes.
   The tests compare the validator with it, and fail for any check that no
   document triggers.

To add checks for your own installation without changing this file, mount a
`.sch` file instead; see [Custom validators](custom-validators.md).
