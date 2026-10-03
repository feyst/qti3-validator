# Example packages

Two packages to try the service with. Both hold the same test: twelve items
of different interaction types in two sections, a stimulus and an image.

```sh
curl -X POST http://localhost:8080/api/validate \
  --data-binary @examples/with-errors.zip
```

- **`valid.zip`:** every document is valid; `summary.outcome` is `VALID`.
- **`with-errors.zip`:** the same package with a problem of each kind;
  `summary.outcome` is `FATAL`, the most severe one in it.

| File in `with-errors.zip` | Change | Reported as |
| --- | --- | --- |
| `items/tekstselectie.xml` | Cut off halfway | `FATAL` `invalid_xml`, and its other checks `NOT_RUN` |
| `items/invul.xml` | `expected-length="tien"`, not a number | `ERROR` `validation` (XML Schema) |
| `items/meerkeuze.xml` | An attribute QTI does not have, `colour` | `ERROR` `schematron` (a 1EdTech rule) |
| `items/volgorde.xml` | Bound to a response that is not declared | `ERROR` `schematron` (an [additional check](../docs/additional-checks.md)) |
| `items/keuzelijst.xml` | A correct answer that is not one of the choices | `WARNING` `schematron` |
| `items/invul.xml` | An outcome `MAXSCORE` that is never used | `WARNING` `schematron` |
| `notities/opmerkingen.xml` | Not a QTI document, and not listed in the manifest | `NOT_RUN` `skipped`, and `WARNING` `reference` |

An `EXCEPTION` cannot be caused by a file: it means the service itself
failed.
