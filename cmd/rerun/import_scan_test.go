package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const scanMakefile = "build:\n\tgo build\n\ntest:\n\tgo test\n"

// candidateSummary is "path kind count" per file, for compact assertions.
func candidateSummary(cs []importCandidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, fmt.Sprintf("%s %s %d", c.Path, c.Kind, c.Count))
	}
	return out
}

func TestScanImportableTreeOrderAndCounts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Makefile"), scanMakefile)
	writeFile(t, filepath.Join(dir, "docker-compose.yml"), "services:\n  db:\n    image: x\n  web:\n    image: y\n")
	writeFile(t, filepath.Join(dir, "api", "Makefile"), scanMakefile)
	writeFile(t, filepath.Join(dir, "web", "rerun.yml"), "targets:\n  dev:\n    run: a\n  lint:\n    run: b\n  test:\n    run: c\n")
	writeFile(t, filepath.Join(dir, "deep", "a", "Makefile"), "x:\n\techo\n")

	got, truncated := scanImportable(dir, importScanOpts{})
	if truncated {
		t.Fatal("truncated = true, want false")
	}
	want := []string{
		"Makefile make 2",
		"docker-compose.yml compose 2",
		"api/Makefile make 2",
		"deep/a/Makefile make 1",
		"web/rerun.yml rerun 3",
	}
	if !reflect.DeepEqual(candidateSummary(got), want) {
		t.Fatalf("candidates = %v\nwant %v", candidateSummary(got), want)
	}
	var groups []string
	for _, c := range got {
		groups = append(groups, c.Group)
	}
	if wantG := []string{"", "", "api", "deep/a", "web"}; !reflect.DeepEqual(groups, wantG) {
		t.Fatalf("groups = %q, want %q", groups, wantG)
	}
}

func TestScanImportableSubfolderRerunCoversSiblings(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "api", "Makefile"), scanMakefile)
	writeFile(t, filepath.Join(dir, "api", "compose.yaml"), "services:\n  db:\n    image: x\n")
	writeFile(t, filepath.Join(dir, "api", "rerun.yml"), "imports:\n  - path: Makefile\ntargets:\n  go:\n    run: go\n")
	writeFile(t, filepath.Join(dir, "rerun.yml"), "targets:\n  root:\n    run: r\n")

	got, _ := scanImportable(dir, importScanOpts{})
	if want := []string{"api/rerun.yml rerun 1"}; !reflect.DeepEqual(candidateSummary(got), want) {
		t.Fatalf("candidates = %v, want %v", candidateSummary(got), want)
	}
	if !got[0].HasImports {
		t.Fatal("HasImports = false, want true for a rerun.yml with imports:")
	}
}

func TestScanImportableRootRerunIsNotListed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "rerun.yml"), "targets:\n  root:\n    run: r\n")
	writeFile(t, filepath.Join(dir, "Makefile"), scanMakefile)

	got, _ := scanImportable(dir, importScanOpts{})
	if want := []string{"Makefile make 2"}; !reflect.DeepEqual(candidateSummary(got), want) {
		t.Fatalf("candidates = %v, want %v", candidateSummary(got), want)
	}
}

func TestScanImportableGitignore(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".gitignore"), "gen/\nlocal.mk\n")
	writeFile(t, filepath.Join(dir, "Makefile"), scanMakefile)
	writeFile(t, filepath.Join(dir, "gen", "Makefile"), scanMakefile)
	writeFile(t, filepath.Join(dir, "api", "Makefile"), scanMakefile)
	writeFile(t, filepath.Join(dir, "api", "local.mk"), scanMakefile)

	got, _ := scanImportable(dir, importScanOpts{})
	if want := []string{"Makefile make 2", "api/Makefile make 2"}; !reflect.DeepEqual(candidateSummary(got), want) {
		t.Fatalf("with gitignore: candidates = %v, want %v", candidateSummary(got), want)
	}

	got, _ = scanImportable(dir, importScanOpts{NoGitignore: true})
	if len(got) != 3 {
		t.Fatalf("with NoGitignore: got %d candidates, want 3: %v", len(got), candidateSummary(got))
	}
}

func TestScanImportableSkipsVendoredDirs(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"node_modules", "vendor", ".git"} {
		writeFile(t, filepath.Join(dir, d, "pkg", "Makefile"), scanMakefile)
	}
	writeFile(t, filepath.Join(dir, "Makefile"), scanMakefile)

	got, _ := scanImportable(dir, importScanOpts{})
	if want := []string{"Makefile make 2"}; !reflect.DeepEqual(candidateSummary(got), want) {
		t.Fatalf("candidates = %v, want %v", candidateSummary(got), want)
	}
}

func TestScanImportableDepthCap(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a", "b", "c", "d", "Makefile"), scanMakefile)
	got, truncated := scanImportable(dir, importScanOpts{})
	if truncated || len(got) != 1 {
		t.Fatalf("depth 4: got %v truncated=%v, want one candidate, not truncated", candidateSummary(got), truncated)
	}

	writeFile(t, filepath.Join(dir, "a", "b", "c", "d", "e", "Makefile"), scanMakefile)
	got, truncated = scanImportable(dir, importScanOpts{})
	if !truncated || len(got) != 1 {
		t.Fatalf("depth 5: got %v truncated=%v, want one candidate and truncated", candidateSummary(got), truncated)
	}
}

func TestScanImportableCountCap(t *testing.T) {
	dir := t.TempDir()
	for i := range importScanMaxFiles + 1 {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("d%03d", i), "Makefile"), scanMakefile)
	}
	got, truncated := scanImportable(dir, importScanOpts{})
	if len(got) != importScanMaxFiles || !truncated {
		t.Fatalf("got %d candidates truncated=%v, want %d and truncated", len(got), truncated, importScanMaxFiles)
	}
}

func TestScanImportableExactlyAtCountCapIsNotTruncated(t *testing.T) {
	dir := t.TempDir()
	for i := range importScanMaxFiles {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("d%03d", i), "Makefile"), scanMakefile)
	}
	got, truncated := scanImportable(dir, importScanOpts{})
	if len(got) != importScanMaxFiles || truncated {
		t.Fatalf("got %d candidates truncated=%v, want %d and not truncated", len(got), truncated, importScanMaxFiles)
	}
}

func TestScanImportableReportsParseErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bad", "compose.yml"), "services: [\n")
	writeFile(t, filepath.Join(dir, "bad", "rerun.yml"), "targets:\n  x:\n    nope: 1\n")

	got, _ := scanImportable(dir, importScanOpts{})
	if len(got) != 1 || got[0].Path != "bad/rerun.yml" || got[0].Err == nil {
		t.Fatalf("candidates = %v, want bad/rerun.yml with an error", candidateSummary(got))
	}
	if !strings.Contains(got[0].Err.Error(), "nope") {
		t.Fatalf("error = %v, want it to name the bad key", got[0].Err)
	}
}

func TestScanImportableTruncatesWhenACandidateIsPastTheDepthCap(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Makefile"), scanMakefile)
	writeFile(t, filepath.Join(dir, "a", "b", "c", "d", "e", "f", "Makefile"), scanMakefile)

	got, truncated := scanImportable(dir, importScanOpts{})
	if !truncated {
		t.Fatalf("truncated = false with a Makefile two levels past the cap; found %v", candidateSummary(got))
	}

	other := t.TempDir()
	writeFile(t, filepath.Join(other, "Makefile"), scanMakefile)
	if err := os.MkdirAll(filepath.Join(other, "a", "b", "c", "d", "e", "f"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, truncated := scanImportable(other, importScanOpts{}); truncated {
		t.Fatal("truncated = true for empty folders past the cap")
	}
}
