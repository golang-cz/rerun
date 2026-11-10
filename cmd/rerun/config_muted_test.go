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
	writeFile(t, path, "targets:\n  api:\n    run: echo hi\n    muted: true\n\n  web:\n    run: echo web\n")

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
	writeFile(t, path, "groups:\n  backend:\n    muted: true\n\ntargets:\n  api:\n    group: backend\n    run: echo api\n\n  worker:\n    group: backend\n    run: echo worker\n")

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
	writeFile(t, path, "groups:\n  backend:\n    muted: true\n\ntargets:\n  api:\n    group: backend\n    muted: false\n    run: echo api\n")

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
		map[string]string{"bpn/rerun.yml": "targets:\n  api:\n    run: echo api\n    muted: true\n"})

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
		map[string]string{"bpn/rerun.yml": "targets:\n  api:\n    run: echo api\n\n  worker:\n    run: echo worker\n    muted: false\n"})

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

func TestParseConfigFoldGroupFoldsWithoutMuting(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "groups:\n  backend:\n    fold: true\n\ntargets:\n  api:\n    group: backend\n    run: echo api\n\n  web:\n    run: echo web\n")

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	api, _ := cfg.Find("api")
	if !api.AutoFold || mutedOf(t, cfg, "api") {
		t.Errorf("api AutoFold = %v muted = %v, want folded and not muted", api.AutoFold, mutedOf(t, cfg, "api"))
	}
	web, _ := cfg.Find("web")
	if web.AutoFold || mutedOf(t, cfg, "web") {
		t.Error("an ungrouped target took the group's fold")
	}
}

func TestParseConfigRootFoldGroupDoesNotMuteImportedTargets(t *testing.T) {
	f := newImportFixture(t, "imports:\n  - path: ../bpn/rerun.yml\n\ngroups:\n  bpn:\n    fold: true\n",
		map[string]string{"bpn/rerun.yml": "targets:\n  api:\n    run: echo api\n"})

	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	api, _ := cfg.Find("bpn/api")
	if !api.AutoFold || mutedOf(t, cfg, "bpn/api") {
		t.Errorf("bpn/api AutoFold = %v muted = %v, want folded and not muted", api.AutoFold, mutedOf(t, cfg, "bpn/api"))
	}
}

func TestImportFoldDoesNotMuteTargets(t *testing.T) {
	f := newImportFixture(t,
		"imports:\n  - path: ../bpn/rerun.yml\n    fold: true\n",
		map[string]string{"bpn/rerun.yml": "targets:\n  api:\n    run: echo api\n  web:\n    run: echo web\n    muted: true\n"})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if mutedOf(t, cfg, "bpn/api") {
		t.Error("bpn/api starts muted under an import with fold: true")
	}
	if !mutedOf(t, cfg, "bpn/web") {
		t.Error("bpn/web with its own muted: true starts muted")
	}
	if api, _ := cfg.Find("bpn/api"); !api.AutoFold {
		t.Error("bpn/api AutoFold = false, want the import's fold: true")
	}
}
