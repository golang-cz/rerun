package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const writeFixture = `# rerun config for the team

api:
  # the API server
  cmd: go run ./cmd/api # local dev
  on: &watch [cmd, internal]

web:
  cmd: pnpm dev
  on: *watch
`

func writeConfigFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, content)
	return path
}

func readConfigText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSetColumnsTargetKeepsCommentsAnchorsAndBlankLines(t *testing.T) {
	path := writeConfigFixture(t, writeFixture)

	if err := setColumns(path, "", "api", []string{"level {.level}", "msg {.msg}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	want := `# rerun config for the team

api:
  # the API server
  cmd: go run ./cmd/api # local dev
  on: &watch [cmd, internal]
  columns: ['level {.level}', 'msg {.msg}']

web:
  cmd: pnpm dev
  on: *watch
`
	if got := readConfigText(t, path); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
}

func TestSetColumnsSchemaReplacesColumnsInPlace(t *testing.T) {
	path := writeConfigFixture(t, `# shared log shapes
schemas:
  backend:
    columns: [level] # the JSON log shape

api:
  schema: backend
  cmd: go run ./cmd/api
`)

	if err := setColumns(path, "backend", "api", []string{"status {.status}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	want := `# shared log shapes
schemas:
  backend:
    columns: ['status {.status}'] # the JSON log shape

api:
  schema: backend
  cmd: go run ./cmd/api
`
	if got := readConfigText(t, path); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
}

func TestSetColumnsCreatesMissingSchemasBlock(t *testing.T) {
	path := writeConfigFixture(t, "api:\n  cmd: echo hi\n")

	if err := setColumns(path, "backend", "", []string{"level {.level}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	want := "api:\n  cmd: echo hi\nschemas:\n  backend:\n    columns: ['level {.level}']\n"
	if got := readConfigText(t, path); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
}

func TestSetColumnsRoundTripsThroughParse(t *testing.T) {
	path := writeConfigFixture(t, "api:\n  schema: backend\n  cmd: echo hi\n\nschemas:\n  backend:\n    columns: [\"a {.b}\"]\n")

	want := []Column{{Name: "level", Expr: ".level"}, {Name: "msg", Expr: ".msg"}}
	if err := setColumns(path, "backend", "", []string{"level {.level}", "msg {.msg}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig after write: %v", err)
	}
	api, _ := cfg.Find("api")
	if !reflect.DeepEqual(api.Columns, want) {
		t.Errorf("columns = %+v, want %+v", api.Columns, want)
	}
}

func TestSetColumnsAliasValueIsReplacedAndAnchorKept(t *testing.T) {
	path := writeConfigFixture(t, "api:\n  cmd: x\n  columns: &cols [\"a {.b}\"]\n\nweb:\n  cmd: y\n  columns: *cols\n")

	if err := setColumns(path, "", "web", []string{"c {.d}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig after write: %v", err)
	}
	api, _ := cfg.Find("api")
	web, _ := cfg.Find("web")
	if !reflect.DeepEqual(api.Columns, []Column{{Name: "a", Expr: ".b"}}) {
		t.Errorf("api columns = %+v, want the anchored list untouched", api.Columns)
	}
	if !reflect.DeepEqual(web.Columns, []Column{{Name: "c", Expr: ".d"}}) {
		t.Errorf("web columns = %+v, want the new list", web.Columns)
	}
}

func TestSetColumnsRejectsBadInputWithoutWriting(t *testing.T) {
	path := writeConfigFixture(t, writeFixture)

	if err := setColumns(path, "", "api", []string{"no braces"}); err == nil {
		t.Fatal("expected an error for a column without {.expr}, got nil")
	}
	if err := setColumns(path, "", "nope", []string{"a {.b}"}); err == nil {
		t.Fatal("expected an error for an unknown target, got nil")
	}
	if got := readConfigText(t, path); got != writeFixture {
		t.Errorf("file changed after a rejected write:\n%s", got)
	}
}
