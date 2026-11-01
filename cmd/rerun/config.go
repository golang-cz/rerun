package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/golang-cz/rerun"
	"gopkg.in/yaml.v3"
)

const ConfigFileName = "rerun.yml"

const defaultDelay = 200 * time.Millisecond

// ErrConfigNotFound reports that no rerun.yml was found on the way up.
var ErrConfigNotFound = errors.New("no rerun.yml found")

type configNotFoundError struct{ dir string }

func (e *configNotFoundError) Error() string {
	return fmt.Sprintf("%v in %s or any parent directory", ErrConfigNotFound, e.dir)
}

func (e *configNotFoundError) Unwrap() error { return ErrConfigNotFound }

type Target struct {
	Name     string
	Desc     string
	On       []string
	Except   []string
	Cmd      string
	URL      string
	Delay    time.Duration
	Cooldown time.Duration
	Grace    time.Duration
	// Selected is false for `selected: false` targets: they exist in the
	// list but start off until they are started by hand.
	Selected bool
	// Open asks rerun to open URL in the browser on the first start.
	Open bool
}

type Config struct {
	Path    string
	Targets []Target
}

func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Targets))
	for _, t := range c.Targets {
		names = append(names, t.Name)
	}
	return names
}

func (c *Config) Find(name string) (Target, bool) {
	for _, t := range c.Targets {
		if t.Name == name {
			return t, true
		}
	}
	return Target{}, false
}

func LoadConfig(startDir string) (*Config, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return nil, err
	}

	for {
		path := filepath.Join(dir, ConfigFileName)
		if _, err := os.Stat(path); err == nil {
			return ParseConfig(path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}

		if isGitRoot(dir) {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return nil, &configNotFoundError{dir: startDir}
}

func isGitRoot(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func ParseConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %s", path, cleanYAMLError(err))
	}

	root := &doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil, fmt.Errorf("%s: no targets defined", path)
		}
		root = root.Content[0]
	}
	root = deref(root)
	if root.Kind == 0 || (root.Kind == yaml.ScalarNode && root.Tag == "!!null") {
		return nil, fmt.Errorf("%s: no targets defined", path)
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: expected a mapping of target names", path)
	}
	if len(root.Content) == 0 {
		return nil, fmt.Errorf("%s: no targets defined", path)
	}

	cfg := &Config{Path: path}
	seen := make(map[string]bool)
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := deref(root.Content[i]), deref(root.Content[i+1])
		if key.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("%s: target names must be strings", path)
		}
		name := key.Value
		if seen[name] {
			return nil, fmt.Errorf("%s: duplicate target %q", path, name)
		}
		seen[name] = true

		target, err := parseTarget(path, name, value)
		if err != nil {
			return nil, err
		}
		cfg.Targets = append(cfg.Targets, target)
	}

	return cfg, nil
}

func parseTarget(path, name string, node *yaml.Node) (Target, error) {
	node = deref(node)
	if node.Kind != yaml.MappingNode {
		return Target{}, fmt.Errorf("%s: target %q: expected a mapping of fields", path, name)
	}

	target := Target{Name: name, Delay: defaultDelay, Grace: rerun.DefaultGrace, Selected: true}
	seen := make(map[string]bool)
	for i := 0; i+1 < len(node.Content); i += 2 {
		field := deref(node.Content[i]).Value
		value := deref(node.Content[i+1])
		if seen[field] {
			return Target{}, fmt.Errorf("%s: target %q: duplicate field %q", path, name, field)
		}
		seen[field] = true

		switch field {
		case "desc":
			s, err := scalarField(path, name, field, value)
			if err != nil {
				return Target{}, err
			}
			target.Desc = s

		case "on", "except":
			list, err := stringOrList(path, name, field, value)
			if err != nil {
				return Target{}, err
			}
			if field == "on" {
				target.On = list
			} else {
				target.Except = list
			}

		case "cmd", "url":
			s, err := scalarField(path, name, field, value)
			if err != nil {
				return Target{}, err
			}
			if field == "cmd" {
				target.Cmd = s
			} else {
				target.URL = s
			}

		case "open", "selected":
			b, err := boolField(path, name, field, value)
			if err != nil {
				return Target{}, err
			}
			if field == "open" {
				target.Open = b
			} else {
				target.Selected = b
			}

		case "delay", "cooldown", "grace":
			d, err := durationField(path, name, field, value)
			if err != nil {
				return Target{}, err
			}
			switch field {
			case "delay":
				target.Delay = d
			case "cooldown":
				target.Cooldown = d
			case "grace":
				target.Grace = d
			}

		default:
			return Target{}, fmt.Errorf("%s: target %q: unknown field %q", path, name, field)
		}
	}

	if strings.TrimSpace(target.Cmd) == "" {
		return Target{}, fmt.Errorf("%s: target %q: cmd is required", path, name)
	}
	return target, nil
}

// deref follows `*name` to the anchored node, which may itself be an alias.
func deref(node *yaml.Node) *yaml.Node {
	for node != nil && node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	return node
}

func scalarField(path, name, field string, node *yaml.Node) (string, error) {
	node = deref(node)
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", fmt.Errorf("%s: target %q: field %q: expected a string, got %s", path, name, field, yamlType(node))
	}
	return node.Value, nil
}

func boolField(path, name, field string, node *yaml.Node) (bool, error) {
	node = deref(node)
	if node.Kind != yaml.ScalarNode || node.Tag != "!!bool" {
		return false, fmt.Errorf("%s: target %q: field %q: expected a boolean, got %s", path, name, field, yamlType(node))
	}
	b, err := strconv.ParseBool(node.Value)
	if err != nil {
		return false, fmt.Errorf("%s: target %q: field %q: expected a boolean, got %q", path, name, field, node.Value)
	}
	return b, nil
}

// yamlType names a node's type for error messages, e.g. "int" for !!int.
func yamlType(node *yaml.Node) string {
	if node.Tag == "" {
		return "unknown"
	}
	return strings.TrimPrefix(node.Tag, "!!")
}

func stringOrList(path, name, field string, node *yaml.Node) ([]string, error) {
	node = deref(node)
	switch node.Kind {
	case yaml.ScalarNode:
		// `on:` with no value decodes to a null scalar; that means "no paths".
		if node.Tag == "!!null" || strings.TrimSpace(node.Value) == "" {
			return nil, nil
		}
		if node.Tag != "!!str" {
			return nil, fmt.Errorf("%s: target %q: field %q: expected a string or a list of strings, got %s", path, name, field, yamlType(node))
		}
		return []string{node.Value}, nil

	case yaml.SequenceNode:
		list := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			item = deref(item)
			if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
				return nil, fmt.Errorf("%s: target %q: field %q: expected a list of strings, got %s", path, name, field, yamlType(item))
			}
			list = append(list, item.Value)
		}
		return list, nil

	default:
		return nil, fmt.Errorf("%s: target %q: field %q: expected a string or a list of strings", path, name, field)
	}
}

func durationField(path, name, field string, node *yaml.Node) (time.Duration, error) {
	node = deref(node)
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return 0, fmt.Errorf("%s: target %q: field %q: expected a duration string, got %s", path, name, field, yamlType(node))
	}
	d, err := time.ParseDuration(node.Value)
	if err != nil {
		return 0, fmt.Errorf("%s: target %q: field %q: invalid duration %q", path, name, field, node.Value)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s: target %q: field %q: duration must not be negative, got %q", path, name, field, node.Value)
	}
	return d, nil
}

func cleanYAMLError(err error) string {
	return strings.TrimPrefix(err.Error(), "yaml: ")
}
