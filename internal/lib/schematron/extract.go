package schematron

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// NamespaceSchematron is the ISO Schematron namespace.
const NamespaceSchematron = "http://purl.oclc.org/dsdl/schematron"

// Extract reads a schema file and returns the ISO Schematron rules in it,
// wherever they appear: a standalone sch:schema, or rules embedded in an
// XSD's xs:appinfo. A file without rules gives a RuleSet without patterns.
func Extract(source string, r io.Reader) (RuleSet, error) { //nolint:gocognit // one streaming pass over the ISO elements; tested against the reference implementation
	rs := RuleSet{Source: source, Namespaces: map[string]string{}, Diagnostics: map[string][]MessagePart{}}
	dec := xml.NewDecoder(r)
	var (
		pattern *Pattern
		rule    *Rule
		message *[]MessagePart // the assertion or diagnostic being read
		diagID  string
		diag    []MessagePart
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return rs, fmt.Errorf("%s: %w", source, err)
		}
		line, _ := dec.InputPos()
		fail := func(format string, args ...any) error {
			return fmt.Errorf("%s:%d: %s", source, line, fmt.Sprintf(format, args...))
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != NamespaceSchematron {
				if message != nil {
					return rs, fail("element %s in a Schematron message is not supported", t.Name.Local)
				}
				continue
			}
			switch t.Name.Local {
			case "schema":
				rs.QueryBinding = attr(t, "queryBinding")
			case "title", "p", "phase":
				// Documentation, and phases: all patterns always run.
				if err := dec.Skip(); err != nil {
					return rs, err
				}
			case "ns":
				prefix, uri := attr(t, "prefix"), attr(t, "uri")
				if old, ok := rs.Namespaces[prefix]; ok && old != uri {
					return rs, fail("prefix %q bound to both %s and %s", prefix, old, uri)
				}
				rs.Namespaces[prefix] = uri
			case "let":
				name, value := attr(t, "name"), attr(t, "value")
				if name == "" || value == "" {
					return rs, fail("sch:let needs name and value attributes")
				}
				let := Let{Name: name, Value: normalize(value)}
				switch {
				case rule != nil:
					rule.Lets = append(rule.Lets, let)
				case pattern != nil:
					pattern.Lets = append(pattern.Lets, let)
				default:
					rs.Lets = append(rs.Lets, let)
				}
			case "pattern":
				rs.Patterns = append(rs.Patterns, Pattern{
					ID:       attr(t, "id"),
					Abstract: attr(t, "abstract") == "true",
					IsA:      attr(t, "is-a"),
				})
				pattern = &rs.Patterns[len(rs.Patterns)-1]
			case "param":
				if pattern == nil || pattern.IsA == "" {
					return rs, fail("sch:param outside a pattern with is-a")
				}
				if pattern.Params == nil {
					pattern.Params = map[string]string{}
				}
				pattern.Params[attr(t, "name")] = attr(t, "value")
			case "rule":
				if pattern == nil {
					return rs, fail("sch:rule outside sch:pattern")
				}
				r := Rule{ID: attr(t, "id"), Abstract: attr(t, "abstract") == "true", Context: normalize(attr(t, "context"))}
				if !r.Abstract && r.Context == "" {
					return rs, fail("sch:rule without context")
				}
				pattern.Rules = append(pattern.Rules, r)
				rule = &pattern.Rules[len(pattern.Rules)-1]
			case "extends":
				if rule == nil || attr(t, "rule") == "" {
					return rs, fail("sch:extends needs a rule attribute and must be inside sch:rule")
				}
				rule.Extends = append(rule.Extends, attr(t, "rule"))
			case "assert", "report":
				if rule == nil {
					return rs, fail("sch:%s outside sch:rule", t.Name.Local)
				}
				rule.Assertions = append(rule.Assertions, Assertion{
					ID:          attr(t, "id"),
					Report:      t.Name.Local == "report",
					Test:        normalize(attr(t, "test")),
					Role:        attr(t, "role"),
					Flag:        attr(t, "flag"),
					Diagnostics: strings.Fields(attr(t, "diagnostics")),
				})
				message = &rule.Assertions[len(rule.Assertions)-1].Message
			case "diagnostics":
			case "diagnostic":
				id := attr(t, "id")
				if id == "" {
					return rs, fail("sch:diagnostic without id")
				}
				diagID, diag = id, nil
				message = &diag
			case "value-of", "name":
				if message == nil {
					return rs, fail("sch:%s outside a message", t.Name.Local)
				}
				sel := attr(t, "select")
				if t.Name.Local == "name" {
					path := attr(t, "path")
					if path == "" {
						path = "."
					}
					sel = "name(" + path + ")"
				}
				*message = append(*message, MessagePart{Select: sel})
			case "emph", "dir", "span":
				// Inline markup: its text becomes part of the message.
				if message == nil {
					return rs, fail("sch:%s outside a message", t.Name.Local)
				}
			default:
				return rs, fail("unsupported Schematron element sch:%s", t.Name.Local)
			}
		case xml.EndElement:
			if t.Name.Space != NamespaceSchematron {
				continue
			}
			switch t.Name.Local {
			case "pattern":
				pattern = nil
			case "rule":
				rule = nil
			case "assert", "report":
				*message = normalizeMessage(*message)
				message = nil
			case "diagnostic":
				rs.Diagnostics[diagID] = normalizeMessage(diag)
				message = nil
			}
		case xml.CharData:
			if message != nil {
				*message = append(*message, MessagePart{Text: string(t)})
			}
		}
	}
	return rs, nil
}

func attr(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Space == "" && a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// normalizeMessage collapses whitespace runs in the text parts to one space
// and trims the message as a whole.
func normalizeMessage(parts []MessagePart) []MessagePart {
	var out []MessagePart
	for _, p := range parts {
		if p.Select == "" {
			p.Text = collapseSpace(p.Text)
			if n := len(out); n > 0 && out[n-1].Select == "" {
				out[n-1].Text = collapseSpace(out[n-1].Text + p.Text)
				continue
			}
		}
		out = append(out, p)
	}
	if n := len(out); n > 0 && out[0].Select == "" {
		out[0].Text = strings.TrimLeft(out[0].Text, " ")
	}
	if n := len(out); n > 0 && out[n-1].Select == "" {
		out[n-1].Text = strings.TrimRight(out[n-1].Text, " ")
	}
	kept := out[:0]
	for _, p := range out {
		if p.Select != "" || p.Text != "" {
			kept = append(kept, p)
		}
	}
	return kept
}

// collapseSpace replaces every run of XML whitespace by one space.
func collapseSpace(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			space = true
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	if space {
		b.WriteByte(' ')
	}
	return b.String()
}
