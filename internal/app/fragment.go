package app

import (
	"bytes"
	"context"
	"fmt"

	"qti3-validator/internal/domain/qti"
	"qti3-validator/internal/lib/xmldoc"
)

// unsupportedDocument reports whether a document was well-formed but its
// root is not a document type the validator knows.
func unsupportedDocument(res qti.DocumentResult) bool {
	return len(res.Errors) == 1 && res.Errors[0].Code == qti.CodeUnsupportedDocument
}

// validateFragment validates a QTI fragment of a package: XML that a QTI
// document includes with XInclude, listed in the manifest as a resource of
// type imsqti_fragment_xmlv3p0. data is well-formed. Its root is ordinary
// QTI content, such as a div, so it must be in the QTI namespace. When the
// schema declares that root as a global element, as it does for the HTML
// content elements, the fragment is validated against it; other roots, such
// as a qti-response-condition, are valid only where they are included, and
// XIncludes are not resolved, so their content is not checked. The
// Schematron rules do not run: they need the whole document.
func (v *Validator) validateFragment(ctx context.Context, data []byte, req qti.VersionRequest, limits qti.Limits) qti.DocumentResult {
	root, err := xmldoc.DetectRoot(bytes.NewReader(data))
	if err != nil {
		return detectionFailure(err)
	}
	if root.Space != qti.NamespaceASI {
		return qti.Failure(qti.OutcomeInvalid, "", qti.CodeUnsupportedDocument, fmt.Sprintf(
			"root element {%s}%s of a QTI fragment is not in the QTI namespace %s", root.Space, root.Local, qti.NamespaceASI))
	}
	choice := qti.ChooseVersion(req, qti.DeclaredVersion{})
	docType := qti.DocumentType{Namespace: root.Space, Root: root.Local, Schema: qti.SchemaFragment}
	res, verdict := checkSchema(ctx, v.versions[choice.Version.Name].Schema, docType, data, limits)
	if verdict.RootUndeclared && res.Outcome == qti.OutcomeInvalid {
		kept := res.Errors[:0:0]
		for _, e := range res.Errors {
			if e.Code != qti.CodeValidation {
				kept = append(kept, e)
			}
		}
		res.Errors = kept
		if len(kept) == 0 {
			res.Valid, res.Outcome = true, qti.OutcomeValid
		}
	}
	res.Version = choice.Version.Name
	return res
}

// requireManifest makes the package's imsmanifest.xml an error when it is
// readable but not a QTI 3 content package manifest, such as a manifest in
// the namespace of plain IMS Content Packaging.
func requireManifest(data []byte, res qti.DocumentResult) qti.DocumentResult {
	unsupported := unsupportedDocument(res)
	if res.Schema == qti.SchemaManifest || res.Schema == "" && !unsupported {
		return res // a QTI 3 manifest, or one that could not be read: its own errors say so
	}
	root, err := xmldoc.DetectRoot(bytes.NewReader(data))
	if err != nil {
		return res
	}
	f := qti.Finding{Code: qti.CodeUnsupportedDocument, Message: fmt.Sprintf(
		"%s is not a QTI 3 content package manifest: its root element is {%s}%s, not {%s}manifest",
		qti.ManifestName, root.Space, root.Local, qti.NamespaceManifest)}
	if unsupported {
		res.Errors[0] = f
		return res
	}
	res.AddError(f, qti.OutcomeInvalid)
	return res
}
