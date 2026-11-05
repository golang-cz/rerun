package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// jsonField is what a click on an expansion row can pick: the key's yq path and
// name, and a scalar value's yq literal. Spans are cells within the pane row.
type jsonField struct {
	keyX, valX [2]int
	path       string
	key        string
	lit        string
}

// Scalars are tinted so a click target reads without relying on color.
const (
	sgrValOn  = "\x1b[36m"
	sgrValOff = "\x1b[39m"
)

// maxBlockRows bounds one expansion, so a huge object cannot flood the pane.
const maxBlockRows = 500

var identKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Quoted before YAML can read it back as a number, in any base.
var yamlNumber = regexp.MustCompile(`^(0[xX][0-9a-fA-F_]+|0[oO][0-7_]+|0[bB][01_]+|[-+]?(\.[0-9_]+|[0-9][0-9_]*(\.[0-9_]*)?)([eE][-+]?[0-9_]+)?)$`)

var yamlDate = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}`)

func isJSONObject(line string) bool {
	s := strings.TrimSpace(line)
	return strings.HasPrefix(s, "{") && json.Valid([]byte(s))
}

// jsonBlock renders a JSON object line as YAML, keys in document order. Every
// row sits under the line's message column at base.
func jsonBlock(line string, base int) ([]dispRow, bool) {
	if !isJSONObject(line) {
		return nil, false
	}
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	root, err := decodeNode(dec)
	if err != nil {
		return nil, false
	}
	w := &yamlWriter{}
	if root.isEmpty() {
		w.add(spaces(base)+"{}", nil)
	} else {
		w.mapping(root, base, "", spaces(base))
	}
	rows := w.rows
	if len(rows) > maxBlockRows {
		hidden := len(rows) - maxBlockRows
		rows = append(rows[:maxBlockRows], dispRow{text: spaces(base) + fmt.Sprintf("… %d more lines", hidden)})
	}
	for i := range rows {
		rows[i].sub = i
	}
	return rows, true
}

type yNode struct {
	kind  json.Delim // '{' or '[' for a container; zero for a scalar
	tok   json.Token
	keys  []string
	items []*yNode
}

func (n *yNode) isScalar() bool { return n.kind == 0 }

func (n *yNode) isEmpty() bool { return len(n.items) == 0 }

func decodeNode(dec *json.Decoder) (*yNode, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return &yNode{tok: tok}, nil
	}
	n := &yNode{kind: d}
	for dec.More() {
		if d == '{' {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			k, _ := kt.(string)
			n.keys = append(n.keys, k)
		}
		child, err := decodeNode(dec)
		if err != nil {
			return nil, err
		}
		n.items = append(n.items, child)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return n, nil
}

type yamlWriter struct {
	rows []dispRow
}

func (w *yamlWriter) add(text string, f *jsonField) {
	w.rows = append(w.rows, dispRow{sub: len(w.rows), text: text, field: f})
}

func (w *yamlWriter) mapping(n *yNode, col int, path, first string) {
	pre := first
	for i, k := range n.keys {
		child := n.items[i]
		cp := yqKeyPath(path, k)
		kt := yamlKeyText(k)
		keyX := [2]int{col, col + displayWidth(kt)}
		head := pre + kt
		pre = spaces(col)
		switch {
		case child.isScalar():
			w.scalar(head+": ", col, child, cp, k, keyX)
		case child.isEmpty():
			w.add(head+": "+emptyText(child), &jsonField{keyX: keyX, path: cp, key: k})
		default:
			w.add(head+":", &jsonField{keyX: keyX, path: cp, key: k})
			w.nested(child, col+2, cp)
		}
	}
}

func (w *yamlWriter) sequence(n *yNode, col int, path, first string) {
	pre := first
	for i, child := range n.items {
		ip := path + "[" + strconv.Itoa(i) + "]"
		head := pre + "- "
		pre = spaces(col)
		switch {
		case child.isScalar():
			w.scalar(head, col, child, ip, "", [2]int{})
		case child.isEmpty():
			w.add(head+emptyText(child), nil)
		case child.kind == '{':
			w.mapping(child, col+2, ip, head)
		default:
			w.sequence(child, col+2, ip, head)
		}
	}
}

func (w *yamlWriter) nested(n *yNode, col int, path string) {
	if n.kind == '{' {
		w.mapping(n, col, path, spaces(col))
		return
	}
	w.sequence(n, col, path, spaces(col))
}

// A block body indents from col, so it stays inside the key or dash that owns it.
func (w *yamlWriter) scalar(head string, col int, n *yNode, path, key string, keyX [2]int) {
	lit := yqLiteral(n.tok)
	if s, ok := n.tok.(string); ok && strings.Contains(s, "\n") && !hasTerminalControl(s) {
		header, body := yamlBlock(s)
		start := displayWidth(head)
		w.add(head+header, &jsonField{keyX: keyX, valX: [2]int{start, start + displayWidth(header)}, path: path, key: key, lit: lit})
		for _, line := range body {
			if line == "" {
				w.add("", nil)
				continue
			}
			w.add(spaces(col+2)+line, nil)
		}
		return
	}
	text := yamlScalar(n.tok)
	start := displayWidth(head)
	w.add(head+sgrValOn+text+sgrValOff, &jsonField{keyX: keyX, valX: [2]int{start, start + displayWidth(text)}, path: path, key: key, lit: lit})
}

// The chomping marker is what keeps a string's trailing newlines exact.
func yamlBlock(s string) (string, []string) {
	trimmed := strings.TrimSuffix(s, "\n")
	body := strings.Split(trimmed, "\n")
	chomp := "-"
	switch {
	case trimmed == s:
	case trimmed == "" || strings.HasSuffix(trimmed, "\n"):
		chomp = "+"
	default:
		chomp = ""
	}
	indent := ""
	for _, line := range body {
		if line != "" {
			if strings.HasPrefix(line, " ") {
				indent = "2"
			}
			break
		}
	}
	return "|" + indent + chomp, body
}

func yamlScalar(tok json.Token) string {
	if s, ok := tok.(string); ok {
		if yamlNeedsQuote(s) {
			return jsonQuote(s)
		}
		return s
	}
	return yqLiteral(tok)
}

func yamlKeyText(k string) string {
	if yamlNeedsQuote(k) {
		return jsonQuote(k)
	}
	return k
}

// Plain text that YAML would read as another type must be quoted.
func yamlNeedsQuote(s string) bool {
	if s == "" || strings.TrimSpace(s) != s {
		return true
	}
	if strings.ContainsAny(s[:1], "-?:,[]{}#&*!|>'\"%@`.") {
		return true
	}
	if strings.Contains(s, ": ") || strings.Contains(s, " #") || strings.HasSuffix(s, ":") {
		return true
	}
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x2028 || r == 0x2029 || r == 0xfeff {
			return true
		}
	}
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "y", "n", "null", "~":
		return true
	}
	return yamlNumber.MatchString(s) || yamlDate.MatchString(s)
}

func hasTerminalControl(s string) bool {
	for _, r := range s {
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return true
		}
	}
	return false
}

func emptyText(n *yNode) string {
	if n.kind == '{' {
		return "{}"
	}
	return "[]"
}

func spaces(n int) string { return strings.Repeat(" ", n) }

func yqKeyPath(path, key string) string {
	if identKey.MatchString(key) {
		return path + "." + key
	}
	if path == "" {
		return ".[" + jsonQuote(key) + "]"
	}
	return path + "[" + jsonQuote(key) + "]"
}

func yqLiteral(tok json.Token) string {
	switch v := tok.(type) {
	case string:
		return jsonQuote(v)
	case json.Number:
		return v.String()
	case bool:
		if v {
			return "true"
		}
		return "false"
	}
	return "null"
}

// The JSON encoder leaves C1 controls raw, which a terminal would act on.
func jsonQuote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return fmt.Sprintf("%q", s)
	}
	out := strings.TrimSuffix(b.String(), "\n")
	var c strings.Builder
	for _, r := range out {
		if r >= 0x7f && r <= 0x9f {
			fmt.Fprintf(&c, `\u%04x`, r)
			continue
		}
		c.WriteRune(r)
	}
	return c.String()
}
