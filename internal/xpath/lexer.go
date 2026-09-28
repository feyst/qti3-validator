package xpath

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type tokenKind uint8

const (
	tokEOF      tokenKind = iota
	tokPunct              // ( ) [ ] . .. @ , ::
	tokOperator           // and or mod div / // | + - = != < <= > >= *
	tokNameTest           // * | NCName:* | QName
	tokNodeType           // comment text processing-instruction node, followed by (
	tokFunction           // QName followed by (
	tokAxis               // NCName followed by ::
	tokLiteral
	tokNumber
	tokVariable // $QName, value without $
)

type token struct {
	kind tokenKind
	text string
	num  float64
	pos  int
}

// lex splits an expression into tokens, applying the disambiguation rules
// of XPath 1.0 section 3.7.
func lex(expr string) ([]token, error) {
	var toks []token
	i := 0
	// operatorContext reports whether the previous token forces "*" to be
	// the multiply operator and an NCName to be an operator name.
	operatorContext := func() bool {
		if len(toks) == 0 {
			return false
		}
		p := toks[len(toks)-1]
		switch p.kind {
		case tokOperator:
			return false
		case tokPunct:
			return p.text == ")" || p.text == "]" || p.text == "." || p.text == ".."
		}
		return true
	}
	for {
		for i < len(expr) && isSpace(expr[i]) {
			i++
		}
		if i >= len(expr) {
			toks = append(toks, token{kind: tokEOF, pos: i})
			return toks, nil
		}
		start := i
		c := expr[i]
		switch {
		case c == '(' || c == ')' || c == '[' || c == ']' || c == ',' || c == '@':
			toks = append(toks, token{kind: tokPunct, text: string(c), pos: start})
			i++
		case c == ':' && i+1 < len(expr) && expr[i+1] == ':':
			toks = append(toks, token{kind: tokPunct, text: "::", pos: start})
			i += 2
		case c == '.' && i+1 < len(expr) && expr[i+1] == '.':
			toks = append(toks, token{kind: tokPunct, text: "..", pos: start})
			i += 2
		case c == '.' && (i+1 >= len(expr) || !isDigit(expr[i+1])):
			toks = append(toks, token{kind: tokPunct, text: ".", pos: start})
			i++
		case isDigit(c) || c == '.':
			for i < len(expr) && isDigit(expr[i]) {
				i++
			}
			if i < len(expr) && expr[i] == '.' {
				i++
				for i < len(expr) && isDigit(expr[i]) {
					i++
				}
			}
			n, err := strconv.ParseFloat(expr[start:i], 64)
			if err != nil {
				return nil, fmt.Errorf("invalid number at %d", start)
			}
			toks = append(toks, token{kind: tokNumber, num: n, pos: start})
		case c == '"' || c == '\'':
			end := strings.IndexByte(expr[i+1:], c)
			if end < 0 {
				return nil, fmt.Errorf("unterminated string literal at %d", start)
			}
			toks = append(toks, token{kind: tokLiteral, text: expr[i+1 : i+1+end], pos: start})
			i += end + 2
		case c == '$':
			i++
			name, n := scanQName(expr[i:])
			if n == 0 {
				return nil, fmt.Errorf("invalid variable reference at %d", start)
			}
			i += n
			toks = append(toks, token{kind: tokVariable, text: name, pos: start})
		case c == '*':
			kind := tokNameTest
			if operatorContext() {
				kind = tokOperator
			}
			toks = append(toks, token{kind: kind, text: "*", pos: start})
			i++
		case c == '/' || c == '|' || c == '+' || c == '-' || c == '=' || c == '!' || c == '<' || c == '>':
			op := string(c)
			if i+1 < len(expr) && (c == '/' && expr[i+1] == '/' || (c == '!' || c == '<' || c == '>') && expr[i+1] == '=') {
				op += string(expr[i+1])
			}
			if op == "!" {
				return nil, fmt.Errorf("unexpected '!' at %d", start)
			}
			toks = append(toks, token{kind: tokOperator, text: op, pos: start})
			i += len(op)
		default:
			name, n := scanNCName(expr[i:])
			if n == 0 {
				return nil, fmt.Errorf("unexpected character %q at %d", c, start)
			}
			i += n
			if operatorContext() {
				switch name {
				case "and", "or", "mod", "div":
					toks = append(toks, token{kind: tokOperator, text: name, pos: start})
					continue
				}
				return nil, fmt.Errorf("unexpected name %q at %d", name, start)
			}
			// NCName:* or a QName.
			if i+1 < len(expr) && expr[i] == ':' && expr[i+1] != ':' {
				if expr[i+1] == '*' {
					toks = append(toks, token{kind: tokNameTest, text: name + ":*", pos: start})
					i += 2
					continue
				}
				local, m := scanNCName(expr[i+1:])
				if m == 0 {
					return nil, fmt.Errorf("invalid qualified name at %d", start)
				}
				name += ":" + local
				i += 1 + m
			}
			j := i
			for j < len(expr) && isSpace(expr[j]) {
				j++
			}
			switch {
			case j < len(expr) && expr[j] == '(':
				kind := tokFunction
				switch name {
				case "comment", "text", "processing-instruction", "node":
					kind = tokNodeType
				}
				toks = append(toks, token{kind: kind, text: name, pos: start})
			case j+1 < len(expr) && expr[j] == ':' && expr[j+1] == ':':
				toks = append(toks, token{kind: tokAxis, text: name, pos: start})
			default:
				toks = append(toks, token{kind: tokNameTest, text: name, pos: start})
			}
		}
	}
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func scanQName(s string) (string, int) {
	name, n := scanNCName(s)
	if n == 0 {
		return "", 0
	}
	if n+1 < len(s) && s[n] == ':' {
		if local, m := scanNCName(s[n+1:]); m > 0 {
			return name + ":" + local, n + 1 + m
		}
	}
	return name, n
}

func scanNCName(s string) (string, int) {
	i := 0
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if i == 0 && !isNameStart(r) || i > 0 && !isNameChar(r) {
			break
		}
		i += size
	}
	return s[:i], i
}

func isNameStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

func isNameChar(r rune) bool {
	return isNameStart(r) || r == '-' || r == '.' || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r) ||
		unicode.Is(unicode.Mc, r) || r == 0xB7
}
