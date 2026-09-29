package qti

import "strings"

// ManifestName is the name of a content package's manifest, at its root.
const ManifestName = "imsmanifest.xml"

// IsQTIResourceType reports whether a manifest resource type is a QTI type,
// such as imsqti_item_xmlv3p0. QTI 3 content packaging gives every QTI
// resource such a type, so its files must be QTI documents.
func IsQTIResourceType(t string) bool { return strings.HasPrefix(t, "imsqti_") }

// SafeEntryName rejects package entry names that would escape a directory if
// the package were extracted: absolute paths, drive letters, backslashes and
// "..".
func SafeEntryName(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") ||
		len(name) >= 2 && name[1] == ':' {
		return false
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}
