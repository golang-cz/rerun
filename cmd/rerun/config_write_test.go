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

func TestSetColumnsKeepsFourSpaceIndent(t *testing.T) {
	path := writeConfigFixture(t, "api:\n    cmd: go run\n    on: [cmd]\n\nweb:\n    cmd: pnpm dev\n")

	if err := setColumns(path, "", "api", []string{"level {.level}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	want := "api:\n    cmd: go run\n    on: [cmd]\n    columns: ['level {.level}']\n\nweb:\n    cmd: pnpm dev\n"
	if got := readConfigText(t, path); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
}

func TestSetColumnsReplacesBlockListAndKeepsTheRest(t *testing.T) {
	path := writeConfigFixture(t, "api:\n  cmd: go run\n  columns:\n    - level {.level}\n    - msg {.msg} # the text\n  on: [cmd]\n\nweb:\n  cmd: pnpm dev\n")

	if err := setColumns(path, "", "api", []string{"status {.status}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	want := "api:\n  cmd: go run\n  columns: ['status {.status}']\n  on: [cmd]\n\nweb:\n  cmd: pnpm dev\n"
	if got := readConfigText(t, path); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
}

func TestSetColumnsKeepsCRLFLineEndings(t *testing.T) {
	path := writeConfigFixture(t, "api:\r\n  cmd: go run\r\n\r\nweb:\r\n  cmd: pnpm dev\r\n")

	if err := setColumns(path, "", "api", []string{"level {.level}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	want := "api:\r\n  cmd: go run\r\n  columns: ['level {.level}']\r\n\r\nweb:\r\n  cmd: pnpm dev\r\n"
	if got := readConfigText(t, path); got != want {
		t.Errorf("file = %q\nwant %q", got, want)
	}
}

func TestSetColumnsKeepsMissingFinalNewline(t *testing.T) {
	path := writeConfigFixture(t, "api:\n  cmd: go run\nweb:\n  cmd: pnpm dev")

	if err := setColumns(path, "", "web", []string{"level {.level}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	want := "api:\n  cmd: go run\nweb:\n  cmd: pnpm dev\n  columns: ['level {.level}']"
	if got := readConfigText(t, path); got != want {
		t.Errorf("file = %q\nwant %q", got, want)
	}
}

func TestSetColumnsWritesThroughSymlinkAndKeepsMode(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.yml")
	writeFile(t, real, "api:\n  cmd: go run\n")
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
	if got := readConfigText(t, real); got != "api:\n  cmd: go run\n  columns: ['level {.level}']\n" {
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
	const fixture = "api:\n  cmd: x\n  columns: &cols ['a {.b}']\n\nweb:\n  cmd: y\n  columns: *cols\n"
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
	path := writeConfigFixture(t, "api:\n  cmd: x\n  columns: &cols ['a {.b}']\n")

	if err := setColumns(path, "", "api", []string{"c {.d}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}
	if got := readConfigText(t, path); got != "api:\n  cmd: x\n  columns: &cols ['c {.d}']\n" {
		t.Errorf("file = %q", got)
	}
}

func TestSetColumnsSchemaCreatesEntryUnderEmptySchemas(t *testing.T) {
	path := writeConfigFixture(t, "schemas:\napi:\n  cmd: echo hi\n")

	if err := setColumns(path, "backend", "", []string{"level {.level}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}

	want := "schemas:\n  backend:\n    columns: ['level {.level}']\napi:\n  cmd: echo hi\n"
	if got := readConfigText(t, path); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
}

func TestSetColumnsLongListStaysOnOneLine(t *testing.T) {
	path := writeConfigFixture(t, "api:\n  cmd: x\n")
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
