package qti

// Version is a QTI version and the entry schemas that define it. Together
// the two schemas reach every schema the version needs: items, tests,
// sections, stimuli, the package manifest and its LOM and QTI metadata.
type Version struct {
	Name     string // as a manifest's schemaversion states it, e.g. "3.0.1"
	ASI      string // assessment, section and item schema
	Manifest string // content package manifest schema
}

// SchemaFor names the entry schema a document type is validated against.
func (v Version) SchemaFor(t DocumentType) string {
	if t.Namespace == NamespaceASI {
		return v.ASI
	}
	return v.Manifest
}

const (
	schemaBase = "https://purl.imsglobal.org/spec/qti/v3p0/schema/xsd/"

	// LOMSchema is the IEEE LOM schema both versions use for metadata.
	LOMSchema = "https://purl.imsglobal.org/spec/md/v1p3/schema/xsd/imsmd_loose_v1p3p2.xsd"
)

// versions lists the supported QTI versions, oldest first. Both use the same
// namespace, so a document's root does not say which one it targets; the
// last one is the default for documents that do not declare a version.
var versions = []Version{
	{Name: "3.0.0", ASI: schemaBase + "imsqti_asiv3p0_v1p0.xsd", Manifest: schemaBase + "imsqtiv3p0_imscpv1p2_v1p0.xsd"},
	{Name: "3.0.1", ASI: schemaBase + "imsqti_asiv3p0p1_v1p0.xsd", Manifest: schemaBase + "imsqtiv3p0p1_imscpv1p2_v1p0.xsd"},
}

// Versions returns the supported versions, oldest first.
func Versions() []Version { return append([]Version(nil), versions...) }

// SupportedVersions returns the names of the supported versions, oldest first.
func SupportedVersions() []string {
	out := make([]string, len(versions))
	for i, v := range versions {
		out[i] = v.Name
	}
	return out
}

// LatestVersion is the version used when nothing selects another.
func LatestVersion() Version { return versions[len(versions)-1] }

// LookupVersion returns the version with the given name.
func LookupVersion(name string) (Version, bool) {
	for _, v := range versions {
		if v.Name == name {
			return v, true
		}
	}
	return Version{}, false
}

// IsSupportedVersion reports whether name is a supported version.
func IsSupportedVersion(name string) bool {
	_, ok := LookupVersion(name)
	return ok
}
