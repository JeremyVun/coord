package main

import (
	"os"
	"path/filepath"
	"strings"
)

// Repo identifies a git checkout. Common is the shared .git directory, so
// every worktree of one repository resolves to the same Common.
type Repo struct {
	Top    string
	Common string
}

func FindRepo(path string) (*Repo, bool) {
	dir := path
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		dir = filepath.Dir(path)
	}
	for {
		gitPath := filepath.Join(dir, ".git")
		info, err := os.Stat(gitPath)
		if err == nil {
			if info.IsDir() {
				return &Repo{Top: dir, Common: gitPath}, true
			}
			if common, ok := commonDirFromGitFile(gitPath, dir); ok {
				return &Repo{Top: dir, Common: common}, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, false
		}
		dir = parent
	}
}

func commonDirFromGitFile(gitFile, dir string) (string, bool) {
	raw, err := os.ReadFile(gitFile)
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(line, "gitdir:") {
		return "", false
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	commonRaw, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return filepath.Clean(gitDir), true
	}
	common := strings.TrimSpace(string(commonRaw))
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitDir, common)
	}
	return filepath.Clean(common), true
}

// Rel returns the repo-relative key for an absolute path. Directories carry a
// trailing slash so prefix matching can tell them apart from files.
func (r *Repo) Rel(abs string) (string, bool) {
	rel, err := filepath.Rel(r.Top, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	if rel == "." {
		return rootKey, true
	}
	rel = filepath.ToSlash(rel)
	if rel == ".git" || strings.HasPrefix(rel, ".git/") {
		return "", false
	}
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		rel += "/"
	}
	return rel, true
}

func (r *Repo) StatePath() string { return filepath.Join(r.Common, "coord.json") }
func (r *Repo) LockPath() string  { return filepath.Join(r.Common, "coord.lock") }
