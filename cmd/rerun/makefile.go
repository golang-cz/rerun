package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// makeNameRE is the target names safe to pass to a shell unquoted; a leading `-` is a make flag.
var makeNameRE = regexp.MustCompile(`^[A-Za-z0-9_./+][A-Za-z0-9_./+-]*$`)

// makeNames is the order GNU make tries when run without -f.
var makeNames = []string{"GNUmakefile", "makefile", "Makefile"}

// implicitMakefile is the Makefile in dir that a bare `make` would read, or "".
func implicitMakefile(dir string) string {
	for _, name := range makeNames {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

type makeTarget struct {
	Name string
	Desc string
}

// isMakefile matches by file name only; the file is never read or executed here.
func isMakefile(path string) bool {
	base := filepath.Base(path)
	switch base {
	case "Makefile", "makefile", "GNUmakefile":
		return true
	}
	return filepath.Ext(base) == ".mk"
}

// makeDirectives never start a rule, so a `name:` after them is not a target.
var makeDirectives = map[string]bool{
	"export": true, "unexport": true, "override": true, "load": true, "undefine": true,
	"include": true, "-include": true, "sinclude": true, "vpath": true,
	"ifeq": true, "ifneq": true, "ifdef": true, "ifndef": true,
	"else": true, "endif": true, "endef": true,
}

func parseMakefile(r io.Reader) ([]makeTarget, error) {
	var (
		targets     []makeTarget
		index       = map[string]int{}
		pending     []string // comment lines directly above the next rule
		defineDepth int
		// Continuation lines belong to the rule or assignment above them.
		continuation bool
	)

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		// CRLF files would otherwise leave a \r on every line and break the `\` and `define` checks.
		line := strings.TrimSuffix(sc.Text(), "\r")
		cont := strings.HasSuffix(line, `\`)

		if defineDepth > 0 {
			switch firstWord(line) {
			case "define":
				defineDepth++
			case "endef":
				defineDepth--
			}
			continue
		}
		if continuation {
			continuation = cont
			pending = nil
			continue
		}
		// Recipe lines are shell text; a `#` inside them is not a comment for the next target.
		if strings.HasPrefix(line, "\t") {
			continuation = cont
			pending = nil
			continue
		}

		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			if c := strings.TrimSpace(strings.TrimLeft(trimmed, "#")); c != "" {
				pending = append(pending, c)
			}
			continue
		}

		comment := pending
		pending = nil
		continuation = cont

		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "define" || (len(fields) > 1 && (fields[0] == "export" || fields[0] == "override") && fields[1] == "define") {
			defineDepth = 1
			continue
		}
		if makeDirectives[fields[0]] || line[0] == ' ' {
			continue
		}

		colon := strings.IndexByte(trimmed, ':')
		eq := strings.IndexByte(trimmed, '=')
		if colon < 0 || (eq >= 0 && eq < colon) {
			continue
		}
		// `:=` and `::=` are assignments; `a::` (double colon) is still a rule.
		if rest := trimmed[colon+1:]; strings.HasPrefix(rest, "=") || strings.HasPrefix(rest, ":=") {
			continue
		}

		desc := ""
		if h := strings.Index(trimmed, "##"); h >= 0 {
			desc = strings.TrimSpace(trimmed[h+2:])
		}
		if desc == "" {
			desc = strings.Join(comment, " ")
		}
		// Descs are drawn in the TUI, where an ESC would reach the terminal as a control sequence.
		desc = strings.TrimSpace(strings.Map(dropControl, desc))

		for _, name := range strings.Fields(trimmed[:colon]) {
			// Names reach a shell as `make <name>`, so anything outside this set is not a target.
			if !makeNameRE.MatchString(name) || strings.HasPrefix(name, ".") || strings.ContainsAny(name, "%$") {
				continue
			}
			if i, ok := index[name]; ok {
				if targets[i].Desc == "" {
					targets[i].Desc = desc
				}
				continue
			}
			index[name] = len(targets)
			targets = append(targets, makeTarget{Name: name, Desc: desc})
		}
	}
	return targets, sc.Err()
}

func firstWord(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
