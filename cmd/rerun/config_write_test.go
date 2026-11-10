package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const writeFixture = `# rerun config for the team

api:
  # the API server
  run: go run ./cmd/api # local dev
  on: &watch [cmd, internal]

web:
  run: pnpm dev
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
  run: go run ./cmd/api # local dev
  on: &watch [cmd, internal]
  columns: ['level {.level}', 'msg {.msg}']

web:
  run: pnpm dev
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
  run: go run ./cmd/api
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
  run: go run ./cmd/api
`
	if got := readConfigText(t, path); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
}

func TestSetColumnsCreatesMissingSchemasBlock(t *testing.T) {
	path := writeConfigFixture(t, "api:\n  run: echo hi\n")

	if err := setColumns(path, "backend", "", []string{"level {.level}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	want := "api:\n  run: echo hi\ngroups:\n  backend:\n    columns: ['level {.level}']\n"
	if got := readConfigText(t, path); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
}

func TestSetColumnsRoundTripsThroughParse(t *testing.T) {
	path := writeConfigFixture(t, "api:\n  schema: backend\n  run: echo hi\n\nschemas:\n  backend:\n    columns: [\"a {.b}\"]\n")

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
	path := writeConfigFixture(t, "api:\n  run: x\n  columns: &cols [\"a {.b}\"]\n\nweb:\n  run: y\n  columns: *cols\n")

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

func TestSetColumnsKeepsFourSpaceIndent(t *testing.T) {
	path := writeConfigFixture(t, "api:\n    run: go run\n    on: [cmd]\n\nweb:\n    run: pnpm dev\n")

	if err := setColumns(path, "", "api", []string{"level {.level}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	want := "api:\n    run: go run\n    on: [cmd]\n    columns: ['level {.level}']\n\nweb:\n    run: pnpm dev\n"
	if got := readConfigText(t, path); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
}

func TestSetColumnsReplacesBlockListAndKeepsTheRest(t *testing.T) {
	path := writeConfigFixture(t, "api:\n  run: go run\n  columns:\n    - level {.level}\n    - msg {.msg} # the text\n  on: [cmd]\n\nweb:\n  run: pnpm dev\n")

	if err := setColumns(path, "", "api", []string{"status {.status}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	want := "api:\n  run: go run\n  columns: ['status {.status}']\n  on: [cmd]\n\nweb:\n  run: pnpm dev\n"
	if got := readConfigText(t, path); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
}

func TestSetColumnsKeepsCRLFLineEndings(t *testing.T) {
	path := writeConfigFixture(t, "api:\r\n  run: go run\r\n\r\nweb:\r\n  run: pnpm dev\r\n")

	if err := setColumns(path, "", "api", []string{"level {.level}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	want := "api:\r\n  run: go run\r\n  columns: ['level {.level}']\r\n\r\nweb:\r\n  run: pnpm dev\r\n"
	if got := readConfigText(t, path); got != want {
		t.Errorf("file = %q\nwant %q", got, want)
	}
}

func TestSetColumnsKeepsMissingFinalNewline(t *testing.T) {
	path := writeConfigFixture(t, "api:\n  run: go run\nweb:\n  run: pnpm dev")

	if err := setColumns(path, "", "web", []string{"level {.level}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	want := "api:\n  run: go run\nweb:\n  run: pnpm dev\n  columns: ['level {.level}']"
	if got := readConfigText(t, path); got != want {
		t.Errorf("file = %q\nwant %q", got, want)
	}
}

func TestSetColumnsWritesThroughSymlinkAndKeepsMode(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.yml")
	writeFile(t, real, "api:\n  run: go run\n")
	if err := os.Chmod(real, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ConfigFileName)
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := setColumns(link, "", "api", []string{"level {.level}"}); err != nil {
		t.Fatalf("setColumns through symlink: %v", err)
	}

	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: info=%v err=%v", info, err)
	}
	if got := readConfigText(t, real); got != "api:\n  run: go run\n  columns: ['level {.level}']\n" {
		t.Errorf("real file = %q", got)
	}
	info, err := os.Stat(real)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestSetColumnsRefusesAnchorUsedByAlias(t *testing.T) {
	const fixture = "api:\n  run: x\n  columns: &cols ['a {.b}']\n\nweb:\n  run: y\n  columns: *cols\n"
	path := writeConfigFixture(t, fixture)

	err := setColumns(path, "", "api", []string{"c {.d}"})
	want := "columns of api are a YAML anchor; edit rerun.yml by hand"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	if got := readConfigText(t, path); got != fixture {
		t.Errorf("file changed after a refused write:\n%s", got)
	}
}

func TestSetColumnsWritesAnchorWithoutAliasesInPlace(t *testing.T) {
	path := writeConfigFixture(t, "api:\n  run: x\n  columns: &cols ['a {.b}']\n")

	if err := setColumns(path, "", "api", []string{"c {.d}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}
	if got := readConfigText(t, path); got != "api:\n  run: x\n  columns: &cols ['c {.d}']\n" {
		t.Errorf("file = %q", got)
	}
}

func TestSetColumnsSchemaCreatesEntryUnderEmptySchemas(t *testing.T) {
	path := writeConfigFixture(t, "schemas:\napi:\n  run: echo hi\n")

	if err := setColumns(path, "backend", "", []string{"level {.level}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	want := "schemas:\n  backend:\n    columns: ['level {.level}']\napi:\n  run: echo hi\n"
	if got := readConfigText(t, path); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
}

func TestSetColumnsLongListStaysOnOneLine(t *testing.T) {
	path := writeConfigFixture(t, "api:\n  run: x\n")
	var cols []string
	for i := 0; i < 12; i++ {
		cols = append(cols, fmt.Sprintf("column%02d {.field%02d}", i, i))
	}

	if err := setColumns(path, "", "api", cols); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(readConfigText(t, path), "\n"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[2], "  columns: [") || !strings.HasSuffix(lines[2], "]") {
		t.Fatalf("expected one columns line, got %q", lines)
	}
}

func TestSetColumnsGroupsBlockKeepsItsSpelling(t *testing.T) {
	path := writeConfigFixture(t, "groups:\n  backend:\n    columns: [level]\n\napi:\n  group: backend\n  run: echo hi\n")

	if err := setColumns(path, "backend", "", []string{"status {.status}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	want := "groups:\n  backend:\n    columns: ['status {.status}']\n\napi:\n  group: backend\n  run: echo hi\n"
	if got := readConfigText(t, path); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
}

func TestSetColumnsFillsFlowEmptyGroup(t *testing.T) {
	path := writeConfigFixture(t, "groups:\n  webapps: {}\n\napi:\n  group: webapps\n  run: echo hi\n")

	if err := setColumns(path, "webapps", "", []string{"level {.level}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	want := "groups:\n  webapps:\n    columns: ['level {.level}']\n\napi:\n  group: webapps\n  run: echo hi\n"
	if got := readConfigText(t, path); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
	if _, err := ParseConfig(path); err != nil {
		t.Errorf("ParseConfig after save: %v", err)
	}
}

func TestSetColumnsFillsFlowEmptyGroupsBlock(t *testing.T) {
	path := writeConfigFixture(t, "groups: {}\napi:\n  run: echo hi\n")

	if err := setColumns(path, "backend", "", []string{"level {.level}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	want := "groups:\n  backend:\n    columns: ['level {.level}']\napi:\n  run: echo hi\n"
	if got := readConfigText(t, path); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
	if _, err := ParseConfig(path); err != nil {
		t.Errorf("ParseConfig after save: %v", err)
	}
}

func TestTemplateColumnsRoundTripThroughWriteAndParse(t *testing.T) {
	path := writeConfigFixture(t, "api:\n  run: echo hi\n")
	edit := colEdit{node: colNode{name: "api"}, cols: []Column{
		{Name: "msg", Template: "{.message} {.error}"},
		{Name: "level", Expr: ".level"},
	}}
	if err := writeColumns(path, edit); err != nil {
		t.Fatalf("writeColumns: %v", err)
	}

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig after write: %v", err)
	}
	api, _ := cfg.Find("api")
	if !reflect.DeepEqual(api.Columns, edit.cols) {
		t.Errorf("columns = %+v, want %+v", api.Columns, edit.cols)
	}
}
