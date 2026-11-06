package main

import (
	"path/filepath"
	"testing"
)

func mutedOf(t *testing.T, cfg *Config, name string) bool {
	t.Helper()
	tgt, ok := cfg.Find(name)
	if !ok {
		t.Fatalf("target %q missing", name)
	}
	return tgt.mutedAtStart()
}

func TestParseConfigMutedTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "api:\n  cmd: echo hi\n  muted: true\n\nweb:\n  cmd: echo web\n")

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !mutedOf(t, cfg, "api") {
		t.Error("api with muted: true starts muted")
	}
	if web, _ := cfg.Find("web"); web.Muted != nil {
		t.Errorf("web without muted: Muted = %v, want unset", *web.Muted)
	}
}

func TestParseConfigMutedGroupIsTheMembersDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "groups:\n  backend:\n    muted: true\n\napi:\n  group: backend\n  cmd: echo api\n\nworker:\n  group: backend\n  cmd: echo worker\n")

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !mutedOf(t, cfg, "api") || !mutedOf(t, cfg, "worker") {
		t.Error("members of a muted: true group start muted")
	}
}

func TestParseConfigTargetMutedFalseOverridesGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "groups:\n  backend:\n    muted: true\n\napi:\n  group: backend\n  muted: false\n  cmd: echo api\n")

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	api, _ := cfg.Find("api")
	if api.Muted == nil || *api.Muted {
		t.Fatalf("api Muted = %v, want an explicit false", api.Muted)
	}
	if mutedOf(t, cfg, "api") {
		t.Error("muted: false on a target overrides its muted: true group")
	}
}

func TestParseConfigMutedImportedTarget(t *testing.T) {
	f := newImportFixture(t, "imports:\n  - path: ../bpn/rerun.yml\n",
		map[string]string{"bpn/rerun.yml": "api:\n  cmd: echo api\n  muted: true\n"})

	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !mutedOf(t, cfg, "bpn/api") {
		t.Error("an imported target with muted: true starts muted")
	}
}

func TestParseConfigRootGroupMutesImportedTargets(t *testing.T) {
	f := newImportFixture(t, "imports:\n  - path: ../bpn/rerun.yml\n\ngroups:\n  bpn:\n    muted: true\n",
		map[string]string{"bpn/rerun.yml": "api:\n  cmd: echo api\n\nworker:\n  cmd: echo worker\n  muted: false\n"})

	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !mutedOf(t, cfg, "bpn/api") {
		t.Error("a root group named like the import mutes its imported targets")
	}
	if mutedOf(t, cfg, "bpn/worker") {
		t.Error("an imported target's own muted: false beats the root group")
	}
}

func TestParseConfigMutedComposeImportedTarget(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "config.json", `{"name":"devbox","services":{"postgres":{}}}`)
	composeYML := "services:\n  postgres:\n    image: postgres\n"
	f := newImportFixture(t, "imports:\n  - path: ../devbox/docker-compose.yml\n\ngroups:\n  devbox:\n    muted: true\n",
		map[string]string{"devbox/docker-compose.yml": composeYML})

	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !mutedOf(t, cfg, "devbox/postgres") {
		t.Error("a root group named like the compose project mutes its services")
	}
}
