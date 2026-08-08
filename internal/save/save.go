// Package save snapshots dirty repositories onto auto/<device> branches
// without touching the working tree or the current branch.
package save

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmdmcy/lmicesync/internal/config"
	"github.com/mmdmcy/lmicesync/internal/scan"
)

func run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir, name}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// AutoBranch returns the auto-save branch name for this device.
func AutoBranch(cfg *config.Config) string {
	return "auto/" + cfg.Device
}

// RepoIsDirty reports whether the worktree has uncommitted or untracked
// changes.
func RepoIsDirty(dir string) (bool, error) {
	out, err := run(dir, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("%s: %v", dir, err)
	}
	return len(out) > 0, nil
}

// Snapshot commits the current worktree state onto the auto branch for the
// device, using a temporary index so the real branch and worktree are never
// disturbed. Returns true when a snapshot commit was created.
func Snapshot(dir string, cfg *config.Config) (bool, error) {
	dirty, err := RepoIsDirty(dir)
	if err != nil {
		return false, err
	}
	if !dirty {
		return false, nil
	}
	branch := AutoBranch(cfg)
	parent, err := run(dir, "rev-parse", "--verify", "-q", "refs/heads/"+branch)
	if err != nil {
		parent = ""
	} else {
		parent = trim(parent)
	}
	head, err := run(dir, "rev-parse", "HEAD")
	if err != nil {
		return false, fmt.Errorf("%s: %v", dir, err)
	}
	head = trim(head)

	indexFile, err := os.CreateTemp("", "lmicesync-index-*")
	if err != nil {
		return false, err
	}
	indexPath := indexFile.Name()
	indexFile.Close()
	defer os.Remove(indexPath)

	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+indexPath)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if _, err := git("read-tree", head); err != nil {
		return false, fmt.Errorf("%s: read-tree: %v", dir, err)
	}
	if _, err := git("add", "-A"); err != nil {
		return false, fmt.Errorf("%s: add: %v", dir, err)
	}
	treeOut, err := git("write-tree")
	if err != nil {
		return false, fmt.Errorf("%s: write-tree: %v", dir, err)
	}
	tree := trim(treeOut)

	msg := fmt.Sprintf("lmicesync auto-save %s (%s)",
		cfg.Device, time.Now().Format(time.RFC3339))
	args := []string{"commit-tree", tree, "-m", msg}
	if parent != "" {
		args = append(args, "-p", parent)
	} else {
		args = append(args, "-p", head)
	}
	commitOut, err := git(args...)
	if err != nil {
		return false, fmt.Errorf("%s: commit-tree: %v", dir, err)
	}
	commit := trim(commitOut)

	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return false, err
	}
	state := filepath.Join(cfg.StateDir, "last-save.tsv")
	f, err := os.OpenFile(state, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		fmt.Fprintf(f, "%s\t%s\n", dir, time.Now().Format(time.RFC3339))
		f.Close()
	}
	if _, err := run(dir, "update-ref", "refs/heads/"+branch, commit); err != nil {
		return false, fmt.Errorf("%s: update-ref: %v", dir, err)
	}
	return true, nil
}

// Run snapshots every dirty repo under the configured roots.
func Run(cfg *config.Config) error {
	repos := scan.FindRepos(cfg.Roots)
	if len(repos) == 0 {
		fmt.Println("no repositories found under configured roots")
		return nil
	}
	saved := 0
	for _, dir := range repos {
		created, err := Snapshot(dir, cfg)
		if err != nil {
			fmt.Printf("ERROR %s: %v\n", dir, err)
			continue
		}
		if created {
			fmt.Printf("saved %s -> %s\n", dir, AutoBranch(cfg))
			saved++
		}
	}
	fmt.Printf("auto-save: %d/%d repositories saved\n", saved, len(repos))
	return nil
}

func trim(s string) string {
	return strings.TrimRight(s, "\n\r ")
}
