package qti

import (
	"fmt"
	"net/url"
	"path"
	"slices"
	"sort"
	"strings"
)

// RefKind is what a reference in a package document points at.
type RefKind int

// The kinds of references.
const (
	RefManifestFile     RefKind = iota // a resource or file in the manifest
	RefItem                            // qti-assessment-item-ref: an item document
	RefSection                         // qti-assessment-section-ref: a section document
	RefStimulus                        // qti-assessment-stimulus-ref: a stimulus document
	RefAsset                           // media, a stylesheet or an included file
	RefTemplateLocation                // where a response processing template can be fetched
	RefModule                          // a PCI module; ".js" may be left out
	RefModuleConfig                    // a PCI module configuration
	RefCatalogFile                     // a file of a catalog card
)

// Reference is a reference from a document to another file in its package.
type Reference struct {
	Kind         RefKind
	Href         string // as written, relative to the document; for RefManifestFile with xml:base applied
	Fallback     string // RefModule: the fallback-path, used when Href does not resolve
	Element      string // the element that holds it, for messages
	Line, Column int
}

// ItemRef is a qti-assessment-item-ref of a test or section.
type ItemRef struct {
	Identifier string
	Href       string
}

// VariableRef is a reference in a test to a variable of one of its items,
// written "ITEM.VARIABLE".
type VariableRef struct {
	Item, Variable string
	Line, Column   int
}

// DocumentReferences is what a package document refers to and declares.
type DocumentReferences struct {
	Root         string // the root element's local name
	References   []Reference
	ItemRefs     []ItemRef     // tests and sections
	VariableRefs []VariableRef // tests
	Declared     []string      // items: the identifiers of declared variables
}

// PackageDocument is a validated XML document of a package.
type PackageDocument struct {
	Schema string // its document type; empty when it is not a QTI document
	Refs   DocumentReferences
}

// PackageContents is what the reference checks need of a package: the names
// of all its files, and the references of its XML documents.
type PackageContents struct {
	Files     []string
	Documents map[string]PackageDocument // by file name
}

// ReferenceFinding is a finding of the reference checks. File is the
// document the reference is in, or for an unlisted file that file.
type ReferenceFinding struct {
	Finding
	Warning bool
}

// The ids of the reference checks, as docs/additional-checks.md lists them.
const (
	CheckManifestFileExists     = "manifest-file-exists"
	CheckFileInManifest         = "file-in-manifest"
	CheckItemRefExists          = "item-ref-exists"
	CheckStimulusRefExists      = "stimulus-ref-exists"
	CheckAssetExists            = "asset-exists"
	CheckTemplateLocationExists = "template-location-exists"
	CheckPCIModuleExists        = "pci-module-exists"
	CheckCatalogFileExists      = "catalog-file-exists"
	CheckTestItemVariable       = "test-item-variable-declared"
)

// builtinVariables are declared implicitly in every item.
var builtinVariables = map[string]bool{"numAttempts": true, "duration": true, "completionStatus": true}

// CheckReferences checks that every reference in a package resolves to a
// file of the package, of the right document type where that matters, that
// every file is listed in the manifest, and that a test refers only to
// variables its items declare. References to other hosts (URLs with a
// scheme) are not checked.
func CheckReferences(p PackageContents) []ReferenceFinding {
	c := refChecker{files: map[string]bool{}, p: p}
	for _, f := range p.Files {
		c.files[f] = true
	}
	names := make([]string, 0, len(p.Documents))
	for name := range p.Documents {
		names = append(names, name)
	}
	sort.Strings(names)
	listed := map[string]bool{}
	_, hasManifest := p.Documents[ManifestName]
	for _, name := range names {
		checked := map[string]bool{} // manifest files, which a resource and its file both name
		for _, ref := range p.Documents[name].Refs.References {
			target, local, inside := resolve(name, ref.Href)
			if ref.Kind == RefManifestFile {
				if checked[target] {
					continue
				}
				checked[target] = true
				if local && inside {
					listed[target] = true
				}
			}
			c.check(name, ref, target, local, inside)
		}
	}
	c.checkVariables(names)
	if hasManifest {
		for _, f := range p.Files {
			if f != ManifestName && !listed[f] {
				c.add(f, 0, 0, CheckFileInManifest, true, "%s is in the package but not listed in %s.", f, ManifestName)
			}
		}
	}
	return c.out
}

type refChecker struct {
	p     PackageContents
	files map[string]bool
	out   []ReferenceFinding
}

func (c *refChecker) add(file string, line, col int, check string, warning bool, format string, args ...any) {
	c.out = append(c.out, ReferenceFinding{Warning: warning, Finding: Finding{
		Code: CodeReference, Rule: check, File: file, Line: line, Column: col, Message: fmt.Sprintf(format, args...),
	}})
}

// rule is how one kind of reference is checked.
type rule struct {
	check   string
	warning bool
	schema  string // the document type the target must be; empty for any file
}

var rules = map[RefKind]rule{
	RefManifestFile:     {check: CheckManifestFileExists},
	RefItem:             {check: CheckItemRefExists, schema: "qti-assessment-item"},
	RefSection:          {check: CheckItemRefExists, schema: "qti-assessment-section"},
	RefStimulus:         {check: CheckStimulusRefExists, schema: "qti-assessment-stimulus"},
	RefAsset:            {check: CheckAssetExists},
	RefTemplateLocation: {check: CheckTemplateLocationExists, warning: true},
	RefModule:           {check: CheckPCIModuleExists},
	RefModuleConfig:     {check: CheckPCIModuleExists},
	RefCatalogFile:      {check: CheckCatalogFileExists},
}

func (c *refChecker) check(from string, ref Reference, target string, local, inside bool) {
	if !local {
		return
	}
	r := rules[ref.Kind]
	if !inside {
		c.add(from, ref.Line, ref.Column, r.check, r.warning, "%s refers to %q, which is outside the package.", ref.Element, ref.Href)
		return
	}
	if !c.exists(ref.Kind, target) {
		if ref.Kind == RefModule && ref.Fallback != "" {
			if fb, local, inside := resolve(from, ref.Fallback); !local || inside && c.exists(ref.Kind, fb) {
				c.add(from, ref.Line, ref.Column, r.check, true,
					"%s: primary-path %q is not in the package; the fallback-path %q is used.", ref.Element, ref.Href, ref.Fallback)
				return
			}
			c.add(from, ref.Line, ref.Column, r.check, false,
				"%s: neither primary-path %q nor fallback-path %q is in the package.", ref.Element, ref.Href, ref.Fallback)
			return
		}
		c.add(from, ref.Line, ref.Column, r.check, r.warning, "%s refers to %q, which is not in the package.", ref.Element, ref.Href)
		return
	}
	if r.schema != "" {
		doc := c.p.Documents[target]
		// An item may also be an LTI link, as the QTI 3 Implementation Guide
		// describes in "Package with a Test and Items with LTI resources".
		if ref.Kind == RefItem && doc.Refs.Root == "cartridge_basiclti_link" {
			return
		}
		if got := doc.Schema; got != r.schema {
			what := "not a QTI document"
			if got != "" {
				what = "a " + got
			}
			c.add(from, ref.Line, ref.Column, r.check, false, "%s refers to %q, which is %s, not a %s.", ref.Element, ref.Href, what, r.schema)
		}
	}
}

// exists reports whether a file is in the package; a PCI module may be
// named without its ".js".
func (c *refChecker) exists(kind RefKind, target string) bool {
	return c.files[target] || kind == RefModule && c.files[target+".js"]
}

// checkVariables checks the "ITEM.VARIABLE" references of tests against the
// variables the referenced items declare. Item-ref identifiers are unique
// only within one test, so each test is checked against its own item refs:
// those in the test itself and in the sections it includes with
// qti-assessment-section-ref, however deeply nested.
func (c *refChecker) checkVariables(names []string) {
	for _, name := range names {
		vrs := c.p.Documents[name].Refs.VariableRefs
		if len(vrs) == 0 {
			continue
		}
		items := c.testItems(name)
		for _, vr := range vrs {
			file := items[vr.Item]
			doc, found := c.p.Documents[file]
			if file == "" || !found || doc.Schema != "qti-assessment-item" || builtinVariables[vr.Variable] {
				// Not an item, an item reference that does not resolve
				// (reported above), or an identifier that names different
				// items in the test and its sections (not unique, so which
				// item is meant is unknown).
				continue
			}
			if !slices.Contains(doc.Refs.Declared, vr.Variable) {
				c.add(name, vr.Line, vr.Column, CheckTestItemVariable, false,
					"qti-variable refers to %s.%s, but %s declares no variable %s.", vr.Item, vr.Variable, file, vr.Variable)
			}
		}
	}
}

// testItems maps the item-ref identifiers of a test to the item files they
// refer to: the item refs of the test and of the section documents it
// includes. An identifier that refers to different files maps to "".
func (c *refChecker) testItems(test string) map[string]string {
	items := map[string]string{}
	visited := map[string]bool{}
	queue := []string{test}
	for len(queue) > 0 {
		doc := queue[0]
		queue = queue[1:]
		if visited[doc] {
			continue
		}
		visited[doc] = true
		refs := c.p.Documents[doc].Refs
		for _, ir := range refs.ItemRefs {
			target, local, inside := resolve(doc, ir.Href)
			if !local || !inside {
				continue
			}
			if prev, ok := items[ir.Identifier]; ok && prev != target {
				target = ""
			}
			items[ir.Identifier] = target
		}
		for _, ref := range refs.References {
			if ref.Kind != RefSection {
				continue
			}
			if target, local, inside := resolve(doc, ref.Href); local && inside && c.p.Documents[target].Schema == "qti-assessment-section" {
				queue = append(queue, target)
			}
		}
	}
	return items
}

// resolve resolves href against the document it is in. local is false for a
// reference to another host, a fragment or a data URI; inside is false when
// the path leaves the package.
func resolve(doc, href string) (target string, local, inside bool) {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return "", false, false
	}
	u, err := url.Parse(href)
	if err != nil {
		return "", true, false
	}
	if u.Scheme != "" || u.Host != "" {
		return "", false, false
	}
	p := path.Join(path.Dir(doc), u.Path)
	if p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(u.Path, "/") {
		return p, true, false
	}
	return p, true, true
}
