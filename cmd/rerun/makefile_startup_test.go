package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// makeDir is a repo dir with a .git stop, so LoadConfig does not walk above it.
func makeDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main\n")
	for rel, content := range files {
		writeFile(t, filepath.Join(dir, rel), content)
	}
	return dir
}

func TestLoadConfigMakefileOnlyStartsFromTheMakefile(t *testing.T) {
	dir := makeDir(t, map[string]string{"Makefile": "build:\n\tgo build\n"})
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.MakeOnly {
		t.Fatal("MakeOnly = false, want true")
	}
	if _, ok := cfg.Find("make/build"); !ok {
		t.Fatalf("targets = %v, want make/build", targetNames(cfg))
	}
	if want := filepath.Join(dir, ConfigFileName); cfg.Path != want {
		t.Fatalf("Path = %q, want %q so a later rerun.yml reloads", cfg.Path, want)
	}
}

func TestLoadConfigWithRerunYMLIsNotMakeOnly(t *testing.T) {
	dir := makeDir(t, map[string]string{
		"Makefile":     "build:\n\tgo build\n",
		ConfigFileName: "web:\n  cmd: sleep 30\n",
	})
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.MakeOnly {
		t.Fatal("MakeOnly = true with a rerun.yml present")
	}
}

func TestLoadConfigNeitherKeepsTheError(t *testing.T) {
	dir := makeDir(t, nil)
	_, err := LoadConfig(dir)
	var nf *configNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want configNotFoundError", err)
	}
}

func TestParseConfigMissingRerunYMLWithMakefileReloadsAsMakeOnly(t *testing.T) {
	dir := makeDir(t, map[string]string{"Makefile": "build:\n\tgo build\n"})
	cfg, err := ParseConfig(filepath.Join(dir, ConfigFileName))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !cfg.MakeOnly || len(cfg.Targets) != 1 {
		t.Fatalf("MakeOnly = %v, targets = %v", cfg.MakeOnly, targetNames(cfg))
	}
}

func TestMakeOnlyWithoutRulesIsAnError(t *testing.T) {
	dir := makeDir(t, map[string]string{"Makefile": "# nothing here\nVAR := 1\n"})
	_, err := LoadConfig(dir)
	if err == nil || !strings.Contains(err.Error(), "no targets defined") {
		t.Fatalf("err = %v, want no targets defined", err)
	}
}

func TestMakeOnlyConfigOpensTheFinderOnFirstFrame(t *testing.T) {
	dir := makeDir(t, map[string]string{"Makefile": "build:\n\tgo build\n"})
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	m := newConfigTUI(cfg.Targets, cfg)
	m.width, m.height = 100, 24
	if !m.finder.open {
		t.Fatal("finder closed on the first frame")
	}
	if view := m.View().Content; !strings.Contains(view, "find…") {
		t.Fatalf("first frame has no open finder:\n%s", view)
	}
}

// listed reports whether the named target has a row in the list as drawn.
func listed(m *tuiModel, name string) bool {
	for _, r := range m.listRows() {
		if !r.header && m.targets[r.target].spec.Name == name {
			return true
		}
	}
	return false
}

// A rerun.yml with a Makefile import: the make group is folded, so the finder must unfold it.
func newMakeTUI(t *testing.T) *tuiModel {
	t.Helper()
	dir := makeDir(t, map[string]string{
		"Makefile":     "test:\n\ttrue\n\nlint:\n\ttrue\n",
		ConfigFileName: "web:\n  cmd: sleep 30\n",
	})
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	m := newConfigTUI(cfg.Targets, cfg)
	m.finder = finderState{}
	m.width, m.height = 100, 24
	return m
}

func TestFinderEnterRunsAMakeRowOnce(t *testing.T) {
	m := newMakeTUI(t)
	if listed(m, "make/test") {
		t.Fatal("make row is listed before its group is unfolded")
	}

	m.Update(ctrlK)
	press(m, "t", "e", "s", "t")
	if got := m.finderResults(m.finder.query); len(got) == 0 || !got[0].make {
		t.Fatalf("results for %q = %v, want the make row first", m.finder.query, got)
	}
	press(m, "enter")

	i := targetIndex(m, "make/test")
	if !listed(m, "make/test") {
		t.Fatal("enter did not unfold the make group")
	}
	if m.solo != i {
		t.Fatalf("solo = %d, want make/test at %d", m.solo, i)
	}
	if got := queuedCtrl(t, m.targets[i].runner); got != ctrlStart {
		t.Fatalf("control = %v, want ctrlStart", got)
	}
	if m.targets[i].state != stateRunning {
		t.Fatalf("state = %v, want running", m.targets[i].state)
	}

	m.Update(ctrlK)
	press(m, "t", "e", "s", "t", "enter")
	select {
	case c := <-m.targets[i].runner.ctrlCh:
		t.Fatalf("enter on a running make row sent control %v, want focus only", c.kind)
	default:
	}
}

func TestFinderEnterReRunsAfterExit(t *testing.T) {
	m := newMakeTUI(t)
	i := targetIndex(m, "make/test")
	m.Update(ctrlK)
	press(m, "t", "e", "s", "t", "enter")
	queuedCtrl(t, m.targets[i].runner)

	m.applyEvent(event{kind: evExited, target: "make/test", status: "exit 0", code: 0})
	if m.targets[i].state != stateExited || m.targets[i].exitLabel != "exit 0" {
		t.Fatalf("state = %v, label %q, want exited with exit 0", m.targets[i].state, m.targets[i].exitLabel)
	}
	if view := m.View().Content; !strings.Contains(view, "exit 0") {
		t.Fatalf("view does not show the exit label:\n%s", view)
	}

	m.Update(ctrlK)
	press(m, "t", "e", "s", "t", "enter")
	if got := queuedCtrl(t, m.targets[i].runner); got != ctrlStart {
		t.Fatalf("control after exit = %v, want ctrlStart", got)
	}
	if m.targets[i].state != stateRunning {
		t.Fatalf("state after re-run = %v, want running", m.targets[i].state)
	}
}

func TestFinderFooterSaysRunOnAMakeRow(t *testing.T) {
	m := newMakeTUI(t)
	m.Update(ctrlK)
	press(m, "t", "e", "s", "t")
	if view := m.View().Content; !strings.Contains(view, "⏎ run") {
		t.Fatalf("footer on a make row lacks ⏎ run:\n%s", view)
	}

	m.finder.query = ""
	press(m, "w", "e", "b")
	if view := m.View().Content; !strings.Contains(view, "⏎ jump") || strings.Contains(view, "⏎ run") {
		t.Fatalf("footer on a rerun.yml row should say jump:\n%s", view)
	}
}

func TestConfigWatchPicksUpARerunYMLCreatedAfterMakeOnlyStart(t *testing.T) {
	dir := makeDir(t, map[string]string{"Makefile": "build:\n\tgo build\n"})
	group, sink := startConfigWatch(t, filepath.Join(dir, ConfigFileName))
	if _, ok := findSnapshot(group, "web"); ok {
		t.Fatal("web loaded before rerun.yml existed")
	}

	create := func() { writeFile(t, filepath.Join(dir, ConfigFileName), "web:\n  cmd: sleep 30\n") }
	waitForKindPoking(t, sink, evReconcile, 10*time.Second, create)

	if _, ok := findSnapshot(group, "web"); !ok {
		t.Fatal("web did not load once rerun.yml appeared")
	}
}

func TestRunConfigModeMakeOnlyWithoutTTYExitsWithHint(t *testing.T) {
	dir := makeDir(t, map[string]string{"Makefile": "build:\n\tgo build\n"})
	t.Chdir(dir)

	stdout, stderr, code := captureStdio(t, func() int { return runConfigMode(Args{}) })

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, ErrConfigNotFound.Error()) || !strings.Contains(stderr, "hint: "+configHint) {
		t.Errorf("stderr = %q, want the config-not-found hint", stderr)
	}
}

func TestImplicitMakefileYieldsToRootMakeTargetName(t *testing.T) {
	dir := makeDir(t, map[string]string{
		"Makefile":     "build:\n\tgo build\n\ntest:\n\tgo test\n",
		ConfigFileName: "make/build:\n  cmd: echo root\n",
	})
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c, _ := cfg.Find("make/build"); c.Cmd != "echo root" {
		t.Errorf("make/build Cmd = %q, want the rerun.yml target", c.Cmd)
	}
	if _, ok := cfg.Find("make/test"); !ok {
		t.Errorf("targets = %v, want make/test still imported", targetNames(cfg))
	}
	if !containsNotice(cfg, `"make/build" is skipped`) {
		t.Errorf("notices = %v, want a skip notice for make/build", cfg.Notices)
	}
}

func TestRootGroupMakeSkipsImplicitMakefileWithNotice(t *testing.T) {
	dir := makeDir(t, map[string]string{
		"Makefile":     "build:\n\tgo build\n",
		ConfigFileName: "groups:\n  make:\n    columns: [\"x {.x}\"]\n\nweb:\n  cmd: sleep 30\n",
	})
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := targetNames(cfg); !reflect.DeepEqual(got, []string{"web"}) {
		t.Errorf("targets = %v, want only web", got)
	}
	if !containsNotice(cfg, "defines group") {
		t.Errorf("notices = %v, want the root-owns-make notice", cfg.Notices)
	}
}

func TestImportClaimingMakeSkipsImplicitMakefileSilently(t *testing.T) {
	dir := makeDir(t, map[string]string{
		"Makefile":     "build:\n\tgo build\n",
		"tools.mk":     "lint:\n\tgo vet\n",
		ConfigFileName: "web:\n  cmd: sleep 30\n\nimports:\n  - path: tools.mk\n    group: make\n",
	})
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if _, ok := cfg.Find("make/lint"); !ok {
		t.Errorf("targets = %v, want make/lint from the explicit import", targetNames(cfg))
	}
	if len(cfg.Notices) != 0 {
		t.Errorf("notices = %v, want none", cfg.Notices)
	}
}

func containsNotice(cfg *Config, sub string) bool {
	for _, n := range cfg.Notices {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

func TestConfigWatchPicksUpAMakefileCreatedBesideRerunYML(t *testing.T) {
	dir := makeDir(t, map[string]string{ConfigFileName: "web:\n  cmd: sleep 30\n"})
	group, sink := startConfigWatch(t, filepath.Join(dir, ConfigFileName))
	if _, ok := findSnapshot(group, "make/build"); ok {
		t.Fatal("make/build loaded before the Makefile existed")
	}

	create := func() { writeFile(t, filepath.Join(dir, "Makefile"), "build:\n\tgo build\n") }
	waitForKindPoking(t, sink, evReconcile, 10*time.Second, create)

	if _, ok := findSnapshot(group, "make/build"); !ok {
		t.Fatal("make/build did not load once the Makefile appeared")
	}
}

func TestSaveColumnsMakeOnlyWritesNothing(t *testing.T) {
	dir := makeDir(t, map[string]string{"Makefile": "build:\n\tgo build\n"})
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	m := newConfigTUI(cfg.Targets, cfg)
	m.edits = []colEdit{{node: colNode{name: "make/build"}, cols: []Column{{Name: "x", Expr: ".x"}}}}
	m.saveColumns()

	if _, err := os.Stat(filepath.Join(dir, ConfigFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("rerun.yml stat = %v, want it not created", err)
	}
	if !strings.Contains(m.notice, "no rerun.yml") {
		t.Errorf("notice = %q, want the no rerun.yml notice", m.notice)
	}
}

func TestEmptyPaneOnAFreshMakeConfigPointsAtFinder(t *testing.T) {
	m := newMakeTUI(t)
	if got := m.emptyHint(); got != "ctrl+k find a target · → unfold make/" {
		t.Fatalf("emptyHint() = %q", got)
	}
}

func TestSoloNonMakeRowKeepsItsOwnEmptyHint(t *testing.T) {
	m := newMakeTUI(t)
	i := targetIndex(m, "web")
	m.solo = i
	if got := m.emptyHint(); got != "web: no output yet" {
		t.Fatalf("emptyHint() = %q, want the solo row's hint", got)
	}
	m.targets[i].building = true
	if got := m.emptyHint(); got != "web: building… output appears when the run starts" {
		t.Fatalf("emptyHint() = %q, want the solo row's building hint", got)
	}
}

func TestEmptyPaneHintDropsUnfoldWhenMakeIsUnfolded(t *testing.T) {
	dir := makeDir(t, map[string]string{
		"Makefile":     "test:\n\ttrue\n",
		ConfigFileName: "groups:\n  make:\n    hide: false\n\nimports:\n  - path: Makefile\n\nweb:\n  cmd: sleep 30\n",
	})
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	m := newConfigTUI(cfg.Targets, cfg)
	m.finder = finderState{}
	m.width, m.height = 100, 24
	if got := m.emptyHint(); got != "ctrl+k find a target" {
		t.Fatalf("emptyHint() = %q, want no unfold part", got)
	}
}

func TestUnreadableImplicitMakefileIsANotice(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a chmod 000 file")
	}
	dir := makeDir(t, map[string]string{
		"Makefile":     "build:\n\tgo build\n",
		ConfigFileName: "web:\n  cmd: sleep 30\n",
	})
	if err := os.Chmod(filepath.Join(dir, "Makefile"), 0); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v, want the Makefile skipped with a notice", err)
	}
	if got := targetNames(cfg); !reflect.DeepEqual(got, []string{"web"}) {
		t.Errorf("targets = %v, want only web", got)
	}
	if !containsNotice(cfg, "is not imported") {
		t.Errorf("notices = %v, want the not-imported notice", cfg.Notices)
	}
}

func TestStoppedMakeRowShowsRunAgainHint(t *testing.T) {
	m := newMakeTUI(t)
	i := -1
	for j, tg := range m.targets {
		if tg.spec.Kind == makeKind {
			i = j
			break
		}
	}
	if i < 0 {
		t.Fatal("no make target in the config")
	}
	m.targets[i].state = stateExited
	m.applyScope(i, "")
	lines := m.rightLines(100)
	if last := lines[len(lines)-1]; !strings.Contains(last, "r run again · ctrl+l clear · ctrl+k find") {
		t.Fatalf("bottom line = %q, want the run-again hint", last)
	}
}
