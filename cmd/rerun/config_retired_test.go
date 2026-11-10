package main

import (
	"strings"
	"testing"
)

// Retired spellings are plain errors, so a stale config fails loudly instead of loading half-read.
func TestRetiredKeysFailAsUnknownFields(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantSub string
	}{
		{"legacy top-level target", "web:\n  run: x\n", `unknown top-level key "web"`},
		{"schemas block", "schemas:\n  backend:\n    columns: [\"a {.b}\"]\ntargets:\n  api:\n    run: x\n", `unknown top-level key "schemas"`},
		{"schema on a target", "groups:\n  backend: {}\ntargets:\n  api:\n    schema: backend\n    run: x\n", `unknown field "schema"`},
		{"collapsed on a group", "groups:\n  ops:\n    collapsed: true\ntargets:\n  web:\n    run: x\n    group: ops\n", `unknown field "collapsed"`},
		{"auto-hide on a group", "groups:\n  ops:\n    auto-hide: true\ntargets:\n  web:\n    run: x\n    group: ops\n", `unknown field "auto-hide"`},
		{"auto-fold on a group", "groups:\n  ops:\n    auto-fold: true\ntargets:\n  web:\n    run: x\n    group: ops\n", `unknown field "auto-fold"`},
		{"hide on a group", "groups:\n  ops:\n    hide: true\ntargets:\n  web:\n    run: x\n    group: ops\n", `unknown field "hide"`},
		{"collapsed on an import", "imports:\n  - path: Makefile\n    collapsed: true\n", `unknown field "collapsed"`},
		{"auto-hide on an import", "imports:\n  - path: Makefile\n    auto-hide: true\n", `unknown field "auto-hide"`},
		{"auto-fold on an import", "imports:\n  - path: Makefile\n    auto-fold: true\n", `unknown field "auto-fold"`},
		{"hide on an import", "imports:\n  - path: Makefile\n    hide: true\n", `unknown field "hide"`},
		{"muted on a target", "targets:\n  api:\n    run: x\n    muted: true\n", `unknown field "muted"`},
		{"muted on a group", "groups:\n  ops:\n    muted: true\ntargets:\n  web:\n    run: x\n    group: ops\n", `unknown field "muted"`},
		{"selected on a target", "targets:\n  api:\n    run: x\n    selected: false\n", `unknown field "selected"`},
		{"mute drop on a target", "targets:\n  api:\n    run: x\n    mute: drop\n", `field "mute": expected a boolean, got str`},
		{"hide on a target", "targets:\n  api:\n    run: x\n    hide: true\n", `unknown field "hide"`},
		{"cmd on a target", "targets:\n  api:\n    cmd: x\n", `unknown field "cmd"`},
		{"url on a target", "targets:\n  web:\n    run: x\n    url: http://localhost:5173\n", `unknown field "url"`},
		{"open-on-start on a target", "targets:\n  web:\n    run: x\n    open-on-start: true\n", `unknown field "open-on-start"`},
		{"boolean open on a target", "targets:\n  web:\n    run: x\n    open: true\n", `target "web": field "open": expected a string, got bool`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := writeOpenConfig(t, tt.content)
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantSub)
			}
		})
	}
}
