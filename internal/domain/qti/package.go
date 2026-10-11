package qti

import (
	"path"
	"strings"
)

// ManifestName is the name of a content package's manifest, at its root.
const ManifestName = "imsmanifest.xml"

// ResourceKind is what a manifest resource type says about its files.
type ResourceKind int

// The kinds of manifest resources.
const (
	// ResourceOther is any resource that is not a QTI resource, such as
	// webcontent or metadata; its XML files need not be QTI documents.
	ResourceOther ResourceKind = iota
	// ResourceDocument is a QTI resource whose files are QTI documents:
	// items, tests, sections, stimuli, response processing templates and
	// outcome declarations.
	ResourceDocument
	// ResourceFragment is a QTI fragment (imsqti_fragment_xmlv3p0): XML that a
	// QTI document includes with XInclude. Its root is ordinary QTI content,
	// such as a div, not a QTI document type.
	ResourceFragment
)

// ResourceTypeFragment is the manifest resource type of a QTI fragment.
const ResourceTypeFragment = "imsqti_fragment_xmlv3p0"

// ClassifyResourceType reports what a manifest resource type says about its
// files. QTI 3 content packaging gives every QTI resource a type starting
// with imsqti_, such as imsqti_item_xmlv3p0.
func ClassifyResourceType(t string) ResourceKind {
	switch {
	case t == ResourceTypeFragment:
		return ResourceFragment
	case strings.HasPrefix(t, "imsqti_"):
		return ResourceDocument
	}
	return ResourceOther
}

// BaseStack follows the xml:base of nested manifest elements, as IMS
// Content Packaging chains them: manifest, resources, resource and file. An
// element without xml:base inherits its parent's.
type BaseStack []string

// Push enters an element with the given xml:base, which may be empty, and
// returns the base that applies to it.
func (s *BaseStack) Push(base string) string {
	if n := len(*s); n > 0 {
		if parent := (*s)[n-1]; base == "" {
			base = parent
		} else {
			base = JoinBase(parent, base)
		}
	}
	*s = append(*s, base)
	return base
}

// Pop leaves the innermost element.
func (s *BaseStack) Pop() {
	if n := len(*s); n > 0 {
		*s = (*s)[:n-1]
	}
}

// JoinBase applies an xml:base to an href. An href with a scheme points
// elsewhere and is kept as it is; with a base that has one, the result
// points elsewhere too.
func JoinBase(base, href string) string {
	switch {
	case base == "" || href == "" || strings.Contains(href, ":"):
		return href
	case strings.Contains(base, ":"):
		return strings.TrimSuffix(base, "/") + "/" + href
	}
	return path.Join(base, href)
}

// PackagePath resolves an href in the document doc to the path of a file
// in the package. ok is false for a reference to another host, a fragment,
// an invalid href, or a path that leaves the package.
func PackagePath(doc, href string) (target string, ok bool) {
	target, local, inside := resolve(doc, href)
	return target, local && inside
}

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
