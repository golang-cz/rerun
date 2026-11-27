package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// impTree is the fixture most picker tests use: a root Makefile, an api folder, and a web folder with its own rerun.yml.
var impTree = map[string]string{
	"Makefile":      "build:\n\tgo build\n",
	"api/Makefile":  "serve:\n\tgo run .\n",
	"web/rerun.yml": "targets:\n  ui:\n    run: npm start\n",
}

// impFixture is a model over dir with no rerun.yml, its config path set as a startup load would set it.
func impFixture(t *testing.T, files map[string]string) (*tuiModel, string) {
	t.Helper()
	dir := makeDir(t, files)
	m := newTUI(nil)
	m.group.configPath = filepath.Join(dir, ConfigFileName)
	m.width, m.height = 100, 24
	return m, dir
}

// impLoaded gives the session a target, so esc may close the picker.
func impLoaded(m *tuiModel) {
	m.group.mu.Lock()
	m.group.targets = []Target{{Name: "loaded"}}
	m.group.mu.Unlock()
}

// impKey sends a printable key the way the decoder does: its text, no modifier.
func impKey(m *tuiModel, s string) {
	m.Update(tea.KeyPressMsg{Code: []rune(s)[0], Text: s})
}

// impCtrl sends a ctrl chord, such as ctrl+s.
func impCtrl(m *tuiModel, letter rune) {
	m.Update(tea.KeyPressMsg{Code: letter, Mod: tea.ModCtrl})
}

// impPressI presses I and returns the scan's message without delivering it.
func impPressI(m *tuiModel) *importScanMsg {
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'I', Text: "I"})
	return findScan(cmd)
}

// findScan runs a command, or a batch of them, and returns the scan result it holds.
func findScan(cmd tea.Cmd) *importScanMsg {
	if cmd == nil {
		return nil
	}
	switch v := cmd().(type) {
	case importScanMsg:
		return &v
	case tea.BatchMsg:
		for _, c := range v {
			if got := findScan(c); got != nil {
				return got
			}
		}
	}
	return nil
}

// impOpen presses I, delivers the scan, and fails the test when the picker does not open.
func impOpen(t *testing.T, m *tuiModel) {
	t.Helper()
	scan := impPressI(m)
	if !m.imports.open {
		t.Fatal("I did not open the import picker")
	}
	if scan != nil {
		m.Update(*scan)
	}
	if !m.imports.open {
		t.Fatal("the scan closed the import picker")
	}
}

// impRow is the index of the row for a folder label such as "api/", or a file's path or base name.
func impRow(t *testing.T, m *tuiModel, label string) int {
	t.Helper()
	for i, r := range m.imports.rows() {
		if r.label == label || !r.folder && m.imports.cands[r.cand].Path == label {
			return i
		}
	}
	t.Fatalf("no row %q in %v", label, impLabels(m))
	return -1
}

func impLabels(m *tuiModel) []string {
	var out []string
	for _, r := range m.imports.rows() {
		out = append(out, r.label)
	}
	return out
}

// impGo moves the cursor onto a row.
func impGo(t *testing.T, m *tuiModel, label string) {
	t.Helper()
	m.imports.cursor = impRow(t, m, label)
	m.fitImports()
}

// impChecked is whether the candidate with this path is checked.
func impChecked(t *testing.T, m *tuiModel, path string) bool {
	t.Helper()
	for i, c := range m.imports.cands {
		if c.Path == path {
			return m.imports.checked[i]
		}
	}
	t.Fatalf("no candidate %q", path)
	return false
}

func TestImportOpensWithEveryFileChecked(t *testing.T) {
	// Home is the temp dir so shortDir shows "~" and the 120-column title keeps "found 3 files".
	dir := makeDir(t, impTree)
	t.Setenv("HOME", dir)
	m := newTUI(nil)
	m.group.configPath = filepath.Join(dir, ConfigFileName)
	m.width, m.height = 160, 24
	impOpen(t, m)
	for _, p := range []string{"Makefile", "api/Makefile", "web/rerun.yml"} {
		if !impChecked(t, m, p) {
			t.Errorf("%s starts unchecked without a rerun.yml", p)
		}
	}
	view := m.View().Content
	if !strings.Contains(view, "No rerun.yml in") || !strings.Contains(view, "found 3 files") {
		t.Fatalf("title does not name the missing rerun.yml:\n%s", view)
	}
	if !strings.Contains(view, "ctrl+s save rerun.yml") {
		t.Fatalf("footer does not offer to save a new rerun.yml:\n%s", view)
	}
}

func TestImportSpaceTogglesOneFile(t *testing.T) {
	m, _ := impFixture(t, impTree)
	impOpen(t, m)
	impGo(t, m, "Makefile")
	press(m, " ")
	if impChecked(t, m, "Makefile") {
		t.Fatal("space did not uncheck the root Makefile")
	}
	press(m, " ")
	if !impChecked(t, m, "Makefile") {
		t.Fatal("second space did not recheck the root Makefile")
	}
}

func TestImportSpaceOnFolderTogglesItsSubtree(t *testing.T) {
	m, _ := impFixture(t, map[string]string{
		"api/Makefile":     "serve:\n\tgo run .\n",
		"api/sub/Makefile": "deep:\n\tgo run .\n",
		"Makefile":         "build:\n\tgo build\n",
	})
	impOpen(t, m)
	impGo(t, m, "api/")
	press(m, " ")
	if impChecked(t, m, "api/Makefile") || impChecked(t, m, "api/sub/Makefile") {
		t.Fatal("space on api/ did not uncheck the whole subtree")
	}
	if !impChecked(t, m, "Makefile") {
		t.Fatal("space on api/ reached outside the folder")
	}
	press(m, " ")
	if !impChecked(t, m, "api/sub/Makefile") {
		t.Fatal("second space on api/ did not check the subtree again")
	}
}

func TestImportFolderShowsPartialState(t *testing.T) {
	m, _ := impFixture(t, map[string]string{
		"api/Makefile":     "serve:\n\tgo run .\n",
		"api/compose.yaml": "services:\n  db:\n    image: postgres\n",
	})
	impOpen(t, m)
	impGo(t, m, "api/")
	press(m, "down")
	press(m, " ") // api/Makefile
	if got := m.imports.rowState(m.imports.rows()[impRow(t, m, "api/")]); got != checkPartial {
		t.Fatalf("api/ state = %d, want partial", got)
	}
	if view := m.View().Content; !strings.Contains(view, "[■] ") {
		t.Fatalf("partial folder has no ■ box:\n%s", view)
	}
	impGo(t, m, "api/")
	press(m, " ")
	if got := m.imports.rowState(m.imports.rows()[impRow(t, m, "api/")]); got != checkAll {
		t.Fatalf("api/ state after checking the rest = %d, want all", got)
	}
}

func TestImportAllKeyTogglesEverything(t *testing.T) {
	m, _ := impFixture(t, impTree)
	impOpen(t, m)
	impKey(m, "a")
	for _, p := range []string{"Makefile", "api/Makefile", "web/rerun.yml"} {
		if impChecked(t, m, p) {
			t.Errorf("a left %s checked when all were checked", p)
		}
	}
	impKey(m, "a")
	for _, p := range []string{"Makefile", "api/Makefile", "web/rerun.yml"} {
		if !impChecked(t, m, p) {
			t.Errorf("a did not check %s", p)
		}
	}
}

func TestImportTypingFiltersRowsAndAIsALetter(t *testing.T) {
	m, _ := impFixture(t, impTree)
	impOpen(t, m)
	for _, k := range []string{"w", "e", "b"} {
		impKey(m, k)
	}
	if got := strings.Join(impLabels(m), ","); got != "./,web/,rerun.yml" {
		t.Fatalf("rows for \"web\" = %s", got)
	}
	impKey(m, "a")
	if m.imports.query != "weba" {
		t.Fatalf("query = %q, want the a typed into it", m.imports.query)
	}
	if !impChecked(t, m, "Makefile") {
		t.Fatal("a with a filter typed still toggled every file")
	}
	press(m, "backspace")
	if m.imports.query != "web" {
		t.Fatalf("backspace left query %q", m.imports.query)
	}
}

func TestImportFilterWithNoMatchSaysSo(t *testing.T) {
	m, _ := impFixture(t, impTree)
	impOpen(t, m)
	for _, k := range []string{"z", "z", "z"} {
		impKey(m, k)
	}
	if view := m.View().Content; !strings.Contains(view, `no matches for "zzz"`) {
		t.Fatalf("no-match row missing:\n%s", view)
	}
}

// impWatch runs the config watcher, which is what applies a picker session.
func impWatch(t *testing.T, m *tuiModel) {
	t.Helper()
	watchGroup(t, m.group, m.group.configPath)
}

// impNames waits until the group's targets satisfy ok, then returns them.
func impNames(t *testing.T, m *tuiModel, ok func([]string) bool) []string {
	t.Helper()
	var names []string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		names = targetNames(&Config{Targets: m.group.snapshotTargets()})
		if ok(names) {
			return names
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("targets never settled, last = %v", names)
	return nil
}

func hasSuffixName(names []string, suffix string) bool {
	return slices.ContainsFunc(names, func(n string) bool { return n == suffix || strings.HasSuffix(n, "/"+suffix) })
}

func TestImportEnterRunsCheckedFilesWithoutWriting(t *testing.T) {
	m, dir := impFixture(t, impTree)
	impWatch(t, m)
	impOpen(t, m)
	impGo(t, m, "api/Makefile")
	press(m, " ")
	press(m, "enter")

	if m.imports.open {
		t.Fatal("enter left the picker open")
	}
	names := impNames(t, m, func(n []string) bool { return hasSuffixName(n, "ui") })
	if !slices.ContainsFunc(names, func(n string) bool { return strings.HasSuffix(n, "build") }) || !slices.ContainsFunc(names, func(n string) bool { return strings.HasSuffix(n, "ui") }) {
		t.Fatalf("session targets = %v, want the checked files' targets", names)
	}
	if slices.ContainsFunc(names, func(n string) bool { return strings.Contains(n, "serve") }) {
		t.Fatalf("unchecked api/Makefile still runs: %v", names)
	}
	if fileExists(filepath.Join(dir, ConfigFileName)) {
		t.Fatal("enter wrote rerun.yml")
	}
}

func TestImportEnterKeepsRerunYMLRootTargets(t *testing.T) {
	m, _ := impFixture(t, map[string]string{
		ConfigFileName: "targets:\n  local:\n    run: echo\nimports:\n  - path: api/Makefile\n    group: api\n",
		"Makefile":     impTree["Makefile"],
		"api/Makefile": impTree["api/Makefile"],
	})
	impWatch(t, m)
	impOpen(t, m)
	press(m, "enter")

	names := impNames(t, m, func(n []string) bool { return slices.Contains(n, "local") && hasSuffixName(n, "serve") })
	if !slices.Contains(names, "local") {
		t.Fatalf("session targets = %v, want the rerun.yml's own local target kept", names)
	}
	if !slices.ContainsFunc(names, func(n string) bool { return strings.Contains(n, "serve") }) {
		t.Fatalf("session targets = %v, want the checked api/Makefile", names)
	}
	if slices.ContainsFunc(names, func(n string) bool { return n == "build" || strings.HasSuffix(n, "/build") }) {
		t.Fatalf("unchecked root Makefile runs: %v", names)
	}
}

func TestImportCtrlSWritesImportsAndClosesPicker(t *testing.T) {
	m, dir := impFixture(t, impTree)
	impOpen(t, m)
	impGo(t, m, "api/Makefile")
	press(m, " ")
	impCtrl(m, 's')

	if m.imports.open {
		t.Fatal("ctrl+s left the picker open")
	}
	cfg, err := ParseConfig(filepath.Join(dir, ConfigFileName))
	if err != nil {
		t.Fatalf("saved rerun.yml does not load: %v", err)
	}
	if len(cfg.Files) != 3 {
		t.Fatalf("saved config reads %d files, want rerun.yml plus two imports: %v", len(cfg.Files), cfg.Files)
	}
	for _, f := range cfg.Files {
		if strings.Contains(f, filepath.Join("api", "Makefile")) {
			t.Fatalf("unchecked api/Makefile was saved: %v", cfg.Files)
		}
	}
}

func TestImportCtrlSKeepsCommentsAndOtherKeysWhenUpdating(t *testing.T) {
	m, dir := impFixture(t, map[string]string{
		"Makefile":     "build:\n\tgo build\n",
		"api/Makefile": "serve:\n\tgo run .\n",
		ConfigFileName: "# team config\ntargets:\n  web:\n    run: sleep 30\nimports:\n  - path: Makefile\n",
	})
	m.group.configPath = filepath.Join(dir, ConfigFileName)
	impOpen(t, m)
	if impChecked(t, m, "api/Makefile") {
		t.Fatal("api/Makefile pre-checked though rerun.yml does not import it")
	}
	if !impChecked(t, m, "Makefile") {
		t.Fatal("Makefile not pre-checked though rerun.yml imports it")
	}
	impGo(t, m, "api/Makefile")
	press(m, " ")
	impCtrl(m, 's')

	raw, err := os.ReadFile(filepath.Join(dir, ConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	data := string(raw)
	for _, want := range []string{"# team config", "web:", "run: sleep 30", "path: api/Makefile", "group: api"} {
		if !strings.Contains(data, want) {
			t.Errorf("saved rerun.yml lost or missed %q:\n%s", want, data)
		}
	}
}

func TestImportExistingRerunYMLFooterAndTitleNameIt(t *testing.T) {
	m, dir := impFixture(t, map[string]string{
		"Makefile":     "build:\n\tgo build\n",
		ConfigFileName: "imports:\n  - path: Makefile\n",
	})
	m.group.configPath = filepath.Join(dir, ConfigFileName)
	impOpen(t, m)
	view := m.View().Content
	if !strings.Contains(view, "Import files") || !strings.Contains(view, "ctrl+s update rerun.yml imports") {
		t.Fatalf("picker does not name the existing rerun.yml:\n%s", view)
	}
}

func TestImportEscClosesWithoutWriting(t *testing.T) {
	m, dir := impFixture(t, impTree)
	impLoaded(m)
	impOpen(t, m)
	impKey(m, "a")
	press(m, "esc")
	if m.imports.open {
		t.Fatal("esc left the picker open")
	}
	if fileExists(filepath.Join(dir, ConfigFileName)) {
		t.Fatal("esc wrote rerun.yml")
	}
}

func TestImportFileWithParseErrorStartsUnchecked(t *testing.T) {
	m, _ := impFixture(t, map[string]string{
		"Makefile":         "build:\n\tgo build\n",
		"api/compose.yaml": "services: [\n",
	})
	impOpen(t, m)
	if impChecked(t, m, "api/compose.yaml") {
		t.Fatal("a file that fails to parse starts checked")
	}
	impGo(t, m, "compose.yaml")
	press(m, " ")
	if !strings.Contains(m.imports.msg, "api/compose.yaml") {
		t.Fatalf("space on a broken file gave no reason: %q", m.imports.msg)
	}
	if impChecked(t, m, "api/compose.yaml") {
		t.Fatal("space checked a file that fails to parse")
	}
}

func TestImportNestedImportsWarningShows(t *testing.T) {
	m, _ := impFixture(t, map[string]string{
		"web/rerun.yml": "targets:\n  ui:\n    run: npm start\nimports:\n  - path: ../Makefile\n",
		"Makefile":      "build:\n\tgo build\n",
	})
	impOpen(t, m)
	if view := m.View().Content; !strings.Contains(view, "nested imports ignored") {
		t.Fatalf("nested-imports hint missing:\n%s", view)
	}
}

func TestImportTruncatedScanShowsRow(t *testing.T) {
	m, dir := impFixture(t, impTree)
	cands, _ := scanImportable(dir, importScanOpts{})
	m.imports = newImportPicker(filepath.Join(dir, ConfigFileName), cands, true, false, nil, nil)
	if view := m.View().Content; !strings.Contains(view, "scan truncated") {
		t.Fatalf("truncated scan has no row:\n%s", view)
	}
}

func TestImportKeyWithNoFilesOpensNothing(t *testing.T) {
	m, _ := impFixture(t, map[string]string{"README.md": "hi\n"})
	if scan := impPressI(m); scan != nil {
		m.Update(*scan)
	}
	if m.imports.open {
		t.Fatal("I opened a picker with nothing to list")
	}
}

func TestImportSaveIsPickedUpByTheWatcher(t *testing.T) {
	dir := makeDir(t, map[string]string{
		"Makefile":     "build:\n\tgo build\n",
		"api/Makefile": "serve:\n\tgo run .\n",
	})
	path := filepath.Join(dir, ConfigFileName)
	group, sink := startConfigWatch(t, path)
	cands, _ := scanImportable(dir, importScanOpts{})

	waitForKindPoking(t, sink, evReconcile, 10*time.Second, func() {
		if err := saveImports(path, cands); err != nil {
			t.Errorf("saveImports: %v", err)
		}
	})
	if n := len(group.snapshotTargets()); n != 2 {
		t.Fatalf("watcher loaded %d targets from the saved imports, want 2", n)
	}
}

func TestImportHelpListsTheKey(t *testing.T) {
	found := false
	for _, row := range helpKeys {
		if row[0] == "I" {
			found = true
		}
	}
	if !found {
		t.Fatal("? help has no entry for I")
	}
}

func TestImportSessionSurvivesAMakefileTouch(t *testing.T) {
	m, dir := impFixture(t, impTree)
	impWatch(t, m)
	impOpen(t, m)
	impGo(t, m, "api/Makefile")
	press(m, " ")
	press(m, "enter")
	impNames(t, m, func(n []string) bool { return hasSuffixName(n, "ui") })

	writeFile(t, filepath.Join(dir, "Makefile"), "build:\n\tgo build\n\nextra:\n\tgo vet\n")
	names := impNames(t, m, func(n []string) bool { return hasSuffixName(n, "extra") })
	if !hasSuffixName(names, "ui") || !hasSuffixName(names, "build") {
		t.Fatalf("targets = %v after a Makefile touch, want the session's files kept", names)
	}
	if hasSuffixName(names, "serve") {
		t.Fatalf("unchecked api/Makefile came back after a touch: %v", names)
	}
}

// Enter while the watcher reloads must not apply twice or race; run with -race.
func TestImportEnterRacesAWatcherReload(t *testing.T) {
	m, dir := impFixture(t, impTree)
	impWatch(t, m)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.WriteFile(filepath.Join(dir, "Makefile"), []byte("build:\n\tgo build\n# "+strings.Repeat("x", i%5)+"\n"), 0o644)
			time.Sleep(5 * time.Millisecond)
		}
	}()
	for range 5 {
		impOpen(t, m)
		impGo(t, m, "api/Makefile")
		press(m, " ")
		press(m, "enter")
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	<-done
	names := impNames(t, m, func(n []string) bool { return hasSuffixName(n, "ui") })
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Fatalf("duplicate target %q in %v", n, names)
		}
		seen[n] = true
	}
}

func TestImportCtrlSOfEmptySetRefuses(t *testing.T) {
	m, dir := impFixture(t, impTree)
	impOpen(t, m)
	press(m, "a")
	impCtrl(m, 's')

	if !m.imports.open {
		t.Fatal("ctrl+s closed the picker on an empty set")
	}
	if !strings.Contains(m.imports.msg, "nothing checked") {
		t.Fatalf("msg = %q, want nothing checked", m.imports.msg)
	}
	if fileExists(filepath.Join(dir, ConfigFileName)) {
		t.Fatal("ctrl+s wrote rerun.yml for an empty set")
	}
}

func TestImportCtrlSKeepsPickerOpenOnAGroupClash(t *testing.T) {
	m, dir := impFixture(t, map[string]string{
		"a/Makefile": "build:\n\tgo build\n",
		"b/Makefile": "build:\n\tgo build\n",
	})
	impOpen(t, m)
	for i := range m.imports.cands {
		m.imports.cands[i].Group = "same"
	}
	impCtrl(m, 's')
	if !m.imports.open || !strings.Contains(m.imports.msg, "duplicate import group") {
		t.Fatalf("open = %v, msg = %q; want the picker kept open with the clash", m.imports.open, m.imports.msg)
	}
	if fileExists(filepath.Join(dir, ConfigFileName)) {
		t.Fatal("ctrl+s wrote a rerun.yml that does not load")
	}
}

func TestImportCtrlSThroughSymlinkInsideTreeEditsTheTarget(t *testing.T) {
	m, dir := impFixture(t, map[string]string{
		"real.yml":     "targets:\n  web:\n    run: sleep 30\n",
		"api/Makefile": "serve:\n\tgo run .\n",
	})
	link := filepath.Join(dir, ConfigFileName)
	if err := os.Symlink("real.yml", link); err != nil {
		t.Fatal(err)
	}
	impOpen(t, m)
	impGo(t, m, "api/Makefile")
	press(m, " ")
	impCtrl(m, 's')

	if m.imports.open {
		t.Fatalf("ctrl+s failed: %s", m.imports.msg)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("rerun.yml is no longer a symlink: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "real.yml"))
	if !strings.Contains(string(raw), "api/Makefile") || !strings.Contains(string(raw), "sleep 30") {
		t.Fatalf("real.yml = %q, want imports added and targets kept", raw)
	}
}

func TestImportCtrlSThroughSymlinkOutsideTreeRefuses(t *testing.T) {
	m, dir := impFixture(t, map[string]string{"api/Makefile": "serve:\n\tgo run .\n"})
	outside := filepath.Join(t.TempDir(), "outside.yml")
	const orig = "targets:\n  web:\n    run: sleep 30\n"
	writeFile(t, outside, orig)
	if err := os.Symlink(outside, filepath.Join(dir, ConfigFileName)); err != nil {
		t.Fatal(err)
	}
	impOpen(t, m)
	impGo(t, m, "api/Makefile")
	press(m, " ")
	impCtrl(m, 's')

	if !m.imports.open || !strings.Contains(m.imports.msg, "outside") {
		t.Fatalf("open = %v, msg = %q; want the picker kept open with an outside-tree message", m.imports.open, m.imports.msg)
	}
	if raw, _ := os.ReadFile(outside); string(raw) != orig {
		t.Fatalf("outside file changed: %q", raw)
	}
}

func TestImportCtrlSThroughDanglingSymlinkRefuses(t *testing.T) {
	m, dir := impFixture(t, map[string]string{"api/Makefile": "serve:\n\tgo run .\n"})
	target := filepath.Join(dir, "missing.yml")
	if err := os.Symlink(target, filepath.Join(dir, ConfigFileName)); err != nil {
		t.Fatal(err)
	}
	impOpen(t, m)
	impCtrl(m, 's')
	if m.imports.open && m.imports.msg == "" {
		t.Fatal("picker stayed open with no message")
	}
	if fileExists(target) {
		t.Fatal("a dangling symlink's target was created")
	}
}

func TestImportEnterHoldsTheNothingSavedNotice(t *testing.T) {
	m, _ := impFixture(t, impTree)
	impOpen(t, m)
	press(m, "enter")
	if !m.now.Before(m.keepNoticeUntil) {
		t.Fatal("enter did not hold its notice against the reload banner")
	}
}

// A folder holding both a Makefile and a compose file must not clash on its group name.
func TestImportEnterLoadsMonorepoWithMakeAndComposeInOneFolder(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	for _, withRoot := range []bool{false, true} {
		files := map[string]string{
			"Makefile":           "build:\n\tgo build\n",
			"docker-compose.yml": "services:\n  db:\n    image: postgres\n",
			"api/Makefile":       "serve:\n\tgo run .\n",
			"api/compose.yaml":   "services:\n  cache:\n    image: redis\n",
			"web/rerun.yml":      "targets:\n  ui:\n    run: npm start\n",
			"tools/Makefile":     "gen:\n\techo gen\n",
		}
		if withRoot {
			files[ConfigFileName] = "targets:\n  top:\n    run: echo hi\n"
		}
		m, _ := impFixture(t, files)
		impWatch(t, m)
		impOpen(t, m)
		if withRoot {
			press(m, "a")
		}
		press(m, "enter")
		if m.imports.open || m.imports.msg != "" {
			t.Fatalf("withRoot=%v: enter left the picker open, msg %q", withRoot, m.imports.msg)
		}
		names := impNames(t, m, func(n []string) bool {
			return hasSuffixName(n, "serve") && hasSuffixName(n, "cache") && hasSuffixName(n, "gen")
		})
		for _, want := range []string{"build", "serve", "gen", "ui", "db", "cache"} {
			if !hasSuffixName(names, want) {
				t.Errorf("withRoot=%v: targets = %v, want %s", withRoot, names, want)
			}
		}
	}
}

func TestImportOpensOnASpinnerBeforeTheScanLands(t *testing.T) {
	m, _ := impFixture(t, impTree)
	scan := impPressI(m)
	if !m.imports.open || !m.imports.scanning || scan == nil {
		t.Fatalf("open = %v, scanning = %v, scan = %v, want an open picker waiting on its scan", m.imports.open, m.imports.scanning, scan)
	}
	if view := m.View().Content; !strings.Contains(view, "scanning") {
		t.Fatalf("no scanning row:\n%s", view)
	}
	impKey(m, "x")
	impCtrl(m, 's')
	press(m, "enter")
	if !m.imports.open || m.imports.query != "" {
		t.Fatalf("keys acted while scanning: open = %v, query = %q", m.imports.open, m.imports.query)
	}
	m.Update(*scan)
	if m.imports.scanning || len(m.imports.cands) != 3 {
		t.Fatalf("scanning = %v, cands = %d, want the tree filled in", m.imports.scanning, len(m.imports.cands))
	}
}

func TestImportEscWhileScanningDropsTheLateResult(t *testing.T) {
	m, _ := impFixture(t, impTree)
	impLoaded(m)
	scan := impPressI(m)
	press(m, "esc")
	if m.imports.open {
		t.Fatal("esc left the scanning picker open")
	}
	m.Update(*scan)
	if m.imports.open {
		t.Fatal("a late scan reopened the picker")
	}
}

func TestImportErrorWrapsInsideTheModal(t *testing.T) {
	m, _ := impFixture(t, impTree)
	m.width, m.height = 100, 30
	impOpen(t, m)
	m.imports.msg = "rerun.yml: " + strings.Repeat("duplicate import group please fix it ", 6) + "END"
	m.fitImports()
	if view := m.View().Content; !strings.Contains(view, "END") {
		t.Fatalf("error cut off:\n%s", view)
	}
}

func TestImportEscKeepsThePickerWhenNothingIsLoaded(t *testing.T) {
	m, _ := impFixture(t, impTree)
	impOpen(t, m)
	press(m, "esc")
	if !m.imports.open || !strings.Contains(m.View().Content, importNothingLoaded) {
		t.Fatalf("esc with nothing loaded: open = %v, want it kept with the hint", m.imports.open)
	}
}

func TestImportSkippedWorktreesShowRow(t *testing.T) {
	m, dir := impFixture(t, impTree)
	cands, _ := scanImportable(dir, importScanOpts{})
	m.imports = newImportPicker(filepath.Join(dir, ConfigFileName), cands, false, false, nil, nil)
	m.imports.skips = []string{"2 other worktrees of app skipped"}
	if view := m.View().Content; !strings.Contains(view, "2 other worktrees of app skipped") {
		t.Fatalf("skip row missing:\n%s", view)
	}
}
