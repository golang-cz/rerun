package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseGrammar(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want Args
	}{
		{
			name: "ad-hoc",
			argv: []string{"--", "go", "test", "./..."},
			want: Args{Cmd: []string{"go", "test", "./..."}},
		},
		{
			name: "bare watchlist and except",
			argv: []string{"a", "b", "--except", "c", "d", "--", "go", "test"},
			want: Args{Watch: []string{"a", "b"}, Except: []string{"c", "d"}, Cmd: []string{"go", "test"}},
		},
		{
			name: "on synonym",
			argv: []string{"--on", "a", "b", "--except", "c", "d", "--", "go", "test"},
			want: Args{Watch: []string{"a", "b"}, Except: []string{"c", "d"}, Cmd: []string{"go", "test"}},
		},
		{
			name: "bare watch only",
			argv: []string{"a", "--", "cmd"},
			want: Args{Watch: []string{"a"}, Cmd: []string{"cmd"}},
		},
		{
			name: "except only",
			argv: []string{"--except", "vendor", "--", "cmd"},
			want: Args{Except: []string{"vendor"}, Cmd: []string{"cmd"}},
		},
		{
			name: "value flags",
			argv: []string{"-delay", "300ms", "-grace", "2s", "--", "cmd"},
			want: Args{Delay: 300 * time.Millisecond, Grace: 2 * time.Second, Cmd: []string{"cmd"}},
		},
		{
			name: "cooldown flag",
			argv: []string{"-cooldown", "1s", "--", "cmd"},
			want: Args{Cooldown: 1 * time.Second, Cmd: []string{"cmd"}},
		},
		{
			name: "bool flag",
			argv: []string{"-no-gitignore", "--", "cmd"},
			want: Args{NoGitignore: true, Cmd: []string{"cmd"}},
		},
		{
			name: "single bare arg is a target",
			argv: []string{"test"},
			want: Args{Target: "test"},
		},
		{
			name: "multiple bare args are targets",
			argv: []string{"frontend", "api"},
			want: Args{Targets: []string{"frontend", "api"}},
		},
		{
			name: "zero args defers to rerun.yml",
			argv: nil,
			want: Args{},
		},
		{
			name: "legacy synonyms",
			argv: []string{"-watch", "a", "-ignore", "b", "-run", "c", "d"},
			want: Args{Watch: []string{"a"}, Except: []string{"b"}, Cmd: []string{"c", "d"}, Legacy: true},
		},
		{
			name: "legacy run without watch",
			argv: []string{"-run", "go", "test"},
			want: Args{Cmd: []string{"go", "test"}, Legacy: true},
		},
		{
			name: "help short",
			argv: []string{"-h"},
			want: Args{Help: true},
		},
		{
			name: "help long",
			argv: []string{"-help"},
			want: Args{Help: true},
		},
		{
			name: "version",
			argv: []string{"-version"},
			want: Args{Version: true},
		},
		{
			name: "version short",
			argv: []string{"-v"},
			want: Args{Version: true},
		},
		{
			name: "version double dash",
			argv: []string{"--version"},
			want: Args{Version: true},
		},
		{
			name: "target with delay override",
			argv: []string{"test", "-delay", "1s"},
			want: Args{Target: "test", Delay: time.Second},
		},
		{
			name: "target with all tuning overrides",
			argv: []string{"test", "-grace", "2s", "-cooldown", "100ms"},
			want: Args{Target: "test", Grace: 2 * time.Second, Cooldown: 100 * time.Millisecond},
		},
		{
			name: "target with bool flag",
			argv: []string{"test", "-no-gitignore"},
			want: Args{Target: "test", NoGitignore: true},
		},
		{
			name: "list switch back",
			argv: []string{"--except", "x", "--on", "y", "--", "cmd"},
			want: Args{Watch: []string{"y"}, Except: []string{"x"}, Cmd: []string{"cmd"}},
		},
		{
			name: "flags after separator are verbatim",
			argv: []string{"--", "-delay", "1s"},
			want: Args{Cmd: []string{"-delay", "1s"}},
		},
		{
			name: "legacy run after on",
			argv: []string{"--on", "a", "-run", "c"},
			want: Args{Watch: []string{"a"}, Cmd: []string{"c"}, Legacy: true},
		},
		{
			name: "help wins over other args",
			argv: []string{"a", "-delay", "1s", "-h", "q"},
			want: Args{Help: true, Watch: []string{"a"}, Delay: time.Second},
		},
		{
			name: "version wins over other args",
			argv: []string{"--on", "x", "-version"},
			want: Args{Version: true, Watch: []string{"x"}},
		},
		{
			// An empty bare word is a watch word, not a target name.
			name: "empty string is a watch word",
			argv: []string{"", "--", "cmd"},
			want: Args{Watch: []string{""}, Cmd: []string{"cmd"}},
		},
		{
			name: "empty except list",
			argv: []string{"--except", "--", "cmd"},
			want: Args{Cmd: []string{"cmd"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			got, err := parseArgs(tt.argv, strings.NewReader(""), true, &stderr)
			if err != nil {
				t.Fatalf("parseArgs(%q) error: %v", tt.argv, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseArgs(%q)\n got: %+v\nwant: %+v", tt.argv, got, tt.want)
			}
			if !tt.want.Legacy && stderr.Len() != 0 {
				t.Fatalf("unexpected stderr: %q", stderr.String())
			}
		})
	}
}

// TestParseOnEqualsBare proves --on and bare words parse identically.
func TestParseOnEqualsBare(t *testing.T) {
	var stderr bytes.Buffer
	bare, err := parseArgs([]string{"a", "b", "--except", "c", "--", "cmd"}, strings.NewReader(""), true, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	on, err := parseArgs([]string{"--on", "a", "b", "--except", "c", "--", "cmd"}, strings.NewReader(""), true, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(bare, on) {
		t.Fatalf("bare %+v != --on %+v", bare, on)
	}
}

// TestLegacyEqualsNew proves the legacy spelling matches the new one.
func TestLegacyEqualsNew(t *testing.T) {
	var stderr bytes.Buffer
	legacy, err := parseArgs([]string{"-watch", "a", "-ignore", "b", "-run", "c", "d"}, strings.NewReader(""), true, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !legacy.Legacy {
		t.Fatal("Legacy flag not set")
	}
	legacy.Legacy = false

	modern, err := parseArgs([]string{"--on", "a", "--except", "b", "--", "c", "d"}, strings.NewReader(""), true, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(legacy, modern) {
		t.Fatalf("legacy %+v != modern %+v", legacy, modern)
	}
}

func TestLegacyDeprecationPrintedOnce(t *testing.T) {
	var stderr bytes.Buffer
	got, err := parseArgs([]string{"-watch", "a", "-ignore", "b", "-run", "c"}, strings.NewReader(""), true, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Legacy {
		t.Fatal("Legacy flag not set")
	}
	want := legacyDeprecation + "\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name    string
		argv    []string
		wantSub string
	}{
		{"empty command after separator", []string{"--"}, "empty command"},
		{"empty command after watch", []string{"a", "--"}, "empty command"},
		{"double separator", []string{"--", "--", "x"}, "empty command"},
		{"legacy run empty", []string{"-run"}, "empty command"},
		{"missing delay value", []string{"-delay"}, "missing value"},
		{"bad duration", []string{"-delay", "abc", "--", "cmd"}, `invalid duration "abc"`},
		{"bad cooldown", []string{"-cooldown", "5", "--", "cmd"}, "invalid duration"},
		{"unknown flag", []string{"-bogus"}, `unknown flag "-bogus"`},
		{"bare dash", []string{"-", "--", "cmd"}, "reserved for stdin"},
		{"bare dash as except", []string{"--except", "-", "--", "cmd"}, "reserved for stdin"},
		{"on without separator", []string{"--on", "a"}, noCmdHint},
		{"target with list flag", []string{"test", "--on", "x"}, noCmdHint},
		{"target with except", []string{"test", "--except", "x"}, noCmdHint},
		{"target with legacy watch", []string{"test", "-watch", "x"}, noCmdHint},
		{"negative delay", []string{"-delay", "-1s", "--", "cmd"}, "must not be negative"},
		{"negative grace", []string{"-grace", "-2s", "--", "cmd"}, "must not be negative"},
		{"negative target delay", []string{"test", "-cooldown", "-100ms"}, "must not be negative"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			_, err := parseArgs(tt.argv, strings.NewReader(""), true, &stderr)
			if err == nil {
				t.Fatalf("parseArgs(%q) expected error, got nil", tt.argv)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantSub)
			}
		})
	}
}

func TestReadStdinListCap(t *testing.T) {
	atCap := strings.Repeat("a.go\n", maxStdinPaths)
	paths, err := readStdinList(strings.NewReader(atCap))
	if err != nil {
		t.Fatalf("readStdinList at the cap: %v", err)
	}
	if len(paths) != maxStdinPaths {
		t.Fatalf("got %d paths, want %d", len(paths), maxStdinPaths)
	}

	overCap := strings.Repeat("a.go\n", maxStdinPaths+1)
	if _, err := readStdinList(strings.NewReader(overCap)); err == nil {
		t.Fatalf("readStdinList accepted %d paths, want an error", maxStdinPaths+1)
	} else if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %q, want an \"exceeds\" message", err.Error())
	}
}

func TestParseStdinWatchlist(t *testing.T) {
	var stderr bytes.Buffer
	got, err := parseArgs([]string{"--", "go", "test"}, strings.NewReader("a.go\n b.go \n\n"), false, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	want := Args{Watch: []string{"a.go", "b.go"}, Cmd: []string{"go", "test"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseStdinSkipped(t *testing.T) {
	tests := []struct {
		name    string
		argv    []string
		isTTY   bool
		wantLen int
	}{
		{"tty", []string{"--", "cmd"}, true, 0},
		{"watch words given", []string{"x", "--", "cmd"}, false, 1},
		{"on words given", []string{"--on", "x", "--", "cmd"}, false, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			got, err := parseArgs(tt.argv, strings.NewReader("a.go\n"), tt.isTTY, &stderr)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Watch) != tt.wantLen {
				t.Fatalf("Watch = %q, want len %d", got.Watch, tt.wantLen)
			}
		})
	}
}

func TestUsageMentionsDeprecatedFlags(t *testing.T) {
	for _, token := range []string{"-watch", "-ignore", "-run", "--on", "--except"} {
		if !strings.Contains(usage, token) {
			t.Fatalf("usage does not mention %q", token)
		}
	}
}
