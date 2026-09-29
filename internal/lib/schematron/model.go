// Package schematron runs ISO Schematron rules, such as those 1EdTech embeds
// in the xs:appinfo of the QTI 3 XSDs.
//
// Rules are extracted from schema files at build time (Extract) and stored
// as JSON. At startup they are compiled once (Compile); each document is
// then parsed into an XPath DOM and checked (Engine.Validate).
//
// Supported is ISO Schematron with the default query binding (XSLT 1.0
// patterns and XPath 1.0 expressions, plus current() and generate-id()):
// ns, let, pattern (including abstract patterns with param), rule (including
// abstract rules and extends), assert, report, value-of, name, diagnostics,
// and the inline elements emph, dir and span. Phases are not selected: all
// patterns run. Anything else, such as another query binding, include,
// key() or document(), fails extraction or compilation instead of being
// skipped.
package schematron

// RuleSet holds the rules of one schema file.
type RuleSet struct {
	Source       string                   `json:"source"` // schema URL
	QueryBinding string                   `json:"query_binding,omitempty"`
	Namespaces   map[string]string        `json:"namespaces"` // prefix -> namespace URI
	Lets         []Let                    `json:"lets,omitempty"`
	Patterns     []Pattern                `json:"patterns"`
	Diagnostics  map[string][]MessagePart `json:"diagnostics,omitempty"`
}

// Pattern is a group of rules. Within a pattern each node is checked by the
// first rule whose context matches it.
type Pattern struct {
	ID       string            `json:"id,omitempty"`
	Abstract bool              `json:"abstract,omitempty"`
	IsA      string            `json:"is_a,omitempty"`   // instance of this abstract pattern
	Params   map[string]string `json:"params,omitempty"` // for IsA
	Lets     []Let             `json:"lets,omitempty"`
	Rules    []Rule            `json:"rules,omitempty"`
}

// Rule checks its assertions on every node its context matches. An
// abstract rule has no context and is used through Extends.
type Rule struct {
	ID         string      `json:"id,omitempty"`
	Abstract   bool        `json:"abstract,omitempty"`
	Context    string      `json:"context,omitempty"`
	Extends    []string    `json:"extends,omitempty"`
	Lets       []Let       `json:"lets,omitempty"`
	Assertions []Assertion `json:"assertions,omitempty"`
}

// Assertion is a sch:assert, which fails when Test is false, or a
// sch:report, which fires when Test is true.
type Assertion struct {
	ID          string        `json:"id,omitempty"`
	Report      bool          `json:"report,omitempty"`
	Test        string        `json:"test"`
	Role        string        `json:"role,omitempty"`
	Flag        string        `json:"flag,omitempty"`
	Diagnostics []string      `json:"diagnostics,omitempty"`
	Message     []MessagePart `json:"message"`
}

// Let binds a variable to the value of an XPath expression.
type Let struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// MessagePart is literal text, or an XPath expression (sch:value-of,
// sch:name) evaluated on the context node.
type MessagePart struct {
	Text   string `json:"text,omitempty"`
	Select string `json:"select,omitempty"`
}
