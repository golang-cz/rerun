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
	pillOpExists
	pillOpMissing
	pillOpYq
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
	// null marks a clicked null literal: it matches a JSON null, never the string "null".
	null bool
}

// pillToken is one whitespace-delimited term and where it starts in the input.
type pillToken struct {
	start int
	text  string
}

// parsePills compiles a query bar input into ANDed pills, split on unquoted spaces.
func parsePills(input string, cols []Column) ([]pill, error) {
	tokens, err := splitPills(input)
	if err != nil {
		return nil, err
	}
	pills := make([]pill, 0, len(tokens))
	for _, tok := range tokens {
		p, rest, err := parsePill(tok, input, cols)
		if err != nil {
			return nil, err
		}
		p.finalize()
		pills = append(pills, p)
		if rest {
			break // a raw yq pill swallowed the remaining input
		}
	}
	return pills, nil
}

// splitPills splits on spaces outside double quotes and /regex/ regions. \"
// and \\ escape inside quotes, any escaped char stays escaped inside a regex.
func splitPills(input string) ([]pillToken, error) {
	var tokens []pillToken
	var cur strings.Builder
	start := 0
	inQuote, inSlash, started := false, false, false
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
		case c == ' ' && !inQuote && !inSlash:
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
	if started {
		tokens = append(tokens, pillToken{start, cur.String()})
	}
	return tokens, nil
}

// parsePill compiles one token; rest reports that a raw yq pill consumed the
// remaining input.
func parsePill(tok pillToken, input string, cols []Column) (pill, bool, error) {
	p := pill{raw: tok.text}
	s := tok.text
	if strings.HasPrefix(s, "-") {
		p.neg = true
		s = s[1:]
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
		if valStr == "*" {
			p.op = pillOpExists
			if opStr == "!=" {
				p.op = pillOpMissing
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
	case "~", "=~":
		body := scanValue(valStr)
		if body == "" {
			return *p, fmt.Errorf("%s: missing value", raw)
		}
		isRegex := opStr == "=~"
		if strings.HasPrefix(body, "/") {
			if len(body) < 2 || !unescapedSuffix(body, '/') {
				return *p, fmt.Errorf("%s: unterminated regex", raw)
			}
			body = body[1 : len(body)-1]
			isRegex = true
		}
		if isRegex {
			re, err := regexp.Compile(body)
			if err != nil {
				return *p, fmt.Errorf("%s: bad regex: %v", raw, err)
			}
			p.op = pillOpRegex
			p.re = re
		} else {
			p.op = pillOpContains
			p.values = []string{body}
		}
	}
	return *p, nil
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
			case "!=", ">=", "<=", "=~":
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
	if p.saved != "" {
		return slices.ContainsFunc(p.sub, func(s pill) bool { return s.needsJSON() })
	}
	return p.op != pillOpText
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
	if p.op == pillOpText {
		return strings.Contains(strings.ToLower(line), p.lowers[0]) != p.neg
	}
	if root == nil {
		return false // a JSON pill never keeps a non-JSON line, negated or not
	}
	return p.matchDoc(root) != p.neg
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
	case pillOpExists:
		return node != nil
	case pillOpMissing:
		return node == nil
	case pillOpEq:
		return node != nil && p.anyEqual(node)
	case pillOpNe:
		return node != nil && !p.anyEqual(node)
	case pillOpContains:
		return node != nil && strings.Contains(strings.ToLower(node.Value), p.lowers[0])
	case pillOpRegex:
		return node != nil && p.re.MatchString(node.Value)
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
	switch p.op {
	case pillOpContains, pillOpRegex:
		return p.docSpans(root, line)
	case pillOpEq, pillOpNe, pillOpGt, pillOpGe, pillOpLt, pillOpLe, pillOpExists:
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
