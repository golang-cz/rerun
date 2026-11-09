package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/golang-cz/rerun"
	"gopkg.in/yaml.v3"
)

// importsKey is the reserved top-level key that lists other rerun files.
const importsKey = "imports"

// importFields is the accepted keys of one import entry.
var importFields = []string{"path", "group", "optional", "hide", "auto-hide", "start", "groups", "services", "profiles", "targets"}

// importSpec is one parsed `imports:` entry.
type importSpec struct {
	path     string
	group    string
	optional bool
	hide     bool
	// autoHide is the import's `auto-hide:`; nil leaves it to the root group or the Makefile default.
	autoHide *bool
	groups   []string
	services []string
	profiles []string
	// targets is the Makefile-only filter: the rule names to keep.
	targets []string
	// start and startSet are the import's `start:` default for its members.
	start     startMode
	startSet  bool
	pathNode  *yaml.Node
	groupNode *yaml.Node
	groupsRef *yaml.Node
	// servicesRef and profilesRef locate the compose-only keys, for errors on rerun files.
	servicesRef *yaml.Node
	profilesRef *yaml.Node
	targetsRef  *yaml.Node
	// implicit marks the root Makefile that is imported without being listed.
	implicit bool
}

// parseImports merges the root file's imports into cfg; a root group named like an import gives its targets columns.
func (cfg *Config) parseImports(src *configSource, node *yaml.Node, rootFile string, rootSchemas map[string]groupDef) error {
	var specs []importSpec
	if node != nil && !(node.Kind == yaml.ScalarNode && node.Tag == "!!null") {
		if node.Kind != yaml.SequenceNode {
			return src.at(node, nodeSpan(node), "imports: expected a list of entries with a path")
		}
		for _, item := range node.Content {
			spec, err := parseImportSpec(src, deref(item))
			if err != nil {
				return err
			}
			specs = append(specs, spec)
		}
	}
	// Beside a rerun.yml only its imports: list load; a bare Makefile dir loads its Makefile.
	mk := implicitMakefile(filepath.Dir(rootFile))
	rootExists := fileExists(rootFile)
	// Last, so an explicit import of the same file is seen first and wins the dedupe.
	if mk != "" && !rootExists {
		specs = append(specs, importSpec{path: filepath.Base(mk), group: makeKind, optional: true, implicit: true})
	}

	rootCanon := canonicalPath(rootFile)
	seenFiles := map[string][]string{}
	seenGroups := map[string]bool{}
	names := map[string]bool{}
	for _, t := range cfg.Targets {
		names[t.Name] = true
	}
	// claim reserves a group name for this import, or reports the clash.
	claim := func(spec importSpec, group string) error {
		if seenGroups[group] {
			node := spec.groupNode
			if node == nil {
				node = spec.pathNode
			}
			return src.at(node, nodeSpan(node), fmt.Sprintf("duplicate import group %q; set group: on one of them", group))
		}
		seenGroups[group] = true
		return nil
	}

	for _, spec := range specs {
		abs, err := resolveImportPath(filepath.Dir(rootFile), spec.path)
		if err != nil {
			return src.at(spec.pathNode, nodeSpan(spec.pathNode), fmt.Sprintf("import %q: %v", spec.path, err))
		}
		canon := canonicalPath(abs)
		if canon == rootCanon {
			return src.at(spec.pathNode, nodeSpan(spec.pathNode), fmt.Sprintf("import %q: a file cannot import itself", spec.path))
		}
		if first, ok := seenFiles[canon]; ok {
			if spec.implicit {
				continue
			}
			if !slices.Equal(first, spec.groups) {
				cfg.Notices = append(cfg.Notices, fmt.Sprintf("import %q is listed again with different groups; the first one wins", spec.path))
			}
			continue
		}
		seenFiles[canon] = spec.groups
		// A compose import's default group is its project name, known only after docker reads the file.
		explicitGroup := spec.group != ""
		if !explicitGroup {
			spec.group = filepath.Base(filepath.Dir(abs))
			if isMakefile(abs) {
				spec.group = makeGroup(rootFile, abs)
			}
		}

		data, err := os.ReadFile(abs)
		if errors.Is(err, fs.ErrNotExist) {
			if err := claim(spec, spec.group); err != nil {
				return err
			}
			if spec.optional {
				cfg.Missing = append(cfg.Missing, missingImport{Group: spec.group, Path: abs})
				continue
			}
			return src.at(spec.pathNode, nodeSpan(spec.pathNode), fmt.Sprintf("import %q: file not found; set optional: true to skip it", spec.path))
		}
		if err != nil && spec.implicit {
			cfg.Notices = append(cfg.Notices, fmt.Sprintf("%s is not imported: %v", filepath.Base(abs), err))
			continue
		}
		if err != nil {
			return src.at(spec.pathNode, nodeSpan(spec.pathNode), fmt.Sprintf("import %q: %v", spec.path, err))
		}

		var targets []Target
		if isMakefile(abs) {
			ref := spec.groupsRef
			if ref == nil {
				ref = spec.servicesRef
			}
			if ref == nil {
				ref = spec.profilesRef
			}
			if ref != nil {
				return src.at(ref, nodeSpan(ref), fmt.Sprintf("import %q: groups, services and profiles apply to rerun and compose files; a Makefile filters with targets:", spec.path))
			}
			mk, err := makefileTargets(spec, abs, data)
			if err != nil && spec.implicit {
				cfg.Notices = append(cfg.Notices, fmt.Sprintf("%s is not imported: %v", filepath.Base(abs), err))
				continue
			}
			if err != nil {
				node := spec.targetsRef
				if node == nil {
					node = spec.pathNode
				}
				return src.at(node, nodeSpan(node), fmt.Sprintf("import %q: %v", spec.path, err))
			}
			if err := claim(spec, spec.group); err != nil {
				return err
			}
			cfg.Files = append(cfg.Files, abs)
			targets = mk
		} else {
			if spec.targetsRef != nil {
				return src.at(spec.targetsRef, nodeSpan(spec.targetsRef), fmt.Sprintf("import %q: targets applies to Makefile imports; a rerun or compose file filters with groups: or services:", spec.path))
			}
			isrc, iroot, err := parseConfigBytes(abs, data)
			if err != nil {
				return err
			}

			if mappingValue(iroot, "services") != nil {
				if spec.groupsRef != nil {
					return src.at(spec.groupsRef, nodeSpan(spec.groupsRef), fmt.Sprintf("import %q: groups applies to rerun files; a compose import filters with services: and profiles:", spec.path))
				}
				project, services, profilesOf, err := resolveComposeImport(spec, abs)
				if errors.Is(err, errNoDocker) && spec.optional {
					cfg.Notices = append(cfg.Notices, fmt.Sprintf("import %q: docker not found on PATH; skipped", spec.path))
					continue
				}
				if err != nil {
					return src.at(spec.pathNode, nodeSpan(spec.pathNode), fmt.Sprintf("import %q: %v", spec.path, err))
				}
				if !explicitGroup {
					spec.group = project
				}
				if err := claim(spec, spec.group); err != nil {
					return err
				}
				cfg.Files = append(cfg.Files, abs)
				if len(services) == 0 {
					cfg.Notices = append(cfg.Notices, fmt.Sprintf("import %q: no services in %s", spec.path, abs))
				}
				targets = composeTargets(abs, spec.group, project, services, spec.profiles, profilesOf)
			} else {
				if spec.servicesRef != nil || spec.profilesRef != nil {
					ref := spec.servicesRef
					if ref == nil {
						ref = spec.profilesRef
					}
					return src.at(ref, nodeSpan(ref), fmt.Sprintf("import %q: services and profiles apply to compose files; this file is a rerun file", spec.path))
				}
				if err := claim(spec, spec.group); err != nil {
					return err
				}
				if mappingValue(iroot, importsKey) != nil {
					cfg.Notices = append(cfg.Notices, fmt.Sprintf("%s: imports are one level deep; the imports key here is ignored", abs))
				}
				cfg.Files = append(cfg.Files, abs)
				if canon != abs && isSymlink(abs) {
					cfg.Files = append(cfg.Files, canon)
				}

				file, err := parseFileBody(isrc, iroot, abs)
				if err != nil {
					return err
				}
				if len(spec.groups) > 0 {
					for _, g := range spec.groups {
						if _, ok := file.schemas[g]; !ok {
							return src.at(spec.groupsRef, nodeSpan(spec.groupsRef), fmt.Sprintf("import %q: unknown group %q", spec.path, g))
						}
					}
				}
				targets = file.targets
			}
		}

		for _, t := range targets {
			if len(spec.groups) > 0 && !slices.Contains(spec.groups, t.Schema) {
				continue
			}
			t.Name = spec.group + "/" + t.Name
			if names[t.Name] {
				if spec.implicit {
					cfg.Notices = append(cfg.Notices, fmt.Sprintf("make target %q is skipped: the name is already used", t.Name))
					continue
				}
				return src.at(spec.pathNode, nodeSpan(spec.pathNode), fmt.Sprintf("duplicate target %q", t.Name))
			}
			names[t.Name] = true
			t.Import = spec.group
			// The importer's same-named group fills in only where the file gives no group.
			inFileGroup := t.Schema != ""
			if inFileGroup {
				t.Schema = spec.group + "/" + t.Schema
				t.Sub = t.Schema
			}
			if root, ok := rootSchemas[spec.group]; ok && !inFileGroup {
				if t.Muted == nil {
					t.Muted = root.muted
				}
				// The importer's group hands its saved queries down too; the file's own win a name clash.
				t.groupQueries = mergeQueries(root.queries, t.groupQueries)
				if t.groupQueryCols == nil {
					t.groupQueryCols = root.columns
				}
				t.Queries = mergeQueries(root.queries, t.Queries)
				if !t.startSet && root.startSet {
					t.Start, t.startSet = root.start, true
				}
				if t.Muted == nil && root.hide {
					muted := true
					t.Muted = &muted
				}
				if root.hide {
					t.GroupHide = true
				}
				if !t.ownColumns {
					t.Schema = spec.group
					t.SchemaFile = rootFile
					t.SchemaLocal = spec.group
					t.Columns = slices.Clone(root.columns)
				}
			}
			// Makefile imports fold by default; a root groups: entry with hide: false unfolds one.
			unfolded := rootSchemas[spec.group].hideSet && !rootSchemas[spec.group].hide
			if !t.startSet && spec.startSet {
				t.Start, t.startSet = spec.start, true
			}
			t.AutoHide = autoHideOf(spec, rootSchemas[spec.group], t.Kind == makeKind)
			if spec.hide || (t.Kind == makeKind && !unfolded) {
				t.GroupHide, t.ImportHide = true, true
			}
			// The import's hide is the outermost level: it only applies where nothing above set muted.
			if spec.hide && t.Muted == nil {
				muted := true
				t.Muted = &muted
			}
			cfg.Targets = append(cfg.Targets, t)
		}
	}
	if mk != "" && rootExists {
		if _, listed := seenFiles[canonicalPath(mk)]; !listed {
			cfg.Notices = append(cfg.Notices, fmt.Sprintf("%s not imported · add it under imports:", filepath.Base(mk)))
		}
	}
	loaded := map[string]bool{}
	for _, t := range cfg.Targets {
		if t.Import != "" {
			loaded[t.Import] = true
		}
	}
	for i := range cfg.Targets {
		if t := &cfg.Targets[i]; t.Import == "" && loaded[t.Schema] {
			t.GroupImport = t.Schema
		}
	}
	return nil
}

// autoHideOf is an import's auto-hide: its own, else its root group's, else on for a Makefile.
func autoHideOf(spec importSpec, root groupDef, makefile bool) bool {
	switch {
	case spec.autoHide != nil:
		return *spec.autoHide
	case root.autoHideSet:
		return root.autoHide
	}
	return makefile
}

// errNoDocker is returned when a compose import needs the docker CLI and it is not on PATH.
var errNoDocker = errors.New("docker not found on PATH")

// resolveComposeImport reads a compose file's project name and services through docker.
func resolveComposeImport(spec importSpec, abs string) (string, []string, map[string][]string, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return "", nil, nil, errNoDocker
	}
	return resolveCompose(abs, spec.profiles, spec.services)
}

// composeTargets makes one compose target per service. They never watch or build, so they start on r only.
func composeTargets(file, group, project string, services, profiles []string, profilesOf map[string][]string) []Target {
	dir := filepath.Dir(file)
	out := make([]Target, 0, len(services))
	for _, svc := range services {
		t := Target{
			Name:     svc,
			Kind:     composeKind,
			Selected: true,
			Start:    startManual,
			Dir:      dir,
			File:     file,
			Compose:  &composeTarget{File: file, Project: project, Service: svc, Profiles: profiles},
		}
		if p := listProfile(profilesOf[svc], profiles); p != "" {
			t.Sub = group + "/" + p
		}
		out = append(out, t)
	}
	return out
}

// makefileTargets makes one manual target per Makefile rule, in file order, limited to spec.targets when set.
func makefileTargets(spec importSpec, path string, data []byte) ([]Target, error) {
	rules, err := parseMakefile(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	for _, want := range spec.targets {
		if !slices.ContainsFunc(rules, func(r makeTarget) bool { return r.Name == want }) {
			return nil, fmt.Errorf("no target %q in %s", want, path)
		}
	}
	// A bare make reads only the Makefile of its own dir, so a .mk file needs -f.
	words := []string{"make"}
	if filepath.Ext(path) == ".mk" {
		words = append(words, "-f", filepath.Base(path))
	}
	cmd := rerun.ShellScript(words) + " "
	dir := filepath.Dir(path)
	var out []Target
	for _, r := range rules {
		if len(spec.targets) > 0 && !slices.Contains(spec.targets, r.Name) {
			continue
		}
		out = append(out, Target{Kind: makeKind, Name: r.Name, Desc: r.Desc, Cmd: cmd + r.Name, Dir: dir, Start: startManual, Selected: true})
	}
	return out, nil
}

// makeGroup is the default group of a Makefile import: `make` beside rerun.yml, else make/<dir name>.
func makeGroup(rootFile, abs string) string {
	if filepath.Dir(abs) == filepath.Dir(rootFile) {
		return makeKind
	}
	return makeKind + "/" + filepath.Base(filepath.Dir(abs))
}

// listProfile is the profile a service groups under: its first one the import names, else its first.
func listProfile(svcProfiles, filter []string) string {
	for _, p := range svcProfiles {
		if len(filter) == 0 || slices.Contains(filter, p) {
			return p
		}
	}
	return ""
}

// parseImportSpec reads one `imports:` entry, a map with a path and optional keys.
func parseImportSpec(src *configSource, node *yaml.Node) (importSpec, error) {
	var spec importSpec
	if node.Kind != yaml.MappingNode {
		return spec, src.at(node, nodeSpan(node), "imports: each entry is a map with a path")
	}

	seen := make(map[string]bool)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := deref(node.Content[i]), deref(node.Content[i+1])
		field := key.Value
		if seen[field] {
			return spec, src.at(key, len(field), fmt.Sprintf("import: duplicate field %q", field))
		}
		seen[field] = true

		switch field {
		case "path", "group":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
				return spec, src.at(value, nodeSpan(value), fmt.Sprintf("import: field %q: expected a string, got %s", field, yamlType(value)))
			}
			if field == "path" {
				spec.path, spec.pathNode = strings.TrimSpace(value.Value), value
			} else {
				spec.group, spec.groupNode = strings.TrimSpace(value.Value), value
			}

		case "auto-hide":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!bool" {
				return spec, src.at(value, nodeSpan(value), fmt.Sprintf("import: field %q: expected a boolean, got %s", field, yamlType(value)))
			}
			b, err := strconv.ParseBool(value.Value)
			if err != nil {
				return spec, src.at(value, nodeSpan(value), fmt.Sprintf("import: field %q: expected a boolean, got %q", field, value.Value))
			}
			spec.autoHide = &b

		case "optional", "hide":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!bool" {
				return spec, src.at(value, nodeSpan(value), fmt.Sprintf("import: field %q: expected a boolean, got %s", field, yamlType(value)))
			}
			b, err := strconv.ParseBool(value.Value)
			if err != nil {
				return spec, src.at(value, nodeSpan(value), fmt.Sprintf("import: field %q: expected a boolean, got %q", field, value.Value))
			}
			if field == "optional" {
				spec.optional = b
			} else {
				spec.hide = b
			}

		case "start":
			m, err := parseStartMode(src, "import", field, value)
			if err != nil {
				return spec, err
			}
			spec.start, spec.startSet = m, true

		case "groups":
			list, _, err := stringOrList(src, "import", field, value)
			if err != nil {
				return spec, err
			}
			spec.groups, spec.groupsRef = list, value

		case "targets":
			list, _, err := stringOrList(src, "import", field, value)
			if err != nil {
				return spec, err
			}
			spec.targets, spec.targetsRef = list, value

		case "services", "profiles":
			list, _, err := stringOrList(src, "import", field, value)
			if err != nil {
				return spec, err
			}
			if field == "services" {
				spec.services, spec.servicesRef = list, value
			} else {
				spec.profiles, spec.profilesRef = list, value
			}

		default:
			e := src.at(key, len(field), fmt.Sprintf("import: unknown field %q", field))
			e.note = "fields: " + strings.Join(importFields, ", ")
			return spec, e
		}
	}

	if spec.pathNode == nil || spec.path == "" {
		return spec, src.at(node, 1, "import: path is required")
	}
	return spec, nil
}

// canonicalPath resolves symlinks so one file imported twice is recognised, or returns p when it cannot.
func canonicalPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// isSymlink reports whether p itself is a symlink, not one of its parent dirs.
func isSymlink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// resolveImportPath makes an import path absolute, relative to the importing file's dir.
func resolveImportPath(dir, p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return filepath.Clean(p), nil
}

// fileExists reports whether p can be stat'd.
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
