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

func TestParseConfigHideTargetStartsMuted(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "api:\n  cmd: echo hi\n  hide: true\n\nweb:\n  cmd: echo web\n")

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

func TestParseConfigHideGroupMutesMembersAndFoldsGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "groups:\n  backend:\n    hide: true\n\napi:\n  group: backend\n  cmd: echo api\n\nweb:\n  cmd: echo web\n")

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !mutedOf(t, cfg, "api") {
		t.Error("a member of a hide: true group starts muted")
	}
	api, _ := cfg.Find("api")
	if !api.GroupHide {
		t.Error("api GroupHide = false, want the group's hide: true")
	}
	web, _ := cfg.Find("web")
	if web.GroupHide || mutedOf(t, cfg, "web") {
		t.Error("an ungrouped target took the group's hide")
	}
}

func TestParseConfigHidePrecedence(t *testing.T) {
	tests := []struct {
		name, content string
		want          bool
	}{
		{"target muted false beats target hide", "groups:\n  backend:\n    hide: true\n\napi:\n  group: backend\n  hide: true\n  muted: false\n  cmd: echo api\n", false},
		{"target hide beats group muted false", "groups:\n  backend:\n    muted: false\n\napi:\n  group: backend\n  hide: true\n  cmd: echo api\n", true},
		{"group muted false beats group hide", "groups:\n  backend:\n    muted: false\n    hide: true\n\napi:\n  group: backend\n  cmd: echo api\n", false},
		{"group muted true with group hide", "groups:\n  backend:\n    muted: true\n    hide: true\n\napi:\n  group: backend\n  cmd: echo api\n", true},
		{"target hide false beats group hide", "groups:\n  backend:\n    hide: true\n\napi:\n  group: backend\n  hide: false\n  cmd: echo api\n", false},
		{"target hide false beats group muted true", "groups:\n  backend:\n    muted: true\n\napi:\n  group: backend\n  hide: false\n  cmd: echo api\n", false},
		{"target muted true beats target hide false", "groups:\n  backend:\n    hide: true\n\napi:\n  group: backend\n  hide: false\n  muted: true\n  cmd: echo api\n", true},
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

func TestParseConfigRootHideGroupMutesImportedTargets(t *testing.T) {
	f := newImportFixture(t, "imports:\n  - path: ../bpn/rerun.yml\n\ngroups:\n  bpn:\n    hide: true\n",
		map[string]string{"bpn/rerun.yml": "api:\n  cmd: echo api\n\nworker:\n  cmd: echo worker\n  muted: false\n"})

	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !mutedOf(t, cfg, "bpn/api") {
		t.Error("an imported target takes the root hide: true group's start muted")
	}
	if mutedOf(t, cfg, "bpn/worker") {
		t.Error("an imported target's own muted: false beats the root hide group")
	}
}

func TestParseConfigHideRejectsNonBool(t *testing.T) {
	tests := []struct{ name, content string }{
		{"target", "api:\n  cmd: echo api\n  hide: maybe\n"},
		{"group", "groups:\n  backend:\n    hide: maybe\n\napi:\n  group: backend\n  cmd: echo api\n"},
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

func TestImportHideMutesTargetsAndFoldsSection(t *testing.T) {
	f := newImportFixture(t,
		"imports:\n  - path: ../bpn/rerun.yml\n    hide: true\n",
		map[string]string{"bpn/rerun.yml": "api:\n  run: echo api\nweb:\n  run: echo web\n  muted: false\n"})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !mutedOf(t, cfg, "bpn/api") {
		t.Error("bpn/api starts muted under an import with hide: true")
	}
	if mutedOf(t, cfg, "bpn/web") {
		t.Error("bpn/web with its own muted: false starts unmuted")
	}
	api, _ := cfg.Find("bpn/api")
	if !api.GroupHide || !api.ImportHide {
		t.Errorf("bpn/api GroupHide=%v ImportHide=%v, want both true", api.GroupHide, api.ImportHide)
	}
}

func TestImportHidePrecedence(t *testing.T) {
	tests := []struct {
		name, root, file string
		want             bool
	}{
		{"import hide mutes an unset target", "imports:\n  - path: ../bpn/rerun.yml\n    hide: true\n", "api:\n  run: echo api\n", true},
		{"target hide false beats import hide", "imports:\n  - path: ../bpn/rerun.yml\n    hide: true\n", "api:\n  hide: false\n  run: echo api\n", false},
		{"target muted true beats target hide false", "imports:\n  - path: ../bpn/rerun.yml\n    hide: true\n", "api:\n  hide: false\n  muted: true\n  run: echo api\n", true},
		{"group muted false beats import hide", "groups:\n  bpn:\n    muted: false\nimports:\n  - path: ../bpn/rerun.yml\n    hide: true\n", "api:\n  run: echo api\n", false},
		{"group hide with import hide mutes", "groups:\n  bpn:\n    hide: true\nimports:\n  - path: ../bpn/rerun.yml\n    hide: true\n", "api:\n  run: echo api\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newImportFixture(t, tt.root, map[string]string{"bpn/rerun.yml": tt.file})
			cfg, err := ParseConfig(f.root)
			if err != nil {
				t.Fatalf("ParseConfig: %v", err)
			}
			if got := mutedOf(t, cfg, "bpn/api"); got != tt.want {
				t.Fatalf("muted = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestImportHideRejectsNonBoolean(t *testing.T) {
	f := newImportFixture(t,
		"imports:\n  - path: ../bpn/rerun.yml\n    hide: sometimes\n",
		map[string]string{"bpn/rerun.yml": "api:\n  run: echo api\n"})
	_, err := ParseConfig(f.root)
	if err == nil || !strings.Contains(err.Error(), `field "hide"`) {
		t.Fatalf("ParseConfig error = %v, want a non-boolean import hide rejected", err)
	}
}
