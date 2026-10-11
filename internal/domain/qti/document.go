package qti

import "encoding/xml"

// Namespaces of the documents the validator accepts.
const (
	NamespaceASI      = "http://www.imsglobal.org/xsd/imsqtiasi_v3p0"
	NamespaceManifest = "http://www.imsglobal.org/xsd/qti/qtiv3p0/imscp_v1p1"
	NamespaceLOM      = "http://ltsc.ieee.org/xsd/LOM"
	NamespaceMetadata = "http://www.imsglobal.org/xsd/imsqti_metadata_v3p0"
)

// DocumentType is a root element the validator accepts, and the name it
// reports as the schema.
type DocumentType struct {
	Namespace string
	Root      string
	Schema    string
}

// Name is the document type's root element name.
func (t DocumentType) Name() xml.Name { return xml.Name{Space: t.Namespace, Local: t.Root} }

// Schema names that the package checks need to recognise.
const (
	SchemaManifest = "imscp-manifest"
	// SchemaFragment is what a QTI fragment of a package reports: XML that
	// another QTI document includes with XInclude, listed in the manifest as
	// a resource of type imsqti_fragment_xmlv3p0. Its root is ordinary QTI
	// content, such as a div, so it is not a document type of its own.
	SchemaFragment = "qti-fragment"
)

// documentTypes lists the QTI document types. Matching uses both the
// namespace and the local name, so <qti-assessment-item> in another
// namespace is rejected. Every root is a global element of the QTI schemas.
var documentTypes = []DocumentType{
	{NamespaceASI, "qti-assessment-item", "qti-assessment-item"},
	{NamespaceASI, "qti-assessment-test", "qti-assessment-test"},
	{NamespaceASI, "qti-assessment-section", "qti-assessment-section"},
	{NamespaceASI, "qti-assessment-stimulus", "qti-assessment-stimulus"},
	{NamespaceASI, "qti-response-processing", "qti-response-processing"},
	// The root of an outcomes resource (imsqti_outcomes_xmlv3p0), such as an
	// outcome a test declares for CASE alignment.
	{NamespaceASI, "qti-outcome-declaration", "qti-outcome-declaration"},
	{NamespaceManifest, "manifest", SchemaManifest},
	{NamespaceLOM, "lom", "lom"},
	{NamespaceMetadata, "qtiMetadata", "qti-metadata"},
}

// DocumentTypes returns the QTI document types.
func DocumentTypes() []DocumentType { return append([]DocumentType(nil), documentTypes...) }

// LookupDocumentType returns the QTI document type with the given root.
func LookupDocumentType(root xml.Name) (DocumentType, bool) {
	for _, t := range documentTypes {
		if t.Name() == root {
			return t, true
		}
	}
	return DocumentType{}, false
}
