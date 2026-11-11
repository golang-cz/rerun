package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const fakeComposeProject = `{"name":"devbox","services":{"postgres":{}}}`

func TestLoadConfigComposeOnlyLoadsTheComposeFile(t *testing.T) {
	dir := fakeDocker(t)
	writeFake(t, dir, "config.json", fakeComposeProject)
	proj := makeDir(t, map[string]string{"compose.yaml": "services:\n  postgres:\n    image: postgres\n"})
	cfg, err := LoadConfig(proj)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.MakeOnly {
		t.Fatal("MakeOnly = false, want true")
	}
	if !slices.Contains(targetNames(cfg), "devbox/postgres") {
		t.Fatalf("targets = %v, want devbox/postgres", targetNames(cfg))
	}
}

func TestLoadConfigMakefileAndComposeLoadBoth(t *testing.T) {
	dir := fakeDocker(t)
	writeFake(t, dir, "config.json", fakeComposeProject)
	proj := makeDir(t, map[string]string{
		"Makefile":           "build:\n\tgo build\n",
		"docker-compose.yml": "services:\n  postgres:\n    image: postgres\n",
	})
	cfg, err := LoadConfig(proj)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	names := targetNames(cfg)
	if !slices.Contains(names, "make/build") || !slices.Contains(names, "devbox/postgres") {
		t.Fatalf("targets = %v, want make/build and devbox/postgres", names)
	}
}

func TestParseConfigMissingRerunYMLWithComposeReloadsAsMakeOnly(t *testing.T) {
	dir := fakeDocker(t)
	writeFake(t, dir, "config.json", fakeComposeProject)
	proj := makeDir(t, map[string]string{"compose.yaml": "services:\n  postgres:\n    image: postgres\n"})
	cfg, err := ParseConfig(filepath.Join(proj, ConfigFileName))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !cfg.MakeOnly || !slices.Contains(targetNames(cfg), "devbox/postgres") {
		t.Fatalf("MakeOnly = %v, targets = %v", cfg.MakeOnly, targetNames(cfg))
	}
}

func TestImplicitComposeUsesDockerPrecedence(t *testing.T) {
	dir := makeDir(t, map[string]string{
		"docker-compose.yml":  "services: {}\n",
		"docker-compose.yaml": "services: {}\n",
		"compose.yml":         "services: {}\n",
		"compose.yaml":        "services: {}\n",
	})
	if got, want := implicitCompose(dir), filepath.Join(dir, "compose.yaml"); got != want {
		t.Fatalf("implicitCompose = %q, want %q", got, want)
	}
	if err := os.Remove(filepath.Join(dir, "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	if got, want := implicitCompose(dir), filepath.Join(dir, "compose.yml"); got != want {
		t.Fatalf("after removing compose.yaml: implicitCompose = %q, want %q", got, want)
	}
}

func TestImplicitComposeEmptyWithoutAFile(t *testing.T) {
	dir := makeDir(t, map[string]string{"Makefile": "build:\n\tgo build\n"})
	if got := implicitCompose(dir); got != "" {
		t.Fatalf("implicitCompose = %q, want empty", got)
	}
}

func TestLoadConfigComposeOnlyWithoutDockerNamesDocker(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	proj := makeDir(t, map[string]string{"compose.yaml": "services:\n  postgres:\n    image: postgres\n"})
	_, err := LoadConfig(proj)
	if err == nil || !strings.Contains(err.Error(), "docker") {
		t.Fatalf("err = %v, want an error naming docker", err)
	}
}

func TestLoadConfigMakefileAndComposeWithoutDockerKeepsMakefile(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	proj := makeDir(t, map[string]string{
		"Makefile":     "build:\n\tgo build\n",
		"compose.yaml": "services:\n  postgres:\n    image: postgres\n",
	})
	cfg, err := LoadConfig(proj)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !slices.Contains(targetNames(cfg), "make/build") {
		t.Fatalf("targets = %v, want make/build", targetNames(cfg))
	}
	if !containsNotice(cfg, "docker not found") {
		t.Fatalf("notices = %v, want docker not found", cfg.Notices)
	}
}

func TestLoadConfigComposeFailureKeepsMakefile(t *testing.T) {
	fakeDocker(t) // no config.json, so `docker compose config` fails
	proj := makeDir(t, map[string]string{
		"Makefile":     "build:\n\tgo build\n",
		"compose.yaml": "services:\n  postgres:\n    image: postgres\n",
	})
	cfg, err := LoadConfig(proj)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !slices.Contains(targetNames(cfg), "make/build") {
		t.Fatalf("targets = %v, want make/build", targetNames(cfg))
	}
	if !containsNotice(cfg, "compose.yaml is not imported") {
		t.Fatalf("notices = %v, want compose.yaml is not imported", cfg.Notices)
	}
}

func TestRerunYMLBesidesComposeDoesNotLoadIt(t *testing.T) {
	dir := fakeDocker(t)
	writeFake(t, dir, "config.json", fakeComposeProject)
	proj := makeDir(t, map[string]string{
		ConfigFileName: "targets:\n  web:\n    run: sleep 30\n",
		"compose.yaml": "services:\n  postgres:\n    image: postgres\n",
	})
	cfg, err := LoadConfig(proj)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.MakeOnly || slices.Contains(targetNames(cfg), "devbox/postgres") {
		t.Fatalf("MakeOnly = %v, targets = %v; compose must not load beside rerun.yml", cfg.MakeOnly, targetNames(cfg))
	}
	if !containsNotice(cfg, "compose.yaml not imported") {
		t.Fatalf("notices = %v, want compose.yaml not imported", cfg.Notices)
	}
}

func TestConfigWatchPicksUpAComposeFileCreatedAfterStart(t *testing.T) {
	dir := fakeDocker(t)
	writeFake(t, dir, "config.json", fakeComposeProject)
	proj := makeDir(t, map[string]string{"Makefile": "build:\n\tgo build\n"})
	group, sink := startConfigWatch(t, filepath.Join(proj, ConfigFileName))
	if _, ok := findSnapshot(group, "devbox/postgres"); ok {
		t.Fatal("devbox/postgres loaded before a compose file existed")
	}

	create := func() {
		writeFile(t, filepath.Join(proj, "compose.yaml"), "services:\n  postgres:\n    image: postgres\n")
	}
	waitForKindPoking(t, sink, evReconcile, 10*time.Second, create)

	if _, ok := findSnapshot(group, "devbox/postgres"); !ok {
		t.Fatal("devbox/postgres did not load once compose.yaml appeared")
	}
}

func TestComposeProjectNamedMakeClashesWithTheMakefileGroup(t *testing.T) {
	dir := fakeDocker(t)
	writeFake(t, dir, "config.json", `{"name":"make","services":{"postgres":{}}}`)
	proj := makeDir(t, map[string]string{
		"Makefile":     "build:\n\tgo build\n",
		"compose.yaml": "services:\n  postgres:\n    image: postgres\n",
	})
	cfg, err := LoadConfig(proj)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !slices.Contains(targetNames(cfg), "make/build") {
		t.Fatalf("targets = %v, want make/build", targetNames(cfg))
	}
	if !containsNotice(cfg, `group "make" is already used`) {
		t.Fatalf("notices = %v, want a group clash notice", cfg.Notices)
	}
}
