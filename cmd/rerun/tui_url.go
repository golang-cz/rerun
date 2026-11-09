package main

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// captureURL reads a JSON object output line's top-level url/addr/listen/port
// and updates the target's spec URL, so the row link, the title line, and `o`
// follow the server that just started. Last capture wins; a line without a
// usable key changes nothing.
func (m *tuiModel) captureURL(t *tuiTarget, line string) {
	// A command in open: is the user's choice, so an endpoint never replaces it.
	if url := jsonLineURL(line); url != "" && (t.spec.Open == "" || isOpenLink(t.spec.Open)) {
		t.spec.Open = url
	}
}

// jsonLineURL extracts an endpoint from one JSON object line. Precedence:
// url, then addr (combined with a port), then listen, then a port alone as
// localhost. Values must be strings or numbers; anything else is ignored.
func jsonLineURL(line string) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return ""
	}
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return ""
	}

	port, hasPort := portString(obj["port"])
	if url, ok := valueString(obj["url"]); ok && url != "" {
		return url
	}
	if addr, ok := valueString(obj["addr"]); ok && addr != "" {
		if strings.Contains(addr, "://") {
			return addr
		}
		if hasPort && port != "" {
			return "http://" + addr + ":" + port
		}
		return addr
	}
	if listen, ok := valueString(obj["listen"]); ok && listen != "" {
		return listen
	}
	if host, ok := valueString(obj["host"]); ok && host != "" && hasPort && port != "" {
		return "http://" + host + ":" + port
	}
	if hasPort && port != "" {
		return "http://localhost:" + port
	}
	return ""
}

// valueString accepts the JSON value shapes a config endpoint may use: a
// string as-is, a number verbatim.
func valueString(v any) (string, bool) {
	switch s := v.(type) {
	case string:
		return s, true
	case json.Number:
		return s.String(), true
	}
	return "", false
}

// portString normalizes a port to its digits: a string as-is, a whole number
// as an integer, so 9000.0 does not become "9000.0".
func portString(v any) (string, bool) {
	switch s := v.(type) {
	case string:
		return s, true
	case json.Number:
		f, err := s.Float64()
		if err != nil || f != math.Trunc(f) || f < 0 {
			return "", false
		}
		return strconv.FormatInt(int64(f), 10), true
	}
	return "", false
}

// localhostLinkRE is a plain http link to this machine, with its port and path.
var localhostLinkRE = regexp.MustCompile("http://(?:localhost|127\\.0\\.0\\.1|0\\.0\\.0\\.0):\\d+[^\\s\"'<>`]*")

// localLink is the newest local http link in lines, or "". A 0.0.0.0 bind opens as localhost.
func localLink(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		found := localhostLinkRE.FindAllString(ansi.Strip(lines[i]), -1)
		if len(found) == 0 {
			continue
		}
		link := strings.TrimRight(found[len(found)-1], ".,;:)]}")
		return strings.Replace(link, "0.0.0.0", "localhost", 1)
	}
	return ""
}
