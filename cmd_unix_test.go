//go:build darwin || linux

package rerun

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestPlainArgv(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "server")
	writeScript(t, bin, "#!/bin/sh\n")

	cases := []struct {
		name   string
		script string
		want   []string
		ok     bool
	}{
		{"plain args", "true -x=1", []string{"true", "-x=1"}, true},
		{"absolute binary", bin + " -port 8080", []string{bin, "-port", "8080"}, true},
		{"real binary", "/bin/ls -l", []string{"/bin/ls", "-l"}, true},
		{"relative binary", "./bin/server -x", []string{"./bin/server", "-x"}, true},
		{"missing binary goes direct", "definitely-not-a-binary-xyz arg", []string{"definitely-not-a-binary-xyz", "arg"}, true},
		{"tab separated", "true\t-x", []string{"true", "-x"}, true},
		{"env assignment prefix", "FOO=1 true", nil, false},
		{"and list", "cd x && y", nil, false},
		{"builtin exec", "exec " + bin, nil, false},
		{"builtin cd", "cd /", nil, false},
		{"empty", "", nil, false},
		{"whitespace only", " \t ", nil, false},
		{"newline", "true\nfalse", nil, false},
		{"carriage return", "true\rfalse", nil, false},
		{"colon builtin", ":", nil, false},
		{"break builtin", "break", nil, false},
		{"continue builtin", "continue", nil, false},
		{"non-breaking space", "true\u00a0-x", nil, false},
		{"vertical tab", "true\v-x", nil, false},
		{"form feed", "true\f-x", nil, false},
	}
	for _, c := range shellMeta {
		cases = append(cases, struct {
			name   string
			script string
			want   []string
			ok     bool
		}{fmt.Sprintf("metachar %q", c), "true " + string(c), nil, false})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := plainArgv(tc.script)
			if ok != tc.ok || !slices.Equal(got, tc.want) {
				t.Fatalf("plainArgv(%q) = %q, %v; want %q, %v", tc.script, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestStartShebanglessScriptNamesShebang(t *testing.T) {
	script := filepath.Join(t.TempDir(), "run.sh")
	writeScript(t, script, "echo hi\n")

	_, err := StartCommand([]string{script}, 0)
	if err == nil {
		t.Fatalf("StartCommand(%q) succeeded; want an exec error for a script without #!", script)
	}
	if !strings.Contains(err.Error(), "#!") {
		t.Fatalf("StartCommand(%q) error = %v; want it to suggest a #! line", script, err)
	}
}

func TestStartMissingBinaryNamesProgram(t *testing.T) {
	_, err := StartCommand([]string{"definitely-not-a-binary-xyz arg"}, 0)
	if err == nil || !strings.Contains(err.Error(), "definitely-not-a-binary-xyz") {
		t.Fatalf("StartCommand error = %v; want it to name the program", err)
	}
}

func TestPlainArgvGoRun(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	got, ok := plainArgv("go run .")
	if !ok || !slices.Equal(got, []string{"go", "run", "."}) {
		t.Fatalf("plainArgv(\"go run .\") = %q, %v", got, ok)
	}
}

func TestNewExecCmdPlainSkipsShell(t *testing.T) {
	cmd := newExecCmd([]string{"true", "-x"})
	if cmd.Path == "/bin/sh" {
		t.Fatalf("cmd.Path = %q, want the binary, not /bin/sh", cmd.Path)
	}
	if want := []string{"true", "-x"}; !slices.Equal(cmd.Args, want) {
		t.Fatalf("cmd.Args = %q, want %q", cmd.Args, want)
	}
}

func TestNewExecCmdMetacharUsesShell(t *testing.T) {
	cmd := newExecCmd([]string{"true && true"})
	if cmd.Path != "/bin/sh" {
		t.Fatalf("cmd.Path = %q, want /bin/sh", cmd.Path)
	}
	if want := []string{"/bin/sh", "-c", "true && true"}; !slices.Equal(cmd.Args, want) {
		t.Fatalf("cmd.Args = %q, want %q", cmd.Args, want)
	}
}

func TestNewExecCmdRelativePathRunsInDir(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, filepath.Join(dir, "rerun-plainargv-test", "server"), "#!/bin/sh\nexit 0\n")

	cmd := newExecCmd([]string{"./rerun-plainargv-test/server"})
	WithDir(dir)(cmd)
	if cmd.Path != "./rerun-plainargv-test/server" {
		t.Fatalf("cmd.Path = %q, want the relative binary", cmd.Path)
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("cmd.Run() in dir: %v", err)
	}
}

func TestNewExecCmdColonUsesShell(t *testing.T) {
	cmd := newExecCmd([]string{":"})
	if cmd.Path != "/bin/sh" {
		t.Fatalf("cmd.Path = %q, want /bin/sh", cmd.Path)
	}
}
