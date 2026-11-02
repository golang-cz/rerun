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

// identKey is a key yq reads as `.key` without quoting.
var identKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// isJSONObject reports whether a raw line is a complete JSON object, the only
// kind of line that gets a (+) cell.
func isJSONObject(line string) bool {
	s := strings.TrimSpace(line)
	return strings.HasPrefix(s, "{") && json.Valid([]byte(s))
}

// jsonBlock pretty-prints a JSON object line, two spaces per level. Every row is
// indented base cells so it sits under the line's message column.
func jsonBlock(line string, base int) ([]dispRow, bool) {
	if !isJSONObject(line) {
		return nil, false
	}
	w := &jsonWalker{dec: json.NewDecoder(strings.NewReader(line)), base: base}
	w.dec.UseNumber()
	if err := w.value(0, "", "", ""); err != nil {
		return nil, false
	}
	return w.rows, true
}

type jsonWalker struct {
	dec  *json.Decoder
	base int
	rows []dispRow
}

// value emits one JSON value at depth, after head (`"key": ` or empty). path is
// the value's yq path and key its object key, "" for an array element.
func (w *jsonWalker) value(depth int, path, key, head string) error {
	tok, err := w.dec.Token()
	if err != nil {
		return err
	}
	pad := strings.Repeat("  ", depth)
	lead := strings.Repeat(" ", w.base) + pad
	start := w.base + displayWidth(pad)
	keyX := [2]int{start, start + displayWidth(jsonQuote(key))}
	if key == "" {
		keyX = [2]int{}
	}

	d, isDelim := tok.(json.Delim)
	if !isDelim {
		lit := yqLiteral(tok)
		valStart := start + displayWidth(head)
		w.emit(lead+head+lit, &jsonField{
			keyX: keyX, valX: [2]int{valStart, valStart + displayWidth(lit)},
			path: path, key: key, lit: lit,
		})
		return nil
	}

	open, closer := "{", "}"
	if d == '[' {
		open, closer = "[", "]"
	}
	var field *jsonField
	if key != "" {
		field = &jsonField{keyX: keyX, path: path, key: key}
	}
	w.emit(lead+head+open, field)

	for i := 0; w.dec.More(); i++ {
		if d == '{' {
			kt, err := w.dec.Token()
			if err != nil {
				return err
			}
			k, _ := kt.(string)
			if err := w.value(depth+1, yqKeyPath(path, k), k, jsonQuote(k)+": "); err != nil {
				return err
			}
		} else if err := w.value(depth+1, path+"["+strconv.Itoa(i)+"]", "", ""); err != nil {
			return err
		}
		if w.dec.More() {
			w.rows[len(w.rows)-1].text += ","
		}
	}
	if _, err := w.dec.Token(); err != nil {
		return err
	}
	w.emit(lead+closer, nil)
	return nil
}

func (w *jsonWalker) emit(text string, f *jsonField) {
	w.rows = append(w.rows, dispRow{sub: len(w.rows), text: text, field: f})
}

// yqKeyPath extends a yq path by one object key.
func yqKeyPath(path, key string) string {
	if identKey.MatchString(key) {
		return path + "." + key
	}
	if path == "" {
		return ".[" + jsonQuote(key) + "]"
	}
	return path + "[" + jsonQuote(key) + "]"
}

// yqLiteral is a JSON scalar as a yq literal: numbers and bools as written, strings quoted.
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

// jsonQuote is s as a JSON string, without HTML escaping.
func jsonQuote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return fmt.Sprintf("%q", s)
	}
	return strings.TrimSuffix(b.String(), "\n")
}
