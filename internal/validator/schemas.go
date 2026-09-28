package validator

import (
	"embed"
	"io/fs"
)

// The schemas are fetched at build time by cmd/fetchschemas, pinned by
// schemas/schemas.lock. Each file lives gzip-compressed under its URL host
// and path plus ".gz", so the absolute schemaLocation URLs in the XSDs map
// directly to embedded files. The same command extracts the Schematron
// rules embedded in the XSDs to schematron.json.gz.
//
//go:embed schemas/purl.imsglobal.org schemas/schematron.json.gz
var embeddedSchemas embed.FS

// rulesFile is the extracted Schematron rules, relative to the schema tree.
const rulesFile = "schematron.json.gz"

// AdditionalRules is the source name of the validator's own Schematron
// rules, rules/qti3-additional-checks.sch, which run on every QTI document
// after the rules embedded in the XSDs. docs/additional-checks.md describes
// them.
const AdditionalRules = "qti3-additional-checks.sch"

// SchemaFS returns the embedded schema tree, rooted at the URL host.
func SchemaFS() fs.FS {
	sub, err := fs.Sub(embeddedSchemas, "schemas")
	if err != nil {
		panic(err) // the embed pattern guarantees the directory exists
	}
	return sub
}

// VersionSchemas are the entry schemas of one QTI version. Together they
// reach every schema that version needs: items, tests, sections, stimuli,
// the package manifest and its LOM and QTI metadata.
type VersionSchemas struct {
	Version  string // as a manifest's schemaversion states it
	ASI      string
	Manifest string
}

const (
	qtiSchemaBase = "https://purl.imsglobal.org/spec/qti/v3p0/schema/xsd/"
	lomSchemaURL  = "https://purl.imsglobal.org/spec/md/v1p3/schema/xsd/imsmd_loose_v1p3p2.xsd"
)

// Versions lists the supported QTI versions, oldest first. Both use the
// same namespace, so a document's root does not say which one it targets;
// the last one is the default for documents that do not declare a version.
var Versions = []VersionSchemas{
	{Version: "3.0.0", ASI: qtiSchemaBase + "imsqti_asiv3p0_v1p0.xsd", Manifest: qtiSchemaBase + "imsqtiv3p0_imscpv1p2_v1p0.xsd"},
	{Version: "3.0.1", ASI: qtiSchemaBase + "imsqti_asiv3p0p1_v1p0.xsd", Manifest: qtiSchemaBase + "imsqtiv3p0p1_imscpv1p2_v1p0.xsd"},
}

// SupportedVersions returns the version names, oldest first.
func SupportedVersions() []string {
	out := make([]string, len(Versions))
	for i, v := range Versions {
		out[i] = v.Version
	}
	return out
}

// LatestVersion is the version used when nothing selects another.
func LatestVersion() string { return Versions[len(Versions)-1].Version }

// IsSupportedVersion reports whether v names a supported version.
func IsSupportedVersion(v string) bool {
	for _, s := range Versions {
		if s.Version == v {
			return true
		}
	}
	return false
}

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
