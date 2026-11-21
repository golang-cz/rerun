package main

import (
	"os"
	"path/filepath"
	"strings"
)

// gitCheckout is a directory holding a .git, identified by the repository it belongs to.
type gitCheckout struct {
	dir string
	// id is the common git dir; linked worktrees share their main checkout's, submodules have their own.
	id string
	// name is the main checkout's folder name, for the skip row.
	name string
	// main is set for a checkout whose .git is a directory.
	main bool
}

// checkoutAt reads dir's .git, or returns nil when it has none.
func checkoutAt(dir string) *gitCheckout {
	dotGit := filepath.Join(dir, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil {
		return nil
	}
	c := &gitCheckout{dir: dir}
	common := dotGit
	if !info.IsDir() {
		gitdir := readGitdirFile(dir, dotGit)
		common = gitdir
		if i := strings.LastIndex(gitdir, string(filepath.Separator)+"worktrees"+string(filepath.Separator)); i >= 0 {
			common = gitdir[:i]
		}
	} else {
		c.main = true
	}
	if common == "" {
		common = dotGit
	}
	if r, err := filepath.EvalSymlinks(common); err == nil {
		common = r
	}
	c.id = common
	base := filepath.Base(common)
	if base == ".git" {
		c.name = filepath.Base(filepath.Dir(common))
	} else {
		c.name = strings.TrimSuffix(base, ".git")
	}
	return c
}

// readGitdirFile is the path a `gitdir: <path>` file points at, or "" if it is not one.
func readGitdirFile(dir, file string) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	path, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return ""
	}
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return filepath.Clean(path)
}

// checkoutOfRoot is the checkout holding the scan root, walking up from it.
func (s *importScanner) checkoutOfRoot() *gitCheckout {
	if top := repoTop(s.root); top != "" {
		return checkoutAt(top)
	}
	return nil
}
