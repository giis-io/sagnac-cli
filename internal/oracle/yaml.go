package oracle

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Reader of the YAML subset of meta.yaml (docs/contrat.md, section 2), the
// same as the reader of the reference executor:
//   - block mappings (`key: value`, `key:` followed by an indented block);
//   - block lists (`- value`, `- key: value` for a list of mappings);
//   - inline lists (`[a, "b", 3]`) and the empty mapping `{}`;
//   - scalars: plain, double-quoted (JSON escapes), single-quoted strings,
//     integers, true/false, null/~; `#` comments.
//
// Anything else (anchors, `|` and `>` blocks, non-empty inline mappings,
// several documents) is an error instead of a misreading. Values are
// map[string]any, []any, string, int64, bool or nil.

type yamlError struct {
	line   int
	reason string
}

func (e *yamlError) Error() string {
	return "meta.yaml line " + strconv.Itoa(e.line) + ": " + e.reason
}

// pyWhitespace matches what Python's str.isspace and re `\s` accept.
const pyWhitespace = `\t\n\v\f\r \x{1c}-\x{1f}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

var (
	yamlInteger = regexp.MustCompile(`^[-+]?[0-9]+$`)
	yamlKey     = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9_.-]*)[` + pyWhitespace + `]*:(?:[` + pyWhitespace + `]+(.*))?$`)
)

func isPySpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

func pyStrip(s string) string  { return strings.TrimFunc(s, isPySpace) }
func pyLStrip(s string) string { return strings.TrimLeftFunc(s, isPySpace) }
func pyRStrip(s string) string { return strings.TrimRightFunc(s, isPySpace) }

// pySplitLines splits like Python's str.splitlines.
func pySplitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch r {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			lines = append(lines, s[start:i])
			start = i + size
		case '\r':
			lines = append(lines, s[start:i])
			if i+1 < len(s) && s[i+1] == '\n' {
				size++
			}
			start = i + size
		}
		i += size
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func withoutComment(line string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && inDouble:
			i++
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '#' && !inSingle && !inDouble && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			return pyRStrip(line[:i])
		}
	}
	return pyRStrip(line)
}

func splitInlineList(text string, num int) ([]any, error) {
	var elements []string
	var current strings.Builder
	var quote rune
	for _, c := range text {
		switch {
		case quote != 0:
			current.WriteRune(c)
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
			current.WriteRune(c)
		case c == ',':
			elements = append(elements, pyStrip(current.String()))
			current.Reset()
		case strings.ContainsRune("[]{}", c):
			return nil, &yamlError{num, "nested inline structure"}
		default:
			current.WriteRune(c)
		}
	}
	if quote != 0 {
		return nil, &yamlError{num, "unclosed quote"}
	}
	if last := pyStrip(current.String()); last != "" {
		elements = append(elements, last)
	} else if len(elements) > 0 {
		return nil, &yamlError{num, "empty element in an inline list"}
	}
	values := make([]any, 0, len(elements))
	for _, e := range elements {
		v, err := yamlScalar(e, num)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, nil
}

func yamlScalar(text string, num int) (any, error) {
	t := pyStrip(text)
	if t == "" {
		return nil, nil
	}
	switch {
	case strings.HasPrefix(t, `"`):
		if utf8.RuneCountInString(t) < 2 || !strings.HasSuffix(t, `"`) {
			return nil, &yamlError{num, "unclosed double quote"}
		}
		var s string
		if err := json.Unmarshal([]byte(t), &s); err != nil {
			return nil, &yamlError{num, "invalid double-quoted string"}
		}
		return s, nil
	case strings.HasPrefix(t, "'"):
		if utf8.RuneCountInString(t) < 2 || !strings.HasSuffix(t, "'") {
			return nil, &yamlError{num, "unclosed single quote"}
		}
		return strings.ReplaceAll(t[1:len(t)-1], "''", "'"), nil
	case strings.HasPrefix(t, "["):
		if !strings.HasSuffix(t, "]") {
			return nil, &yamlError{num, "unclosed inline list"}
		}
		return splitInlineList(t[1:len(t)-1], num)
	case t == "{}":
		return map[string]any{}, nil
	case strings.ContainsAny(t[:1], "{&*!|>%@`"):
		return nil, &yamlError{num, "unsupported YAML syntax"}
	case yamlInteger.MatchString(t):
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			return nil, &yamlError{num, "integer out of range"}
		}
		return n, nil
	case t == "true" || t == "false":
		return t == "true", nil
	case t == "null" || t == "~":
		return nil, nil
	case strings.Contains(t, ": ") || strings.HasSuffix(t, ":"):
		return nil, &yamlError{num, "unexpected mapping in a value"}
	}
	return t, nil
}

type yamlLine struct {
	indent  int
	content string
	num     int
}

type yamlReader struct {
	lines []yamlLine
	i     int
}

func isListItem(content string) bool { return content == "-" || strings.HasPrefix(content, "- ") }

func (r *yamlReader) block(indent int) (any, error) {
	if isListItem(r.lines[r.i].content) {
		return r.list(r.lines[r.i].indent)
	}
	return r.mapping(r.lines[r.i].indent)
}

func (r *yamlReader) mapping(indent int) (any, error) {
	result := map[string]any{}
	for r.i < len(r.lines) {
		l := r.lines[r.i]
		if l.indent < indent {
			break
		}
		if l.indent > indent {
			return nil, &yamlError{l.num, "unexpected indentation"}
		}
		if isListItem(l.content) {
			break
		}
		m := yamlKey.FindStringSubmatch(l.content)
		if m == nil {
			return nil, &yamlError{l.num, "key: value expected"}
		}
		key, value := m[1], m[2]
		if _, dup := result[key]; dup {
			return nil, &yamlError{l.num, "duplicate key"}
		}
		r.i++
		var v any
		var err error
		if value == "" {
			v, err = r.child(indent)
		} else {
			v, err = yamlScalar(value, l.num)
		}
		if err != nil {
			return nil, err
		}
		result[key] = v
	}
	return result, nil
}

func (r *yamlReader) child(indent int) (any, error) {
	if r.i >= len(r.lines) {
		return nil, nil
	}
	l := r.lines[r.i]
	if l.indent > indent || (l.indent == indent && isListItem(l.content)) {
		return r.block(l.indent)
	}
	return nil, nil
}

func (r *yamlReader) list(indent int) (any, error) {
	result := []any{}
	for r.i < len(r.lines) {
		l := r.lines[r.i]
		if l.indent < indent {
			break
		}
		if l.indent > indent {
			return nil, &yamlError{l.num, "unexpected indentation in a list"}
		}
		if !isListItem(l.content) {
			break
		}
		rest := pyStrip(l.content[1:])
		var v any
		var err error
		switch {
		case rest == "":
			r.i++
			v, err = r.child(indent)
		case yamlKey.MatchString(rest) && !strings.HasPrefix(rest, "'") && !strings.HasPrefix(rest, `"`):
			// "- key: value" starts a mapping indented two more columns.
			r.lines[r.i] = yamlLine{indent + 2, rest, l.num}
			v, err = r.mapping(indent + 2)
		default:
			r.i++
			v, err = yamlScalar(rest, l.num)
		}
		if err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, nil
}

// parseYAML reads the subset described above.
func parseYAML(text string) (any, error) {
	r := &yamlReader{}
	for n, raw := range pySplitLines(text) {
		num := n + 1
		lead := raw[:len(raw)-len(pyLStrip(raw))]
		if strings.Contains(lead, "\t") {
			return nil, &yamlError{num, "tab in indentation"}
		}
		content := withoutComment(raw)
		stripped := pyStrip(content)
		if stripped == "" {
			continue
		}
		if stripped == "---" || stripped == "..." {
			if len(r.lines) > 0 {
				return nil, &yamlError{num, "several documents"}
			}
			continue
		}
		indent := utf8.RuneCountInString(content) - utf8.RuneCountInString(pyLStrip(content))
		r.lines = append(r.lines, yamlLine{indent, stripped, num})
	}
	if len(r.lines) == 0 {
		return nil, nil
	}
	v, err := r.block(r.lines[0].indent)
	if err != nil {
		return nil, err
	}
	if r.i < len(r.lines) {
		return nil, &yamlError{r.lines[r.i].num, "content not read (indentation?)"}
	}
	return v, nil
}
