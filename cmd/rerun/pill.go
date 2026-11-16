package main

import (
	"container/list"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mikefarah/yq/v4/pkg/yqlib"
)

// pillKind is a pill's mode: filter hides non-matching lines, highlight keeps
// every line and only marks hits.
type pillKind int

const (
	pillFilter pillKind = iota
	pillHighlight
)

// pillOp is the comparison a pill applies; pillOpText and pillOpYq carry no field.
type pillOp int

const (
	pillOpText pillOp = iota
	pillOpEq
	pillOpNe
	pillOpGt
	pillOpGe
	pillOpLt
	pillOpLe
	pillOpContains
	pillOpRegex
	pillOpYq
	pillOpNotRegex  // !~: the field exists and does not match the regex
	pillOpBlank     // ="": the field is missing or empty
	pillOpFilled    // !="": the field exists and is not empty
	pillOpLineRegex // |~ and !~ standalone: the whole raw line matches the regex
)

// pill is one compiled query term: full-text, field comparison, raw yq, or a
// saved-query reference resolved later.
type pill struct {
	raw      string
	kind     pillKind
	neg      bool
	field    string
	op       pillOp
	values   []string
	lowers   []string // values lowercased once at parse time
	lowRunes []rune   // lowers[0] as runes, the foldSpans needle
	num      float64
	re       *regexp.Regexp
	yq       *yqlib.ExpressionNode
	saved    string
	sub      []pill // a saved query's parsed pills, resolved when the pill is added
	alts     []pill // an OR pill's alternatives: it matches when any one does
	// null marks a clicked null literal: it matches a JSON null, never the string "null".
	null bool
}

// pillToken is one whitespace-delimited term and where it starts in the input.
type pillToken struct {
	start int
	text  string
}

// pipelineStages are the Loki parser stages rerun ignores; the value counts their quoted arguments.
var pipelineStages = map[string]int{"json": 0, "logfmt": 0, "unpack": 0, "pattern": 1, "regexp": 1}

// orKeyword joins its neighbor terms into one pill; only the bare token counts.
const orKeyword = "OR"

// parsePills compiles a query bar input into ANDed pills, split on unquoted spaces.
// A run of terms joined by OR is one pill that matches when any term does.
func parsePills(input string, cols []Column) ([]pill, error) {
	tokens, err := splitPills(input)
	if err != nil {
		return nil, err
	}
	tokens, err = joinLogQL(tokens)
	if err != nil {
		return nil, err
	}
	groups, err := groupOR(tokens)
	if err != nil {
		return nil, err
	}
	pills := make([]pill, 0, len(groups))
	for _, g := range groups {
		if len(g) > 1 {
			p, err := parseOR(g, input, cols)
			if err != nil {
				return nil, err
			}
			pills = append(pills, p)
			continue
		}
		p, rest, err := parsePill(g[0], input, cols)
		if err != nil {
			return nil, err
		}
		if rest && hasORToken(input[g[0].start:]) {
			return nil, fmt.Errorf("%s: a .yq term cannot join an OR", g[0].text)
		}
		p.finalize()
		pills = append(pills, p)
		if rest {
			break // a raw yq pill swallowed the remaining input
		}
	}
	return pills, nil
}

// selectorStart matches the head of a Loki stream selector: {label op.
var selectorStart = regexp.MustCompile(`^\{\s*[A-Za-z_]\w*\s*(=~|!~|!=|=)`)

// fieldName matches a bare field path such as `level` or `http.status`.
var fieldName = regexp.MustCompile(`^[A-Za-z_][\w.]*$`)

// fieldOps are the comparisons a spaced field takes: `level != x`.
var fieldOps = map[string]bool{"=": true, "!=": true, "=~": true, "!~": true, ">": true, ">=": true, "<": true, "<=": true, "~": true}

// isSelector reports whether tok is one whole stream selector, its closing brace last outside quotes.
func isSelector(tok string) bool {
	if !selectorStart.MatchString(tok) {
		return false
	}
	inQuote := false
	for i := 0; i < len(tok); i++ {
		switch {
		case tok[i] == '\\' && inQuote && i+1 < len(tok):
			i++
		case tok[i] == '"':
			inQuote = !inQuote
		case tok[i] == '}' && !inQuote:
			return i == len(tok)-1
		}
	}
	return false
}

// Tokens are read in order, so a line filter's value never reads as a field name.
func joinLogQL(tokens []pillToken) ([]pillToken, error) {
	out := make([]pillToken, 0, len(tokens))
	hasValue := func(i int) bool { return i+1 < len(tokens) && tokens[i+1].text != orKeyword }
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		switch {
		case isSelector(tok.text):
			continue
		case tok.text == "|":
			if i+1 >= len(tokens) || !isBareWord(tokens[i+1].text) {
				continue // a label filter after the pipe is just a filter
			}
			args, ok := pipelineStages[tokens[i+1].text]
			if !ok {
				continue // an unknown stage word parses as a plain filter
			}
			i++
			for i+1 < len(tokens) && strings.HasPrefix(tokens[i+1].text, "--") {
				i++ // a stage flag such as --strict
			}
			for k := 0; k < args && i+1 < len(tokens) && strings.HasPrefix(tokens[i+1].text, `"`); k++ {
				i++
			}
		case fieldOps[tok.text] && len(out) > 0 && fieldName.MatchString(out[len(out)-1].text) && hasValue(i):
			out[len(out)-1].text += tok.text + tokens[i+1].text
			i++
		case (tok.text == "|=" || tok.text == "|~" || tok.text == "!=" || tok.text == "!~") && hasValue(i):
			tok.text += " " + tokens[i+1].text
			i++
			out = append(out, tok)
		default:
			out = append(out, tok)
		}
	}
	return out, nil
}

func isBareWord(s string) bool {
	return s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ_") == ""
}

// groupOR gathers each run of terms joined by OR; a lone term is a group of one.
func groupOR(tokens []pillToken) ([][]pillToken, error) {
	var groups [][]pillToken
	var cur []pillToken
	pending := false // an OR still waits for its right term
	for _, tok := range tokens {
		if tok.text == orKeyword {
			if len(cur) == 0 || pending {
				return nil, fmt.Errorf("OR needs a term on each side")
			}
			pending = true
			continue
		}
		if len(cur) > 0 && !pending {
			groups = append(groups, cur)
			cur = nil
		}
		cur = append(cur, tok)
		pending = false
	}
	if pending {
		return nil, fmt.Errorf("OR needs a term on each side")
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	return groups, nil
}

// hasORToken reports whether an OR keyword appears as its own token in s.
func hasORToken(s string) bool {
	toks, err := splitPills(s)
	return err == nil && slices.ContainsFunc(toks, func(t pillToken) bool { return t.text == orKeyword })
}

// parseOR compiles a group of OR-joined terms into one pill; raw yq cannot be an alternative.
func parseOR(group []pillToken, input string, cols []Column) (pill, error) {
	var out pill
	raws := make([]string, 0, len(group))
	for _, tok := range group {
		alt, _, err := parsePill(tok, input, cols)
		if err != nil {
			return pill{}, err
		}
		if alt.op == pillOpYq {
			return pill{}, fmt.Errorf("%s: a .yq term cannot join an OR", tok.text)
		}
		alt.finalize()
		out.alts = append(out.alts, alt)
		raws = append(raws, alt.raw)
	}
	out.raw = strings.Join(raws, " "+orKeyword+" ")
	return out, nil
}

// terms lists the pill's own terms: its OR alternatives, else the pill itself.
func (p pill) terms() []pill {
	if len(p.alts) > 0 {
		return p.alts
	}
	return []pill{p}
}

// splitPills splits on spaces outside double quotes and /regex/ regions. \"
// and \\ escape inside quotes, any escaped char stays escaped inside a regex.
func splitPills(input string) ([]pillToken, error) {
	var tokens []pillToken
	var cur strings.Builder
	start := 0
	inQuote, inSlash, inBrace, started := false, false, false, false
	for i := 0; i < len(input); i++ {
		c := input[i]
		if !started && c != ' ' {
			start = i
		}
		switch {
		case c == '\\' && inQuote && i+1 < len(input) && (input[i+1] == '"' || input[i+1] == '\\'):
			cur.WriteByte(c)
			i++
			cur.WriteByte(input[i])
			started = true
		case c == '\\' && inSlash && i+1 < len(input):
			// An escaped char never closes the regex; \/ is a literal slash.
			cur.WriteByte(c)
			i++
			cur.WriteByte(input[i])
			started = true
		case c == '"' && !inSlash:
			inQuote = !inQuote
			cur.WriteByte(c)
			started = true
		case c == '{' && !started && !inQuote:
			// A Loki stream selector may hold spaces and commas; it stays one token to be dropped.
			inBrace = true
			cur.WriteByte(c)
			started = true
		case c == '}' && inBrace && !inQuote:
			inBrace = false
			cur.WriteByte(c)
			started = true
		case c == '~' && !inQuote && !inSlash && i+1 < len(input) && input[i+1] == '/':
			// A /.../ regex value may hold spaces and pipes.
			inSlash = true
			cur.WriteByte(c)
			i++
			cur.WriteByte(input[i])
			started = true
		case c == '/' && inSlash:
			inSlash = false
			cur.WriteByte(c)
			started = true
		case c == ' ' && !inQuote && !inSlash && !inBrace:
			if started {
				tokens = append(tokens, pillToken{start, cur.String()})
				cur.Reset()
				started = false
			}
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	if inQuote {
		return nil, fmt.Errorf("unterminated quote in %q", input)
	}
	if inSlash {
		return nil, fmt.Errorf("unterminated regex in %q", input)
	}
	if inBrace {
		return nil, fmt.Errorf("unterminated selector in %q", input)
	}
	if started {
		tokens = append(tokens, pillToken{start, cur.String()})
	}
	return tokens, nil
}

// parsePill compiles one token; rest reports that a raw yq pill consumed the remaining input.
func parsePill(tok pillToken, input string, cols []Column) (pill, bool, error) {
	p, rest, err := parsePillToken(tok, input, cols)
	if err == nil && p.op != pillOpYq {
		p.raw = p.canonical()
	}
	return p, rest, err
}

// parsePillToken is parsePill before the canonical text is set.
func parsePillToken(tok pillToken, input string, cols []Column) (pill, bool, error) {
	p := pill{raw: tok.text}
	s := tok.text
	if strings.HasPrefix(s, "-") {
		p.neg = true
		s = s[1:]
	}
	if p.neg && strings.HasPrefix(s, "|~") {
		return p, false, fmt.Errorf("%s: write !~ for a negated line regex", tok.text)
	}
	if p.neg && strings.HasPrefix(s, "|=") {
		return p, false, fmt.Errorf("%s: write != for a negated line filter", tok.text)
	}
	if s == "" {
		return p, false, fmt.Errorf("%s: missing value", tok.text)
	}
	if strings.HasPrefix(s, "@") {
		name := s[1:]
		if name == "" {
			return p, false, fmt.Errorf("%s: missing saved-query name", tok.text)
		}
		p.saved = name
		return p, false, nil
	}
	for _, op := range []string{"|=", "|~", "!=", "!~"} {
		if rest, ok := strings.CutPrefix(s, op); ok {
			r, err := p.parseLineOp(op, rest, tok.text)
			return r, false, err
		}
	}
	// A fully quoted token is always full text, even one holding pill syntax.
	if inner, ok := unquote(s); ok {
		if inner == "" {
			return p, false, fmt.Errorf("%s: missing value", tok.text)
		}
		p.op = pillOpText
		p.values = []string{inner}
		return p, false, nil
	}
	if strings.HasPrefix(s, ".") && !quotedKeyField(s) {
		// Raw yq owns the rest of the input when the whole of it parses.
		yqIn := input[tok.start+len(tok.text)-len(s):]
		if expr, err := yqParse(yqIn); err == nil {
			p.op = pillOpYq
			p.yq = expr
			p.raw = yqIn
			return p, true, nil
		}
	}
	r, err := p.parseBody(s, tok.text, cols)
	return r, false, err
}

// quotedKeyField reports whether s is a field pill on a yq-quoted key, the one
// dot-prefixed shape that is not raw yq: ."a-b"=1.
func quotedKeyField(s string) bool {
	if !strings.HasPrefix(s, `."`) {
		return false
	}
	i, _ := findOp(s)
	return i >= 0
}

// parseBody classifies one unquoted pill body: a quoted-key field comparison,
// raw yq when it starts with a dot, a field comparison when a pill operator is
// present, full text otherwise.
func (p *pill) parseBody(s, raw string, cols []Column) (pill, error) {
	if strings.HasPrefix(s, ".") {
		if quotedKeyField(s) {
			i, opStr := findOp(s)
			if valStr := s[i+len(opStr):]; valStr != "" {
				return p.parseField(s[:i], opStr, valStr, raw, cols)
			}
			return *p, fmt.Errorf("%s: missing value", raw)
		}
		expr, err := yqParse(s)
		if err != nil {
			return *p, fmt.Errorf("%s: %v", raw, err)
		}
		p.op = pillOpYq
		p.yq = expr
		return *p, nil
	}
	if i, opStr := findOp(s); i >= 0 {
		field, valStr := s[:i], s[i+len(opStr):]
		if field == "" {
			return *p, fmt.Errorf("%s: missing field", raw)
		}
		if valStr == "" {
			return *p, fmt.Errorf("%s: missing value", raw)
		}
		return p.parseField(field, opStr, valStr, raw, cols)
	}
	p.op = pillOpText
	p.values = []string{s}
	return *p, nil
}

func (p *pill) parseField(field, opStr, valStr, raw string, cols []Column) (pill, error) {
	// A visible column name resolves to the column's expression before the yq path.
	path := field
	for _, c := range cols {
		if c.Name == field && c.Expr != "" {
			path = c.Expr
			break
		}
	}
	if !strings.HasPrefix(path, ".") {
		path = "." + path
	}
	expr, err := yqParse(path)
	if err != nil {
		return *p, fmt.Errorf("%s: invalid field %q: %v", raw, field, err)
	}
	p.field = field
	p.yq = expr
	switch opStr {
	case "=", "!=":
		if valStr == `""` {
			// Loki's empty-string forms: missing-or-empty and present-and-non-empty.
			p.op = pillOpBlank
			if opStr == "!=" {
				p.op = pillOpFilled
			}
			return *p, nil
		}
		if valStr == "*" {
			// The bare-star forms alias Loki's empty-string ones: =* is !="", != * is ="".
			p.op = pillOpFilled
			if opStr == "!=" {
				p.op = pillOpBlank
			}
			return *p, nil
		}
		if opStr == "=" && valStr == "null" {
			// A bare null is the JSON literal; the string "null" needs quotes.
			p.op, p.null = pillOpEq, true
			return *p, nil
		}
		p.values = scanValues(valStr)
		if len(p.values) == 1 && p.values[0] == "" {
			return *p, fmt.Errorf("%s: missing value", raw)
		}
		p.op = pillOpEq
		if opStr == "!=" {
			p.op = pillOpNe
		}
	case ">", ">=", "<", "<=":
		f, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			return *p, fmt.Errorf("%s: %q is not a number", raw, valStr)
		}
		p.num = f
		switch opStr {
		case ">":
			p.op = pillOpGt
		case ">=":
			p.op = pillOpGe
		case "<":
			p.op = pillOpLt
		case "<=":
			p.op = pillOpLe
		}
	case "~", "=~", "!~":
		body := scanValue(valStr)
		if body == "" {
			return *p, fmt.Errorf("%s: missing value", raw)
		}
		body, isSlash, err := slashRegex(valStr, body)
		if err != nil {
			return *p, fmt.Errorf("%s: %v", raw, err)
		}
		if opStr == "~" && !isSlash {
			p.op = pillOpContains
			p.values = []string{body}
			break
		}
		re, err := regexp.Compile(body)
		if err != nil {
			return *p, fmt.Errorf("%s: bad regex: %v", raw, err)
		}
		p.op, p.re = pillOpRegex, re
		if opStr == "!~" {
			p.op = pillOpNotRegex
		}
	}
	return *p, nil
}

// parseLineOp compiles a LogQL line filter; a bare value is taken as written.
func (p pill) parseLineOp(op, rest, raw string) (pill, error) {
	rest = strings.TrimLeft(rest, " ")
	val, quoted := unquote(rest)
	if !quoted {
		val = rest
	}
	if val == "" {
		return p, fmt.Errorf("%s: missing value", raw)
	}
	switch op {
	case "|=":
		p.op, p.values = pillOpText, []string{val}
	case "!=":
		p.op, p.values, p.neg = pillOpText, []string{val}, true
	default:
		re, err := regexp.Compile(val)
		if err != nil {
			return p, fmt.Errorf("%s: bad regex: %v", raw, err)
		}
		p.op, p.re, p.neg = pillOpLineRegex, re, op == "!~"
	}
	return p, nil
}

// slashRegex strips the /.../ delimiters from a bare regex value; a quoted value is literal.
func slashRegex(valStr, body string) (string, bool, error) {
	if _, quoted := unquote(valStr); quoted || !strings.HasPrefix(body, "/") {
		return body, false, nil
	}
	if len(body) < 2 || !unescapedSuffix(body, '/') {
		return "", false, fmt.Errorf("unterminated regex")
	}
	return body[1 : len(body)-1], true, nil
}

// findOp locates the first pill operator outside quotes; == is yq equality.
func findOp(s string) (int, string) {
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
			continue
		}
		if inQuote {
			continue
		}
		if i+1 < len(s) {
			switch s[i : i+2] {
			case "!=", ">=", "<=", "=~", "!~":
				return i, s[i : i+2]
			case "==":
				i++
				continue
			}
		}
		switch s[i] {
		case '=', '>', '<', '~':
			return i, string(s[i])
		}
	}
	return -1, ""
}

// unquote strips one surrounding pair of double quotes, resolving \" and \\.
func unquote(s string) (string, bool) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", false
	}
	n := 0
	for i := len(s) - 2; i >= 0 && s[i] == '\\'; i-- {
		n++
	}
	if n%2 == 1 {
		return "", false
	}
	var b strings.Builder
	inner := s[1 : len(s)-1]
	for i := 0; i < len(inner); i++ {
		if inner[i] == '\\' && i+1 < len(inner) && (inner[i+1] == '"' || inner[i+1] == '\\') {
			i++
			b.WriteByte(inner[i])
			continue
		}
		if inner[i] == '"' {
			return "", false
		}
		b.WriteByte(inner[i])
	}
	return b.String(), true
}

// unescapedSuffix reports whether s ends in c without a backslash escaping it.
func unescapedSuffix(s string, c byte) bool {
	if len(s) == 0 || s[len(s)-1] != c {
		return false
	}
	n := 0
	for i := len(s) - 2; i >= 0 && s[i] == '\\'; i-- {
		n++
	}
	return n%2 == 0
}

// scanValues splits an any-of list on '|' outside quotes and resolves each
// part's quoting and escapes.
func scanValues(s string) []string {
	parts := splitValues(s)
	for i := range parts {
		parts[i] = scanValue(parts[i])
	}
	return parts
}

// scanValue resolves one pill value: surrounding quotes come off, and the \",
// \\ and \| escapes splitPills preserved turn literal.
func scanValue(s string) string {
	if inner, ok := unquote(s); ok {
		return inner
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\' || s[i+1] == '|') {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// splitValues splits an any-of list on unquoted, unescaped '|', keeping the
// escape sequences for scanValue to resolve.
func splitValues(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && (s[i+1] == '|' || s[i+1] == '"' || s[i+1] == '\\'):
			cur.WriteByte(c)
			i++
			cur.WriteByte(s[i])
		case c == '"':
			inQuote = !inQuote
			cur.WriteByte(c)
		case c == '|' && !inQuote:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(out, cur.String())
}

// finalize precomputes the lowered needles per-line matching reuses.
func (p *pill) finalize() {
	p.lowers = make([]string, len(p.values))
	for i, v := range p.values {
		p.lowers[i] = strings.ToLower(v)
	}
	if (p.op == pillOpText || p.op == pillOpContains) && len(p.lowers) > 0 {
		p.lowRunes = []rune(p.lowers[0])
	}
}

// needsJSON reports whether the pill reads the decoded document.
func (p pill) needsJSON() bool {
	if len(p.alts) > 0 {
		return slices.ContainsFunc(p.alts, func(s pill) bool { return s.needsJSON() })
	}
	if p.saved != "" {
		return slices.ContainsFunc(p.sub, func(s pill) bool { return s.needsJSON() })
	}
	switch p.op {
	case pillOpText, pillOpLineRegex:
		return false
	}
	return true
}

// match reports whether the pill keeps the line; a negated JSON pill still
// drops a non-JSON line.
func (p pill) match(line string) bool {
	if !p.needsJSON() {
		return p.matchRoot(nil, line)
	}
	return p.matchRoot(decodeJSONLine(line), line)
}

// matchRoot evaluates the pill against an already-decoded line; root is nil
// for a non-JSON line.
func (p pill) matchRoot(root *yqlib.CandidateNode, line string) bool {
	if len(p.alts) > 0 {
		for _, a := range p.alts {
			if a.matchRoot(root, line) {
				return true
			}
		}
		return false
	}
	// A saved query ANDs its pills, negated as a whole; an unresolved one filters nothing.
	if p.saved != "" {
		ok := true
		for _, s := range p.sub {
			if !s.matchRoot(root, line) {
				ok = false
				break
			}
		}
		return ok != p.neg
	}
	switch p.op {
	case pillOpText:
		return strings.Contains(strings.ToLower(line), p.lowers[0]) != p.neg
	case pillOpLineRegex:
		return p.re.MatchString(line) != p.neg
	}
	if root == nil {
		return false // a JSON pill never keeps a non-JSON line, negated or not
	}
	return p.matchDoc(root) != p.neg
}

// emptyScalar is a scalar "": a map or list is present, however it is written.
func emptyScalar(n *yqlib.CandidateNode) bool {
	return n.Kind == yqlib.ScalarNode && n.Tag == "!!str" && n.Value == ""
}

func (p pill) matchDoc(root *yqlib.CandidateNode) bool {
	if p.op == pillOpYq {
		for _, n := range evalNodes(p.yq, root) {
			if yqTruthy(n) {
				return true
			}
		}
		return false
	}
	if p.null {
		if n := firstNode(p.yq, root); n != nil {
			return n.Tag == "!!null"
		}
		return false
	}
	node := firstValue(p.yq, root)
	switch p.op {
	case pillOpEq:
		return node != nil && p.anyEqual(node)
	case pillOpNe:
		return node != nil && !p.anyEqual(node)
	case pillOpContains:
		return node != nil && strings.Contains(strings.ToLower(node.Value), p.lowers[0])
	case pillOpRegex:
		return node != nil && p.re.MatchString(node.Value)
	case pillOpNotRegex:
		return node != nil && !p.re.MatchString(node.Value)
	case pillOpBlank:
		return node == nil || emptyScalar(node)
	case pillOpFilled:
		return node != nil && !emptyScalar(node)
	case pillOpGt:
		f, ok := numericValue(node)
		return ok && f > p.num
	case pillOpGe:
		f, ok := numericValue(node)
		return ok && f >= p.num
	case pillOpLt:
		f, ok := numericValue(node)
		return ok && f < p.num
	case pillOpLe:
		f, ok := numericValue(node)
		return ok && f <= p.num
	}
	return false
}

// anyEqual compares numerically for JSON numbers, case-insensitively otherwise.
func (p pill) anyEqual(node *yqlib.CandidateNode) bool {
	nf, nok := numericValue(node)
	for _, v := range p.values {
		if nok {
			if vf, err := strconv.ParseFloat(v, 64); err == nil && vf == nf {
				return true
			}
			continue
		}
		if strings.EqualFold(node.Value, v) {
			return true
		}
	}
	return false
}

// spans returns byte ranges to highlight in the raw line.
func (p pill) spans(line string) [][2]int {
	var root *yqlib.CandidateNode
	if p.needsJSON() {
		root = decodeJSONLine(line)
	}
	return p.spansRoot(root, line)
}

// spansRoot is spans with the line's decode shared across a pill set.
func (p pill) spansRoot(root *yqlib.CandidateNode, line string) [][2]int {
	if p.neg {
		return nil
	}
	if len(p.alts) > 0 {
		// Each alternative that keeps the line marks its own hits.
		var out [][2]int
		for _, a := range p.alts {
			if a.matchRoot(root, line) {
				out = append(out, a.spansRoot(root, line)...)
			}
		}
		return out
	}
	if p.saved != "" {
		var out [][2]int
		for _, s := range p.sub {
			out = append(out, s.spansRoot(root, line)...)
		}
		return out
	}
	if p.op == pillOpText {
		return foldSpans(line, p.lowRunes)
	}
	if p.op == pillOpLineRegex {
		var out [][2]int
		for _, idx := range p.re.FindAllStringIndex(line, -1) {
			if idx[0] < idx[1] {
				out = append(out, [2]int{idx[0], idx[1]})
			}
		}
		return out
	}
	switch p.op {
	case pillOpContains, pillOpRegex:
		return p.docSpans(root, line)
	case pillOpEq, pillOpNe, pillOpNotRegex, pillOpGt, pillOpGe, pillOpLt, pillOpLe, pillOpFilled:
		return p.valueSpan(root, line)
	}
	return nil
}

// valueSpan highlights the field's value for every comparison op, so a
// numeric or exists pill still marks what it matched.
func (p pill) valueSpan(root *yqlib.CandidateNode, line string) [][2]int {
	if root == nil || !p.matchDoc(root) {
		return nil
	}
	node := firstValue(p.yq, root)
	if p.null {
		// A null pill's node is the null literal itself, which firstValue skips.
		node = firstNode(p.yq, root)
	}
	if node == nil {
		return nil
	}
	start, end, ok := valueOffset(line, p.field, node)
	if !ok {
		return nil
	}
	return [][2]int{{start, end}}
}

// docSpans highlights only the field's value occurrence in the raw line, never
// a lookalike substring elsewhere. Offsets into the decoded value shift when
// the raw line escapes it; the common unescaped case is exact.
func (p pill) docSpans(root *yqlib.CandidateNode, line string) [][2]int {
	if root == nil {
		return nil
	}
	node := firstValue(p.yq, root)
	if node == nil {
		return nil
	}
	start, _, ok := valueOffset(line, p.field, node)
	if !ok {
		return nil
	}
	switch p.op {
	case pillOpContains:
		var out [][2]int
		for _, sp := range foldSpans(node.Value, p.lowRunes) {
			out = append(out, [2]int{start + sp[0], start + sp[1]})
		}
		return out
	case pillOpRegex:
		var out [][2]int
		for _, idx := range p.re.FindAllStringIndex(node.Value, -1) {
			out = append(out, [2]int{start + idx[0], start + idx[1]})
		}
		return out
	}
	return nil
}

// valueOffset locates the field's value in the raw line, start inclusive and
// end exclusive: the key-value pair first, then the quoted value, then the
// bare scalar. The raw line escapes a string, so the search uses its JSON
// form and the span covers the escaped bytes.
func valueOffset(line, field string, node *yqlib.CandidateNode) (int, int, bool) {
	v := node.Value
	if v == "" {
		return 0, 0, false
	}
	if node.Tag == "!!str" {
		quoted := strconv.Quote(v)
		for _, kv := range []string{`"` + lastKey(field) + `":` + quoted, `"` + lastKey(field) + `": ` + quoted} {
			if i := strings.Index(line, kv); i >= 0 {
				return i + len(kv) - len(quoted) + 1, i + len(kv) - 1, true
			}
		}
		if i := strings.Index(line, quoted); i >= 0 {
			return i + 1, i + len(quoted) - 1, true
		}
		return 0, 0, false
	}
	if i := strings.Index(line, v); i >= 0 {
		return i, i + len(v), true
	}
	return 0, 0, false
}

// lastKey is a field path's final segment, unquoting a yq-quoted key.
func lastKey(field string) string {
	if i := strings.LastIndex(field, "."); i >= 0 {
		field = field[i+1:]
	}
	return strings.Trim(field, `"`)
}

// pillSet is a compiled query: one JSON decode per line serves every pill.
type pillSet struct {
	pills []pill
	json  bool // any pill reads the decoded document
}

// compilePills parses input and precomputes what per-line matching needs.
func compilePills(input string, cols []Column) (pillSet, error) {
	pills, err := parsePills(input, cols)
	if err != nil {
		return pillSet{}, err
	}
	return pillSetOf(pills), nil
}

// pillSetOf wraps already-parsed pills with the precomputed decode flag.
func pillSetOf(pills []pill) pillSet {
	s := pillSet{pills: pills}
	for _, p := range pills {
		s.json = s.json || p.needsJSON()
	}
	return s
}

func (s pillSet) match(line string) bool {
	var root *yqlib.CandidateNode
	if s.json {
		root = decodeJSONLine(line)
	}
	for _, p := range s.pills {
		if !p.matchRoot(root, line) {
			return false
		}
	}
	return true
}

func decodeJSONLine(line string) *yqlib.CandidateNode {
	dec := yqlib.NewJSONDecoder()
	if err := dec.Init(strings.NewReader(line)); err != nil {
		return nil
	}
	node, err := dec.Decode()
	if err != nil {
		return nil
	}
	return node
}

func evalNodes(expr *yqlib.ExpressionNode, root *yqlib.CandidateNode) []*yqlib.CandidateNode {
	in := list.New()
	in.PushBack(root)
	out, err := yqNav.GetMatchingNodes(yqlib.Context{MatchingNodes: in}, expr)
	if err != nil {
		return nil
	}
	var nodes []*yqlib.CandidateNode
	for el := out.MatchingNodes.Front(); el != nil; el = el.Next() {
		if n, ok := el.Value.(*yqlib.CandidateNode); ok {
			nodes = append(nodes, n)
		}
	}
	return nodes
}

// firstValue returns the field's first non-null node; null counts as missing.
func firstValue(expr *yqlib.ExpressionNode, root *yqlib.CandidateNode) *yqlib.CandidateNode {
	for _, n := range evalNodes(expr, root) {
		if n != nil && n.Tag != "!!null" {
			return n
		}
	}
	return nil
}

// firstNode returns the field's first node, null included; a null pill needs it.
func firstNode(expr *yqlib.ExpressionNode, root *yqlib.CandidateNode) *yqlib.CandidateNode {
	for _, n := range evalNodes(expr, root) {
		if n != nil {
			return n
		}
	}
	return nil
}

func numericValue(node *yqlib.CandidateNode) (float64, bool) {
	if node == nil || node.Kind != yqlib.ScalarNode {
		return 0, false
	}
	if node.Tag != "!!int" && node.Tag != "!!float" {
		return 0, false
	}
	f, err := strconv.ParseFloat(node.Value, 64)
	return f, err == nil
}

// mergeSpans sorts spans and folds overlaps, so painting never styles a cell twice.
func mergeSpans(spans [][2]int) [][2]int {
	if len(spans) == 0 {
		return nil
	}
	slices.SortFunc(spans, func(a, b [2]int) int { return a[0] - b[0] })
	out := spans[:1]
	for _, sp := range spans[1:] {
		last := &out[len(out)-1]
		if sp[0] <= last[1] {
			last[1] = max(last[1], sp[1])
			continue
		}
		out = append(out, sp)
	}
	return out
}

// foldSpans finds every case-insensitive occurrence of low (the needle's
// lowercased runes) in line, returning byte spans into the original line.
func foldSpans(line string, low []rune) [][2]int {
	if len(low) == 0 {
		return nil
	}
	var out [][2]int
	for i := 0; i < len(line); {
		if end, ok := foldMatchAt(line, i, low); ok {
			out = append(out, [2]int{i, end})
			i = end
			continue
		}
		_, size := utf8.DecodeRuneInString(line[i:])
		i += size
	}
	return out
}

// foldMatchAt compares rune by rune, so a rune whose lowercase form shrinks
// (İ, the Kelvin sign) never shifts the returned byte offsets.
func foldMatchAt(line string, i int, low []rune) (int, bool) {
	j := i
	for _, tr := range low {
		if j >= len(line) {
			return 0, false
		}
		r, size := utf8.DecodeRuneInString(line[j:])
		if unicode.ToLower(r) != tr {
			return 0, false
		}
		j += size
	}
	return j, true
}

// canonical is the pill's shortest text, which re-parses to the same pill.
func (p pill) canonical() string {
	neg := ""
	if p.neg && p.op != pillOpLineRegex {
		neg = "-"
	}
	switch {
	case p.saved != "":
		return neg + "@" + p.saved
	case p.op == pillOpText:
		return neg + textValue(p.values[0])
	case p.op == pillOpLineRegex:
		if p.neg {
			return "!~ " + quoteRaw(p.re.String())
		}
		return "|~ " + quoteRaw(p.re.String())
	}
	op := map[pillOp]string{
		pillOpEq: "=", pillOpNe: "!=", pillOpContains: "~", pillOpRegex: "=~", pillOpNotRegex: "!~",
		pillOpBlank: "=", pillOpFilled: "!=", pillOpGt: ">", pillOpGe: ">=", pillOpLt: "<", pillOpLe: "<=",
	}[p.op]
	value := ""
	switch p.op {
	case pillOpBlank, pillOpFilled:
		value = `""`
	case pillOpEq, pillOpNe:
		if p.null {
			value = "null"
			break
		}
		parts := make([]string, len(p.values))
		for i, v := range p.values {
			parts[i] = fieldValue(v)
		}
		value = strings.Join(parts, "|")
	case pillOpContains:
		value = fieldValue(p.values[0])
	case pillOpRegex, pillOpNotRegex:
		value = regexValue(p.re.String())
	default:
		value = strconv.FormatFloat(p.num, 'f', -1, 64)
	}
	return neg + p.field + op + value
}

// quoteRaw wraps v in double quotes, escaping only the \\ and \" that unquote resolves.
func quoteRaw(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}

// fieldValue is a comparison value as written: quoted when it would not read back whole.
func fieldValue(v string) string {
	if v == "" || v == "null" || strings.ContainsAny(v, " \t|*\"\\") || strings.HasPrefix(v, "/") {
		return quoteRaw(v)
	}
	return v
}

// regexValue is a regex pattern as written: quoted only for spaces, quotes, backslashes or a leading slash.
func regexValue(v string) string {
	if v == "" || strings.ContainsAny(v, " \t\"\\") || strings.HasPrefix(v, "/") {
		return quoteRaw(v)
	}
	return v
}

// textValue is a full-text term as written: quoted when it would parse as a pill operator or OR.
func textValue(v string) string {
	if v == orKeyword || strings.HasPrefix(v, ".") || strings.HasPrefix(v, "@") || strings.HasPrefix(v, "-") ||
		strings.HasPrefix(v, "|") || strings.HasPrefix(v, "!") || strings.HasPrefix(v, "{") ||
		strings.ContainsAny(v, "=<>~!{}") || v == "" {
		return quoteRaw(v)
	}
	if strings.ContainsAny(v, " \t|*\"\\") {
		return quoteRaw(v)
	}
	return v
}
