package rerun

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// rewriteInPlace writes data over the file without truncating, so the kernel
// reports one Write with the full content.
func rewriteInPlace(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
}

// nextChangeFor waits for a ChangeSet that names file, skipping any others.
func nextChangeFor(t *testing.T, changes <-chan ChangeSet, file string, within time.Duration) ChangeSet {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case change := <-changes:
			if _, ok := change.Files[file]; ok {
				return change
			}
		case <-deadline:
			t.Fatalf("no ChangeSet for %s within %v", file, within)
		}
	}
}

func newSaveWatcher(t *testing.T, dir string) (*Watcher, <-chan ChangeSet) {
	t.Helper()
	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(dir)
	changes := w.Watch(20 * time.Millisecond)
	t.Cleanup(func() { w.Close() })
	return w, changes
}

func TestPureChmodIsDropped(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.go")
	writeFiles(t, a)

	_, changes := newSaveWatcher(t, dir)

	// A dropped chmod must not seed the hash, or the next write would look unchanged.
	if err := os.Chmod(a, 0o600); err != nil {
		t.Fatal(err)
	}
	assertNoChange(t, changes, 300*time.Millisecond)

	rewriteInPlace(t, a, []byte("package x\n"))
	nextChangeFor(t, changes, a, 2*time.Second)
}

func TestWriteWithChmodPasses(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.go")
	writeFiles(t, a)

	_, changes := newSaveWatcher(t, dir)

	rewriteInPlace(t, a, []byte("package y\n"))
	nextChangeFor(t, changes, a, 2*time.Second)
	if err := os.Chmod(a, 0o600); err != nil {
		t.Fatal(err)
	}
	rewriteInPlace(t, a, []byte("package z\n"))
	nextChangeFor(t, changes, a, 2*time.Second)
}

func TestSameBytesRewriteIsDroppedAfterFirstEvent(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.go")
	writeFiles(t, a)

	_, changes := newSaveWatcher(t, dir)

	// The first event always passes and seeds the hash.
	rewriteInPlace(t, a, []byte("package x\n"))
	nextChangeFor(t, changes, a, 2*time.Second)

	rewriteInPlace(t, a, []byte("package x\n"))
	assertNoChange(t, changes, 300*time.Millisecond)
}

func TestChangedBytesPass(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.go")
	writeFiles(t, a)

	_, changes := newSaveWatcher(t, dir)

	rewriteInPlace(t, a, []byte("package x\n"))
	nextChangeFor(t, changes, a, 2*time.Second)

	rewriteInPlace(t, a, []byte("package y\n"))
	nextChangeFor(t, changes, a, 2*time.Second)
}

func TestLargeFileAlwaysPasses(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "big.bin")
	big := bytes.Repeat([]byte("a"), maxHashBytes+1)
	if err := os.WriteFile(a, big, 0o644); err != nil {
		t.Fatal(err)
	}

	_, changes := newSaveWatcher(t, dir)

	for i := 0; i < 2; i++ {
		rewriteInPlace(t, a, big)
		nextChangeFor(t, changes, a, 2*time.Second)
	}
}

func TestDeleteClearsHashEntry(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.go")
	writeFiles(t, a)

	_, changes := newSaveWatcher(t, dir)

	rewriteInPlace(t, a, []byte("package x\n"))
	nextChangeFor(t, changes, a, 2*time.Second)

	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	nextChangeFor(t, changes, a, 2*time.Second)

	// Recreated with the old bytes: without the cleared entry this would look unchanged.
	writeFiles(t, a)
	nextChangeFor(t, changes, a, 2*time.Second)
}
