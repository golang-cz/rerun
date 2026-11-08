package main

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// migrateNotice is the footer notice every file in the legacy form shows.
const migrateNotice = migrateNoticeText

// withoutMigrateNotice drops the legacy-form notice, so a test asserts only the
// notices it is about.
func withoutMigrateNotice(notices []string) []string {
	out := make([]string, 0, len(notices))
	for _, n := range notices {
		if !strings.Contains(n, migrateNotice) {
			out = append(out, n)
		}
	}
	return out
}

func TestParseConfigTargetsKeyMatchesLegacy(t *testing.T) {
	dir := t.TempDir()
	body := `  api:
    desc: the API
    cmd: go run ./cmd/api
    on: [cmd, internal]

  web:
    cmd: pnpm dev
    on: web
`
	legacy := filepath.Join(dir, "legacy.yml")
	writeFile(t, legacy, body)
	marked := filepath.Join(dir, "marked.yml")
	writeFile(t, marked, "targets:\n"+body)

	legacyCfg, err := ParseConfig(legacy)
	if err != nil {
		t.Fatalf("legacy: ParseConfig: %v", err)
	}
	markedCfg, err := ParseConfig(marked)
	if err != nil {
		t.Fatalf("targets: ParseConfig: %v", err)
	}

	legacyTargets := legacyCfg.Targets
	markedTargets := markedCfg.Targets
	for i := range legacyTargets {
		legacyTargets[i].File, markedTargets[i].File = "", ""
	}
	if !reflect.DeepEqual(legacyTargets, markedTargets) {
		t.Errorf("targets = %+v, want the legacy form's %+v", markedTargets, legacyTargets)
	}
}

func TestParseConfigRejectsUnknownTopLevelKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "targets:\n  api:\n    cmd: echo hi\n\napi:\n  cmd: echo hi\n")

	_, err := ParseConfig(path)
	if err == nil || !strings.Contains(err.Error(), `unknown top-level key "api"`) {
		t.Fatalf("err = %v, want an unknown top-level key error", err)
	}
}

func TestParseConfigTargetsKeyAcceptsReservedTargetNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "targets:\n  groups:\n    cmd: echo groups\n  imports:\n    cmd: echo imports\n")

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if got := cfg.Names(); !reflect.DeepEqual(got, []string{"groups", "imports"}) {
		t.Errorf("names = %v, want the reserved names as targets", got)
	}
}

func TestParseConfigLegacyFormNotices(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "api:\n  cmd: echo hi\n")

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if len(cfg.Notices) != 1 || !strings.Contains(cfg.Notices[0], migrateNotice) {
		t.Fatalf("notices = %q, want one migrate notice", cfg.Notices)
	}

	writeFile(t, path, "targets:\n  api:\n    cmd: echo hi\n")
	cfg, err = ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if len(cfg.Notices) != 0 {
		t.Errorf("notices = %q, want none in the targets: form", cfg.Notices)
	}
}

func TestParseConfigRejectsLegacyTargetNamedTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "api:\n  cmd: echo hi\ntargets:\n  cmd: echo targets\n")

	_, err := ParseConfig(path)
	if err == nil || !strings.Contains(err.Error(), "targets is reserved") {
		t.Fatalf("err = %v, want the reserved-name error", err)
	}
}

func TestMigrateRejectsLegacyTargetNamedTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "api:\n  cmd: echo hi\ntargets:\n  cmd: echo targets\n")

	before := readConfigText(t, path)
	if _, err := migrateConfig(path); err == nil || !strings.Contains(err.Error(), "targets is reserved") {
		t.Fatalf("err = %v, want the reserved-name error", err)
	}
	if after := readConfigText(t, path); after != before {
		t.Errorf("migrate rewrote the file:\n%s", after)
	}
}

func TestParseConfigImportsOnlyRootHasNoMigrateNotice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	writeFile(t, path, "imports:\n  - path: b/rerun.yml\n")
	writeFile(t, filepath.Join(dir, "b", ConfigFileName), "api:\n  cmd: echo hi\n")

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if len(cfg.Notices) != 0 {
		t.Errorf("notices = %q, want none for an imports-only root", cfg.Notices)
	}
}

func TestMigrateKeepsCommentsOrderAndTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, `# rerun config for the team

groups:
  backend:
    columns: ["level {.level}"]

# the API server
api:
  group: backend
  cmd: go run ./cmd/api # local dev
  on: [cmd, internal]

web:
  cmd: pnpm dev
`)

	before, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig before migrate: %v", err)
	}
	status, err := migrateConfig(path)
	if err != nil {
		t.Fatalf("migrateConfig: %v", err)
	}
	if status != migrated {
		t.Fatalf("status = %v, want migrated", status)
	}

	text := readConfigText(t, path)
	for _, want := range []string{"# rerun config for the team", "# the API server", "# local dev"} {
		if !strings.Contains(text, want) {
			t.Errorf("migrated file lost %q:\n%s", want, text)
		}
	}
	order := []string{"groups:", "targets:", "api:", "web:"}
	lines := strings.Split(text, "\n")
	last := -1
	for _, key := range order {
		i := slices.IndexFunc(lines, func(l string) bool { return strings.TrimSpace(l) == key })
		if i < 0 {
			t.Fatalf("migrated file has no %q:\n%s", key, text)
		}
		if i < last {
			t.Fatalf("%q is out of order:\n%s", key, text)
		}
		last = i
	}

	after, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig after migrate: %v", err)
	}
	if len(after.Notices) != 0 {
		t.Errorf("notices = %q, want a migrated file to be quiet", after.Notices)
	}
	if !reflect.DeepEqual(before.Targets, after.Targets) {
		t.Errorf("targets = %+v, want the same as before the migrate: %+v", after.Targets, before.Targets)
	}

	status, err = migrateConfig(path)
	if err != nil {
		t.Fatalf("migrateConfig again: %v", err)
	}
	if status != migrateAlready {
		t.Errorf("status = %v, want migrateAlready", status)
	}
	if again := readConfigText(t, path); again != text {
		t.Errorf("a second migrate changed the file:\n%s", again)
	}
}

func TestMigrateLeavesReservedKeysInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	writeFile(t, path, "api:\n  cmd: echo hi\n\nimports:\n  - path: tools.mk\n")
	writeFile(t, filepath.Join(dir, "tools.mk"), "lint:\n\tgo vet\n")

	if _, err := migrateConfig(path); err != nil {
		t.Fatalf("migrateConfig: %v", err)
	}
	text := readConfigText(t, path)
	if strings.Index(text, "targets:") > strings.Index(text, "imports:") {
		t.Errorf("imports moved above targets:\n%s", text)
	}
	if _, err := ParseConfig(path); err != nil {
		t.Fatalf("ParseConfig after migrate: %v", err)
	}
}

func TestSetColumnsMatchesTheFileForm(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "targets form",
			in:   "targets:\n  api:\n    cmd: go run ./cmd/api\n",
			want: "targets:\n  api:\n    cmd: go run ./cmd/api\n    columns: ['level {.level}']\n",
		},
		{
			name: "legacy form",
			in:   "api:\n  cmd: go run ./cmd/api\n",
			want: "api:\n  cmd: go run ./cmd/api\n  columns: ['level {.level}']\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ConfigFileName)
			writeFile(t, path, tt.in)

			if err := setColumns(path, "", "api", []string{"level {.level}"}); err != nil {
				t.Fatalf("setColumns: %v", err)
			}
			if got := readConfigText(t, path); got != tt.want {
				t.Errorf("file =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestSetColumnsSchemaInTargetsFormKeepsTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "targets:\n  api:\n    cmd: echo hi\n")

	if err := setColumns(path, "backend", "", []string{"level {.level}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}
	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig after write: %v", err)
	}
	if got := cfg.Names(); !reflect.DeepEqual(got, []string{"api"}) {
		t.Errorf("names = %v, want the target kept", got)
	}
	if len(cfg.Notices) != 0 {
		t.Errorf("notices = %q, want the groups block added, not a form change", cfg.Notices)
	}
}

func TestParseArgsMigrate(t *testing.T) {
	args, err := parseArgs([]string{"-migrate"}, nil, true, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if !args.Migrate {
		t.Error("Migrate = false, want true")
	}
}

func TestMigrateFollowsASymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.yml")
	writeFile(t, real, "api:\n  cmd: echo hi\n")
	link := filepath.Join(dir, ConfigFileName)
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink: %v", err)
	}

	if _, err := migrateConfig(link); err != nil {
		t.Fatalf("migrateConfig: %v", err)
	}
	if got := readConfigText(t, real); !strings.Contains(got, "targets:") {
		t.Errorf("target of the symlink was not migrated:\n%s", got)
	}
}
