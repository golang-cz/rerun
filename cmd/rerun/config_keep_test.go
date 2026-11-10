package main

import (
	"strings"
	"testing"
	"time"
)

func keepOf(t *testing.T, cfg *Config, name string) keepSpec {
	t.Helper()
	tgt, ok := cfg.Find(name)
	if !ok {
		t.Fatalf("target %q missing", name)
	}
	return tgt.keep()
}

func TestKeepParsesLinesAndDurations(t *testing.T) {
	cfg, err := writeOpenConfig(t, "targets:\n  a:\n    run: x\n    keep: 5000\n  b:\n    run: x\n    keep: 1h\n  c:\n    run: x\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := keepOf(t, cfg, "a"); got.lines != 5000 || got.lineCap() != 5000 {
		t.Errorf("a = %+v, want 5000 lines", got)
	}
	if got := keepOf(t, cfg, "b"); got.age != time.Hour || got.lineCap() != keepAgeLineCap {
		t.Errorf("b = %+v, want 1h under the hidden cap", got)
	}
	if got := keepOf(t, cfg, "c"); got.lineCap() != defaultKeepLines {
		t.Errorf("c = %+v, want the default %d lines", got, defaultKeepLines)
	}
}

func TestKeepInheritsTargetThenGroupThenImportThenDefault(t *testing.T) {
	f := newImportFixture(t,
		"imports:\n  - path: ../bpn/rerun.yml\n    keep: 300\ngroups:\n  backend:\n    keep: 200\ntargets:\n  own:\n    group: backend\n    keep: 100\n    run: x\n  inherits:\n    group: backend\n    run: x\n  plain:\n    run: x\n",
		map[string]string{"bpn/rerun.yml": "groups:\n  tools:\n    keep: 400\ntargets:\n  grouped:\n    group: tools\n    run: x\n  bare:\n    run: x\n  own:\n    keep: 50\n    run: x\n"})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]int{
		"own": 100, "inherits": 200, "plain": defaultKeepLines,
		"bpn/grouped": 400, "bpn/bare": 300, "bpn/own": 50,
	} {
		if got := keepOf(t, cfg, name).lineCap(); got != want {
			t.Errorf("%s keeps %d lines, want %d", name, got, want)
		}
	}
}

func TestMuteInheritsLikeKeepAndImportsTakeIt(t *testing.T) {
	f := newImportFixture(t,
		"imports:\n  - path: ../bpn/rerun.yml\n    mute: true\n",
		map[string]string{"bpn/rerun.yml": "targets:\n  api:\n    run: x\n  web:\n    mute: false\n    run: x\n"})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if !mutedOf(t, cfg, "bpn/api") || mutedOf(t, cfg, "bpn/web") {
		t.Error("the import's mute: true should reach bpn/api, and a target's own mute: false should win")
	}
}

func TestKeepErrors(t *testing.T) {
	tests := []struct{ name, value, want string }{
		{"zero", "0", "a line count must be at least 1"},
		{"negative", "-5", "a line count must be at least 1"},
		{"float", "1.5", "expected a line count like 5000 or a duration like 1h"},
		{"bool", "true", "expected a line count like 5000 or a duration like 1h"},
		{"bad duration", "soon", "expected a line count like 5000 or a duration like 1h"},
		{"negative duration", "-1h", "a duration must be positive"},
		{"zero duration", "0s", "a duration must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := writeOpenConfig(t, "targets:\n  api:\n    run: x\n    keep: "+tt.value+"\n")
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), `field "keep"`) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestConfigGroupsOrderTheList(t *testing.T) {
	cfg, err := writeOpenConfig(t, "groups:\n  zeta: {}\n  alpha: {}\ntargets:\n  a1:\n    group: alpha\n    run: x\n  z1:\n    group: zeta\n    run: x\n  free:\n    run: x\n  a2:\n    group: alpha\n    run: x\n")
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t, cfg.Targets...)
	want := []string{"free", "▾zeta", "z1", "▾alpha", "a1", "a2"}
	if got := listNames(m); !equalStrings(got, want) {
		t.Fatalf("list = %v, want %v", got, want)
	}
}
