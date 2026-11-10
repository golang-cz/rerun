package main

import (
	"path/filepath"
	"strings"
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
	writeFile(t, path, "api:\n  run: echo hi\n  muted: true\n\nweb:\n  run: echo web\n")

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
	writeFile(t, path, "groups:\n  backend:\n    muted: true\n\napi:\n  group: backend\n  run: echo api\n\nworker:\n  group: backend\n  run: echo worker\n")

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
	writeFile(t, path, "groups:\n  backend:\n    muted: true\n\napi:\n  group: backend\n  muted: false\n  run: echo api\n")

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
		map[string]string{"bpn/rerun.yml": "api:\n  run: echo api\n  muted: true\n"})

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
		map[string]string{"bpn/rerun.yml": "api:\n  run: echo api\n\nworker:\n  run: echo worker\n  muted: false\n"})

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

func TestParseConfigRetiredTargetHideStartsMuted(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "api:\n  run: echo hi\n  hide: true\n\nweb:\n  run: echo web\n")

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !mutedOf(t, cfg, "api") {
		t.Error("api with hide: true starts muted")
	}
	if mutedOf(t, cfg, "web") {
		t.Error("web without hide starts unmuted")
	}
}

func TestParseConfigFoldGroupFoldsWithoutMuting(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "groups:\n  backend:\n    fold: true\n\napi:\n  group: backend\n  run: echo api\n\nweb:\n  run: echo web\n")

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

func TestParseConfigHidePrecedence(t *testing.T) {
	tests := []struct {
		name, content string
		want          bool
	}{
		{"target muted false beats target hide", "groups:\n  backend:\n    hide: true\n\napi:\n  group: backend\n  hide: true\n  muted: false\n  run: echo api\n", false},
		{"target hide beats group muted false", "groups:\n  backend:\n    muted: false\n\napi:\n  group: backend\n  hide: true\n  run: echo api\n", true},
		{"group muted false beats group hide", "groups:\n  backend:\n    muted: false\n    hide: true\n\napi:\n  group: backend\n  run: echo api\n", false},
		{"group muted true with group hide", "groups:\n  backend:\n    muted: true\n    hide: true\n\napi:\n  group: backend\n  run: echo api\n", true},
		{"target hide false beats group hide", "groups:\n  backend:\n    hide: true\n\napi:\n  group: backend\n  hide: false\n  run: echo api\n", false},
		{"target hide false beats group muted true", "groups:\n  backend:\n    muted: true\n\napi:\n  group: backend\n  hide: false\n  run: echo api\n", false},
		{"target muted true beats target hide false", "groups:\n  backend:\n    hide: true\n\napi:\n  group: backend\n  hide: false\n  muted: true\n  run: echo api\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ConfigFileName)
			writeFile(t, path, tt.content)
			cfg, err := ParseConfig(path)
			if err != nil {
				t.Fatalf("ParseConfig: %v", err)
			}
			if got := mutedOf(t, cfg, "api"); got != tt.want {
				t.Errorf("muted at start = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseConfigRootFoldGroupDoesNotMuteImportedTargets(t *testing.T) {
	f := newImportFixture(t, "imports:\n  - path: ../bpn/rerun.yml\n\ngroups:\n  bpn:\n    fold: true\n",
		map[string]string{"bpn/rerun.yml": "api:\n  run: echo api\n"})

	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	api, _ := cfg.Find("bpn/api")
	if !api.AutoFold || mutedOf(t, cfg, "bpn/api") {
		t.Errorf("bpn/api AutoFold = %v muted = %v, want folded and not muted", api.AutoFold, mutedOf(t, cfg, "bpn/api"))
	}
}

func TestParseConfigHideRejectsNonBool(t *testing.T) {
	tests := []struct{ name, content string }{
		{"target", "api:\n  run: echo api\n  hide: maybe\n"},
		{"group", "groups:\n  backend:\n    hide: maybe\n\napi:\n  group: backend\n  run: echo api\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ConfigFileName)
			writeFile(t, path, tt.content)
			_, err := ParseConfig(path)
			if err == nil || !strings.Contains(err.Error(), `field "hide"`) || !strings.Contains(err.Error(), "expected a boolean") {
				t.Fatalf("ParseConfig error = %v, want a non-boolean hide rejected", err)
			}
		})
	}
}

func TestImportFoldDoesNotMuteTargets(t *testing.T) {
	f := newImportFixture(t,
		"imports:\n  - path: ../bpn/rerun.yml\n    fold: true\n",
		map[string]string{"bpn/rerun.yml": "api:\n  run: echo api\nweb:\n  run: echo web\n  muted: true\n"})
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

func TestImportRetiredHideRejectsNonBoolean(t *testing.T) {
	f := newImportFixture(t,
		"imports:\n  - path: ../bpn/rerun.yml\n    hide: sometimes\n",
		map[string]string{"bpn/rerun.yml": "api:\n  run: echo api\n"})
	_, err := ParseConfig(f.root)
	if err == nil || !strings.Contains(err.Error(), `field "hide"`) {
		t.Fatalf("ParseConfig error = %v, want a non-boolean import hide rejected", err)
	}
}
