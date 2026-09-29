package schemastore

// overrides replace upstream schema files the XSD library cannot compile.
// They are served instead of the pinned file at the same URL; the pinned
// file itself stays byte-identical to upstream.
var overrides = map[string]string{
	// The SSML 1.1 core profile wraps synthesis-nonamespace.xsd in an
	// xs:redefine, which the library does not support. The redefine only
	// tightens two types: <speak> requires version and xml:lang, and <mark>
	// requires name. This wrapper includes the same upstream definitions
	// without those two restrictions, so they are not enforced.
	"https://purl.imsglobal.org/spec/ssml/v1p1/schema/xsd/ssmlv1p1-core.xsd": `<?xml version="1.0" encoding="UTF-8"?>
<xsd:schema xmlns:xsd="http://www.w3.org/2001/XMLSchema" xmlns="http://www.w3.org/2001/10/synthesis" targetNamespace="http://www.w3.org/2001/10/synthesis" elementFormDefault="qualified">
	<xsd:import namespace="http://www.w3.org/XML/1998/namespace" schemaLocation="https://purl.imsglobal.org/spec/w3/2001/schema/xsd/xml.xsd"/>
	<xsd:include schemaLocation="synthesis-nonamespace.xsd"/>
</xsd:schema>`,
}
