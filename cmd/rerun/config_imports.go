package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// importsKey is the reserved top-level key that lists other rerun files.
const importsKey = "imports"

// importFields is the accepted keys of one import entry.
var importFields = []string{"path", "group", "optional", "groups"}

// importSpec is one parsed `imports:` entry.
type importSpec struct {
	path      string
	group     string
	optional  bool
	groups    []string
	pathNode  *yaml.Node
	groupNode *yaml.Node
	groupsRef *yaml.Node
}

// parseImports merges the root file's imports into cfg; a root group named like an import gives its targets columns.
func (cfg *Config) parseImports(src *configSource, node *yaml.Node, rootFile string, rootSchemas map[string][]Column) error {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return nil
	}
	if node.Kind != yaml.SequenceNode {
		return src.at(node, nodeSpan(node), "imports: expected a list of entries with a path")
	}

	rootCanon := canonicalPath(rootFile)
	seenFiles := map[string][]string{}
	seenGroups := map[string]bool{}
	names := map[string]bool{}
	for _, t := range cfg.Targets {
		names[t.Name] = true
	}

	for _, item := range node.Content {
		spec, err := parseImportSpec(src, deref(item))
		if err != nil {
			return err
		}
		abs, err := resolveImportPath(filepath.Dir(rootFile), spec.path)
		if err != nil {
			return src.at(spec.pathNode, nodeSpan(spec.pathNode), fmt.Sprintf("import %q: %v", spec.path, err))
		}
		canon := canonicalPath(abs)
		if canon == rootCanon {
			return src.at(spec.pathNode, nodeSpan(spec.pathNode), fmt.Sprintf("import %q: a file cannot import itself", spec.path))
		}
		if first, ok := seenFiles[canon]; ok {
			if !slices.Equal(first, spec.groups) {
				cfg.Notices = append(cfg.Notices, fmt.Sprintf("import %q is listed again with different groups; the first one wins", spec.path))
			}
			continue
		}
		seenFiles[canon] = spec.groups
		if spec.group == "" {
			spec.group = filepath.Base(filepath.Dir(abs))
		}
		if seenGroups[spec.group] {
			node := spec.groupNode
			if node == nil {
				node = spec.pathNode
			}
			return src.at(node, nodeSpan(node), fmt.Sprintf("duplicate import group %q; set group: on one of them", spec.group))
		}
		seenGroups[spec.group] = true

		data, err := os.ReadFile(abs)
		if errors.Is(err, fs.ErrNotExist) {
			if spec.optional {
				cfg.Missing = append(cfg.Missing, missingImport{Group: spec.group, Path: abs})
				continue
			}
			return src.at(spec.pathNode, nodeSpan(spec.pathNode), fmt.Sprintf("import %q: file not found; set optional: true to skip it", spec.path))
		}
		if err != nil {
			return src.at(spec.pathNode, nodeSpan(spec.pathNode), fmt.Sprintf("import %q: %v", spec.path, err))
		}

		isrc, iroot, err := parseConfigBytes(abs, data)
		if err != nil {
			return err
		}
		if mappingValue(iroot, "services") != nil {
			return src.at(spec.pathNode, nodeSpan(spec.pathNode), "compose imports not supported yet")
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

		for _, t := range file.targets {
			if len(spec.groups) > 0 && !slices.Contains(spec.groups, t.Schema) {
				continue
			}
			t.Name = spec.group + "/" + t.Name
			if names[t.Name] {
				return src.at(spec.pathNode, nodeSpan(spec.pathNode), fmt.Sprintf("duplicate target %q", t.Name))
			}
			names[t.Name] = true
			t.Import = spec.group
			// The importer's same-named group fills in only where the file gives no group.
			inFileGroup := t.Schema != ""
			if inFileGroup {
				t.Schema = spec.group + "/" + t.Schema
			}
			if rootCols, ok := rootSchemas[spec.group]; ok && !t.ownColumns && !inFileGroup {
				t.Schema = spec.group
				t.SchemaFile = rootFile
				t.SchemaLocal = spec.group
				t.Columns = slices.Clone(rootCols)
			}
			cfg.Targets = append(cfg.Targets, t)
		}
	}
	return nil
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

		case "optional":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!bool" {
				return spec, src.at(value, nodeSpan(value), fmt.Sprintf("import: field %q: expected a boolean, got %s", field, yamlType(value)))
			}
			b, err := strconv.ParseBool(value.Value)
			if err != nil {
				return spec, src.at(value, nodeSpan(value), fmt.Sprintf("import: field %q: expected a boolean, got %q", field, value.Value))
			}
			spec.optional = b

		case "groups":
			list, _, err := stringOrList(src, "import", field, value)
			if err != nil {
				return spec, err
			}
			spec.groups, spec.groupsRef = list, value

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
