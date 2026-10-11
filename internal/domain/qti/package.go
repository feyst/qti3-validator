package qti

import (
	"net/url"
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

// IncludedFiles returns the files of a package that one of its documents
// includes as XML with XInclude: the fragments of the package, whether or
// not the manifest lists them as such.
func IncludedFiles(p PackageContents) map[string]bool {
	included := map[string]bool{}
	for name := range p.Documents {
		for _, ref := range p.Documents[name].Refs.References {
			if !ref.Include {
				continue
			}
			if target, ok := PackagePath(name, ref.Href); ok {
				included[target] = true
			}
		}
	}
	return included
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

// JoinBase applies an xml:base to an href, as RFC 3986 resolves a reference
// against a base, with one difference: the base is always a directory, also
// without a trailing slash, as packages write it both ways. So "items" and
// "items/" both make "a.xml" into "items/a.xml".
//
// An href with a scheme, or one that starts with "//", points elsewhere and
// is kept as it is, as is one that is only a fragment ("#id"); with a base that has a scheme, the result points
// elsewhere too. An href that starts with "/" replaces the path of the
// base. A scheme is what RFC 3986 says it is: "img/a:b.png" has none.
func JoinBase(base, href string) string {
	switch {
	case base == "" || href == "" || hasScheme(href) || strings.HasPrefix(href, "//") || strings.HasPrefix(href, "#"):
		return href
	case hasScheme(base) || strings.HasPrefix(base, "//"):
		b, errBase := url.Parse(strings.TrimSuffix(base, "/") + "/")
		h, errHref := url.Parse(href)
		if errBase != nil || errHref != nil {
			return strings.TrimSuffix(base, "/") + "/" + href // not a URI reference, which the reference checks report
		}
		return b.ResolveReference(h).String()
	case strings.HasPrefix(href, "/"):
		return href
	}
	return path.Join(base, href)
}

// hasScheme reports whether a URI reference starts with a scheme: a letter,
// then letters, digits, "+", "-" and ".", up to a ":" (RFC 3986, 3.1).
func hasScheme(ref string) bool {
	for i, c := range []byte(ref) {
		switch {
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return i > 0 && c == ':'
		}
	}
	return false
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
